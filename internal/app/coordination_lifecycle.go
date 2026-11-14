package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const (
	archiveSweepInterval = 30 * time.Second
	archiveSweepBatch    = 500
)

type promoteOutcome struct {
	Detach bool `json:"detach"`
}

// PromoteCapsule turns a subagent Capsule into a main Capsule. The link
// stays open unless detach is set, so the lead can still message the subagent
// and receive its reports. Detaching closes the link; a Capsule promoted
// earlier with its link kept can be detached later. Promotion grants no
// coordination authority and is never undone.
func (s *Service) PromoteCapsule(
	ctx context.Context, id domain.CapsuleID, expected domain.ResourceVersion, detach bool, key string,
) (domain.Capsule, error) {
	if expected <= 0 {
		return domain.Capsule{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Capsule{}, err
	}
	scope := "capsule:" + string(id) + ":promote"
	var result domain.Capsule
	var parent domain.ThreadID
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[promoteOutcome](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Detach != detach {
				return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
			}
			result, err = tx.GetCapsule(ctx, id)
			return err
		}
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if capsule.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if capsule.State == domain.CapsuleDeleting || capsule.State == domain.CapsuleDeleted ||
			capsule.DesiredState == domain.IntentDeleted {
			return fmt.Errorf("%w: Capsule is being deleted", domain.ErrIllegalTransition)
		}
		if capsule.Role != domain.CapsuleSub && (capsule.LinkID == "" || !detach) {
			return fmt.Errorf("%w: Capsule is already a main Capsule", domain.ErrConflict)
		}
		link, err := tx.GetThreadLink(ctx, capsule.LinkID)
		if err != nil {
			return err
		}
		if spawnHeld(link) {
			return fmt.Errorf("%w: spawn is waiting for operator approval", domain.ErrIllegalTransition)
		}
		parent = link.ParentThreadID
		now := s.clock.Now().UTC()
		previousLink := link.ResourceVersion
		if link.PromotedAt.IsZero() {
			link.PromotedAt = now
		}
		link.ArchivedAt = time.Time{}
		detached := detach && link.State == domain.LinkActive
		if detached {
			link.State, link.CloseReason = domain.LinkClosed, domain.LinkDetached
		}
		link.UpdatedAt = now
		link.ResourceVersion++
		if err := tx.UpdateThreadLink(ctx, link, previousLink); err != nil {
			return err
		}
		event, data := "thread_link.promoted", []byte(nil)
		if detached {
			event, data = "thread_link.closed", []byte(`{"reason":"detached"}`)
		}
		if err := s.appendEvent(ctx, tx, "thread_link", string(link.ID), event, link.ResourceVersion, data); err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		if capsule.Role == domain.CapsuleSub {
			capsule.Role, capsule.PromotedAt = domain.CapsuleMain, now
		}
		if detach {
			capsule.LeadThreadID, capsule.LinkID = "", ""
		}
		capsule.UpdatedAt = now
		capsule.ResourceVersion++
		if err := tx.PromoteCapsule(ctx, capsule, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "capsule", string(capsule.ID), "capsule.promoted",
			capsule.ResourceVersion, []byte(`{"detach":`+strconv.FormatBool(detach)+`}`)); err != nil {
			return err
		}
		result = capsule
		return putReplay(ctx, tx, scope, key, promoteOutcome{Detach: detach}, now)
	})
	if err != nil {
		return domain.Capsule{}, err
	}
	if parent != "" {
		s.invalidateCoordinationPush(parent)
	}
	return result, nil
}

// SweepArchivableSubagents archives finished subagents that stayed idle for their
// grant's archiveAfter, then deletes the Capsules of archived subagents whose
// work is saved. Archiving ends the idle session, pauses the Thread and marks
// the link. It never sends a message and never deletes a transcript; a
// workspace with unsaved changes is left to idle pause (ADR 0019).
func (s *Service) SweepArchivableSubagents(ctx context.Context) {
	if !s.CoordinationSupported() {
		return
	}
	var links []domain.ThreadLink
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		links, err = reader.ListArchivableThreadLinks(ctx, s.clock.Now().UTC(), archiveSweepBatch)
		return err
	}); err != nil {
		return
	}
	for _, link := range links {
		if ctx.Err() != nil {
			return
		}
		s.archiveSubagent(ctx, link)
	}
	s.cleanupArchivedSubagents(ctx)
}

