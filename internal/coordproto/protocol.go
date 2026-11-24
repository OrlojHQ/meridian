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
	"io/fs"
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
	MaxModelBytes         = 256
	MaxModelLabelBytes    = 128
	MaxHarnessModels      = 128
	MaxWaitLinks          = 32
	MaxWaitSeconds        = 3600
	DefaultWaitSeconds    = 600
	MaxReportSummaryBytes = 16 << 10
	MaxTitleBytes         = 512
	MaxBodyBytes          = 64 << 10
	MaxBranchBytes        = 255
	MaxMessageBytes       = 512
	DefaultPatchBytes     = 1 << 20
	MaxPatchBytes         = 4 << 20
	MaxResponseBytes      = MaxPatchBytes + 64<<10

	// MaxFileBytes caps the patch and report files a lead Capsule's mailbox
	// keeps under FilesDirName for toFile requests. The oldest are removed
	// first.
	MaxFileBytes = 16 << 20
	FilesDirName = "coord"

	// MaxContextFiles caps the notes a spawn may attach from NotesDirName
	// in the lead Capsule. The prompt with the notes appended still fits
	// MaxPromptBytes.
	MaxContextFiles     = 8
	MaxContextPathBytes = 255
	NotesDirName        = "notes"

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
	KindSpawn           Kind = "spawn"
	KindSend            Kind = "send"
	KindWait            Kind = "wait"
	KindCancel          Kind = "cancel"
	KindStatus          Kind = "status"
	KindList            Kind = "list"
	KindResult          Kind = "result"
	KindPatch           Kind = "patch"
	KindReport          Kind = "report"
	KindProposeDelivery Kind = "propose-delivery"
	KindWhoAmI          Kind = "whoami"
	// Working notes and Project notes (ADR 0060).
	KindWriteNote   Kind = "write-note"
	KindSyncNotes   Kind = "sync-notes"
	KindProposeNote Kind = "propose-note"
)

// Notes bounds (ADR 0060). meridiand enforces the full content grammar.
const (
	MaxNoteNameBytes = 48
	MaxNoteBytes     = 32 << 10
	// MaxNoteFiles bounds a sync: a tree holds at most 32 live notes.
	MaxNoteFiles = 32
	// MaxNoteProposals is how many of its own proposals a Thread sees.
	MaxNoteProposals = 16
	// WorkingNotesDirName and ProjectNotesDirName are the directories under
	// $HOME/.meridian that capsuled writes the notes into.
	WorkingNotesDirName = "working-notes"
	ProjectNotesDirName = "project-notes"
)

