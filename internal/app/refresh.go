package app

import (
	"context"
	"errors"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const liveRefreshBatchSize = 100

// RefreshLiveRuntime reconciles started, non-terminal Runs in Ready Capsules
// with their runtime so Run state and structured Thread transcripts stay
// current while no client is watching. It never starts, resends, or resumes
// work: queued Runs remain owned by startup recovery and explicit retries.
// Only persisted agent output records Capsule activity, so refreshing does not
// defeat idle pausing.
func (s *Service) RefreshLiveRuntime(ctx context.Context) error {
	if s.runtime == nil && s.structured == nil {
		return nil
	}
	var runs []domain.Run
	var threads []domain.Thread
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if runs, err = reader.ListRecoverableRuns(ctx); err != nil {
			return err
		}
		if s.structured != nil {
			threads, err = reader.ListRecoverableThreads(ctx)
		}
		return err
	}); err != nil {
		return err
	}
	threadByRun := make(map[domain.RunID]domain.Thread, len(threads))
	for _, thread := range threads {
		threadByRun[thread.CurrentRunID] = thread
	}

	var failures []error
	refreshed := 0
	for _, run := range runs {
		if refreshed == liveRefreshBatchSize {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if run.State == domain.RunQueued {
			continue
		}
		capsule, err := s.GetCapsule(ctx, run.CapsuleID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if capsule.State != domain.CapsuleReady ||
			capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" {
			continue
		}
		refreshed++
		if thread, ok := threadByRun[run.ID]; ok {
			err = s.reconcileStructuredRun(ctx, capsule, thread, run)
		} else if s.runtime != nil {
			_, err = s.GetRun(ctx, run.ID)
		}
		if err != nil && !errors.Is(err, domain.ErrConflict) &&
			!errors.Is(err, domain.ErrNotFound) &&
			!errors.Is(err, domain.ErrIllegalTransition) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
