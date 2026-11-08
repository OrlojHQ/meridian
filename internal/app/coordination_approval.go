package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type grantPatchOutcome struct {
	SpawnApproval      domain.SpawnApproval `json:"spawnApproval,omitempty"`
	MaxChildren        int                  `json:"maxChildren,omitempty"`
	MaxDescendants     int                  `json:"maxDescendants,omitempty"`
	MaxLiveCapsules    int                  `json:"maxLiveCapsules,omitempty"`
	MaxMessagesPerLink int                  `json:"maxMessagesPerLink,omitempty"`
	AllowedHarnesses   []string             `json:"allowedHarnesses,omitempty"`
	DeadlineSeconds    int64                `json:"deadlineSeconds,omitempty"`
	OnDeadline         domain.OnDeadline    `json:"onDeadline,omitempty"`
	ArchiveAfter       int64                `json:"archiveAfterSeconds"`
	HasArchiveAfter    bool                 `json:"hasArchiveAfter"`
}

func (a grantPatchOutcome) equal(b grantPatchOutcome) bool {
	return a.SpawnApproval == b.SpawnApproval && a.MaxChildren == b.MaxChildren &&
		a.MaxDescendants == b.MaxDescendants && a.MaxLiveCapsules == b.MaxLiveCapsules &&
		a.MaxMessagesPerLink == b.MaxMessagesPerLink && a.DeadlineSeconds == b.DeadlineSeconds &&
		a.OnDeadline == b.OnDeadline && a.ArchiveAfter == b.ArchiveAfter &&
		a.HasArchiveAfter == b.HasArchiveAfter && slices.Equal(a.AllowedHarnesses, b.AllowedHarnesses)
}

func patchOutcome(patch domain.CoordinationGrantPatch) grantPatchOutcome {
	out := grantPatchOutcome{ArchiveAfter: -1}
	if patch.SpawnApproval != nil {
		out.SpawnApproval = *patch.SpawnApproval
	}
	if patch.MaxChildren != nil {
		out.MaxChildren = *patch.MaxChildren
	}
	if patch.MaxDescendants != nil {
		out.MaxDescendants = *patch.MaxDescendants
	}
	if patch.MaxLiveCapsules != nil {
		out.MaxLiveCapsules = *patch.MaxLiveCapsules
	}
	if patch.MaxMessagesPerLink != nil {
		out.MaxMessagesPerLink = *patch.MaxMessagesPerLink
	}
	if patch.AllowedHarnesses != nil {
		out.AllowedHarnesses = slices.Clone(*patch.AllowedHarnesses)
	}
	if patch.Deadline != nil {
		out.DeadlineSeconds = int64(*patch.Deadline / time.Second)
	}
	if patch.OnDeadline != nil {
		out.OnDeadline = *patch.OnDeadline
	}
	if patch.ArchiveAfter != nil {
		out.HasArchiveAfter = true
		out.ArchiveAfter = int64(*patch.ArchiveAfter / time.Second)
	}
	return out
}

// UpdateCoordinationGrant applies an operator patch to an active grant.
// Limits may be loosened or tightened. Pending spawns are left queued when
// spawnApproval becomes auto.
func (s *Service) UpdateCoordinationGrant(
	ctx context.Context,
	id domain.CoordinationGrantID,
	expected domain.ResourceVersion,
	patch domain.CoordinationGrantPatch,
	key string,
) (domain.CoordinationGrant, error) {
	if expected <= 0 {
		return domain.CoordinationGrant{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.CoordinationGrant{}, err
	}
	scope := "coordination_grant:" + string(id) + ":update"
	want := patchOutcome(patch)
	var grant domain.CoordinationGrant
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[grantPatchOutcome](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if !replay.equal(want) {
				return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
			}
			grant, err = tx.GetCoordinationGrant(ctx, id)
			return err
		}
		current, err := tx.GetCoordinationGrant(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if current.State != domain.GrantActive {
			return fmt.Errorf("%w: coordination grant is not active", domain.ErrIllegalTransition)
		}
		now := s.clock.Now().UTC()
		updated, err := current.Apply(patch, now)
		if err != nil {
			return err
		}
		if err := s.validateGrantHarnesses(ctx, tx, updated); err != nil {
			return err
		}
		previous := current.ResourceVersion
		updated.UpdatedAt = now
		updated.ResourceVersion++
		if err := tx.UpdateCoordinationGrant(ctx, updated, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "coordination_grant", string(updated.ID),
			"coordination_grant.updated", updated.ResourceVersion, nil); err != nil {
			return err
		}
		grant = updated
		return putReplay(ctx, tx, scope, key, want, now)
	})
	if err != nil {
		return domain.CoordinationGrant{}, err
	}
	s.invalidateCoordinationPush(grant.RootThreadID)
	return grant, nil
}

