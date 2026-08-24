// Package domain contains Meridian's provider-independent lifecycle model.
package domain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ProjectID string
type CapsuleID string
type TimelineID string
type MomentID string
type RunID string
type ThreadID string
type ThreadMessageID string
type ThreadBlockID string
type EventID string
type ResourceVersion int64

type Project struct {
	ID              ProjectID
	Name            string
	RepositoryURL   string
	Setup           []string
	ImageReference  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

type CapsuleState string

const (
	CapsuleCreating  CapsuleState = "Creating"
	CapsulePreparing CapsuleState = "Preparing"
	CapsuleReady     CapsuleState = "Ready"
	CapsulePaused    CapsuleState = "Paused"
	CapsuleFailed    CapsuleState = "Failed"
	CapsuleSealed    CapsuleState = "Sealed"
	CapsuleDeleting  CapsuleState = "Deleting"
	CapsuleDeleted   CapsuleState = "Deleted"
)

type CapsuleIntent string

const (
	IntentReady   CapsuleIntent = "Ready"
	IntentPaused  CapsuleIntent = "Paused"
	IntentDeleted CapsuleIntent = "Deleted"
	IntentSealed  CapsuleIntent = "Sealed"
)

type Capsule struct {
	ID                 CapsuleID
	ProjectID          ProjectID
	TimelineID         TimelineID
	Name               string
	State              CapsuleState
	DesiredState       CapsuleIntent
	ProviderResourceID string
	OriginMomentID     MomentID
	RestoreComplete    bool
	Maintenance        string
	Failure            string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ResourceVersion    ResourceVersion
}

type TimelineReason string

const (
	TimelineRoot   TimelineReason = "root"
	TimelineShard  TimelineReason = "shard"
	TimelineRewind TimelineReason = "rewind"
)

type Timeline struct {
	ID                 TimelineID
	ProjectID          ProjectID
	CapsuleID          CapsuleID
	ForkedFromMomentID MomentID
	Reason             TimelineReason
	CreatedAt          time.Time
}

type Moment struct {
	ID               MomentID
	ProjectID        ProjectID
	CapsuleID        CapsuleID
	TimelineID       TimelineID
	ParentMomentID   MomentID
	Name             string
	ArchiveSHA256    string
	ArchiveSize      int64
	ManifestSHA256   string
	ImageDigest      string
	ProjectSetupHash string
	GitBranch        string
	GitHEAD          string
	GitDirtySummary  string
	CreatedAt        time.Time
	Final            bool
}

func (r TimelineReason) Valid() bool {
	return r == TimelineRoot || r == TimelineShard || r == TimelineRewind
}

func (t Timeline) Validate() error {
	if t.ID == "" || t.ProjectID == "" || t.CapsuleID == "" || !t.Reason.Valid() {
		return fmt.Errorf("%w: invalid Timeline", ErrInvalid)
	}
	if t.Reason == TimelineRoot && t.ForkedFromMomentID != "" {
		return fmt.Errorf("%w: root Timeline cannot have a fork Moment", ErrInvalid)
	}
	if t.Reason != TimelineRoot && t.ForkedFromMomentID == "" {
		return fmt.Errorf("%w: descendant Timeline requires a fork Moment", ErrInvalid)
	}
	return nil
}

func (m Moment) Validate() error {
	if m.ID == "" || m.ProjectID == "" || m.CapsuleID == "" || m.TimelineID == "" ||
		m.Name == "" || len(m.Name) > 128 || !validSHA256(m.ArchiveSHA256) ||
		!validSHA256(m.ManifestSHA256) || m.ArchiveSize < 0 || m.ImageDigest == "" ||
		!validSHA256(m.ProjectSetupHash) {
		return fmt.Errorf("%w: invalid Moment", ErrInvalid)
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

type Event struct {
	ID              EventID
	Sequence        int64
	AggregateType   string
	AggregateID     string
	Type            string
	Timestamp       time.Time
	ResourceVersion ResourceVersion
	Data            []byte
}

type RunState string

const (
	RunQueued     RunState = "Queued"
	RunStarting   RunState = "Starting"
	RunRunning    RunState = "Running"
	RunSucceeded  RunState = "Succeeded"
	RunFailed     RunState = "Failed"
	RunCancelling RunState = "Cancelling"
	RunCancelled  RunState = "Cancelled"
)

type Run struct {
	ID              RunID
	CapsuleID       CapsuleID
	Harness         string
	State           RunState
	ExitStatus      int
	HasExitStatus   bool
	Failure         string
	EventCursor     uint64
	CreatedAt       time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

// ThreadState is the durable lifecycle of an explicit structured agent thread.
// Only Active threads accept appended messages.
type ThreadState string

const (
	ThreadActive   ThreadState = "active"
	ThreadPaused   ThreadState = "paused"
	ThreadArchived ThreadState = "archived"
	ThreadDeleted  ThreadState = "deleted"
)

type Thread struct {
	ID              ThreadID
	CapsuleID       CapsuleID
	State           ThreadState
	CurrentRunID    RunID
	AdapterID       string
	WrappedDEK      []byte
	KEKID           string
	KEKVersion      uint32
	EnvelopeVersion uint16
	MessageCount    int64
	EncryptedBytes  int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       time.Time
	ResourceVersion ResourceVersion
}

type ThreadMessageRole string

const (
	ThreadRoleSystem    ThreadMessageRole = "system"
	ThreadRoleUser      ThreadMessageRole = "user"
	ThreadRoleAssistant ThreadMessageRole = "assistant"
	ThreadRoleTool      ThreadMessageRole = "tool"
)

type ThreadMessageKind string

const (
	ThreadMessagePrompt     ThreadMessageKind = "prompt"
	ThreadMessageResponse   ThreadMessageKind = "response"
	ThreadMessageToolCall   ThreadMessageKind = "tool_call"
	ThreadMessageToolResult ThreadMessageKind = "tool_result"
	ThreadMessageStatus     ThreadMessageKind = "status"
)

type ThreadBlockKind string

const (
	ThreadBlockText       ThreadBlockKind = "text"
	ThreadBlockJSON       ThreadBlockKind = "json"
	ThreadBlockToolCall   ThreadBlockKind = "tool_call"
	ThreadBlockToolResult ThreadBlockKind = "tool_result"
	ThreadBlockError      ThreadBlockKind = "error"
)

// ThreadMessage and ThreadBlock contain encrypted envelopes only. Plaintext
// transcript content must never cross the persistence port.
type ThreadMessage struct {
	ID        ThreadMessageID
	ThreadID  ThreadID
	Sequence  int64
	Role      ThreadMessageRole
	Kind      ThreadMessageKind
	CreatedAt time.Time
	Blocks    []ThreadBlock
}

type ThreadBlock struct {
	ID              ThreadBlockID
	ThreadID        ThreadID
	MessageID       ThreadMessageID
	MessageSequence int64
	Sequence        int64
	Kind            ThreadBlockKind
	EnvelopeVersion uint16
	Ciphertext      []byte
	CreatedAt       time.Time
}

// ThreadDelivery is a content-free durable acknowledgement for one controller
// frame. ControllerID is stable across retries and is also used by capsuled as
// defense-in-depth deduplication.
type ThreadDelivery struct {
	ThreadID     ThreadID
	MessageID    ThreadMessageID
	RunID        RunID
	ControllerID string
	DeliveredAt  time.Time
	CreatedAt    time.Time
}

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("resource version conflict")
	ErrIllegalTransition = errors.New("illegal lifecycle transition")
	ErrInvalid           = errors.New("invalid input")
	ErrUnsupported       = errors.New("provider capability unsupported")
	ErrResourceExhausted = errors.New("resource capacity exhausted")
	ErrCorrupt           = errors.New("artifact corruption")
	ErrTranscriptCorrupt = errors.New("transcript corruption")
	ErrKeyMismatch       = errors.New("transcript key mismatch")
)

func (s CapsuleState) Valid() bool {
	switch s {
	case CapsuleCreating, CapsulePreparing, CapsuleReady, CapsulePaused,
		CapsuleFailed, CapsuleSealed, CapsuleDeleting, CapsuleDeleted:
		return true
	default:
		return false
	}
}

func (s CapsuleState) Terminal() bool {
	return s == CapsuleSealed || s == CapsuleDeleted
}

func (s RunState) Valid() bool {
	switch s {
	case RunQueued, RunStarting, RunRunning, RunSucceeded, RunFailed, RunCancelling, RunCancelled:
		return true
	default:
		return false
	}
}

func (s RunState) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunCancelled
}

func (s ThreadState) Valid() bool {
	switch s {
	case ThreadActive, ThreadPaused, ThreadArchived, ThreadDeleted:
		return true
	default:
		return false
	}
}

func (r ThreadMessageRole) Valid() bool {
	switch r {
	case ThreadRoleSystem, ThreadRoleUser, ThreadRoleAssistant, ThreadRoleTool:
		return true
	default:
		return false
	}
}

func (k ThreadMessageKind) Valid() bool {
	switch k {
	case ThreadMessagePrompt, ThreadMessageResponse, ThreadMessageToolCall,
		ThreadMessageToolResult, ThreadMessageStatus:
		return true
	default:
		return false
	}
}

func (k ThreadBlockKind) Valid() bool {
	switch k {
	case ThreadBlockText, ThreadBlockJSON, ThreadBlockToolCall, ThreadBlockToolResult, ThreadBlockError:
		return true
	default:
		return false
	}
}

func CanTransitionThread(from, to ThreadState) bool {
	if from == to {
		return true
	}
	switch from {
	case ThreadActive:
		return to == ThreadPaused || to == ThreadArchived || to == ThreadDeleted
	case ThreadPaused:
		return to == ThreadActive || to == ThreadArchived || to == ThreadDeleted
	case ThreadArchived:
		return to == ThreadDeleted
	default:
		return false
	}
}

func (t *Thread) Transition(to ThreadState, now time.Time) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown Thread state", ErrInvalid)
	}
	if !CanTransitionThread(t.State, to) {
		return fmt.Errorf("%w: Thread lifecycle transition", ErrIllegalTransition)
	}
	t.State = to
	t.UpdatedAt = now.UTC()
	t.ResourceVersion++
	if to == ThreadDeleted {
		t.DeletedAt = now.UTC()
		t.WrappedDEK = nil
		t.CurrentRunID = ""
	}
	return nil
}

