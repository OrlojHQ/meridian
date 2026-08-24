package capsuleproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/harness"
)

const (
	structuredHandshakeTimeout = 5 * time.Second
	structuredWriteTimeout     = 5 * time.Second
	structuredCancelGrace      = 2 * time.Second
	structuredMaxWait          = 25 * time.Second
	structuredWriteQueue       = 64
	structuredHTTPBodyLimit    = int64(adapterproto.MaxFrameBytes + 4096)
)

var errAdapterEnd = errors.New("adapter ended session")

type StructuredStartRequest struct {
	RunID   string             `json:"runId"`
	Harness string             `json:"harness"`
	Frame   adapterproto.Frame `json:"frame"`
	Secrets map[string]string  `json:"secrets,omitempty"`
}

type StructuredStatusResponse struct {
	RunStatusResponse
	Protocol     string                     `json:"protocol"`
	Capabilities *adapterproto.Capabilities `json:"capabilities,omitempty"`
	RestartCount uint32                     `json:"restartCount"`
}

type StructuredSendRequest struct {
	Frame adapterproto.Frame `json:"frame"`
}

type StructuredEvent struct {
	Sequence uint64             `json:"sequence"`
	Frame    adapterproto.Frame `json:"frame"`
}

type StructuredEventsResponse struct {
	Events        []StructuredEvent `json:"events"`
	NextCursor    uint64            `json:"nextCursor"`
	Gap           bool              `json:"gap,omitempty"`
	AvailableFrom uint64            `json:"availableFrom,omitempty"`
}