// Request is one tool request. Which fields are allowed depends on Type; see
// Validate.
type Request struct {
	Protocol       string   `json:"protocol"`
	Type           Kind     `json:"type"`
	Key            string   `json:"key,omitempty"`
	Name           string   `json:"name,omitempty"`
	Harness        string   `json:"harness,omitempty"`
	Model          string   `json:"model,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	Link           string   `json:"link,omitempty"`
	Links          []string `json:"links,omitempty"`
	All            bool     `json:"all,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
	Status         string   `json:"status,omitempty"`
	Summary        string   `json:"summary,omitempty"`
	MaxBytes       int      `json:"maxBytes,omitempty"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	// ToFile asks the lead Capsule's mailbox to write a patch or full report
	// to a private file under $HOME/.meridian/coord and return its path
	// instead of the content. It never leaves the Capsule.
	ToFile bool `json:"toFile,omitempty"`
	// ContextFiles names notes under $HOME/.meridian/notes in the lead
	// Capsule to append to a spawn prompt. The lead Capsule's mailbox reads
	// them and removes the field before relaying, so meridiand only ever
	// sees the combined prompt.
	ContextFiles []string `json:"contextFiles,omitempty"`
	// BaseRevision is the note revision a write-note or propose-note was
	// written against: the revision the writer last read, 0 to create.
	BaseRevision int64 `json:"baseRevision,omitempty"`
	// Content is a note's whole new text.
	Content string `json:"content,omitempty"`
	// FromFile names a private note under $HOME/.meridian/notes to use as
	// the content. The Capsule's mailbox reads it and relays Content
	// instead, so meridiand never sees the field.
	FromFile string `json:"fromFile,omitempty"`
	// FromWorkingNote names a working note of the proposer's own tree whose
	// current revision a propose-note offers as a Project note.
	FromWorkingNote string `json:"fromWorkingNote,omitempty"`
	// Delete removes the note (write-note) or proposes its removal.
	Delete bool `json:"delete,omitempty"`
}

// Mutating requests carry an idempotency key and are recorded by meridiand.
func (r Request) Mutating() bool {
	return r.Type == KindSpawn || r.Type == KindSend || r.Type == KindCancel ||
		r.Type == KindReport || r.Type == KindProposeDelivery ||
		r.Type == KindWriteNote || r.Type == KindProposeNote
}

// Remote requests are relayed to meridiand. The rest are served by capsuled.
func (r Request) Remote() bool {
	return r.Mutating() || r.Type == KindStatus || r.Type == KindList || r.Type == KindPatch ||
		r.Type == KindSyncNotes
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
		key, name, harness, model, prompt, link, links, all, timeout, status, summary, maxBytes, title, body, branch, toFile, contextFiles bool
		baseRevision, content, fromFile, fromWorkingNote, delete                                                                           bool
	}
	var allowed fields
	switch r.Type {
	case KindSpawn:
		allowed = fields{key: true, name: true, harness: true, model: true, prompt: true, contextFiles: true}
	case KindSend:
		allowed = fields{key: true, link: true, prompt: true}
	case KindCancel:
		allowed = fields{key: true, link: true}
	case KindStatus:
		allowed = fields{link: true}
	case KindResult:
		allowed = fields{link: true, toFile: true}
	case KindPatch:
		allowed = fields{link: true, maxBytes: true, toFile: true}
	case KindReport:
		allowed = fields{key: true, status: true, summary: true}
	case KindProposeDelivery:
		allowed = fields{key: true, title: true, body: true, branch: true}
	case KindWait:
		allowed = fields{links: true, all: true, timeout: true}
	case KindWriteNote:
		allowed = fields{key: true, name: true, baseRevision: true, content: true, fromFile: true, delete: true}
	case KindProposeNote:
		allowed = fields{key: true, name: true, baseRevision: true, content: true, fromFile: true,
			fromWorkingNote: true, delete: true}
	case KindList, KindWhoAmI, KindSyncNotes:
	default:
		return fmt.Errorf("%w: unknown request type", ErrInvalidRecord)
	}
	present := fields{
		key: r.Key != "", name: r.Name != "", harness: r.Harness != "", model: r.Model != "",
		prompt: r.Prompt != "", link: r.Link != "", links: r.Links != nil,
		all: r.All, timeout: r.TimeoutSeconds != 0, status: r.Status != "",
		summary: r.Summary != "", maxBytes: r.MaxBytes != 0,
		title: r.Title != "", body: r.Body != "", branch: r.Branch != "", toFile: r.ToFile,
		contextFiles: r.ContextFiles != nil,
		baseRevision: r.BaseRevision != 0, content: r.Content != "", fromFile: r.FromFile != "",
		fromWorkingNote: r.FromWorkingNote != "", delete: r.Delete,
	}
	if (present.key && !allowed.key) || (present.name && !allowed.name) ||
		(present.harness && !allowed.harness) || (present.model && !allowed.model) ||
		(present.prompt && !allowed.prompt) ||
		(present.link && !allowed.link) || (present.links && !allowed.links) ||
		(present.all && !allowed.all) || (present.timeout && !allowed.timeout) ||
		(present.status && !allowed.status) || (present.summary && !allowed.summary) ||
		(present.maxBytes && !allowed.maxBytes) || (present.title && !allowed.title) ||
		(present.body && !allowed.body) || (present.branch && !allowed.branch) ||
		(present.toFile && !allowed.toFile) || (present.contextFiles && !allowed.contextFiles) ||
		(present.baseRevision && !allowed.baseRevision) || (present.content && !allowed.content) ||
		(present.fromFile && !allowed.fromFile) || (present.fromWorkingNote && !allowed.fromWorkingNote) ||
		(present.delete && !allowed.delete) {
		return fmt.Errorf("%w: field not allowed for %s", ErrInvalidRecord, r.Type)
	}
	if allowed.key && !validToken(r.Key, MaxKeyBytes) {
		return fmt.Errorf("%w: key must be 1-%d printable characters", ErrInvalidRecord, MaxKeyBytes)
	}
	if r.Type == KindWriteNote || r.Type == KindProposeNote {
		if err := validNoteRequest(r); err != nil {
			return err
		}
	} else if allowed.name && !ValidName(r.Name) {
		return fmt.Errorf("%w: name must match [a-z0-9-]{1,48}", ErrInvalidRecord)
	}
	if present.harness && !validToken(r.Harness, MaxHarnessBytes) {
		return fmt.Errorf("%w: invalid harness", ErrInvalidRecord)
	}
	if present.model && !ValidModel(r.Model) {
		return fmt.Errorf("%w: invalid model", ErrInvalidRecord)
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
	if allowed.title && !validTitle(r.Title) {
		return fmt.Errorf("%w: title must be 1-%d bytes of UTF-8 without newlines", ErrInvalidRecord, MaxTitleBytes)
	}
	if r.Body != "" && (!utf8.ValidString(r.Body) || strings.ContainsRune(r.Body, 0) || len(r.Body) > MaxBodyBytes) {
		return fmt.Errorf("%w: body must be at most %d bytes of UTF-8", ErrInvalidRecord, MaxBodyBytes)
	}
	if allowed.branch && !ValidBranch(r.Branch) {
		return fmt.Errorf("%w: invalid branch", ErrInvalidRecord)
	}
	if present.contextFiles {
		if err := validContextFiles(r.ContextFiles); err != nil {
			return err
		}
	}
	return nil
}

// validNoteRequest checks a write-note or propose-note: a note name, a
// base revision, and exactly one source of content or a deletion.
func validNoteRequest(r Request) error {
	if !ValidNoteName(r.Name) {
		return fmt.Errorf("%w: a note name must match [a-z0-9][a-z0-9-]{0,47}, without .md", ErrInvalidRecord)
	}
	if r.BaseRevision < 0 {
		return fmt.Errorf("%w: baseRevision must be the revision you read, or 0 to create", ErrInvalidRecord)
	}
	sources := 0
	for _, present := range []bool{r.Content != "", r.FromFile != "", r.FromWorkingNote != "", r.Delete} {
		if present {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("%w: give exactly one of content, fromFile, fromWorkingNote or delete", ErrInvalidRecord)
	}
	if r.Content != "" && (len(r.Content) > MaxNoteBytes || !utf8.ValidString(r.Content) || strings.ContainsRune(r.Content, 0)) {
		return fmt.Errorf("%w: content must be at most %d bytes of UTF-8", ErrInvalidRecord, MaxNoteBytes)
	}
	if r.FromFile != "" && !ValidContextPath(r.FromFile) {
		return fmt.Errorf("%w: fromFile must be a path relative to $HOME/.meridian/%s without \"..\"", ErrInvalidRecord, NotesDirName)
	}
	if r.FromWorkingNote != "" && !ValidNoteName(r.FromWorkingNote) {
		return fmt.Errorf("%w: fromWorkingNote must name a working note", ErrInvalidRecord)
	}
	return nil
}

// ValidNoteName is the note name grammar, [a-z0-9][a-z0-9-]{0,47}; the
// file is the name with a fixed .md suffix.
func ValidNoteName(name string) bool {
	return name != "" && name[0] != '-' && ValidName(name)
}

// validContextFiles checks the closed grammar of a spawn's contextFiles: 1 to
// MaxContextFiles distinct slash-separated paths relative to the notes
// directory, with no empty, "." or ".." element and no backslash or control
// character. The lead Capsule's mailbox still opens them through a root at
// that directory, so a symlink cannot lead out of it.
func validContextFiles(paths []string) error {
	if len(paths) == 0 || len(paths) > MaxContextFiles {
		return fmt.Errorf("%w: contextFiles must name 1-%d files", ErrInvalidRecord, MaxContextFiles)
	}
	seen := make(map[string]bool, len(paths))
	for _, name := range paths {
		if !ValidContextPath(name) {
			return fmt.Errorf("%w: contextFiles must be paths relative to $HOME/.meridian/%s without \"..\"", ErrInvalidRecord, NotesDirName)
		}
		if seen[name] {
			return fmt.Errorf("%w: contextFiles names a file twice", ErrInvalidRecord)
		}
		seen[name] = true
	}
	return nil
}

// ValidContextPath reports whether name is a relative path a spawn may
// attach from the notes directory.
func ValidContextPath(name string) bool {
	if name == "" || len(name) > MaxContextPathBytes || !utf8.ValidString(name) ||
		!fs.ValidPath(name) || name == "." || strings.ContainsRune(name, '\\') {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validTitle(value string) bool {
	return validText(value, MaxTitleBytes) && !strings.ContainsAny(value, "\r\n")
}

// ValidBranch is the closed git branch grammar a propose-delivery request may name.
func ValidBranch(value string) bool {
	if value == "" || len(value) > MaxBranchBytes || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		strings.Contains(value, "//") || strings.Contains(value, "@{") ||
		strings.ContainsAny(value, " ~^:?*[\\\x00\r\n") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
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

// ValidModel accepts a non-empty bounded model identifier without control
// characters.
func ValidModel(value string) bool {
	return validLine(value, MaxModelBytes)
}

func validLine(value string, limit int) bool {
	if !validText(value, limit) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
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
	CodeModelUnavailable     = "model_unavailable"
	CodeLimitExceeded        = "limit_exceeded"
	CodeCapacityExhausted    = "capacity_exhausted"
	CodeGrantInactive        = "grant_inactive"
	// CodeCoordinationLapsed refuses mutating requests while the operator
	// has been away longer than the grant's unattended limit. It clears
	// when the operator next messages the lead; retrying sooner cannot help.
	CodeCoordinationLapsed = "coordination_lapsed"
	CodeUnavailable        = "unavailable"
	CodeConflict           = "conflict"
	CodeLinkClosed         = "link_closed"
	// CodeContextTooLarge refuses a spawn whose prompt with its attached
	// notes would exceed MaxPromptBytes. Nothing is cut to fit.
	CodeContextTooLarge = "context_too_large"
	CodeInternal        = "internal"
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
	Protocol     string        `json:"protocol"`
	OK           bool          `json:"ok"`
	Error        *Error        `json:"error,omitempty"`
	Link         *Link         `json:"link,omitempty"`
	Links        []Link        `json:"links,omitempty"`
	Reports      []Report      `json:"reports,omitempty"`
	MessageID    string        `json:"messageId,omitempty"`
	TimedOut     bool          `json:"timedOut,omitempty"`
	Self         *Self         `json:"self,omitempty"`
	Patch        string        `json:"patch,omitempty"`
	Truncated    bool          `json:"truncated,omitempty"`
	Head         string        `json:"head,omitempty"`
	BaseCommit   string        `json:"baseCommit,omitempty"`
	FilesChanged int           `json:"filesChanged,omitempty"`
	Insertions   int           `json:"insertions,omitempty"`
	Deletions    int           `json:"deletions,omitempty"`
	Delivery     *DeliveryView `json:"delivery,omitempty"`
	// File and FileBytes answer a toFile request: the absolute path of the
	// private file in the lead Capsule and its size. Content stays in the file.
	File      string `json:"file,omitempty"`
	FileBytes int    `json:"fileBytes,omitempty"`
	// Note answers a write-note: the note's new revision, or on conflict
	// its current revision and who wrote it.
	Note *NoteRef `json:"note,omitempty"`
	// Notes answers a sync-notes: names, revisions and which changed.
	Notes []NoteRef `json:"notes,omitempty"`
	// NoteFiles carries working-note content from meridiand to the
	// Capsule's mailbox, which writes the files and removes the field
	// before it answers the tool. A tool never receives it.
	NoteFiles []NoteFile `json:"noteFiles,omitempty"`
	// Proposal answers a propose-note: content-free.
	Proposal *NoteProposalRef `json:"proposal,omitempty"`
}

// NoteRef is a content-free view of one note.
type NoteRef struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Size     int    `json:"size,omitempty"`
	Changed  bool   `json:"changed,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	// WrittenBy names who wrote the current revision: a subagent's link
	// name, "lead", or "operator".
	WrittenBy string `json:"writtenBy,omitempty"`
}

