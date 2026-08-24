package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const idleScanBatchSize = 100

// StartIdleScanner enables idle pausing. A non-positive threshold leaves the
// feature disabled; callers must explicitly configure both values to start it.
func (r *Reconciler) StartIdleScanner(
	ctx context.Context,
	interval time.Duration,
	threshold time.Duration,
) error {
	if threshold <= 0 {
		return nil
	}
	if interval <= 0 {
		return fmt.Errorf("%w: idle scan interval must be positive", domain.ErrInvalid)
	}
	r.mu.Lock()
	if r.idleStarted {
		r.mu.Unlock()
		return nil
	}
	select {
	case <-r.done:
		r.mu.Unlock()
		return context.Canceled
	default:
	}
	r.idleStarted = true
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.done:
				return
			case <-ticker.C:
				_, _ = r.ScanIdleCapsules(ctx, threshold)
			}
		}
	}()
	return nil
}

// ScanIdleCapsules requests bounded, race-checked desired-state transitions.
// Provider pausing remains exclusively in the normal reconcile path.
func (r *Reconciler) ScanIdleCapsules(
	ctx context.Context,
	threshold time.Duration,
) (int, error) {
	if threshold <= 0 {
		return 0, nil
	}
	capabilities, err := r.provider.Capabilities(ctx)
	if err != nil {
		return 0, err
	}
	if !capabilities.Pause {
		return 0, nil
	}
	cutoff := r.clock.Now().UTC().Add(-threshold)
	var candidates []domain.Capsule
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		candidates, err = reader.ListIdleCapsules(ctx, cutoff, idleScanBatchSize)
		return err
	}); err != nil {
		return 0, err
	}
	paused := 0
	for _, candidate := range candidates {
		changed, err := r.requestIdlePause(ctx, candidate.ID, cutoff)
		if err != nil {
			return paused, err
		}
		if !changed {
			continue
		}
		paused++
		if err := r.Enqueue(ctx, candidate.ID); err != nil {
			return paused, err
		}
	}
	return paused, nil
}

func (r *Reconciler) requestIdlePause(
	ctx context.Context,
	id domain.CapsuleID,
	cutoff time.Time,
) (bool, error) {
	changed := false
	err := r.store.Transact(ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady ||
			capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" ||
			capsule.LastActivityAt.After(cutoff) {
			return nil
		}
		activeRun, err := tx.HasActiveRun(ctx, id)
		if err != nil {
			return err
		}
		if activeRun {
			return nil
		}
		if _, err := tx.GetActiveThread(ctx, id); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.DesiredState = domain.IntentPaused
		capsule.UpdatedAt = r.clock.Now().UTC()
		capsule.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, capsule, previous); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.Event{
			ID: domain.EventID(r.ids.NewID()), AggregateType: "capsule",
			AggregateID: string(id), Type: "capsule.idle_pause_requested",
			Timestamp: capsule.UpdatedAt, ResourceVersion: capsule.ResourceVersion,
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