func (s *Service) validateGrantHarnesses(ctx context.Context, reader ports.Reader, grant domain.CoordinationGrant) error {
	project, err := reader.GetProject(ctx, grant.ProjectID)
	if err != nil {
		return err
	}
	for _, harness := range grant.AllowedHarnesses {
		if _, err := s.structuredHarnessImage(project, harness); err != nil {
			return fmt.Errorf("%w: coordination harness %q is not an allowlisted structured harness",
				domain.ErrInvalid, harness)
		}
	}
	return nil
}

// ListCoordinationRequests lists operator-visible coordination requests.
func (s *Service) ListCoordinationRequests(
	ctx context.Context, state domain.CoordinationRequestState,
) ([]domain.CoordinationRequest, error) {
	if state == "" {
		state = domain.RequestPendingApproval
	}
	switch state {
	case domain.RequestPendingApproval, domain.RequestCompleted, domain.RequestRejected:
	default:
		return nil, fmt.Errorf("%w: coordination request state must be pending_approval, completed or rejected",
			domain.ErrInvalid)
	}
	var requests []domain.CoordinationRequest
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		requests, err = reader.ListCoordinationRequests(ctx, state)
		return err
	})
	return requests, err
}

func (s *Service) GetCoordinationRequest(
	ctx context.Context, id domain.CoordinationRequestID,
) (domain.CoordinationRequest, error) {
	var request domain.CoordinationRequest
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		request, err = reader.GetCoordinationRequestByID(ctx, id)
		return err
	})
	return request, err
}

// ApproveCoordinationRequest starts a spawn the operator queued under
// spawnApproval=operator. The child Capsule is enqueued only after this call.
func (s *Service) ApproveCoordinationRequest(
	ctx context.Context, id domain.CoordinationRequestID, expected domain.ResourceVersion, key string,
) (domain.CoordinationRequest, error) {
	request, _, err := s.decideCoordinationRequest(ctx, id, expected, key, true)
	return request, err
}

// DenyCoordinationRequest refuses a queued spawn. The child Capsule is not
// deleted and still counts toward descendantsCreated.
func (s *Service) DenyCoordinationRequest(
	ctx context.Context, id domain.CoordinationRequestID, expected domain.ResourceVersion, key string,
) (domain.CoordinationRequest, error) {
	request, _, err := s.decideCoordinationRequest(ctx, id, expected, key, false)
	return request, err
}

type requestDecision struct {
	Approve bool `json:"approve"`
}

func (s *Service) decideCoordinationRequest(
	ctx context.Context,
	id domain.CoordinationRequestID,
	expected domain.ResourceVersion,
	key string,
	approve bool,
) (domain.CoordinationRequest, domain.ThreadLink, error) {
	if expected <= 0 {
		return domain.CoordinationRequest{}, domain.ThreadLink{},
			fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.CoordinationRequest{}, domain.ThreadLink{}, err
	}
	action := "deny"
	if approve {
		action = "approve"
	}
	scope := "coordination_request:" + string(id) + ":" + action
	want := requestDecision{Approve: approve}
	var request domain.CoordinationRequest
	var link domain.ThreadLink
	var enqueue domain.CapsuleID
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[requestDecision](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay != want {
				return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
			}
			request, err = tx.GetCoordinationRequestByID(ctx, id)
			if err != nil {
				return err
			}
			link, err = tx.GetThreadLink(ctx, request.LinkID)
			return err
		}
		current, err := tx.GetCoordinationRequestByID(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if current.Kind != domain.CoordinationSpawn {
			return fmt.Errorf("%w: only spawn requests can be approved or denied", domain.ErrInvalid)
		}
		if current.State != domain.RequestPendingApproval {
			return fmt.Errorf("%w: coordination request is not waiting for approval", domain.ErrIllegalTransition)
		}
		link, err = tx.GetThreadLink(ctx, current.LinkID)
		if err != nil {
			return err
		}
		if link.State != domain.LinkPendingApproval {
			return fmt.Errorf("%w: spawn is not waiting for approval", domain.ErrIllegalTransition)
		}
		now := s.clock.Now().UTC()
		if approve {
			if err := s.activateHeldSpawnTx(ctx, tx, current, link, now); err != nil {
				return err
			}
			enqueue = link.ChildCapsuleID
		} else {
			if err := s.closeLinkTx(ctx, tx, link, domain.LinkDenied,
				domain.ReportFailed, domain.ReportDenied, 0, now); err != nil {
				return err
			}
			previous := current.ResourceVersion
			current.State = domain.RequestRejected
			current.UpdatedAt = now
			current.ResourceVersion++
			if err := tx.UpdateCoordinationRequest(ctx, current, previous); err != nil {
				return err
			}
		}
		request, err = tx.GetCoordinationRequestByID(ctx, id)
		if err != nil {
			return err
		}
		link, err = tx.GetThreadLink(ctx, request.LinkID)
		if err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, want, now)
	})
	if err != nil {
		return domain.CoordinationRequest{}, domain.ThreadLink{}, err
	}
	if enqueue != "" {
		_ = s.queue.Enqueue(ctx, enqueue)
	}
	s.invalidateCoordinationPush(link.ParentThreadID)
	return request, link, nil
}

