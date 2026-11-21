package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Working notes and Project notes (ADR 0060). A note is a Markdown file
// meridiand stores encrypted. Project notes belong to a Project and reach
// its Threads; only the owner writes them and agents propose changes.
// Working notes belong to one lead tree, a Coordination Grant, and every
// member reads and writes them freely. Names, sizes, digests and provenance
// are plaintext and content-free; content never leaves its envelope except
// to the owner's API, a member Capsule, or a delivery.

type (
	NoteScopeID    string
	NoteID         string
	NoteProposalID string
)

// NoteTier names which of the two tiers a scope, note or index entry is.
type NoteTier string

const (
	NoteTierProject NoteTier = "project"
	NoteTierWorking NoteTier = "working"
)

const (
	// MaxNoteNameBytes bounds a name without its fixed .md suffix.
	MaxNoteNameBytes = 48
	NoteSuffix       = ".md"
	// MaxNoteBytes bounds one note's content.
	MaxNoteBytes = 32 << 10
	// MaxLiveNotes and MaxScopeNoteBytes bound one Project's notes or one
	// tree's working notes.
	MaxLiveNotes      = 32
	MaxScopeNoteBytes = 128 << 10
	// NoteRetainedRevisions is how many of a note's most recent revisions
	// keep their content. Older ones keep a content-free row.
	NoteRetainedRevisions = 50
	// Working-note write rates, in any rolling hour.
	WorkingNoteWritesPerCapsule = 30
	WorkingNoteWritesPerTree    = 120
	WorkingNoteWriteWindow      = time.Hour
	// Proposal bounds.
	MaxPendingProposalsPerProject = 16
	MaxPendingProposalsPerThread  = 4
	NoteProposalExpiry            = 7 * 24 * time.Hour
	// ProposalStatesShown is how many of its own recent proposals a
	// proposing Thread sees through whoami.
	ProposalStatesShown = 16
	// MaxNotesIndexBytes bounds the index the adapter renders in the
	// harness's system-prompt channel.
	MaxNotesIndexBytes = 8 << 10
)

// ValidNoteName reports whether name, without the .md suffix, matches
// [a-z0-9][a-z0-9-]{0,47}.
func ValidNoteName(name string) bool {
	if name == "" || len(name) > MaxNoteNameBytes {
		return false
	}
	for index := 0; index < len(name); index++ {
		c := name[index]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && index > 0:
		default:
			return false
		}
	}
	return true
}

// NormalizeNoteContent stores CRLF as LF and checks the content grammar:
// UTF-8 without NUL or control characters other than tab and newline, at
// most MaxNoteBytes after normalization. A note may be empty.
func NormalizeNoteContent(content string) (string, error) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if len(content) > MaxNoteBytes {
		return "", fmt.Errorf("%w: a note is at most %d bytes", ErrResourceExhausted, MaxNoteBytes)
	}
	if !utf8.ValidString(content) {
		return "", fmt.Errorf("%w: a note must be UTF-8", ErrInvalid)
	}
	for _, r := range content {
		if (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return "", fmt.Errorf("%w: a note must not contain control characters other than tab and newline", ErrInvalid)
		}
	}
	return content, nil
}

// NoteDigest is the content digest a revision and a proposal record and an
// approval binds.
func NoteDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NoteDelivery is a Project's notesDelivery setting.
type NoteDelivery string

const (
	NoteDeliveryAllThreads NoteDelivery = "all_threads"
	NoteDeliveryLeadsOnly  NoteDelivery = "leads_only"
	NoteDeliveryOff        NoteDelivery = "off"
)

func (d NoteDelivery) Valid() bool {
	return d == NoteDeliveryAllThreads || d == NoteDeliveryLeadsOnly || d == NoteDeliveryOff
}

// NoteChoice is a Thread's "Include Project notes" choice. Empty follows the
// Project setting.
type NoteChoice string

const (
	NoteChoiceDefault NoteChoice = ""
	NoteChoiceInclude NoteChoice = "include"
	NoteChoiceExclude NoteChoice = "exclude"
)

func (c NoteChoice) Valid() bool {
	return c == NoteChoiceDefault || c == NoteChoiceInclude || c == NoteChoiceExclude
}