type HarnessProfileResponse struct {
	Name        string `json:"name"`
	Structured  bool   `json:"structured"`
	AdapterKind string `json:"adapterKind,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	PTY         bool   `json:"pty"`
}

type HarnessProfilesResponse struct {
	Items []HarnessProfileResponse `json:"items"`
}

func validateStructuredStatus(response StructuredStatusResponse) error {
	if response.Protocol != adapterproto.Version || response.PTY {
		return errors.New("capsuled returned incompatible structured session status")
	}
	return nil
}

func validateStructuredEvents(response StructuredEventsResponse, after uint64) error {
	if len(response.Events) > 4096 {
		return errors.New("capsuled returned too many structured events")
	}
	previous := after
	for _, event := range response.Events {
		if event.Sequence <= previous || event.Frame.Cursor != event.Sequence ||
			event.Frame.Validate() != nil ||
			(event.Frame.Type != adapterproto.KindHello && !adapterFrame(event.Frame.Type)) {
			return errors.New("capsuled returned invalid structured event ordering")
		}
		previous = event.Sequence
	}
	if response.NextCursor < previous {
		return errors.New("capsuled returned an invalid structured event cursor")
	}
	if response.Gap {
		if response.AvailableFrom <= after ||
			(len(response.Events) > 0 && response.Events[0].Sequence != response.AvailableFrom) {
			return errors.New("capsuled returned an invalid structured replay gap")
		}
	} else if response.AvailableFrom != 0 {
		return errors.New("capsuled returned unexpected structured replay metadata")
	}
	return nil
}

func (s *Server) harnessProfiles(writer http.ResponseWriter, _ *http.Request) {
	configFile, err := os.Open(filepath.Join(s.config.Workspace, ".meridian", "project.yaml"))
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "harness_configuration_unavailable")
		return
	}
	config, parseErr := harness.Parse(configFile)
	_ = configFile.Close()
	if parseErr != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "harness_configuration_invalid")
		return
	}
	response := HarnessProfilesResponse{Items: make([]HarnessProfileResponse, 0, len(config.Harnesses))}
	for _, profile := range config.Harnesses {
		item := HarnessProfileResponse{Name: profile.Name, PTY: profile.PTY}
		if profile.Interaction == harness.InteractionStructured && profile.Adapter != nil {
			item.Structured = true
			item.Protocol = profile.Adapter.Protocol
			item.AdapterKind = adapterKind(*profile.Adapter)
		}
		response.Items = append(response.Items, item)
	}
	writeJSON(writer, http.StatusOK, response)
}

func adapterKind(adapter harness.Adapter) string {
	if filepath.Base(adapter.Executable) != "meridian-harness-adapter" || len(adapter.Arguments) == 0 {
		return "external"
	}
	switch adapter.Arguments[0] {
	case "opencode", "pi", "mock", "generic":
		return adapter.Arguments[0]
	default:
		return "external"
	}
}

type frameWrite struct {
	ctx    context.Context
	frame  adapterproto.Frame
	result chan error
}

type structuredSession struct {
	mu            sync.Mutex
	run           *supervisedRun
	profile       harness.Profile
	directory     string
	initial       adapterproto.Frame
	command       *exec.Cmd
	cancel        context.CancelFunc
	cancelPending bool
	writes        chan frameWrite
	events        []StructuredEvent
	eventBytes    int64
	next          uint64
	eventLimit    int
	outputLimit   int64
	notify        chan struct{}
	capabilities  *adapterproto.Capabilities
	restartCount  uint32
	controllerIDs map[string]bool
	ready         chan struct{}
	done          chan struct{}
	secrets       map[string]string
}

func (s *Server) startStructured(writer http.ResponseWriter, request *http.Request) {
	var input StructuredStartRequest
	if !decodeRequest(writer, request, structuredHTTPBodyLimit, &input) {
		return
	}
	if !validRunIdentity(input.RunID, input.Harness) ||
		(input.Frame.Type != adapterproto.KindStart && input.Frame.Type != adapterproto.KindResume) ||
		input.Frame.Validate() != nil || input.Frame.Cursor != 0 || input.Frame.Gap != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}

	s.gate.Lock()
	defer s.gate.Unlock()
	if existing := s.lookupStructured(input.RunID); existing != nil {
		writeJSON(writer, http.StatusOK, existing.status())
		return
	}

	profile, directory, resolved, status, code := s.structuredProfile(input.Harness, input.Secrets)
	if code != "" {
		writeProtocolError(writer, status, code)
		return
	}

	s.mu.Lock()
	active := 0
	for _, existing := range s.runs {
		existing.mu.Lock()
		terminal := existing.state == RunSucceeded || existing.state == RunFailed ||
			existing.state == RunCancelled
		existing.mu.Unlock()
		if !terminal {
			active++
		}
	}
	if active >= s.config.RunLimit {
		s.mu.Unlock()
		writeProtocolError(writer, http.StatusTooManyRequests, "run_limit")
		return
	}
	if s.runs[input.RunID] != nil || s.structured[input.RunID] != nil {
		s.mu.Unlock()
		writeProtocolError(writer, http.StatusConflict, "run_id_conflict")
		return
	}
	run := &supervisedRun{
		id: input.RunID, state: RunStarting, startedAt: time.Now().UTC(),
		pty: false, profile: profile, eventLimit: s.config.EventLimit,
		outputLimit: s.config.OutputLimit,
	}
	session := &structuredSession{
		run: run, profile: profile, directory: directory, initial: input.Frame,
		writes:     make(chan frameWrite, structuredWriteQueue),
		eventLimit: s.config.EventLimit, outputLimit: s.config.OutputLimit,
		controllerIDs: make(map[string]bool),
		notify:        make(chan struct{}), ready: make(chan struct{}), done: make(chan struct{}),
		secrets: resolved,
	}
	run.cancel = session.requestCancel
	s.runs[input.RunID] = run
	s.structured[input.RunID] = session
	s.mu.Unlock()

	run.addEvent("run.starting", nil, map[string]any{"structured": true})
	go session.execute()
	writeJSON(writer, http.StatusAccepted, session.status())
}

func validRunIdentity(runID, profile string) bool {
	return runID != "" && len(runID) <= 200 && !strings.ContainsAny(runID, "/\\\x00\r\n") &&
		profile != "" && len(profile) <= 128 && !strings.ContainsAny(profile, "/\\\x00\r\n")
}

func (s *Server) structuredProfile(
	name string, supplied map[string]string,
) (harness.Profile, string, map[string]string, int, string) {
	configFile, err := os.Open(filepath.Join(s.config.Workspace, ".meridian", "project.yaml"))
	if err != nil {
		return harness.Profile{}, "", nil, http.StatusUnprocessableEntity, "harness_configuration_unavailable"
	}
	config, parseErr := harness.Parse(configFile)
	_ = configFile.Close()
	if parseErr != nil {
		return harness.Profile{}, "", nil, http.StatusUnprocessableEntity, "harness_configuration_invalid"
	}
	profile, err := config.Profile(name)
	if err != nil {
		return harness.Profile{}, "", nil, http.StatusNotFound, "harness_not_found"
	}
	if profile.Interaction != harness.InteractionStructured || profile.Adapter == nil {
		return harness.Profile{}, "", nil, http.StatusUnprocessableEntity, "structured_interaction_unavailable"
	}
	resolved, ok := resolveProfileSecrets(profile.SecretRefs, supplied)
	if !ok {
		return harness.Profile{}, "", nil, http.StatusUnprocessableEntity, "secrets_unresolved"
	}
	directory, err := workspacePath(s.config.Workspace, profile.Workdir)
	if err != nil {
		return harness.Profile{}, "", nil, http.StatusBadRequest, "unsafe_working_directory"
	}
	if err := validateWorkspaceDirectory(s.config.Workspace, directory); err != nil {
		return harness.Profile{}, "", nil, http.StatusUnprocessableEntity, "working_directory_unavailable"
	}
	return profile, directory, resolved, 0, ""
}

func (s *Server) structuredStatus(writer http.ResponseWriter, request *http.Request) {
	session := s.lookupStructured(request.PathValue("runID"))
	if session == nil {
		writeProtocolError(writer, http.StatusNotFound, "structured_session_not_found")
		return
	}
	writeJSON(writer, http.StatusOK, session.status())
}

func (s *Server) sendStructured(writer http.ResponseWriter, request *http.Request) {
	session := s.lookupStructured(request.PathValue("runID"))
	if session == nil {
		writeProtocolError(writer, http.StatusNotFound, "structured_session_not_found")
		return
	}
	var input StructuredSendRequest
	if !decodeRequest(writer, request, structuredHTTPBodyLimit, &input) ||
		input.Frame.Validate() != nil || !controllerFrame(input.Frame.Type) ||
		input.Frame.Cursor != 0 || input.Frame.Gap != nil {
		if input.Frame.Type != "" {
			writeProtocolError(writer, http.StatusBadRequest, "invalid_frame")
		}
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), structuredWriteTimeout)
	defer cancel()
	if err := session.sendControllerFrame(ctx, input.Frame); err != nil {
		writeProtocolError(writer, http.StatusConflict, "structured_session_unavailable")
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func controllerFrame(kind adapterproto.Kind) bool {
	switch kind {
	case adapterproto.KindUserMessage, adapterproto.KindPermissionResponse,
		adapterproto.KindInputResponse, adapterproto.KindCancel,
		adapterproto.KindHeartbeat, adapterproto.KindAck:
		return true
	default:
		return false
	}
}

func (s *Server) structuredEvents(writer http.ResponseWriter, request *http.Request) {
	session := s.lookupStructured(request.PathValue("runID"))
	if session == nil {
		writeProtocolError(writer, http.StatusNotFound, "structured_session_not_found")
		return
	}
	after, err := strconv.ParseUint(defaultString(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_cursor")
		return
	}
	waitMillis, err := strconv.ParseUint(defaultString(request.URL.Query().Get("waitMillis"), "0"), 10, 32)
	if err != nil || time.Duration(waitMillis)*time.Millisecond > structuredMaxWait {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_wait")
		return
	}
	response := session.eventsAfter(after)
	if len(response.Events) == 0 && !response.Gap && waitMillis > 0 {
		notify := session.waitChannel()
		timer := time.NewTimer(time.Duration(waitMillis) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-request.Context().Done():
			return
		case <-notify:
		case <-timer.C:
		}
		response = session.eventsAfter(after)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) cancelStructured(writer http.ResponseWriter, request *http.Request) {
	session := s.lookupStructured(request.PathValue("runID"))
	if session == nil {
		writeProtocolError(writer, http.StatusNotFound, "structured_session_not_found")
		return
	}
	session.requestCancel()
	writeJSON(writer, http.StatusAccepted, session.status())
}

func (s *Server) lookupStructured(id string) *structuredSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.structured[id]
}

func (s *structuredSession) execute() {
	defer close(s.done)
	ctx, cancel := context.WithTimeout(context.Background(), s.profile.Timeout)
	s.mu.Lock()
	s.cancel = cancel
	cancelPending := s.cancelPending
	s.mu.Unlock()
	defer cancel()
	if cancelPending {
		cancel()
	}

	adapter := s.profile.Adapter
	command := exec.Command(adapter.Executable, append([]string(nil), adapter.Arguments...)...)
	command.Dir = s.directory
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = childEnvironment(s.secrets)
	clearSecretValues(s.secrets)
	s.secrets = nil
	stdin, err := command.StdinPipe()
	if err != nil {
		s.run.finish(RunFailed, nil, "structured adapter input setup failed")
		return
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		s.run.finish(RunFailed, nil, "structured adapter output setup failed")
		return
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		command.Env = nil
		s.run.finish(RunFailed, nil, "structured adapter failed to start")
		return
	}
	command.Env = nil
	s.mu.Lock()
	s.command = command
	s.mu.Unlock()
	s.run.mu.Lock()
	s.run.command = command
	s.run.state = RunRunning
	s.run.mu.Unlock()
	s.run.addEvent("run.running", nil, map[string]any{"structured": true})

	writerDone := make(chan struct{})
	go func() {
		s.writerLoop(ctx, stdin)
		close(writerDone)
	}()

	hello := adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindHello,
		ID: "capsuled-hello", Capabilities: &adapterproto.Capabilities{
			Resume: true, ToolEvents: true, Permissions: true,
			InputRequests: true, Heartbeat: true, OpaqueMetadata: true,
		},
	}
	handshakeContext, handshakeCancel := context.WithTimeout(ctx, structuredHandshakeTimeout)
	if err := s.writeFrame(handshakeContext, hello); err != nil {
		handshakeCancel()
		s.terminateBeforeWait(command, writerDone)
		s.run.finish(runStateForContext(ctx), nil, "structured adapter handshake failed")
		return
	}
	decoder := adapterproto.NewDecoder(stdout)
	type decoded struct {
		frame adapterproto.Frame
		err   error
	}
	helloResult := make(chan decoded, 1)
	go func() {
		frame, err := decoder.Decode()
		helloResult <- decoded{frame: frame, err: err}
	}()
	var peerHello adapterproto.Frame
	select {
	case value := <-helloResult:
		peerHello, err = value.frame, value.err
	case <-handshakeContext.Done():
		err = handshakeContext.Err()
	}
	handshakeCancel()
	if err != nil || peerHello.Type != adapterproto.KindHello || peerHello.Capabilities == nil {
		s.terminateBeforeWait(command, writerDone)
		s.run.finish(RunFailed, nil, "structured adapter negotiation failed")
		return
	}
	capabilities := *peerHello.Capabilities
	if s.initial.Type == adapterproto.KindResume && !capabilities.Resume {
		s.terminateBeforeWait(command, writerDone)
		s.run.finish(RunFailed, nil, "structured adapter cannot resume")
		return
	}
	s.mu.Lock()
	s.capabilities = &capabilities
	s.mu.Unlock()
	s.addEvent(peerHello)
	writeContext, writeCancel := context.WithTimeout(ctx, structuredWriteTimeout)
	err = s.writeFrame(writeContext, s.initial)
	writeCancel()
	if err != nil {
		s.terminateBeforeWait(command, writerDone)
		s.run.finish(runStateForContext(ctx), nil, "structured adapter start failed")
		return
	}
	close(s.ready)

	readerDone := make(chan error, 1)
	go func() { readerDone <- s.readerLoop(ctx, decoder) }()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()

	select {
	case readErr := <-readerDone:
		if errors.Is(readErr, errAdapterEnd) {
			s.terminateWaiting(command, waitDone, writerDone)
			s.run.finish(s.endState(), nil, "")
			return
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) && ctx.Err() == nil {
			s.terminateWaiting(command, waitDone, writerDone)
			s.run.finish(RunFailed, nil, "structured adapter protocol failed")
			return
		}
		contextErr := ctx.Err()
		s.terminateWaiting(command, waitDone, writerDone)
		if contextErr == nil {
			s.run.finish(RunFailed, nil, "structured adapter stream ended unexpectedly")
			return
		}
		s.finishWait(context.Canceled, contextErr)
	case waitErr := <-waitDone:
		select {
		case <-readerDone:
		case <-time.After(100 * time.Millisecond):
		}
		contextErr := ctx.Err()
		cancel()
		<-writerDone
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
			time.Sleep(20 * time.Millisecond)
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		s.finishWait(waitErr, contextErr)
	case <-ctx.Done():
		s.terminateWaiting(command, waitDone, writerDone)
		s.finishWait(context.Canceled, ctx.Err())
	}
}

func (s *structuredSession) writerLoop(ctx context.Context, stdin io.WriteCloser) {
	defer stdin.Close()
	encoder := adapterproto.NewEncoder(stdin)
	for {
		select {
		case <-ctx.Done():
			s.failPending(ctx.Err())
			return
		case request := <-s.writes:
			err := request.ctx.Err()
			if err == nil {
				err = encoder.Encode(request.frame)
			}
			select {
			case request.result <- err:
			default:
			}
			if err != nil && request.ctx.Err() == nil {
				s.failPending(err)
				return
			}
		}
	}
}

func (s *structuredSession) failPending(err error) {
	for {
		select {
		case request := <-s.writes:
			select {
			case request.result <- err:
			default:
			}
		default:
			return
		}
	}
}

func (s *structuredSession) writeFrame(ctx context.Context, frame adapterproto.Frame) error {
	if err := frame.Validate(); err != nil {
		return err
	}
	request := frameWrite{ctx: ctx, frame: frame, result: make(chan error, 1)}
	select {
	case s.writes <- request:
	case <-s.done:
		return errors.New("structured session ended")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-s.done:
		return errors.New("structured session ended")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *structuredSession) sendFrame(ctx context.Context, frame adapterproto.Frame) error {
	select {
	case <-s.ready:
	case <-s.done:
		return errors.New("structured session ended")
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.writeFrame(ctx, frame)
}

func (s *structuredSession) sendControllerFrame(ctx context.Context, frame adapterproto.Frame) error {
	if frame.ID != "" {
		s.mu.Lock()
		if s.controllerIDs[frame.ID] {
			s.mu.Unlock()
			return nil
		}
		s.controllerIDs[frame.ID] = true
		s.mu.Unlock()
	}
	err := s.sendFrame(ctx, frame)
	if err != nil && frame.ID != "" {
		s.mu.Lock()
		delete(s.controllerIDs, frame.ID)
		s.mu.Unlock()
	}
	return err
}

func (s *structuredSession) readerLoop(ctx context.Context, decoder *adapterproto.Decoder) error {
	for {
		frame, err := decoder.Decode()
		if err != nil {
			return err
		}
		if !adapterFrame(frame.Type) || frame.Cursor != 0 || frame.Gap != nil {
			return adapterproto.ErrInvalidFrame
		}
		s.addEvent(frame)
		if frame.Type == adapterproto.KindHeartbeat && frame.ID != "" {
			ackContext, cancel := context.WithTimeout(ctx, structuredWriteTimeout)
			_ = s.writeFrame(ackContext, adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindAck,
				ID: "capsuled-heartbeat-ack", ResponseTo: frame.ID,
			})
			cancel()
		}
		if frame.Type == adapterproto.KindEnd {
			return errAdapterEnd
		}
	}
}

func adapterFrame(kind adapterproto.Kind) bool {
	switch kind {
	case adapterproto.KindAssistantDelta, adapterproto.KindAssistantMessage,
		adapterproto.KindToolStart, adapterproto.KindToolResult,
		adapterproto.KindStatus, adapterproto.KindError,
		adapterproto.KindPermissionRequest, adapterproto.KindInputRequest,
		adapterproto.KindHeartbeat, adapterproto.KindAck, adapterproto.KindEnd:
		return true
	default:
		return false
	}
}

func (s *structuredSession) addEvent(frame adapterproto.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	frame.Cursor = s.next
	event := StructuredEvent{Sequence: s.next, Frame: frame}
	encoded, _ := json.Marshal(event)
	s.events = append(s.events, event)
	s.eventBytes += int64(len(encoded))
	for len(s.events) > 0 &&
		(len(s.events) > s.eventLimit || s.eventBytes > s.outputLimit) {
		old, _ := json.Marshal(s.events[0])
		s.eventBytes -= int64(len(old))
		s.events = s.events[1:]
	}
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *structuredSession) eventsAfter(after uint64) StructuredEventsResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	response := StructuredEventsResponse{NextCursor: s.next}
	if len(s.events) == 0 {
		if s.next > after {
			response.Gap = true
			response.AvailableFrom = s.next + 1
		}
		return response
	}
	if s.events[0].Sequence > 0 && after < s.events[0].Sequence-1 {
		response.Gap = true
		response.AvailableFrom = s.events[0].Sequence
		after = s.events[0].Sequence - 1
	}
	for _, event := range s.events {
		if event.Sequence > after {
			response.Events = append(response.Events, event)
		}
	}
	return response
}

func (s *structuredSession) waitChannel() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}

func (s *structuredSession) status() StructuredStatusResponse {
	s.mu.Lock()
	capabilities := s.capabilities
	restarts := s.restartCount
	cursor := s.next
	s.mu.Unlock()
	runStatus := s.run.status()
	runStatus.Cursor = cursor
	return StructuredStatusResponse{
		RunStatusResponse: runStatus, Protocol: adapterproto.Version,
		Capabilities: capabilities, RestartCount: restarts,
	}
}

func (s *structuredSession) requestCancel() {
	s.mu.Lock()
	cancel := s.cancel
	if cancel == nil {
		s.cancelPending = true
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	ctx, stop := context.WithTimeout(context.Background(), 250*time.Millisecond)
	_ = s.writeFrame(ctx, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel,
		ID: "capsuled-cancel", Reason: "cancelled",
	})
	stop()
	cancel()
}

func (s *structuredSession) terminate(command *exec.Cmd, writerDone <-chan struct{}) {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-writerDone:
	case <-time.After(structuredCancelGrace):
	}
}

func (s *structuredSession) terminateBeforeWait(command *exec.Cmd, writerDone <-chan struct{}) {
	s.terminate(command, writerDone)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(structuredCancelGrace):
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		<-done
	}
}

func (s *structuredSession) terminateWaiting(
	command *exec.Cmd,
	waitDone <-chan error,
	writerDone <-chan struct{},
) {
	s.terminate(command, writerDone)
	select {
	case <-waitDone:
	case <-time.After(structuredCancelGrace):
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		<-waitDone
	}
}

func (s *structuredSession) finishWait(waitErr, contextErr error) {
	if errors.Is(contextErr, context.DeadlineExceeded) {
		s.run.finish(RunFailed, exitCode(waitErr), "structured adapter timed out")
		return
	}
	if errors.Is(contextErr, context.Canceled) {
		s.run.finish(RunCancelled, exitCode(waitErr), "")
		return
	}
	if waitErr == nil {
		s.run.finish(RunSucceeded, pointerInt(0), "")
		return
	}
	s.run.finish(RunFailed, exitCode(waitErr), "structured adapter exited unsuccessfully")
}

func (s *structuredSession) endState() RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return RunFailed
	}
	status := s.events[len(s.events)-1].Frame.Status
	switch status {
	case adapterproto.StatusSucceeded:
		return RunSucceeded
	case adapterproto.StatusCancelled:
		return RunCancelled
	default:
		return RunFailed
	}
}

func runStateForContext(ctx context.Context) RunState {
	if errors.Is(ctx.Err(), context.Canceled) {
		return RunCancelled
	}
	return RunFailed
}

func pointerInt(value int) *int { return &value }
