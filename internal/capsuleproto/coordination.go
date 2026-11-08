package capsuleproto

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

const (
	CoordinationRequestsPath      = "/v1/coordination/requests"
	CoordinationRepliesPath       = "/v1/coordination/replies"
	CoordinationNotificationsPath = "/v1/coordination/notifications"
	CoordinationConfigPath        = "/v1/coordination/config"

	coordinationConnLimit     = 32
	coordinationReadTimeout   = 10 * time.Second
	coordinationReplyCache    = 128
	coordinationReplyBodySize = 2*coordproto.MaxPatchBytes + 256<<10
	coordinationNotifySize    = 4 << 20
)

// Mailbox is the Capsule-local coordination queue. Tools in the Capsule write
// requests through the socket; meridiand pulls them over the authenticated
// supervisor API and pushes replies, reports, and configuration back. The
// mailbox never calls out of the Capsule and holds no credential.
type Mailbox struct {
	mu           sync.Mutex
	epoch        string
	config       coordproto.Config
	nextCursor   uint64
	pending      []*mailboxEntry
	byKey        map[string]*mailboxEntry
	pendingBytes int
	accepted     []time.Time
	replies      map[string]cachedReply
	replyOrder   []string
	links        map[string]coordproto.Link
	linkOrder    []string
	reports      map[string]coordproto.Report
	consumed     map[string]int
	consumedGen  uint64
	changed      chan struct{}
	now          func() time.Time
	replyLatency time.Duration
}

type mailboxEntry struct {
	cursor   uint64
	key      string
	digest   string
	request  coordproto.Request
	size     int
	done     chan struct{}
	response coordproto.Response
}

type cachedReply struct {
	digest   string
	response coordproto.Response
}

func NewMailbox() *Mailbox {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return &Mailbox{
		epoch:        hex.EncodeToString(raw[:]),
		byKey:        make(map[string]*mailboxEntry),
		replies:      make(map[string]cachedReply),
		links:        make(map[string]coordproto.Link),
		reports:      make(map[string]coordproto.Report),
		consumed:     make(map[string]int),
		changed:      make(chan struct{}),
		now:          time.Now,
		replyLatency: coordproto.MaxReplyLatency,
	}
}

func (m *Mailbox) broadcastLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// Handle serves one tool request.
func (m *Mailbox) Handle(ctx context.Context, request coordproto.Request) coordproto.Response {
	m.mu.Lock()
	enabled := m.config.Enabled
	self := m.config.Self
	m.mu.Unlock()
	if !enabled {
		return coordproto.Fail(coordproto.CodeCoordinationDisabled, "this Thread has no coordination grant")
	}
	role := ""
	if self != nil {
		role = self.Role
	}
	switch request.Type {
	case coordproto.KindWhoAmI:
		response := coordproto.OK()
		copied := *self
		copied.AllowedHarnesses = slices.Clone(self.AllowedHarnesses)
		response.Self = &copied
		return response
	case coordproto.KindWait:
		if role != "lead" {
			return coordproto.Fail(coordproto.CodeCoordinationDisabled, "only a lead Thread can wait on subagents")
		}
		return m.wait(ctx, request)
	case coordproto.KindResult:
		if role != "lead" {
			return coordproto.Fail(coordproto.CodeCoordinationDisabled, "only a lead Thread can read subagent results")
		}
		return m.result(request)
	case coordproto.KindReport:
		if role != "sub" {
			return coordproto.Fail(coordproto.CodeInvalidRequest, "only a subagent Thread can report")
		}
		return m.relay(ctx, request)
	}
	if role != "lead" {
		return coordproto.Fail(coordproto.CodeCoordinationDisabled, "this Thread has no lead coordination grant")
	}
	return m.relay(ctx, request)
}