func (t Thread) Validate() error {
	if t.ID == "" || t.CapsuleID == "" || !t.State.Valid() ||
		len(t.AdapterID) == 0 || len(t.AdapterID) > 128 ||
		t.ResourceVersion <= 0 || t.MessageCount < 0 || t.EncryptedBytes < 0 ||
		t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: invalid Thread", ErrInvalid)
	}
	if t.State == ThreadDeleted {
		if len(t.WrappedDEK) != 0 || t.DeletedAt.IsZero() {
			return fmt.Errorf("%w: invalid deleted Thread", ErrInvalid)
		}
		return nil
	}
	if len(t.WrappedDEK) == 0 || t.KEKID == "" || len(t.KEKID) > 128 ||
		t.KEKVersion == 0 || t.EnvelopeVersion == 0 || !t.DeletedAt.IsZero() {
		return fmt.Errorf("%w: invalid Thread key envelope", ErrInvalid)
	}
	return nil
}

func (m ThreadMessage) Validate() error {
	if m.ID == "" || m.ThreadID == "" || m.Sequence <= 0 ||
		!m.Role.Valid() || !m.Kind.Valid() || m.CreatedAt.IsZero() ||
		len(m.Blocks) == 0 {
		return fmt.Errorf("%w: invalid ThreadMessage", ErrInvalid)
	}
	for index := range m.Blocks {
		block := &m.Blocks[index]
		if block.ID == "" || block.ThreadID != m.ThreadID || block.MessageID != m.ID ||
			block.MessageSequence != m.Sequence || block.Sequence != int64(index+1) ||
			!block.Kind.Valid() || block.EnvelopeVersion == 0 ||
			len(block.Ciphertext) == 0 || block.CreatedAt.IsZero() {
			return fmt.Errorf("%w: invalid ThreadBlock", ErrInvalid)
		}
	}
	return nil
}

