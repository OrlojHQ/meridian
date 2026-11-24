package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type CoordinationGrantID string
type ThreadLinkID string
type CoordinationRequestID string

// CapsuleRole separates operator-created main Capsules from sub-Capsules a
// lead Thread spawned under a Coordination Grant.
type CapsuleRole string

const (
	CapsuleMain CapsuleRole = "main"
	CapsuleSub  CapsuleRole = "sub"
)

func (r CapsuleRole) Valid() bool {
	return r == CapsuleMain || r == CapsuleSub
}

type CoordinationGrantState string

const (
	GrantActive  CoordinationGrantState = "active"
	GrantRevoked CoordinationGrantState = "revoked"
	// GrantExpired is a legacy terminal state: grants that passed their
	// absolute deadline before ADR 0059. New grants lapse instead.
	GrantExpired CoordinationGrantState = "expired"
	// GrantLapsed is presented, never stored. A stored active grant is lapsed
	// once the operator has been away longer than its unattended limit; the
	// next operator message to the lead reopens it (ADR 0059).
	GrantLapsed CoordinationGrantState = "lapsed"
)

type SpawnApproval string

const (
	// SpawnAuto starts each accepted spawn without a further operator click.
	// The grant itself is the approval.
	SpawnAuto SpawnApproval = "auto"
	// SpawnOperator queues each spawn until the operator approves or denies it.
	SpawnOperator SpawnApproval = "operator"
)

// OnDeadline is what happens to running subagents when a grant lapses. The
// name predates ADR 0059, when the trigger was an absolute deadline.
type OnDeadline string

const (
	DeadlineCancelAndPause OnDeadline = "cancel_and_pause"
	DeadlineStopNewWork    OnDeadline = "stop_new_work"
)

type GrantRevokeReason string

const (
	RevokeOperator GrantRevokeReason = "operator"
	// RevokeDeadline is the legacy reason of GrantExpired grants.
	RevokeDeadline  GrantRevokeReason = "deadline"
	RevokeLeadEnded GrantRevokeReason = "lead_ended"
)

type WorkerModelMode string

const (
	WorkerModelMatch  WorkerModelMode = "match"
	WorkerModelCustom WorkerModelMode = "custom"
)

// WorkerModelSetting configures the default model (and optional harness) for
// subagents spawned under a Coordination Grant without an explicit override.
type WorkerModelSetting struct {
	Mode    WorkerModelMode
	Harness string
	Model   string
}

// Normalize validates and normalizes a worker model setting. An empty Mode
// defaults to WorkerModelMatch.
func (w WorkerModelSetting) Normalize() (WorkerModelSetting, error) {
	if w.Mode == "" {
		w.Mode = WorkerModelMatch
	}
	w.Harness = strings.TrimSpace(w.Harness)
	w.Model = strings.TrimSpace(w.Model)
	switch w.Mode {
	case WorkerModelMatch:
		if w.Harness != "" || w.Model != "" {
			return w, fmt.Errorf("%w: workerModel match must not set harness or model", ErrInvalid)
		}
		return w, nil
	case WorkerModelCustom:
		if !ValidModelID(w.Model) {
			return w, fmt.Errorf("%w: workerModel custom requires a valid model", ErrInvalid)
		}
		if w.Harness != "" && !ValidHarnessName(w.Harness) {
			return w, fmt.Errorf("%w: invalid workerModel harness", ErrInvalid)
		}
		return w, nil
	default:
		return w, fmt.Errorf("%w: workerModel mode must be match or custom", ErrInvalid)
	}
}