// cleanupArchivedSubagents deletes the Capsule of each archived subagent once
// its session has ended, when its workspace holds nothing unsaved: the last
// report changed no files, or a Delivery of it succeeded. The link is left as
// it is and no report is written, so the lead is not woken; a later send to
// the subagent is refused because its Capsule is gone. The encrypted Thread
// history is kept, as with any Capsule delete. Deletion is retried on each
// sweep, so a daemon restart only delays it.
func (s *Service) cleanupArchivedSubagents(ctx context.Context) {
	var links []domain.ThreadLink
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		links, err = reader.ListArchivedLiveThreadLinks(ctx, archiveSweepBatch)
		return err
	}); err != nil {
		return
	}
	for _, link := range links {
		if ctx.Err() != nil {
			return
		}
		capsule, err := s.GetCapsule(ctx, link.ChildCapsuleID)
		if err != nil || capsuleRemoved(capsule) || capsule.CanMutate() != nil {
			continue
		}
		item, err := s.boardItem(ctx, capsule)
		if err != nil || !subagentWorkSaved(link, item.LatestDelivery) {
			continue
		}
		// The archive sweep asked the session to end. Pull the runtime's
		// state for a session still stopping; DeleteCapsule refuses while a
		// Run is active, so a later sweep finishes this.
		if thread := item.Thread; thread != nil && item.Run != nil && item.Run.State == domain.RunCancelling {
			_ = s.reconcileStructuredRun(ctx, capsule, *thread, *item.Run)
			if capsule, err = s.GetCapsule(ctx, capsule.ID); err != nil {
				continue
			}
		}
		if _, err := s.DeleteCapsule(ctx, capsule.ID, capsule.ResourceVersion,
			"subagent-cleanup-"+string(link.ID)); err != nil {
			continue
		}
		s.coordMu.Lock()
		if s.coordStopped == nil {
			s.coordStopped = make(map[domain.ThreadLinkID]bool)
		}
		s.coordStopped[link.ID] = true
		s.coordMu.Unlock()
		s.invalidateCoordinationPush(link.ParentThreadID)
	}
}

// subagentWorkSaved reports whether deleting the subagent's workspace loses
// nothing: its last report changed no files, or its latest Delivery shipped.
func subagentWorkSaved(link domain.ThreadLink, delivery *domain.Delivery) bool {
	return link.FilesChanged == 0 || (delivery != nil && delivery.State == domain.DeliverySucceeded)
}

func (s *Service) archiveSubagent(ctx context.Context, link domain.ThreadLink) {
	var grant domain.CoordinationGrant
	var capsule domain.Capsule
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if grant, err = reader.GetCoordinationGrant(ctx, link.GrantID); err != nil {
			return err
		}
		capsule, err = reader.GetCapsule(ctx, link.ChildCapsuleID)
		return err
	}); err != nil || grant.ArchiveAfter <= 0 || capsule.Role != domain.CapsuleSub {
		return
	}
	item, err := s.boardItem(ctx, capsule)
	if err != nil || item.Thread == nil || item.Thread.ID != link.ChildThreadID {
		return
	}
	// A subagent that needs the operator, is working, or has a delivery in
	// flight is never archived.
	if item.Status == BoardNeedsYou || item.Status == BoardWorking {
		return
	}
	thread := *item.Thread
	idleSince := latest(link.UpdatedAt, link.LastReportAt, thread.UpdatedAt, capsule.LastActivityAt)
	if s.clock.Now().Sub(idleSince) < grant.ArchiveAfter {
		return
	}
	live := thread.State == domain.ThreadActive && item.Run != nil &&
		item.Run.ID == thread.CurrentRunID && !item.Run.State.Terminal()
	// Cancelling needs the Capsule running. A stopped Capsule keeps its
	// session until it is resumed, and the sweep never resumes one.
	if live && (capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "") {
		return
	}
	// The link is marked before the session ends so the report scan does not
	// read the cancellation as the subagent's session ending.
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThreadLink(ctx, link.ID)
		if err != nil {
			return err
		}
		if !current.ArchivedAt.IsZero() || !current.PromotedAt.IsZero() ||
			current.ResourceVersion != link.ResourceVersion {
			return errArchiveRaced
		}
		thread, err := tx.GetThread(ctx, link.ChildThreadID)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		if thread.State == domain.ThreadActive && !live {
			previous := thread.ResourceVersion
			if err := thread.Transition(domain.ThreadPaused, now); err != nil {
				return err
			}
			if err := tx.UpdateThread(ctx, thread, previous); err != nil {
				return err
			}
		}
		previous := current.ResourceVersion
		current.ArchivedAt = now
		current.UpdatedAt = now
		current.ResourceVersion++
		if err := tx.UpdateThreadLink(ctx, current, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "thread_link", string(current.ID), "thread_link.archived",
			current.ResourceVersion, nil)
	})
	if err != nil {
		return
	}
	s.invalidateCoordinationPush(link.ParentThreadID)
	if live {
		_, _ = s.CancelThread(ctx, thread.ID, thread.ResourceVersion,
			"archive-"+string(link.ID)+"-"+strconv.FormatInt(int64(thread.ResourceVersion), 10))
	}
}

var errArchiveRaced = errors.New("subagent changed during archive")

// unarchiveTx clears a subagent's archived mark when its Thread resumes or the
// operator messages it, so it leaves the lead's Archived fold.
func (s *Service) unarchiveTx(
	ctx context.Context, tx ports.Transaction, thread domain.ThreadID, now time.Time,
) error {
	link, err := tx.GetThreadLinkByChildThread(ctx, thread)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil || link.ArchivedAt.IsZero() {
		return err
	}
	previous := link.ResourceVersion
	link.ArchivedAt = time.Time{}
	link.UpdatedAt = now
	link.ResourceVersion++
	if err := tx.UpdateThreadLink(ctx, link, previous); err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "thread_link", string(link.ID), "thread_link.unarchived",
		link.ResourceVersion, nil)
}

func latest(times ...time.Time) time.Time {
	var result time.Time
	for _, value := range times {
		if value.After(result) {
			result = value
		}
	}
	return result
}