func (s *Service) activateHeldSpawnTx(
	ctx context.Context,
	tx ports.Transaction,
	request domain.CoordinationRequest,
	link domain.ThreadLink,
	now time.Time,
) error {
	grant, err := tx.GetCoordinationGrant(ctx, request.GrantID)
	if err != nil {
		return err
	}
	if !grant.Usable(now) {
		return fmt.Errorf("%w: coordination grant is not active", domain.ErrIllegalTransition)
	}
	if !slices.Contains(grant.AllowedHarnesses, link.Harness) {
		return fmt.Errorf("%w: coordination harness %q is not allowed by the coordination grant",
			domain.ErrInvalid, link.Harness)
	}
	live, err := s.liveTreeCapsules(ctx, tx, grant.ID)
	if err != nil {
		return err
	}
	if live >= grant.MaxLiveCapsules {
		return fmt.Errorf("%w: live child Capsule limit reached", domain.ErrConflict)
	}
	if s.coordMaxLive > 0 {
		total, err := tx.CountLiveCapsules(ctx)
		if err != nil {
			return err
		}
		if total >= s.coordMaxLive {
			return fmt.Errorf("%w: installation live Capsule limit reached", domain.ErrConflict)
		}
	}
	previousLink := link.ResourceVersion
	link.State = domain.LinkActive
	link.UpdatedAt = now
	link.ResourceVersion++
	if err := tx.UpdateThreadLink(ctx, link, previousLink); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, "thread_link", string(link.ID),
		"thread_link.approved", link.ResourceVersion, nil); err != nil {
		return err
	}
	previous := request.ResourceVersion
	request.State = domain.RequestCompleted
	request.UpdatedAt = now
	request.ResourceVersion++
	return tx.UpdateCoordinationRequest(ctx, request, previous)
}

func (s *Service) rejectPendingSpawnTx(
	ctx context.Context, tx ports.Transaction, link domain.ThreadLink, now time.Time,
) error {
	requests, err := tx.ListCoordinationRequests(ctx, domain.RequestPendingApproval)
	if err != nil {
		return err
	}
	for _, request := range requests {
		if request.LinkID != link.ID || request.Kind != domain.CoordinationSpawn {
			continue
		}
		previous := request.ResourceVersion
		request.State = domain.RequestRejected
		request.UpdatedAt = now
		request.ResourceVersion++
		return tx.UpdateCoordinationRequest(ctx, request, previous)
	}
	return nil
}

func spawnHeld(link domain.ThreadLink) bool {
	return link.State == domain.LinkPendingApproval
}

func (s *Service) spawnHeldForCapsule(ctx context.Context, reader ports.Reader, capsuleID domain.CapsuleID) (bool, error) {
	capsule, err := reader.GetCapsule(ctx, capsuleID)
	if err != nil {
		return false, err
	}
	if capsule.LinkID == "" {
		return false, nil
	}
	link, err := reader.GetThreadLink(ctx, capsule.LinkID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return spawnHeld(link), nil
}