// NoteScope holds one Project's or one grant's data key and counters. The
// key is created with the first note and cleared by a crypto-shred; the row
// stays so the counters and the delivery setting outlive a shred.
type NoteScope struct {
	ID          NoteScopeID
	ProjectID   ProjectID
	GrantID     CoordinationGrantID
	OwnerUserID UserID
	WrappedDEK  []byte
	KEKID       string
	KEKVersion  uint32
	// Delivery is the Project's notesDelivery; a grant scope keeps the
	// default and never reads it.
	Delivery NoteDelivery
	// RevisionCounter numbers every revision in the scope, so a lead wake
	// can list what changed since the last one.
	RevisionCounter int64
	HeadlinedSeq    int64
	ResourceVersion ResourceVersion
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (s NoteScope) Tier() NoteTier {
	if s.GrantID == "" {
		return NoteTierProject
	}
	return NoteTierWorking
}

func (s NoteScope) Validate() error {
	if s.ID == "" || s.ProjectID == "" || !s.Delivery.Valid() || s.ResourceVersion <= 0 ||
		s.RevisionCounter < 0 || s.HeadlinedSeq < 0 || s.HeadlinedSeq > s.RevisionCounter ||
		(len(s.WrappedDEK) == 0) != (s.KEKID == "") {
		return fmt.Errorf("%w: invalid note scope", ErrInvalid)
	}
	return nil
}

// Note is one named note's head. Size and Digest describe its live
// content; a deleted note keeps its history and has Live false.
type Note struct {
	ID          NoteID
	ScopeID     NoteScopeID
	ProjectID   ProjectID
	GrantID     CoordinationGrantID
	OwnerUserID UserID
	Name        string
	HeadSeq     int64
	Live        bool
	Size        int
	Digest      string
	// The head revision's provenance, for the note list.
	HeadKind        NoteRevisionKind
	HeadWriter      NoteWriterKind
	HeadLinkName    string
	ResourceVersion ResourceVersion
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (n Note) Validate() error {
	if n.ID == "" || n.ScopeID == "" || n.ProjectID == "" || !ValidNoteName(n.Name) ||
		n.HeadSeq <= 0 || n.Size < 0 || n.Size > MaxNoteBytes || n.Digest == "" ||
		!n.HeadKind.Valid() || !n.HeadWriter.Valid() || n.ResourceVersion <= 0 ||
		(!n.Live && n.Size != 0) {
		return fmt.Errorf("%w: invalid note", ErrInvalid)
	}
	return nil
}

// NoteRevisionKind is a revision's provenance kind. Working notes use
// agent_write, operator_write, restore and delete; Project notes use
// operator_write, approved_proposal, approved_with_edits, restore and delete.
type NoteRevisionKind string

const (
	NoteRevisionOperatorWrite     NoteRevisionKind = "operator_write"
	NoteRevisionAgentWrite        NoteRevisionKind = "agent_write"
	NoteRevisionApprovedProposal  NoteRevisionKind = "approved_proposal"
	NoteRevisionApprovedWithEdits NoteRevisionKind = "approved_with_edits"
	NoteRevisionRestore           NoteRevisionKind = "restore"
	NoteRevisionDelete            NoteRevisionKind = "delete"
)

func (k NoteRevisionKind) Valid() bool {
	switch k {
	case NoteRevisionOperatorWrite, NoteRevisionAgentWrite, NoteRevisionApprovedProposal,
		NoteRevisionApprovedWithEdits, NoteRevisionRestore, NoteRevisionDelete:
		return true
	}
	return false
}

type NoteWriterKind string

const (
	NoteWriterAgent    NoteWriterKind = "agent"
	NoteWriterOperator NoteWriterKind = "operator"
)

func (k NoteWriterKind) Valid() bool { return k == NoteWriterAgent || k == NoteWriterOperator }

// NoteSource is the content-free provenance of agent text: the Thread,
// Capsule and link that wrote or proposed it, and its lead. For a
// promotion or a fromWorkingNote proposal it also names the working-note
// revision the text came from.
type NoteSource struct {
	ThreadID         ThreadID
	CapsuleID        CapsuleID
	LinkID           ThreadLinkID
	LinkName         string
	LeadThreadID     ThreadID
	GrantID          CoordinationGrantID
	WorkingNoteID    NoteID
	WorkingNoteSeq   int64
	WorkingNoteName  string
	WorkingCapsuleID CapsuleID
}

// NoteRevision is one immutable revision. Ciphertext is nil for a delete,
// a revision past the retained window, and anything shredded.
type NoteRevision struct {
	NoteID          NoteID
	Seq             int64
	ScopeID         NoteScopeID
	ProjectID       ProjectID
	GrantID         CoordinationGrantID
	OwnerUserID     UserID
	ScopeSeq        int64
	Name            string
	Kind            NoteRevisionKind
	Writer          NoteWriterKind
	WriterUserID    UserID
	Source          NoteSource
	ProposalID      NoteProposalID
	BaseSeq         int64
	RestoredFromSeq int64
	Digest          string
	Size            int
	Ciphertext      []byte
	CreatedAt       time.Time
}

func (r NoteRevision) Validate() error {
	if r.NoteID == "" || r.Seq <= 0 || r.ScopeID == "" || r.ProjectID == "" || r.ScopeSeq <= 0 ||
		!ValidNoteName(r.Name) || !r.Kind.Valid() || !r.Writer.Valid() || r.BaseSeq < 0 ||
		r.Size < 0 || r.Size > MaxNoteBytes || r.Digest == "" || r.CreatedAt.IsZero() ||
		(r.Kind == NoteRevisionDelete && len(r.Ciphertext) != 0) {
		return fmt.Errorf("%w: invalid note revision", ErrInvalid)
	}
	return nil
}

// NoteProposalState is a proposal's review state.
type NoteProposalState string

const (
	NoteProposalPending           NoteProposalState = "pending"
	NoteProposalApproved          NoteProposalState = "approved"
	NoteProposalApprovedWithEdits NoteProposalState = "approved_with_edits"
	NoteProposalRejected          NoteProposalState = "rejected"
	NoteProposalStale             NoteProposalState = "stale"
	NoteProposalExpired           NoteProposalState = "expired"
)

func (s NoteProposalState) Valid() bool {
	switch s {
	case NoteProposalPending, NoteProposalApproved, NoteProposalApprovedWithEdits,
		NoteProposalRejected, NoteProposalStale, NoteProposalExpired:
		return true
	}
	return false
}

// NoteProposalReason is the closed, content-free reason a proposal closed
// without approval. There is no free-text reason.
type NoteProposalReason string

const (
	NoteProposalReasonNone         NoteProposalReason = ""
	NoteProposalReasonOperator     NoteProposalReason = "operator"
	NoteProposalReasonOwnerChanged NoteProposalReason = "owner_changed"
	NoteProposalReasonShredded     NoteProposalReason = "shredded"
	NoteProposalReasonPurged       NoteProposalReason = "purged"
)

func (r NoteProposalReason) Valid() bool {
	switch r {
	case NoteProposalReasonNone, NoteProposalReasonOperator, NoteProposalReasonOwnerChanged,
		NoteProposalReasonShredded, NoteProposalReasonPurged:
		return true
	}
	return false
}

// NoteProposalOrigin says whether an agent proposed the change or the
// operator promoted a working note.
type NoteProposalOrigin string

const (
	NoteProposalFromAgent     NoteProposalOrigin = "agent"
	NoteProposalFromPromotion NoteProposalOrigin = "promotion"
)

// NoteProposal is one proposed change to one Project note.
type NoteProposal struct {
	ID             NoteProposalID
	ProjectID      ProjectID
	OwnerUserID    UserID
	Name           string
	Delete         bool
	BaseSeq        int64
	State          NoteProposalState
	Reason         NoteProposalReason
	Origin         NoteProposalOrigin
	Source         NoteSource
	IdempotencyKey string
	RequestDigest  string
	Digest         string
	Size           int
	Ciphertext     []byte
	// ResolvedSeq is the Project-note revision an approval created.
	ResolvedSeq     int64
	ResolvedBy      UserID
	ResolvedAt      time.Time
	ResourceVersion ResourceVersion
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (p NoteProposal) Validate() error {
	if p.ID == "" || p.ProjectID == "" || !ValidNoteName(p.Name) || p.BaseSeq < 0 ||
		!p.State.Valid() || !p.Reason.Valid() ||
		(p.Origin != NoteProposalFromAgent && p.Origin != NoteProposalFromPromotion) ||
		p.Size < 0 || p.Size > MaxNoteBytes || p.Digest == "" || p.ResourceVersion <= 0 ||
		p.CreatedAt.IsZero() || (p.Delete && p.Size != 0) {
		return fmt.Errorf("%w: invalid note proposal", ErrInvalid)
	}
	return nil
}

// EffectiveState is the state a reader sees at now: a pending proposal
// older than the expiry reads as expired before the sweep stores it.
func (p NoteProposal) EffectiveState(now time.Time) NoteProposalState {
	if p.State == NoteProposalPending && !now.Before(p.CreatedAt.Add(NoteProposalExpiry)) {
		return NoteProposalExpired
	}
	return p.State
}

// DeliveredNote is one content-free entry of the set a session received.
type DeliveredNote struct {
	Tier     NoteTier `json:"tier"`
	Name     string   `json:"name"`
	Revision int64    `json:"revision"`
	Size     int      `json:"size"`
	Digest   string   `json:"digest"`
}

// ThreadNotes is a Thread's note choice and the content-free set of note
// revisions its latest session received.
type ThreadNotes struct {
	ThreadID    ThreadID
	ProjectID   ProjectID
	OwnerUserID UserID
	Choice      NoteChoice
	Delivered   []DeliveredNote
	DeliveredAt time.Time
	// Indexed says the harness negotiated the notes index; false means it
	// got the files without being told about them.
	Indexed   bool
	UpdatedAt time.Time
}