// NoteFile is one working note's current text, mailbox-bound only.
type NoteFile struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Content  string `json:"content"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// NotesDelivery is the note sets meridiand sends capsuled with a session
// start (ADR 0060). A set that is not delivered is removed, so a Thread
// whose delivery was turned off keeps no copy from an earlier session.
type NotesDelivery struct {
	Project        []NoteFile `json:"project,omitempty"`
	Working        []NoteFile `json:"working,omitempty"`
	DeliverProject bool       `json:"deliverProject,omitempty"`
	DeliverWorking bool       `json:"deliverWorking,omitempty"`
}

// NoteProposalRef is the content-free state of one proposal.
type NoteProposalRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// DeliveryView is a content-free proposed Delivery. Title and body stay on
// the operator API; they are hostile and never copied into the mailbox.
type DeliveryView struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Branch string `json:"branch"`
}

func OK() Response {
	return Response{Protocol: Version, OK: true}
}

// Link is a content-free view of one child.
type Link struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Harness           string `json:"harness"`
	Model             string `json:"model,omitempty"`
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
	Summary   string `json:"summary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ModelOption is one model a harness offers that a lead may request when
// spawning a subagent.
type ModelOption struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// HarnessModels lists the known models for one allowed subagent harness.
type HarnessModels struct {
	Harness string        `json:"harness"`
	Models  []ModelOption `json:"models,omitempty"`
}

