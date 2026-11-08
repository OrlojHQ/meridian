package domain

import (
	"fmt"
	"slices"
	"time"
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
	GrantExpired CoordinationGrantState = "expired"
)

type SpawnApproval string

const (
	// SpawnAuto starts each accepted spawn without a further operator click.
	// The grant itself is the approval.
	SpawnAuto SpawnApproval = "auto"
	// SpawnOperator queues each spawn until the operator approves or denies it.
	SpawnOperator SpawnApproval = "operator"
)

type OnDeadline string

const (
	DeadlineCancelAndPause OnDeadline = "cancel_and_pause"
	DeadlineStopNewWork    OnDeadline = "stop_new_work"
)

type GrantRevokeReason string

const (
	RevokeOperator  GrantRevokeReason = "operator"
	RevokeDeadline  GrantRevokeReason = "deadline"
	RevokeLeadEnded GrantRevokeReason = "lead_ended"
)

// Coordination limits. Depth is fixed at one until nested leads ship.
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
	CoordinationDefaultDeadline       = 8 * time.Hour
	CoordinationMaxDeadline           = 72 * time.Hour
	CoordinationMinDeadline           = 5 * time.Minute
	CoordinationMaxAllowedHarnesses   = MaxHarnessImages
	CoordinationDefaultArchiveAfter   = 24 * time.Hour
	CoordinationMinArchiveAfter       = time.Minute
	CoordinationMaxArchiveAfter       = 30 * 24 * time.Hour
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
	Deadline           time.Duration
	OnDeadline         OnDeadline
	SpawnApproval      SpawnApproval
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
	case p.Deadline == 0:
		p.Deadline = CoordinationDefaultDeadline
	case p.Deadline < CoordinationMinDeadline || p.Deadline > CoordinationMaxDeadline:
		return p, fmt.Errorf("%w: coordination deadline must be between 5m and 72h", ErrInvalid)
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
	return p, nil
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
	DeadlineAt         time.Time
	OnDeadline         OnDeadline
	// ArchiveAfter is zero when auto-archive is disabled.
	ArchiveAfter       time.Duration
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
		OnDeadline:         g.OnDeadline,
		SpawnApproval:      g.SpawnApproval,
		ArchiveAfter:       &archiveAfter,
	}
}

// Usable reports whether the grant authorizes new coordination work at now.
func (g CoordinationGrant) Usable(now time.Time) bool {
	return g.State == GrantActive && now.Before(g.DeadlineAt)
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
)

// ReportReason qualifies a failed report. Turn-end reports carry no reason.
type ReportReason string

const (
	ReportCancelled     ReportReason = "cancelled"
	ReportCapsuleFailed ReportReason = "capsule_failed"
	ReportSessionEnded  ReportReason = "session_ended"
	ReportRevoked       ReportReason = "revoked"
	ReportDenied        ReportReason = "denied"
)

// ThreadLink is the handle a lead Thread holds for one child Thread. Report
// fields are counters and transcript sequence numbers; report text is always
// re-derived from the child's encrypted transcript.
type ThreadLink struct {
	ID                 ThreadLinkID
	GrantID            CoordinationGrantID
	ParentThreadID     ThreadID
	ParentCapsuleID    CapsuleID
	ChildIntentID      ProjectThreadIntentID
	ChildThreadID      ThreadID
	ChildCapsuleID     CapsuleID
	Depth              int
	Name               string
	Harness            string
	State              ThreadLinkState
	CloseReason        LinkCloseReason
	ReportSeq          int
	ReportDeliveredSeq int
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
	ArchivedAt      time.Time
	PromotedAt      time.Time
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
	Deadline           *time.Duration
	OnDeadline         *OnDeadline
	ArchiveAfter       *time.Duration
}

func (p CoordinationGrantPatch) empty() bool {
	return p.SpawnApproval == nil && p.MaxChildren == nil && p.MaxDescendants == nil &&
		p.MaxLiveCapsules == nil && p.MaxMessagesPerLink == nil && p.AllowedHarnesses == nil &&
		p.Deadline == nil && p.OnDeadline == nil && p.ArchiveAfter == nil
}

func (p CoordinationGrantPatch) Empty() bool {
	return p.empty()
}

// Apply returns a copy of g with the patch applied. Deadline is remaining
// time from now, capped at createdAt plus 72 hours. Identity fields are
// unchanged. The grant's resource version is not bumped here.
func (g CoordinationGrant) Apply(patch CoordinationGrantPatch, now time.Time) (CoordinationGrant, error) {
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
	if patch.Deadline != nil {
		remaining := *patch.Deadline
		if remaining < CoordinationMinDeadline || remaining > CoordinationMaxDeadline {
			return g, fmt.Errorf("%w: coordination deadline must be between 5m and 72h", ErrInvalid)
		}
		at := now.UTC().Add(remaining)
		capAt := g.CreatedAt.Add(CoordinationMaxDeadline)
		if at.After(capAt) {
			if capAt.Sub(now.UTC()) < CoordinationMinDeadline {
				return g, fmt.Errorf("%w: coordination deadline cannot extend past createdAt+72h", ErrInvalid)
			}
			at = capAt
		}
		g.DeadlineAt = at
	}
	return g, nil
}