func (m *Mailbox) relay(ctx context.Context, request coordproto.Request) coordproto.Response {
	key := request.Key
	if !request.Mutating() {
		key = "auto:" + randomHex(12)
	}
	digest := request.Digest()
	encoded, err := coordproto.Encode(request)
	if err != nil {
		return coordproto.Fail(coordproto.CodeInvalidRequest, "request is too large")
	}
	m.mu.Lock()
	if cached, ok := m.replies[key]; ok {
		m.mu.Unlock()
		if cached.digest != digest {
			return coordproto.Fail(coordproto.CodeConflict, "key was already used for a different request")
		}
		return cached.response
	}
	entry, ok := m.byKey[key]
	if ok && entry.digest != digest {
		m.mu.Unlock()
		return coordproto.Fail(coordproto.CodeConflict, "key was already used for a different request")
	}
	if !ok {
		now := m.now()
		cutoff := now.Add(-time.Minute)
		m.accepted = slices.DeleteFunc(m.accepted, func(at time.Time) bool { return !at.After(cutoff) })
		if len(m.accepted) >= coordproto.RequestsPerMinute {
			m.mu.Unlock()
			return coordproto.Fail(coordproto.CodeRateLimited, "too many coordination requests; retry later")
		}
		if len(m.pending) >= coordproto.MaxPendingRequests ||
			m.pendingBytes+len(encoded) > coordproto.MaxPendingBytes {
			m.mu.Unlock()
			return coordproto.Fail(coordproto.CodeBusy, "coordination mailbox is full; retry later")
		}
		m.nextCursor++
		entry = &mailboxEntry{
			cursor: m.nextCursor, key: key, digest: digest, request: request,
			size: len(encoded), done: make(chan struct{}),
		}
		m.pending = append(m.pending, entry)
		m.byKey[key] = entry
		m.pendingBytes += entry.size
		m.accepted = append(m.accepted, now)
		m.broadcastLocked()
	}
	m.mu.Unlock()
	timer := time.NewTimer(m.replyLatency)
	defer timer.Stop()
	select {
	case <-entry.done:
		return entry.response
	case <-timer.C:
		return coordproto.Fail(coordproto.CodePending, "meridiand has not answered yet; retry with the same key")
	case <-ctx.Done():
		return coordproto.Fail(coordproto.CodePending, "request abandoned; retry with the same key")
	}
}

func (m *Mailbox) wait(ctx context.Context, request coordproto.Request) coordproto.Response {
	timeout := time.Duration(request.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = coordproto.DefaultWaitSeconds * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		m.mu.Lock()
		if !m.config.Enabled {
			m.mu.Unlock()
			return coordproto.Fail(coordproto.CodeCoordinationDisabled, "this Thread has no coordination grant")
		}
		for _, link := range request.Links {
			if _, ok := m.links[link]; !ok {
				m.mu.Unlock()
				return coordproto.Fail(coordproto.CodeNotFound, "unknown link "+link)
			}
		}
		targets := request.Links
		if len(targets) == 0 {
			targets = m.linkOrder
		}
		var unseen []coordproto.Report
		ready := request.All
		settled := true
		for _, link := range targets {
			report, ok := m.reports[link]
			fresh := ok && report.Seq > m.consumed[link]
			if fresh {
				unseen = append(unseen, report)
			}
			if !fresh && m.links[link].State == "active" {
				ready = false
				settled = false
			}
		}
		if !request.All {
			ready = len(unseen) > 0
		}
		// Closed links never report again, so waiting on only those returns
		// at once rather than at the timeout.
		if ready || (settled && len(targets) > 0) {
			m.consumeLocked(unseen)
			m.mu.Unlock()
			response := coordproto.OK()
			response.Reports = unseen
			return response
		}
		changed := m.changed
		m.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			response := coordproto.OK()
			response.TimedOut = true
			return response
		case <-ctx.Done():
			return coordproto.Fail(coordproto.CodePending, "wait abandoned")
		}
	}
}

func (m *Mailbox) result(request coordproto.Request) coordproto.Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[request.Link]
	if !ok {
		return coordproto.Fail(coordproto.CodeNotFound, "unknown link "+request.Link)
	}
	response := coordproto.OK()
	response.Link = &link
	if report, ok := m.reports[request.Link]; ok {
		response.Reports = []coordproto.Report{report}
		m.consumeLocked(response.Reports)
	}
	return response
}