// WorkerDefaults describes what a spawn with no explicit harness or model
// override will receive.
type WorkerDefaults struct {
	Mode       string `json:"mode"`
	Harness    string `json:"harness,omitempty"`
	Model      string `json:"model,omitempty"`
	ModelLabel string `json:"modelLabel,omitempty"`
}

type Self struct {
	Role               string          `json:"role"`
	Depth              int             `json:"depth"`
	GrantID            string          `json:"grantId"`
	MaxChildren        int             `json:"maxChildren"`
	MaxDescendants     int             `json:"maxDescendants"`
	MaxLiveCapsules    int             `json:"maxLiveCapsules"`
	MaxMessagesPerLink int             `json:"maxMessagesPerLink"`
	AllowedHarnesses   []string        `json:"allowedHarnesses"`
	WorkerDefaults     *WorkerDefaults `json:"workerDefaults,omitempty"`
	AvailableModels    []HarnessModels `json:"availableModels,omitempty"`
	// DeadlineAt is when the grant lapses if the operator stays away, as of
	// the last configuration. The operator's next message moves it later.
	DeadlineAt time.Time `json:"deadlineAt"`
	// NoteProposals are the states of this Thread's most recent Project
	// note proposals, newest first, content-free (ADR 0060).
	NoteProposals []NoteProposalRef `json:"noteProposals,omitempty"`
}

