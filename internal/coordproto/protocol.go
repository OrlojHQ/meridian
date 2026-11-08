// Package coordproto defines meridian.coord.v1, the closed protocol between a
// lead Thread's tools, the Capsule-local mailbox in capsuled, and meridiand.
//
// Everything a Capsule sends is hostile. Records are single LF-terminated JSON
// objects with a fixed version, closed field sets per request type, and hard
// size limits. Responses carry identifiers, states, and child report text that
// meridiand re-derived from encrypted transcripts.
package coordproto

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Version = "meridian.coord.v1"

	// SocketEnv names the mailbox socket for tools running in the Capsule.
	SocketEnv = "MERIDIAN_COORD_SOCKET"

	MaxRecordBytes        = 256 << 10
	MaxPromptBytes        = 128 << 10
	MaxKeyBytes           = 128
	MaxIDBytes            = 128
	MaxNameBytes          = 48
	MaxHarnessBytes       = 128
	MaxWaitLinks          = 32
	MaxWaitSeconds        = 3600
	DefaultWaitSeconds    = 600
	MaxReportSummaryBytes = 16 << 10
	MaxMessageBytes       = 512
	DefaultPatchBytes     = 1 << 20
	MaxPatchBytes         = 4 << 20
	MaxResponseBytes      = MaxPatchBytes + 64<<10

	// Mailbox bounds. A full mailbox answers busy.
	MaxPendingRequests = 64
	MaxPendingBytes    = 2 << 20
	RequestsPerMinute  = 30

	MaxPollWait     = 25 * time.Second
	MaxBatch        = 64
	MaxLinks        = 64
	MaxReplyLatency = 60 * time.Second
)

var (
	ErrRecordTooLarge = errors.New("coordination record exceeds size limit")
	ErrMissingLF      = errors.New("coordination record is not LF terminated")
	ErrInvalidRecord  = errors.New("invalid coordination record")
)

type Kind string

const (
	KindSpawn  Kind = "spawn"
	KindSend   Kind = "send"
	KindWait   Kind = "wait"
	KindCancel Kind = "cancel"
	KindStatus Kind = "status"
	KindList   Kind = "list"
	KindResult Kind = "result"
	KindPatch  Kind = "patch"
	KindReport Kind = "report"
	KindWhoAmI Kind = "whoami"
)

// Request is one tool request. Which fields are allowed depends on Type; see
// Validate.
type Request struct {
	Protocol       string   `json:"protocol"`
	Type           Kind     `json:"type"`
	Key            string   `json:"key,omitempty"`
	Name           string   `json:"name,omitempty"`
	Harness        string   `json:"harness,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	Link           string   `json:"link,omitempty"`
	Links          []string `json:"links,omitempty"`
	All            bool     `json:"all,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Status         string   `json:"status,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	MaxBytes       int      `json:"maxBytes,omitempty"`
}

// Mutating requests carry an idempotency key and are recorded by meridiand.
func (r Request) Mutating() bool {
	return r.Type == KindSpawn || r.Type == KindSend || r.Type == KindCancel || r.Type == KindReport
}

// Remote requests are relayed to meridiand. The rest are served by capsuled.
func (r Request) Remote() bool {
	return r.Mutating() || r.Type == KindStatus || r.Type == KindList || r.Type == KindPatch
}