func (m *Mailbox) consumeLocked(reports []coordproto.Report) {
	changed := false
	for _, report := range reports {
		if report.Seq > m.consumed[report.Link] {
			m.consumed[report.Link] = report.Seq
			changed = true
		}
	}
	if changed {
		m.consumedGen++
		m.broadcastLocked()
	}
}

// Poll returns pending requests after the cursor. It blocks until a request
// arrives, the consumed generation moves past the caller's, or wait elapses.
func (m *Mailbox) Poll(ctx context.Context, after, consumedGen uint64, wait time.Duration) coordproto.Poll {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		m.mu.Lock()
		poll := coordproto.Poll{
			Epoch: m.epoch, Requests: []coordproto.Pending{},
			Consumed: make(map[string]int, len(m.consumed)), ConsumedGeneration: m.consumedGen,
		}
		for _, entry := range m.pending {
			if entry.cursor > after && len(poll.Requests) < coordproto.MaxBatch {
				poll.Requests = append(poll.Requests, coordproto.Pending{Cursor: entry.cursor, Request: entry.request})
			}
		}
		for link, seq := range m.consumed {
			poll.Consumed[link] = seq
		}
		changed := m.changed
		m.mu.Unlock()
		if len(poll.Requests) > 0 || poll.ConsumedGeneration != consumedGen {
			return poll
		}
		select {
		case <-changed:
		case <-timer.C:
			return poll
		case <-ctx.Done():
			return poll
		}
	}
}

// Reply completes pending requests. Unknown cursors are ignored so retries
// from meridiand are harmless.
func (m *Mailbox) Reply(replies []coordproto.Reply) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, reply := range replies {
		index := slices.IndexFunc(m.pending, func(entry *mailboxEntry) bool { return entry.cursor == reply.Cursor })
		if index < 0 {
			continue
		}
		entry := m.pending[index]
		m.pending = slices.Delete(m.pending, index, index+1)
		delete(m.byKey, entry.key)
		m.pendingBytes -= entry.size
		entry.response = reply.Response
		close(entry.done)
		if entry.request.Mutating() {
			m.cacheReplyLocked(entry.key, cachedReply{digest: entry.digest, response: reply.Response})
		}
		if reply.Response.OK && reply.Response.Link != nil {
			m.rememberLinkLocked(*reply.Response.Link)
		}
	}
	m.broadcastLocked()
}

func (m *Mailbox) cacheReplyLocked(key string, reply cachedReply) {
	if _, ok := m.replies[key]; !ok {
		m.replyOrder = append(m.replyOrder, key)
	}
	m.replies[key] = reply
	for len(m.replyOrder) > coordinationReplyCache {
		delete(m.replies, m.replyOrder[0])
		m.replyOrder = m.replyOrder[1:]
	}
}

func (m *Mailbox) rememberLinkLocked(link coordproto.Link) {
	if _, ok := m.links[link.ID]; !ok {
		if len(m.linkOrder) >= coordproto.MaxLinks {
			return
		}
		m.linkOrder = append(m.linkOrder, link.ID)
	}
	m.links[link.ID] = link
}

// Notify replaces the view of the lead's links and their latest reports.
func (m *Mailbox) Notify(notification coordproto.Notification) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, link := range notification.Links {
		m.rememberLinkLocked(link)
	}
	for _, report := range notification.Reports {
		if _, ok := m.links[report.Link]; !ok {
			continue
		}
		if existing, ok := m.reports[report.Link]; !ok || report.Seq >= existing.Seq {
			m.reports[report.Link] = report
		}
	}
	m.broadcastLocked()
}

