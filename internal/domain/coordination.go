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

// SpawnAuto is the only approval mode in this phase. Operator approval of
// each spawn needs an approval queue that does not exist yet.
const SpawnAuto SpawnApproval = "auto"

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
	case SpawnAuto:
	default:
		return p, fmt.Errorf("%w: coordination spawnApproval must be auto", ErrUnsupported)
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
	DescendantsCreated int
	RevokedAt          time.Time
	RevokeReason       GrantRevokeReason
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ResourceVersion    ResourceVersion
}

func (g CoordinationGrant) Policy() CoordinationPolicy {
	return CoordinationPolicy{
		MaxDepth:           g.MaxDepth,
		MaxChildren:        g.MaxChildren,
		MaxDescendants:     g.MaxDescendants,
		MaxLiveCapsules:    g.MaxLiveCapsules,
		MaxMessagesPerLink: g.MaxMessagesPerLink,
		AllowedHarnesses:   slices.Clone(g.AllowedHarnesses),
		OnDeadline:         g.OnDeadline,
		SpawnApproval:      g.SpawnApproval,
	}
}

// Usable reports whether the grant authorizes new coordination work at now.
func (g CoordinationGrant) Usable(now time.Time) bool {
	return g.State == GrantActive && now.Before(g.DeadlineAt)
}

type ThreadLinkState string

const (
	LinkActive ThreadLinkState = "active"
	LinkClosed ThreadLinkState = "closed"
)

type LinkCloseReason string

const (
	LinkCancelled LinkCloseReason = "cancelled"
	LinkFailed    LinkCloseReason = "failed"
	LinkEnded     LinkCloseReason = "ended"
	LinkRevoked   LinkCloseReason = "revoked"
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
	// OperatorMessages counts operator messages sent straight to the helper;
	// OperatorNoticed is how many of them a lead wake prompt has mentioned.
	OperatorMessages int
	OperatorNoticed  int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ResourceVersion  ResourceVersion
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

// CoordinationRequest is the durable idempotency record for one mutating
// coordination request. Result holds identifiers only, never content.
type CoordinationRequest struct {
	ID              CoordinationRequestID
	GrantID         CoordinationGrantID
	CapsuleID       CapsuleID
	Key             string
	CanonicalDigest string
	Kind            CoordinationRequestKind
	LinkID          ThreadLinkID
	ResultMessageID ThreadMessageID
	CreatedAt       time.Time
}