// ValidModelID accepts a non-empty bounded model identifier without control
// characters, matching adapterproto.ValidModelID.
func ValidModelID(id string) bool {
	if id == "" || len(id) > 256 || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// LinkModelState tracks strict pre-prompt model selection on a subagent link.
type LinkModelState string

const (
	LinkModelNone      LinkModelState = ""
	LinkModelPending   LinkModelState = "pending"
	LinkModelRequested LinkModelState = "requested"
	LinkModelApplied   LinkModelState = "applied"
	// LinkModelDefault records that a Match link's harness could not run the
	// requested model, so the subagent started on the harness default.
	LinkModelDefault LinkModelState = "default"
)

// Coordination limits. Depth is fixed at one until nested leads ship.
// Fan-out and descendants count spawns in the rolling CoordinationSpawnWindow,
// not over the grant's lifetime (ADR 0059).
const (
	CoordinationMaxDepth              = 1
	CoordinationDefaultChildren       = 4
	CoordinationMaxChildren           = 16
	CoordinationDefaultDescendants    = 8
	CoordinationMaxDescendants        = 32
	CoordinationDefaultLiveCapsules   = 4
	CoordinationMaxLiveCapsules       = 16
	CoordinationDefaultMessagesPerLnk = 50
	CoordinationMaxMessagesPerLink    = 500
	CoordinationSpawnWindow           = 24 * time.Hour
	CoordinationDefaultUnattended     = 24 * time.Hour
	CoordinationMaxUnattended         = 72 * time.Hour
	CoordinationMinUnattended         = 5 * time.Minute
	CoordinationMaxAllowedHarnesses   = MaxHarnessImages
	CoordinationDefaultArchiveAfter   = time.Hour
	CoordinationMinArchiveAfter       = time.Minute
	CoordinationMaxArchiveAfter       = 30 * 24 * time.Hour
	// CoordinationModelWait bounds how long a subagent waits for its harness
	// to report models and confirm a requested model switch before failing.
	CoordinationModelWait = 30 * time.Second
	// CoordinationModelStall bounds how long a subagent may stay in model
	// selection while its Capsule, runtime or store keep failing.
	CoordinationModelStall = 5 * time.Minute
)

// CoordinationPolicy is the operator's request for a grant. Zero values take
// the defaults above.
type CoordinationPolicy struct {
	MaxDepth           int
	MaxChildren        int
	MaxDescendants     int
	MaxLiveCapsules    int
	MaxMessagesPerLink int
	AllowedHarnesses   []string
	WorkerModel        WorkerModelSetting
	// UnattendedLimit is how long the grant keeps spending after the
	// operator's last message, approval or grant change.
	UnattendedLimit time.Duration
	OnDeadline      OnDeadline
	SpawnApproval   SpawnApproval
	// ArchiveAfter is how long a finished subagent stays idle before it is
	// archived. Nil takes the default; zero disables auto-archive.
	ArchiveAfter *time.Duration
}

// Normalize applies defaults and rejects values outside the hard limits.
func (p CoordinationPolicy) Normalize() (CoordinationPolicy, error) {
	pick := func(value, fallback, maximum int, field string) (int, error) {
		if value == 0 {
			return fallback, nil
		}
		if value < 1 || value > maximum {
			return 0, fmt.Errorf("%w: coordination %s must be between 1 and %d", ErrInvalid, field, maximum)
		}
		return value, nil
	}
	var err error
	if p.MaxDepth, err = pick(p.MaxDepth, 1, CoordinationMaxDepth, "maxDepth"); err != nil {
		return p, err
	}
	if p.MaxChildren, err = pick(p.MaxChildren, CoordinationDefaultChildren, CoordinationMaxChildren, "maxChildren"); err != nil {
		return p, err
	}
	if p.MaxDescendants, err = pick(
		p.MaxDescendants, max(CoordinationDefaultDescendants, p.MaxChildren),
		CoordinationMaxDescendants, "maxDescendants",
	); err != nil {
		return p, err
	}
	if p.MaxLiveCapsules, err = pick(
		p.MaxLiveCapsules, min(CoordinationDefaultLiveCapsules, p.MaxChildren),
		CoordinationMaxLiveCapsules, "maxLiveCapsules",
	); err != nil {
		return p, err
	}
	if p.MaxMessagesPerLink, err = pick(
		p.MaxMessagesPerLink, CoordinationDefaultMessagesPerLnk,
		CoordinationMaxMessagesPerLink, "maxMessagesPerLink",
	); err != nil {
		return p, err
	}
	if p.MaxDescendants < p.MaxChildren {
		return p, fmt.Errorf("%w: coordination maxDescendants must be at least maxChildren", ErrInvalid)
	}
	switch {
	case p.UnattendedLimit == 0:
		p.UnattendedLimit = CoordinationDefaultUnattended
	case !validUnattendedLimit(p.UnattendedLimit):
		return p, errUnattendedLimit
	}
	switch p.OnDeadline {
	case "":
		p.OnDeadline = DeadlineCancelAndPause
	case DeadlineCancelAndPause, DeadlineStopNewWork:
	default:
		return p, fmt.Errorf("%w: unsupported coordination onDeadline", ErrInvalid)
	}
	switch p.SpawnApproval {
	case "":
		p.SpawnApproval = SpawnAuto
	case SpawnAuto, SpawnOperator:
	default:
		return p, fmt.Errorf("%w: coordination spawnApproval must be auto or operator", ErrInvalid)
	}
	switch {
	case p.ArchiveAfter == nil:
		archiveAfter := CoordinationDefaultArchiveAfter
		p.ArchiveAfter = &archiveAfter
	case *p.ArchiveAfter == 0:
	case *p.ArchiveAfter < CoordinationMinArchiveAfter || *p.ArchiveAfter > CoordinationMaxArchiveAfter ||
		*p.ArchiveAfter%time.Second != 0:
		return p, fmt.Errorf("%w: coordination archiveAfter must be 0 or whole seconds between 1m and 720h", ErrInvalid)
	}
	if len(p.AllowedHarnesses) > CoordinationMaxAllowedHarnesses {
		return p, fmt.Errorf("%w: too many coordination harnesses", ErrInvalid)
	}
	harnesses := make([]string, 0, len(p.AllowedHarnesses))
	for _, name := range p.AllowedHarnesses {
		if !ValidHarnessName(name) {
			return p, fmt.Errorf("%w: invalid coordination harness", ErrInvalid)
		}
		if !slices.Contains(harnesses, name) {
			harnesses = append(harnesses, name)
		}
	}
	slices.Sort(harnesses)
	p.AllowedHarnesses = harnesses
	if p.WorkerModel, err = p.WorkerModel.Normalize(); err != nil {
		return p, err
	}
	return p, nil
}

var errUnattendedLimit = fmt.Errorf(
	"%w: coordination unattendedLimit must be whole seconds between 5m and 72h", ErrInvalid)

func validUnattendedLimit(limit time.Duration) bool {
	return limit >= CoordinationMinUnattended && limit <= CoordinationMaxUnattended && limit%time.Second == 0
}

type CoordinationGrant struct {
	ID                 CoordinationGrantID
	ProjectID          ProjectID
	RootThreadID       ThreadID
	RootCapsuleID      CapsuleID
	State              CoordinationGrantState
	SpawnApproval      SpawnApproval
	MaxDepth           int
	MaxChildren        int
	MaxDescendants     int
	MaxLiveCapsules    int
	MaxMessagesPerLink int
	AllowedHarnesses   []string
	WorkerModel        WorkerModelSetting
	// LastOperatorAt changes only on operator API actions: a message to the
	// lead Thread, a spawn approval, or a grant update. Coordination
	// requests, reports, wakes and routines never move it.
	LastOperatorAt  time.Time
	UnattendedLimit time.Duration
	// LapsedAt is set once the lapse was applied to running subagents and
	// cleared when the operator returns. It is zero while the grant is open.
	LapsedAt   time.Time
	OnDeadline OnDeadline
	// ArchiveAfter is zero when auto-archive is disabled.
	ArchiveAfter time.Duration
	// DescendantsCreated is the lifetime spawn count, kept for display.
	// Limits use the rolling window instead.
	DescendantsCreated int
	RevokedAt          time.Time
	RevokeReason       GrantRevokeReason
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ResourceVersion    ResourceVersion
}

func (g CoordinationGrant) Policy() CoordinationPolicy {
	archiveAfter := g.ArchiveAfter
	return CoordinationPolicy{
		MaxDepth:           g.MaxDepth,
		MaxChildren:        g.MaxChildren,
		MaxDescendants:     g.MaxDescendants,
		MaxLiveCapsules:    g.MaxLiveCapsules,
		MaxMessagesPerLink: g.MaxMessagesPerLink,
		AllowedHarnesses:   slices.Clone(g.AllowedHarnesses),
		WorkerModel:        g.EffectiveWorkerModel(),
		UnattendedLimit:    g.UnattendedLimit,
		OnDeadline:         g.OnDeadline,
		SpawnApproval:      g.SpawnApproval,
		ArchiveAfter:       &archiveAfter,
	}
}

// EffectiveWorkerModel returns the grant's worker model setting with an empty
// mode normalized to match.
func (g CoordinationGrant) EffectiveWorkerModel() WorkerModelSetting {
	setting := g.WorkerModel
	if setting.Mode == "" {
		setting.Mode = WorkerModelMatch
	}
	return setting
}

// LapsesAt is when the grant stops spending if the operator stays away.
func (g CoordinationGrant) LapsesAt() time.Time {
	return g.LastOperatorAt.Add(g.UnattendedLimit)
}

// Lapsed reports whether an active grant has gone unattended past its limit,
// whether or not the coordination loop has applied the lapse yet.
func (g CoordinationGrant) Lapsed(now time.Time) bool {
	return g.State == GrantActive && (!g.LapsedAt.IsZero() || !now.Before(g.LapsesAt()))
}

// Usable reports whether the grant authorizes new coordination work at now.
func (g CoordinationGrant) Usable(now time.Time) bool {
	return g.State == GrantActive && !g.Lapsed(now)
}

// StateAt is the state an operator sees: lapsed is derived from an active
// grant and is never stored.
func (g CoordinationGrant) StateAt(now time.Time) CoordinationGrantState {
	if g.Lapsed(now) {
		return GrantLapsed
	}
	return g.State
}

// Attended records operator presence at now and reopens a lapsed grant. It
// never moves LastOperatorAt backwards. Callers must only call it for an
// operator API action; a Capsule can never reach it.
func (g CoordinationGrant) Attended(now time.Time) CoordinationGrant {
	if now.After(g.LastOperatorAt) {
		g.LastOperatorAt = now.UTC()
	}
	g.LapsedAt = time.Time{}
	return g
}

// SpawnWindowStart is the earliest spawn time that still counts toward the
// fan-out and descendant limits at now.
func SpawnWindowStart(now time.Time) time.Time {
	return now.Add(-CoordinationSpawnWindow)
}

type ThreadLinkState string

const (
	LinkPendingApproval ThreadLinkState = "pending_approval"
	LinkActive          ThreadLinkState = "active"
	LinkClosed          ThreadLinkState = "closed"
)

type LinkCloseReason string

const (
	LinkCancelled LinkCloseReason = "cancelled"
	LinkFailed    LinkCloseReason = "failed"
	LinkEnded     LinkCloseReason = "ended"
	LinkRevoked   LinkCloseReason = "revoked"
	// LinkDenied closes a spawn the operator refused.
	LinkDenied LinkCloseReason = "denied"
	// LinkDetached closes the link of a promoted subagent; the lead can no
	// longer address it and it leaves the tree's live Capsule count.
	LinkDetached LinkCloseReason = "detached"
)

type ReportStatus string

const (
	ReportTurnEnded ReportStatus = "turn_ended"
	ReportFailed    ReportStatus = "failed"
	ReportDone      ReportStatus = "done"
	ReportBlocked   ReportStatus = "blocked"
)

// ReportReason qualifies a failed report. Turn-end reports carry no reason.
type ReportReason string

const (
	ReportCancelled        ReportReason = "cancelled"
	ReportCapsuleFailed    ReportReason = "capsule_failed"
	ReportSessionEnded     ReportReason = "session_ended"
	ReportRevoked          ReportReason = "revoked"
	ReportDenied           ReportReason = "denied"
	ReportModelUnavailable ReportReason = "model_unavailable"
)

// ThreadLink is the handle a lead Thread holds for one child Thread. Report
// fields are counters and transcript sequence numbers; report text is always
// re-derived from the child's encrypted transcript.
type ThreadLink struct {
	ID              ThreadLinkID
	GrantID         CoordinationGrantID
	ParentThreadID  ThreadID
	ParentCapsuleID CapsuleID
	ChildIntentID   ProjectThreadIntentID
	ChildThreadID   ThreadID
	ChildCapsuleID  CapsuleID
	Depth           int
	Name            string
	Harness         string
	RequestedModel  string
	// ModelFallback is true when RequestedModel came from the grant's Match
	// setting. If the harness cannot run it, the subagent starts on the
	// harness default; an explicit or Custom model fails the link instead.
	ModelFallback       bool
	ModelState          LinkModelState
	ModelSelectingSince time.Time
	State               ThreadLinkState
	CloseReason         LinkCloseReason
	ReportSeq           int
	ReportDeliveredSeq  int
	// ReportHeadlinedSeq is the highest report a lead wake showed as a
	// headline. Only wait and result move ReportDeliveredSeq.
	ReportHeadlinedSeq int
	ReportMessageSeq   int
	LastReportStatus   ReportStatus
	LastReportReason   ReportReason
	LastReportAt       time.Time
	MessagesForwarded  int
	// OperatorMessages counts operator messages sent straight to the subagent;
	// OperatorNoticed is how many of them a lead wake prompt has mentioned.
	OperatorMessages int
	OperatorNoticed  int
	// ArchivedAt is set when the auto-archive sweep paused a finished subagent
	// and cleared when its Thread resumes. PromotedAt is permanent.
	ArchivedAt time.Time
	PromotedAt time.Time
	// BaseCommit is the child workspace HEAD when the Capsule became Ready.
	// Head, FilesChanged, Insertions and Deletions are content-free counts
	// from the child's git state; patch bytes are never stored.
	BaseCommit      string
	Head            string
	FilesChanged    int
	Insertions      int
	Deletions       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

func ValidLinkName(name string) bool {
	if len(name) < 1 || len(name) > 48 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

type CoordinationRequestKind string

const (
	CoordinationSpawn  CoordinationRequestKind = "spawn"
	CoordinationSend   CoordinationRequestKind = "send"
	CoordinationCancel CoordinationRequestKind = "cancel"
	CoordinationReport CoordinationRequestKind = "report"
)

type CoordinationRequestState string

const (
	RequestPendingApproval CoordinationRequestState = "pending_approval"
	RequestCompleted       CoordinationRequestState = "completed"
	RequestRejected        CoordinationRequestState = "rejected"
)

// CoordinationRequest is the durable idempotency record for one mutating
// coordination request, and the operator queue for spawnApproval=operator.
// Result holds identifiers only, never content.
type CoordinationRequest struct {
	ID              CoordinationRequestID
	GrantID         CoordinationGrantID
	CapsuleID       CapsuleID
	Key             string
	CanonicalDigest string
	Kind            CoordinationRequestKind
	LinkID          ThreadLinkID
	State           CoordinationRequestState
	ResultMessageID ThreadMessageID
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion ResourceVersion
}

// CoordinationGrantPatch is an operator update to an active grant. Nil
// fields are left unchanged. AllowedHarnesses, when non-nil, replaces the
// allowlist. The operator may loosen or tighten limits; switching
// spawnApproval to auto does not start spawns that are already queued.
type CoordinationGrantPatch struct {
	SpawnApproval      *SpawnApproval
	MaxChildren        *int
	MaxDescendants     *int
	MaxLiveCapsules    *int
	MaxMessagesPerLink *int
	AllowedHarnesses   *[]string
	WorkerModel        *WorkerModelSetting
	UnattendedLimit    *time.Duration
	OnDeadline         *OnDeadline
	ArchiveAfter       *time.Duration
}

func (p CoordinationGrantPatch) empty() bool {
	return p.SpawnApproval == nil && p.MaxChildren == nil && p.MaxDescendants == nil &&
		p.MaxLiveCapsules == nil && p.MaxMessagesPerLink == nil && p.AllowedHarnesses == nil &&
		p.WorkerModel == nil && p.UnattendedLimit == nil && p.OnDeadline == nil && p.ArchiveAfter == nil
}

func (p CoordinationGrantPatch) Empty() bool {
	return p.empty()
}

// Apply returns a copy of g with the patch applied. Identity fields are
// unchanged. The grant's resource version is not bumped here, and operator
// presence is recorded separately with Attended.
func (g CoordinationGrant) Apply(patch CoordinationGrantPatch) (CoordinationGrant, error) {
	if patch.empty() {
		return g, fmt.Errorf("%w: coordination grant update requires a change", ErrInvalid)
	}
	setInt := func(dst *int, src *int, maximum int, field string) error {
		if src == nil {
			return nil
		}
		if *src < 1 || *src > maximum {
			return fmt.Errorf("%w: coordination %s must be between 1 and %d", ErrInvalid, field, maximum)
		}
		*dst = *src
		return nil
	}
	if patch.SpawnApproval != nil {
		switch *patch.SpawnApproval {
		case SpawnAuto, SpawnOperator:
			g.SpawnApproval = *patch.SpawnApproval
		default:
			return g, fmt.Errorf("%w: coordination spawnApproval must be auto or operator", ErrInvalid)
		}
	}
	if err := setInt(&g.MaxChildren, patch.MaxChildren, CoordinationMaxChildren, "maxChildren"); err != nil {
		return g, err
	}
	if err := setInt(&g.MaxDescendants, patch.MaxDescendants, CoordinationMaxDescendants, "maxDescendants"); err != nil {
		return g, err
	}
	if err := setInt(&g.MaxLiveCapsules, patch.MaxLiveCapsules, CoordinationMaxLiveCapsules, "maxLiveCapsules"); err != nil {
		return g, err
	}
	if err := setInt(&g.MaxMessagesPerLink, patch.MaxMessagesPerLink, CoordinationMaxMessagesPerLink, "maxMessagesPerLink"); err != nil {
		return g, err
	}
	if g.MaxDescendants < g.MaxChildren {
		return g, fmt.Errorf("%w: coordination maxDescendants must be at least maxChildren", ErrInvalid)
	}
	if patch.AllowedHarnesses != nil {
		policy := CoordinationPolicy{AllowedHarnesses: *patch.AllowedHarnesses}
		normalized, err := policy.Normalize()
		if err != nil {
			return g, err
		}
		if len(normalized.AllowedHarnesses) == 0 {
			return g, fmt.Errorf("%w: coordination allowedHarnesses must not be empty", ErrInvalid)
		}
		g.AllowedHarnesses = normalized.AllowedHarnesses
	}
	if patch.WorkerModel != nil {
		normalized, err := patch.WorkerModel.Normalize()
		if err != nil {
			return g, err
		}
		g.WorkerModel = normalized
	}
	if patch.OnDeadline != nil {
		switch *patch.OnDeadline {
		case DeadlineCancelAndPause, DeadlineStopNewWork:
			g.OnDeadline = *patch.OnDeadline
		default:
			return g, fmt.Errorf("%w: unsupported coordination onDeadline", ErrInvalid)
		}
	}
	if patch.ArchiveAfter != nil {
		switch {
		case *patch.ArchiveAfter == 0:
			g.ArchiveAfter = 0
		case *patch.ArchiveAfter < CoordinationMinArchiveAfter || *patch.ArchiveAfter > CoordinationMaxArchiveAfter ||
			*patch.ArchiveAfter%time.Second != 0:
			return g, fmt.Errorf("%w: coordination archiveAfter must be 0 or whole seconds between 1m and 720h", ErrInvalid)
		default:
			g.ArchiveAfter = *patch.ArchiveAfter
		}
	}
	if patch.UnattendedLimit != nil {
		if !validUnattendedLimit(*patch.UnattendedLimit) {
			return g, errUnattendedLimit
		}
		g.UnattendedLimit = *patch.UnattendedLimit
	}
	return g, nil
}
