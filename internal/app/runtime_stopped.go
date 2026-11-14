package app

import (
	"context"
	"errors"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const runtimeStoppedFailure = "Capsule runtime stopped while this Run was active"

// failStoppedRuntimeRun records that a started Run's Capsule runtime stopped
// outside Meridian. The supervisor and the Run's processes are gone, so the
// Run fails and a structured Thread pauses with its transcript kept; resuming
// it starts a new session. Nothing is resent.
func (s *Service) failStoppedRuntimeRun(
	ctx context.Context, runID domain.RunID, threadID domain.ThreadID,
) error {
	if _, err := s.transitionRun(ctx, runID, domain.RunFailed, &runCompletion{
		Failure: runtimeStoppedFailure,
	}); err != nil && !errors.Is(err, domain.ErrConflict) &&
		!errors.Is(err, domain.ErrIllegalTransition) {
		return err
	}
	if threadID == "" {
		return nil
	}
	if err := s.pauseCompletedThread(ctx, threadID, runID); err != nil &&
		!errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return nil
}

// recheckStoppedRuntime asks the Capsule reconciler to look at a Capsule whose
// runtime was found stopped, so it can start the runtime again (ADR 0044).
func (s *Service) recheckStoppedRuntime(ctx context.Context, capsuleID domain.CapsuleID, err error) {
	if s.queue != nil && errors.Is(err, domain.ErrRuntimeStopped) {
		_ = s.queue.Enqueue(ctx, capsuleID)
	}
}

// FailStoppedRuntimeRuns fails every started, non-terminal Run of a Capsule
// whose runtime the reconciler found stopped and is about to start again. The
// restarted supervisor would not know those Runs. Queued Runs are left for
// startup recovery and explicit retries.
func (s *Service) FailStoppedRuntimeRuns(ctx context.Context, capsule domain.Capsule) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	var runs []domain.Run
	var threads []domain.Thread
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if runs, err = reader.ListRecoverableRuns(ctx); err != nil {
			return err
		}
		threads, err = reader.ListRecoverableThreads(ctx)
		return err
	}); err != nil {
		return err
	}
	threadByRun := make(map[domain.RunID]domain.ThreadID, len(threads))
	for _, thread := range threads {
		if thread.CapsuleID == capsule.ID {
			threadByRun[thread.CurrentRunID] = thread.ID
		}
	}
	for _, run := range runs {
		if run.CapsuleID != capsule.ID || run.State == domain.RunQueued {
			continue
		}
		if err := s.failStoppedRuntimeRun(ctx, run.ID, threadByRun[run.ID]); err != nil {
			return err
		}
	}
	return nil
}

const runtimeStoppedStartFailure = "Capsule runtime was stopped and is being started again; retry shortly"
