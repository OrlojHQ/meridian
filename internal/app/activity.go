package app

import (
	"context"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// TouchCapsuleActivity records accepted operator or runtime use. Periodic
// read-only polling must not call this helper unless it observes meaningful
// runtime activity.
func (s *Service) TouchCapsuleActivity(ctx context.Context, id domain.CapsuleID) error {
	now := s.clock.Now().UTC()
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		return s.touchCapsuleActivityTx(ctx, tx, id, now)
	})
}

func (s *Service) touchCapsuleActivityTx(
	ctx context.Context,
	tx ports.Transaction,
	id domain.CapsuleID,
	now time.Time,
) error {
	capsule, err := tx.GetCapsule(ctx, id)
	if err != nil {
		return err
	}
	if !now.After(capsule.LastActivityAt) {
		return nil
	}
	// Activity is operational liveness metadata, not a mutation precondition.
	// Updating it must not invalidate a separately reviewed Capsule RV.
	return tx.TouchCapsuleActivity(ctx, id, now)
}