// Configure enables or disables the mailbox. Disabling fails every pending
// request and forgets every link and report.
func (m *Mailbox) Configure(config coordproto.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.config = config
	if !config.Enabled {
		for _, entry := range m.pending {
			entry.response = coordproto.Fail(coordproto.CodeCoordinationDisabled, "coordination grant ended")
			close(entry.done)
		}
		m.pending = nil
		m.byKey = make(map[string]*mailboxEntry)
		m.pendingBytes = 0
		m.links = make(map[string]coordproto.Link)
		m.linkOrder = nil
		m.reports = make(map[string]coordproto.Report)
	}
	m.broadcastLocked()
}

func randomHex(size int) string {
	raw := make([]byte, size)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// ListenCoordination creates the mailbox socket. Its directory is 0700 and
// the socket 0600, so only the Capsule user can connect.
func ListenCoordination(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("coordination socket must be an absolute clean path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create coordination socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect coordination socket directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("coordination socket path exists and is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale coordination socket: %w", err)
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on coordination socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("protect coordination socket: %w", err)
	}
	return listener, nil
}

// ServeCoordination accepts one request per connection until the listener
// closes.
func (s *Server) ServeCoordination(listener net.Listener) error {
	slots := make(chan struct{}, coordinationConnLimit)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				s.serveCoordinationConn(conn)
			}()
		default:
			writeCoordinationResponse(conn, coordproto.Fail(coordproto.CodeBusy, "too many coordination connections"))
			_ = conn.Close()
		}
	}
}

func (s *Server) serveCoordinationConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(coordinationReadTimeout))
	reader := bufio.NewReaderSize(conn, 64<<10)
	record, err := coordproto.ReadRecord(reader)
	if err != nil {
		writeCoordinationResponse(conn, coordproto.Fail(coordproto.CodeInvalidRequest, "request must be one LF-terminated record"))
		return
	}
	request, err := coordproto.DecodeRequest(record)
	if err != nil {
		writeCoordinationResponse(conn, coordproto.Fail(coordproto.CodeInvalidRequest, err.Error()))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var one [1]byte
		_, _ = reader.Read(one[:])
		cancel()
	}()
	writeCoordinationResponse(conn, s.coord.Handle(ctx, request))
}

func writeCoordinationResponse(conn net.Conn, response coordproto.Response) {
	record, err := coordproto.EncodeLimit(response, coordproto.MaxResponseBytes)
	if err != nil {
		record, _ = coordproto.Encode(coordproto.Fail(coordproto.CodeInternal, "response too large"))
	}
	_ = conn.SetWriteDeadline(time.Now().Add(coordinationReadTimeout))
	_, _ = conn.Write(record)
}

func (s *Server) coordinationRequests(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	after, err := parseUintParam(query.Get("after"))
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	consumed, err := parseUintParam(query.Get("consumedGeneration"))
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	waitMillis, err := parseUintParam(query.Get("waitMs"))
	if err != nil || time.Duration(waitMillis)*time.Millisecond > coordproto.MaxPollWait {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	writeJSON(writer, http.StatusOK, s.coord.Poll(
		request.Context(), after, consumed, time.Duration(waitMillis)*time.Millisecond,
	))
}

func (s *Server) coordinationReplies(writer http.ResponseWriter, request *http.Request) {
	var input coordproto.Replies
	if !decodeRequest(writer, request, coordinationReplyBodySize, &input) {
		return
	}
	if len(input.Items) > coordproto.MaxBatch {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	for _, item := range input.Items {
		if item.Response.Protocol != coordproto.Version || item.Response.OK == (item.Response.Error != nil) {
			writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	s.coord.Reply(input.Items)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) coordinationNotifications(writer http.ResponseWriter, request *http.Request) {
	var input coordproto.Notification
	if !decodeRequest(writer, request, coordinationNotifySize, &input) {
		return
	}
	if input.Validate() != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	s.coord.Notify(input)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) coordinationConfig(writer http.ResponseWriter, request *http.Request) {
	var input coordproto.Config
	if !decodeRequest(writer, request, 64<<10, &input) {
		return
	}
	if input.Validate() != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	s.coord.Configure(input)
	writer.WriteHeader(http.StatusNoContent)
}

func parseUintParam(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}