// Digest binds an idempotency key to the exact request it first named.
func (r Request) Digest() string {
	r.Protocol = Version
	encoded, _ := json.Marshal(r)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (r Request) Validate() error {
	if r.Protocol != Version {
		return fmt.Errorf("%w: unsupported protocol", ErrInvalidRecord)
	}
	type fields struct {
		key, name, harness, prompt, link, links, all, timeout, status, summary, maxBytes bool
	}
	var allowed fields
	switch r.Type {
	case KindSpawn:
		allowed = fields{key: true, name: true, harness: true, prompt: true}
	case KindSend:
		allowed = fields{key: true, link: true, prompt: true}
	case KindCancel:
		allowed = fields{key: true, link: true}
	case KindStatus, KindResult:
		allowed = fields{link: true}
	case KindPatch:
		allowed = fields{link: true, maxBytes: true}
	case KindReport:
		allowed = fields{key: true, status: true, summary: true}
	case KindWait:
		allowed = fields{links: true, all: true, timeout: true}
	case KindList, KindWhoAmI:
	default:
		return fmt.Errorf("%w: unknown request type", ErrInvalidRecord)
	}
	present := fields{
		key: r.Key != "", name: r.Name != "", harness: r.Harness != "",
		prompt: r.Prompt != "", link: r.Link != "", links: r.Links != nil,
		all: r.All, timeout: r.TimeoutSeconds != 0, status: r.Status != "",
		summary: r.Summary != "", maxBytes: r.MaxBytes != 0,
	}
	if (present.key && !allowed.key) || (present.name && !allowed.name) ||
		(present.harness && !allowed.harness) || (present.prompt && !allowed.prompt) ||
		(present.link && !allowed.link) || (present.links && !allowed.links) ||
		(present.all && !allowed.all) || (present.timeout && !allowed.timeout) ||
		(present.status && !allowed.status) || (present.summary && !allowed.summary) ||
		(present.maxBytes && !allowed.maxBytes) {
		return fmt.Errorf("%w: field not allowed for %s", ErrInvalidRecord, r.Type)
	}
	if allowed.key && !validToken(r.Key, MaxKeyBytes) {
		return fmt.Errorf("%w: key must be 1-%d printable characters", ErrInvalidRecord, MaxKeyBytes)
	}
	if allowed.name && !ValidName(r.Name) {
		return fmt.Errorf("%w: name must match [a-z0-9-]{1,48}", ErrInvalidRecord)
	}
	if allowed.harness && !validToken(r.Harness, MaxHarnessBytes) {
		return fmt.Errorf("%w: invalid harness", ErrInvalidRecord)
	}
	if allowed.prompt && !validText(r.Prompt, MaxPromptBytes) {
		return fmt.Errorf("%w: prompt must be 1-%d bytes of UTF-8", ErrInvalidRecord, MaxPromptBytes)
	}
	if allowed.link && !validToken(r.Link, MaxIDBytes) {
		return fmt.Errorf("%w: invalid link", ErrInvalidRecord)
	}
	if len(r.Links) > MaxWaitLinks {
		return fmt.Errorf("%w: too many links", ErrInvalidRecord)
	}
	for _, link := range r.Links {
		if !validToken(link, MaxIDBytes) {
			return fmt.Errorf("%w: invalid link", ErrInvalidRecord)
		}
	}
	if r.All && len(r.Links) == 0 {
		return fmt.Errorf("%w: all requires links", ErrInvalidRecord)
	}
	if r.TimeoutSeconds < 0 || r.TimeoutSeconds > MaxWaitSeconds {
		return fmt.Errorf("%w: timeoutSeconds must be between 1 and %d", ErrInvalidRecord, MaxWaitSeconds)
	}
	if allowed.status && r.Status != ReportDone && r.Status != ReportBlocked && r.Status != ReportFailed {
		return fmt.Errorf("%w: report status must be done, blocked or failed", ErrInvalidRecord)
	}
	if allowed.summary && !validText(r.Summary, MaxReportSummaryBytes) {
		return fmt.Errorf("%w: summary must be 1-%d bytes of UTF-8", ErrInvalidRecord, MaxReportSummaryBytes)
	}
	if r.MaxBytes < 0 || r.MaxBytes > MaxPatchBytes {
		return fmt.Errorf("%w: maxBytes must be between 1 and %d", ErrInvalidRecord, MaxPatchBytes)
	}
	return nil
}

func ValidName(name string) bool {
	if len(name) < 1 || len(name) > MaxNameBytes {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func validToken(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

// Error codes. Retryable codes may succeed when retried with the same key.
const (
	CodeInvalidRequest       = "invalid_request"
	CodeCoordinationDisabled = "coordination_disabled"
	CodeBusy                 = "busy"
	CodeRateLimited          = "rate_limited"
	CodeMailboxLost          = "mailbox_lost"
	CodePending              = "pending"
	CodeNotFound             = "not_found"
	CodeHarnessNotAllowed    = "harness_not_allowed"
	CodeLimitExceeded        = "limit_exceeded"
	CodeCapacityExhausted    = "capacity_exhausted"
	CodeGrantInactive        = "grant_inactive"
	CodeUnavailable          = "unavailable"
	CodeConflict             = "conflict"
	CodeLinkClosed           = "link_closed"
	CodeInternal             = "internal"
)

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

func Fail(code, message string) Response {
	if len(message) > MaxMessageBytes {
		message = message[:MaxMessageBytes]
	}
	retryable := code == CodeBusy || code == CodeRateLimited || code == CodePending ||
		code == CodeMailboxLost || code == CodeCapacityExhausted || code == CodeUnavailable
	return Response{Protocol: Version, Error: &Error{Code: code, Message: message, Retryable: retryable}}
}

type Response struct {
	Protocol     string   `json:"protocol"`
	OK           bool     `json:"ok"`
	Error        *Error   `json:"error,omitempty"`
	Link         *Link    `json:"link,omitempty"`
	Links        []Link   `json:"links,omitempty"`
	Reports      []Report `json:"reports,omitempty"`
	MessageID    string   `json:"messageId,omitempty"`
	TimedOut     bool     `json:"timedOut,omitempty"`
	Self         *Self    `json:"self,omitempty"`
	Patch        string   `json:"patch,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	Head         string   `json:"head,omitempty"`
	BaseCommit   string   `json:"baseCommit,omitempty"`
	FilesChanged int      `json:"filesChanged,omitempty"`
	Insertions   int      `json:"insertions,omitempty"`
	Deletions    int      `json:"deletions,omitempty"`
}

func OK() Response {
	return Response{Protocol: Version, OK: true}
}

// Link is a content-free view of one child.
type Link struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Harness           string `json:"harness"`
	State             string `json:"state"`
	CloseReason       string `json:"closeReason,omitempty"`
	Phase             string `json:"phase"`
	ReportSeq         int    `json:"reportSeq"`
	LastReportStatus  string `json:"lastReportStatus,omitempty"`
	MessagesForwarded int    `json:"messagesForwarded"`
	Head              string `json:"head,omitempty"`
	BaseCommit        string `json:"baseCommit,omitempty"`
	FilesChanged      int    `json:"filesChanged,omitempty"`
	Insertions        int    `json:"insertions,omitempty"`
	Deletions         int    `json:"deletions,omitempty"`
}

// Explicit report statuses. Turn-end reports use turn_ended instead.
const (
	ReportDone      = "done"
	ReportBlocked   = "blocked"
	ReportFailed    = "failed"
	ReportTurnEnded = "turn_ended"
)

// Link phases.
const (
	PhaseProvisioning = "provisioning"
	PhaseWorking      = "working"
	PhaseIdle         = "idle"
	PhaseAwaiting     = "awaiting_operator"
	PhaseEnded        = "ended"
	PhaseFailed       = "failed"
)

// Report is a child's result. Summary is child-authored text and remains
// untrusted wherever it is shown.
type Report struct {
	Link      string `json:"link"`
	Name      string `json:"name"`
	Seq       int    `json:"seq"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Summary   string `json:"summary"`
	Truncated bool   `json:"truncated,omitempty"`
}

type Self struct {
	Role               string    `json:"role"`
	Depth              int       `json:"depth"`
	GrantID            string    `json:"grantId"`
	MaxChildren        int       `json:"maxChildren"`
	MaxDescendants     int       `json:"maxDescendants"`
	MaxLiveCapsules    int       `json:"maxLiveCapsules"`
	MaxMessagesPerLink int       `json:"maxMessagesPerLink"`
	AllowedHarnesses   []string  `json:"allowedHarnesses"`
	DeadlineAt         time.Time `json:"deadlineAt"`
}

// Config is pushed by meridiand. Disabled mailboxes answer every request with
// coordination_disabled.
type Config struct {
	Enabled bool  `json:"enabled"`
	Self    *Self `json:"self,omitempty"`
}

func (c Config) Validate() error {
	if !c.Enabled {
		if c.Self != nil {
			return fmt.Errorf("%w: disabled config has no self", ErrInvalidRecord)
		}
		return nil
	}
	if c.Self == nil || !validToken(c.Self.GrantID, MaxIDBytes) || len(c.Self.AllowedHarnesses) > MaxLinks {
		return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
	}
	switch c.Self.Role {
	case "lead":
		if c.Self.Depth < 0 || c.Self.Depth > 3 {
			return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
		}
	case "sub":
		if c.Self.Depth != 1 {
			return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
		}
	default:
		return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
	}
	return nil
}

// Pending is a request waiting in the mailbox for meridiand.
type Pending struct {
	Cursor  uint64  `json:"cursor"`
	Request Request `json:"request"`
}

// Poll is the mailbox state meridiand pulls. Consumed maps link IDs to the
// highest report sequence the lead already read through wait or result.
type Poll struct {
	Epoch              string         `json:"epoch"`
	Requests           []Pending      `json:"requests"`
	Consumed           map[string]int `json:"consumed"`
	ConsumedGeneration uint64         `json:"consumedGeneration"`
}

type Reply struct {
	Cursor   uint64   `json:"cursor"`
	Response Response `json:"response"`
}

type Replies struct {
	Items []Reply `json:"items"`
}

// Notification replaces the mailbox's view of the lead's children and their
// latest reports.
type Notification struct {
	Links   []Link   `json:"links"`
	Reports []Report `json:"reports"`
}

func (n Notification) Validate() error {
	if len(n.Links) > MaxLinks || len(n.Reports) > MaxLinks {
		return fmt.Errorf("%w: too many links", ErrInvalidRecord)
	}
	for _, report := range n.Reports {
		if !validToken(report.Link, MaxIDBytes) || report.Seq < 1 ||
			len(report.Summary) > MaxReportSummaryBytes || !utf8.ValidString(report.Summary) {
			return fmt.Errorf("%w: invalid report", ErrInvalidRecord)
		}
	}
	for _, link := range n.Links {
		if !validToken(link.ID, MaxIDBytes) {
			return fmt.Errorf("%w: invalid link", ErrInvalidRecord)
		}
	}
	return nil
}

// ReadRecord reads one LF-terminated request record of at most MaxRecordBytes.
func ReadRecord(reader *bufio.Reader) ([]byte, error) {
	return ReadRecordLimit(reader, MaxRecordBytes)
}

// ReadRecordLimit reads one LF-terminated record of at most limit bytes.
func ReadRecordLimit(reader *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = MaxRecordBytes
	}
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, ErrRecordTooLarge
		}
		line = append(line, fragment...)
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, ErrMissingLF
		}
		return nil, err
	}
	return line, nil
}