func CanTransitionRun(from, to RunState) bool {
	if from == to {
		return true
	}
	switch from {
	case RunQueued:
		return to == RunStarting || to == RunCancelling || to == RunFailed
	case RunStarting:
		return to == RunRunning || to == RunSucceeded || to == RunFailed || to == RunCancelling || to == RunCancelled
	case RunRunning:
		return to == RunSucceeded || to == RunFailed || to == RunCancelling || to == RunCancelled
	case RunCancelling:
		return to == RunCancelled || to == RunFailed
	default:
		return false
	}
}

func (r *Run) Transition(to RunState, now time.Time) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown Run state %q", ErrInvalid, to)
	}
	if !CanTransitionRun(r.State, to) {
		return fmt.Errorf("%w: Run %s to %s", ErrIllegalTransition, r.State, to)
	}
	r.State = to
	r.UpdatedAt = now.UTC()
	r.ResourceVersion++
	if to == RunStarting && r.StartedAt.IsZero() {
		r.StartedAt = now.UTC()
	}
	if to.Terminal() {
		r.FinishedAt = now.UTC()
	}
	return nil
}

func CanTransition(from, to CapsuleState) bool {
	if from == to {
		return true
	}
	switch from {
	case CapsuleCreating:
		return to == CapsulePreparing || to == CapsuleFailed || to == CapsuleDeleting
	case CapsulePreparing:
		return to == CapsuleReady || to == CapsuleFailed || to == CapsuleDeleting
	case CapsuleReady:
		return to == CapsulePaused || to == CapsuleFailed || to == CapsuleSealed || to == CapsuleDeleting
	case CapsulePaused:
		return to == CapsuleReady || to == CapsuleFailed || to == CapsuleSealed || to == CapsuleDeleting
	case CapsuleFailed:
		return to == CapsuleCreating || to == CapsulePreparing || to == CapsuleReady ||
			to == CapsulePaused || to == CapsuleDeleting
	case CapsuleDeleting:
		return to == CapsuleDeleted || to == CapsuleFailed
	case CapsuleSealed, CapsuleDeleted:
		return false
	default:
		return false
	}
}

func (c *Capsule) Transition(to CapsuleState, now time.Time) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown state %q", ErrInvalid, to)
	}
	if !CanTransition(c.State, to) {
		return fmt.Errorf("%w: %s to %s", ErrIllegalTransition, c.State, to)
	}
	c.State = to
	c.UpdatedAt = now.UTC()
	c.ResourceVersion++
	return nil
}

func (c Capsule) CanMutate() error {
	if c.State.Terminal() {
		return fmt.Errorf("%w: capsule is %s", ErrIllegalTransition, c.State)
	}
	if c.Maintenance != "" {
		return fmt.Errorf("%w: capsule maintenance %q is active", ErrConflict, c.Maintenance)
	}
	return nil
}
