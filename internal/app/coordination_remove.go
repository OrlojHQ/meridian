package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// RemoveSubagent is the operator's per-subagent stop. It closes that one link
// as cancelled, so the lead hears the subagent was cancelled, ends the
// subagent's session, and deletes its Capsule once nothing runs there. The
// Coordination Grant is untouched, so the lead can keep spawning, and the
// deleted Capsule no longer counts toward the grant's live Capsules.
//
// Deletion keeps the subagent's encrypted Thread history, like any Capsule
// delete. A promoted subagent belongs to the operator as a main Capsule and is
// refused here.
func (s *Service) RemoveSubagent(
	ctx context.Context, id domain.ThreadLinkID, expected domain.ResourceVersion, key string,
) (domain.ThreadLink, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.ThreadLink{}, err
	}
	if expected <= 0 {
		return domain.ThreadLink{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.ThreadLink{}, err
	}
	scope := "thread_link:" + string(id) + ":remove"
	var link domain.ThreadLink
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[struct{}](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			link, err = tx.GetThreadLink(ctx, id)
			return err
		}
		current, err := tx.GetThreadLink(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if !current.PromotedAt.IsZero() {
			return fmt.Errorf("%w: a promoted subagent is a main Capsule; delete it from its Capsule", domain.ErrIllegalTransition)
		}
		capsule, err := tx.GetCapsule(ctx, current.ChildCapsuleID)
		if err != nil {
			return err
		}
		if capsuleRemoved(capsule) {
			return fmt.Errorf("%w: the subagent's Capsule is already removed", domain.ErrIllegalTransition)
		}
		if err := capsule.CanMutate(); err != nil {
			return err
		}
		live, err := tx.HasActiveRun(ctx, capsule.ID)
		if err != nil {
			return err
		}
		// Ending a session needs its Capsule running; a stopped Capsule keeps
		// the session until it resumes, and deletion waits for it to end.
		if live && (capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady) {
			return fmt.Errorf("%w: the subagent's Capsule is not running, so its session cannot be stopped; resume it first",
				domain.ErrConflict)
		}
		now := s.clock.Now().UTC()
		if current.State == domain.LinkPendingApproval {
			if err := s.rejectPendingSpawnTx(ctx, tx, current, now); err != nil {
				return err
			}
		}
		if current.State == domain.LinkActive || current.State == domain.LinkPendingApproval {
			if err := s.closeLinkTx(ctx, tx, current, domain.LinkCancelled,
				domain.ReportFailed, domain.ReportCancelled, 0, now); err != nil {
				return err
			}
		}
		link, err = tx.GetThreadLink(ctx, id)
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "thread_link", string(link.ID), "thread_link.remove_requested",
			link.ResourceVersion, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, struct{}{}, now)
	})
	if err != nil {
		return domain.ThreadLink{}, err
	}
	s.invalidateCoordinationPush(link.ParentThreadID)
	s.coordMu.Lock()
	if s.coordRemoving == nil {
		s.coordRemoving = make(map[domain.ThreadLinkID]bool)
	}
	s.coordRemoving[link.ID] = true
	s.coordMu.Unlock()
	s.removeClosedChild(ctx, link)
	if fresh, err := s.GetThreadLink(ctx, id); err == nil {
		link = fresh
	}
	return link, nil
}

// ThreadLinkCapsuleRemoved reports whether the subagent's Capsule is deleted
// or being deleted.
func (s *Service) ThreadLinkCapsuleRemoved(ctx context.Context, link domain.ThreadLink) bool {
	if requirePrincipal(ctx) != nil {
		return false
	}
	capsule, err := s.GetCapsule(ctx, link.ChildCapsuleID)
	if errors.Is(err, domain.ErrNotFound) {
		return true
	}
	return err == nil && capsuleRemoved(capsule)
}

func capsuleRemoved(capsule domain.Capsule) bool {
	return capsule.State == domain.CapsuleDeleted || capsule.State == domain.CapsuleDeleting ||
		capsule.DesiredState == domain.IntentDeleted
}