// DecodeRequest strictly decodes and validates one record.
func DecodeRequest(record []byte) (Request, error) {
	var request Request
	if err := decodeStrict(record, &request); err != nil {
		return Request{}, err
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// DecodeResponse decodes a response record.
func DecodeResponse(record []byte) (Response, error) {
	var response Response
	if err := decodeStrictLimit(record, &response, MaxResponseBytes); err != nil {
		return Response{}, err
	}
	if response.Protocol != Version || response.OK == (response.Error != nil) {
		return Response{}, fmt.Errorf("%w: invalid response", ErrInvalidRecord)
	}
	return response, nil
}

func decodeStrict(record []byte, target any) error {
	return decodeStrictLimit(record, target, MaxRecordBytes)
}

func decodeStrictLimit(record []byte, target any, limit int) error {
	if len(record) > limit {
		return ErrRecordTooLarge
	}
	if len(record) == 0 || record[len(record)-1] != '\n' {
		return ErrMissingLF
	}
	body := record[:len(record)-1]
	if bytes.ContainsAny(body, "\r\n") || !utf8.Valid(body) {
		return fmt.Errorf("%w: record must be one LF-terminated UTF-8 line", ErrInvalidRecord)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data", ErrInvalidRecord)
	}
	return nil
}

// Encode marshals one LF-terminated request record.
func Encode(value any) ([]byte, error) {
	return EncodeLimit(value, MaxRecordBytes)
}

// EncodeLimit marshals one LF-terminated record of at most limit bytes.
func EncodeLimit(value any, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = MaxRecordBytes
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded)+1 > limit {
		return nil, ErrRecordTooLarge
	}
	return append(encoded, '\n'), nil
}

// TruncateUTF8 shortens value to at most limit bytes on a rune boundary.
func TruncateUTF8(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut], true
}
