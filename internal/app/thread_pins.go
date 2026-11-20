package app

import (
	"context"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// PinThread pins a Thread the operator comes back to, so it sits above the
// Project's other Threads (ADR 0059). Any live Thread of a main Capsule can
// be pinned, whether or not it leads subagents. A pin is presentation only:
// it grants nothing, no authorization check reads it, and only the Thread's
// owner sets it. Capsules never can, and no meridian.coord.v1 verb does.
func (s *Service) PinThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (domain.Thread, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Thread{}, err
	}
	return s.setThreadPin(ctx, id, expected, key, true)
}

// UnpinThread clears a pin. Unpinning a Thread that is not pinned changes
// nothing.
func (s *Service) UnpinThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (domain.Thread, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Thread{}, err
	}
	return s.setThreadPin(ctx, id, expected, key, false)
}

func (s *Service) setThreadPin(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string, pinned bool,
) (domain.Thread, error) {
	// Only the owner pins. The installation bearer in team mode sees no
	// Thread, and a Capsule or the system never pins on anyone's behalf, so
	// for them the Thread reads as not found, like any row outside scope.
	if caller, _ := principal.From(ctx); caller.Kind() != principal.KindUser {
		return domain.Thread{}, domain.ErrNotFound
	}
	if expected <= 0 {
		return domain.Thread{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Thread{}, err
	}
	operation := "unpin"
	if pinned {
		operation = "pin"
	}
	scope := "thread:" + string(id) + ":" + operation
	var result domain.Thread
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[threadOutcome](ctx, tx, scope, key)
		if err != nil {
			return err
		}
		if ok {
			result, err = tx.GetThread(ctx, replay.ThreadID)
			return err
		}
		current, err := tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		now := s.clock.Now().UTC()
		if pinned {
			if !current.State.Pinnable() {
				return fmt.Errorf("%w: only an active or paused Thread can be pinned", domain.ErrIllegalTransition)
			}
			capsule, err := tx.GetCapsule(ctx, current.CapsuleID)
			if err != nil {
				return err
			}
			if capsule.Role != domain.CapsuleMain {
				return fmt.Errorf("%w: a subagent Thread cannot be pinned; promote it first", domain.ErrIllegalTransition)
			}
		}
		if pinned != !current.PinnedAt.IsZero() {
			previous := current.ResourceVersion
			if pinned {
				current.PinnedAt = now
			} else {
				current.PinnedAt = time.Time{}
			}
			// A pin is not activity: updated_at stays, so recency and idle
			// pause are unchanged.
			current.ResourceVersion++
			if err := tx.UpdateThread(ctx, current, previous); err != nil {
				return err
			}
		}
		result = current
		return putReplay(ctx, tx, scope, key, threadOutcome{ThreadID: id}, now)
	})
	return result, err
}