// removeClosedChild drives an operator removal: cancel the session, then
// delete the Capsule once no Run is active. The request and each
// coordination tick call it until it finishes. A session that will not end
// within the cancel grace is left to stopClosedChild, which pauses the
// Capsule, and the operator can remove it again.
func (s *Service) removeClosedChild(ctx context.Context, link domain.ThreadLink) {
	s.coordMu.Lock()
	removing := s.coordRemoving[link.ID]
	s.coordMu.Unlock()
	if !removing {
		return
	}
	capsule, err := s.GetCapsule(ctx, link.ChildCapsuleID)
	if errors.Is(err, domain.ErrNotFound) || (err == nil && capsuleRemoved(capsule)) {
		s.finishRemoval(link, true)
		return
	}
	if err != nil {
		return
	}
	if thread, err := s.GetThread(ctx, link.ChildThreadID); err == nil && thread.CurrentRunID != "" {
		run, err := s.GetRunStored(ctx, thread.CurrentRunID)
		if err == nil && run.State == domain.RunCancelling {
			// Pull the runtime's state so a session that finished stopping
			// since the last tick does not hold deletion.
			_ = s.reconcileStructuredRun(ctx, capsule, thread, run)
			run, err = s.GetRunStored(ctx, thread.CurrentRunID)
		}
		if err == nil && !run.State.Terminal() && run.State != domain.RunCancelling {
			_, _ = s.CancelThread(ctx, thread.ID, thread.ResourceVersion,
				"subagent-remove-"+string(link.ID)+"-"+strconv.FormatInt(int64(thread.ResourceVersion), 10))
			run, err = s.GetRunStored(ctx, thread.CurrentRunID)
		}
		if err == nil && !run.State.Terminal() {
			if s.clock.Now().Sub(run.UpdatedAt) >= coordinationCancelGrace {
				s.finishRemoval(link, false)
			}
			return
		}
		if capsule, err = s.GetCapsule(ctx, link.ChildCapsuleID); err != nil {
			return
		}
	}
	if _, err := s.DeleteCapsule(ctx, capsule.ID, capsule.ResourceVersion,
		"subagent-remove-"+string(link.ID)); err != nil {
		return
	}
	s.finishRemoval(link, true)
}

// finishRemoval drops the removal; a deleted Capsule is also marked stopped so
// the closed-link sweep does not try to pause it.
func (s *Service) finishRemoval(link domain.ThreadLink, deleted bool) {
	s.coordMu.Lock()
	delete(s.coordRemoving, link.ID)
	if deleted {
		if s.coordStopped == nil {
			s.coordStopped = make(map[domain.ThreadLinkID]bool)
		}
		s.coordStopped[link.ID] = true
	}
	s.coordMu.Unlock()
	s.invalidateCoordinationPush(link.ParentThreadID)
}

// orphanSweepBatch bounds how many subagents of deleted leads one tick
// deletes; the rest wait for the next tick.
const orphanSweepBatch = 100

// deleteOrphanedSubagents deletes every unpromoted subagent whose lead Capsule
// is deleted, being deleted, or gone. A subagent was started for its lead's
// task, so it goes with the lead whatever its state: idle, working, paused or
// failed, with or without unsaved changes. Each one is deleted as a direct
// operator delete with force would delete it: its sessions are ended (a
// harness that does not answer is ended locally), then its Capsule and
// workspace are deleted and its encrypted Thread history is kept. A promoted
// subagent is a main Capsule and is kept.
//
// The work is derived from stored state and retried every tick, so a daemon
// restart only delays it. A deleted subagent is itself a removed parent, so
// any subagents it started are deleted on a later tick.
func (s *Service) deleteOrphanedSubagents(ctx context.Context) {
	var links []domain.ThreadLink
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		links, err = reader.ListOrphanedSubagentLinks(ctx, orphanSweepBatch)
		return err
	}); err != nil {
		return
	}
	for _, link := range links {
		if ctx.Err() != nil {
			return
		}
		capsule, err := s.GetCapsule(ctx, link.ChildCapsuleID)
		if err != nil || capsuleRemoved(capsule) || capsule.Role != domain.CapsuleSub || capsule.CanMutate() != nil {
			continue
		}
		if _, err := s.ForceDeleteCapsule(ctx, capsule.ID, capsule.ResourceVersion,
			"lead-deleted-"+string(link.ID)); err != nil {
			continue
		}
		s.finishRemoval(link, true)
	}
}