// Clone returns a deep copy of Self.
func (s *Self) Clone() *Self {
	if s == nil {
		return nil
	}
	copied := *s
	if s.AllowedHarnesses != nil {
		copied.AllowedHarnesses = append([]string(nil), s.AllowedHarnesses...)
	}
	if s.WorkerDefaults != nil {
		wd := *s.WorkerDefaults
		copied.WorkerDefaults = &wd
	}
	if s.AvailableModels != nil {
		copied.AvailableModels = make([]HarnessModels, len(s.AvailableModels))
		for i, hm := range s.AvailableModels {
			copied.AvailableModels[i] = HarnessModels{
				Harness: hm.Harness,
				Models:  append([]ModelOption(nil), hm.Models...),
			}
		}
	}
	if s.NoteProposals != nil {
		copied.NoteProposals = append([]NoteProposalRef(nil), s.NoteProposals...)
	}
	return &copied
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
	if c.Self == nil || !validToken(c.Self.GrantID, MaxIDBytes) || len(c.Self.AllowedHarnesses) > MaxLinks ||
		len(c.Self.AvailableModels) > MaxLinks || len(c.Self.NoteProposals) > MaxNoteProposals {
		return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
	}
	if wd := c.Self.WorkerDefaults; wd != nil {
		if (wd.Mode != "match" && wd.Mode != "custom") ||
			(wd.Harness != "" && !validToken(wd.Harness, MaxHarnessBytes)) ||
			(wd.Model != "" && !ValidModel(wd.Model)) ||
			(wd.ModelLabel != "" && !validLine(wd.ModelLabel, MaxModelLabelBytes)) {
			return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
		}
	}
	for _, hm := range c.Self.AvailableModels {
		if !validToken(hm.Harness, MaxHarnessBytes) || len(hm.Models) > MaxHarnessModels {
			return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
		}
		for _, opt := range hm.Models {
			if !ValidModel(opt.ID) || (opt.Label != "" && !validLine(opt.Label, MaxModelLabelBytes)) {
				return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
			}
		}
	}
	for _, proposal := range c.Self.NoteProposals {
		if !validToken(proposal.ID, MaxIDBytes) || !ValidNoteName(proposal.Name) || !validToken(proposal.State, 32) {
			return fmt.Errorf("%w: invalid coordination config", ErrInvalidRecord)
		}
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
