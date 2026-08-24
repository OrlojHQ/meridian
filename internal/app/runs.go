package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const maxPromptBytes = 256 << 10

type RunPage struct {
	Items      []domain.Run
	NextOffset int
}

type RunEventPage struct {
	Items      []domain.Event
	NextCursor int64
	More       bool
}

func (s *Service) StartRun(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	harnessName, prompt, idempotencyKey string,
	columns, rows uint16,
) (domain.Run, error) {
	if s.runtime == nil {
		return domain.Run{}, domain.ErrUnsupported
	}
	harnessName = strings.TrimSpace(harnessName)
	if harnessName == "" || len(harnessName) > 128 || strings.ContainsAny(harnessName, "/\\\x00\r\n") {
		return domain.Run{}, fmt.Errorf("%w: harness name is invalid", domain.ErrInvalid)
	}
	if len(prompt) > maxPromptBytes {
		return domain.Run{}, fmt.Errorf("%w: prompt exceeds size limit", domain.ErrInvalid)
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Run{}, err
	}
	scope := "run:start:" + string(capsuleID)
	var result domain.Run
	var replayed bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[domain.Run](ctx, tx, scope, idempotencyKey)
		if err != nil {
			return err
		}
		if ok {
			result, err = tx.GetRun(ctx, replay.ID)
			replayed = true
			return err
		}
		capsule, err := tx.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
			return fmt.Errorf("%w: Capsule must be Ready to start a Run", domain.ErrIllegalTransition)
		}
		if capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule maintenance is active", domain.ErrConflict)
		}
		active, err := tx.HasActiveRun(ctx, capsuleID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w: Capsule already has an active Run or Thread session", domain.ErrConflict)
		}
		now := s.clock.Now().UTC()
		result = domain.Run{
			ID: domain.RunID(s.ids.NewID()), CapsuleID: capsuleID, Harness: harnessName,
			State: domain.RunQueued, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertRun(ctx, result); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "run", string(result.ID), "run.queued", 1, nil); err != nil {
			return err
		}
		if err := s.touchCapsuleActivityTx(ctx, tx, capsuleID, now); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, idempotencyKey, result, now)
	})
	if err != nil || replayed {
		return result, err
	}
	if result, err = s.transitionRun(ctx, result.ID, domain.RunStarting, nil); err != nil {
		return domain.Run{}, err
	}
	capsule, err := s.GetCapsule(ctx, capsuleID)
	if err != nil {
		return domain.Run{}, err
	}
	project, err := s.GetProject(ctx, capsule.ProjectID)
	if err != nil {
		return domain.Run{}, err
	}
	secretValues, err := s.resolveHarnessSecrets(ctx, project)
	if err != nil {
		_, _ = s.transitionRun(ctx, result.ID, domain.RunFailed, &runCompletion{
			Failure: "Capsule runtime rejected Run start",
		})
		return domain.Run{}, err
	}
	started := time.Now()
	runtimeRun, err := s.runtime.StartRun(ctx, ports.RuntimeRunRequest{
		RunID: result.ID, ResourceID: capsule.ProviderResourceID, Harness: harnessName,
		Prompt: prompt, Columns: columns, Rows: rows,
		Secrets: cloneStringMap(secretValues),
	})
	clearStringMap(secretValues)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "run", operationResult(err), time.Since(started))
	}
	if err != nil {
		failure := "Capsule runtime rejected Run start"
		return s.transitionRun(ctx, result.ID, domain.RunFailed, &runCompletion{Failure: failure})
	}
	return s.applyRuntimeState(ctx, result.ID, runtimeRun)
}

func (s *Service) GetRun(ctx context.Context, id domain.RunID) (domain.Run, error) {
	var run domain.Run
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		run, err = reader.GetRun(ctx, id)
		return err
	})
	if err != nil || s.runtime == nil || run.State.Terminal() {
		return run, err
	}
	capsule, err := s.GetCapsule(ctx, run.CapsuleID)
	if err != nil {
		return domain.Run{}, err
	}
	if capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return run, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	runtimeRun, err := s.runtime.GetRun(ctx, capsule.ProviderResourceID, id)
	if err != nil {
		return domain.Run{}, err
	}
	return s.applyRuntimeState(ctx, id, runtimeRun)
}

func (s *Service) ListRuns(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	offset, limit int,
) (RunPage, error) {
	page := normalizePage(offset, limit)
	var result RunPage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetCapsule(ctx, capsuleID); err != nil {
			return err
		}
		var more bool
		var err error
		result.Items, more, err = reader.ListRuns(ctx, capsuleID, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) CancelRun(
	ctx context.Context,
	id domain.RunID,
	expected domain.ResourceVersion,
	idempotencyKey string,
) (domain.Run, error) {
	if s.runtime == nil {
		return domain.Run{}, domain.ErrUnsupported
	}
	if expected <= 0 {
		return domain.Run{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Run{}, err
	}
	scope := "run:cancel:" + string(id)
	var result domain.Run
	var capsule domain.Capsule
	var replayed bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[domain.Run](ctx, tx, scope, idempotencyKey)
		if err != nil {
			return err
		}
		if ok {
			result, err = tx.GetRun(ctx, replay.ID)
			replayed = true
			return err
		}
		result, err = tx.GetRun(ctx, id)
		if err != nil {
			return err
		}
		if result.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if result.State.Terminal() {
			return fmt.Errorf("%w: Run is already %s", domain.ErrIllegalTransition, result.State)
		}
		capsule, err = tx.GetCapsule(ctx, result.CapsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady ||
			capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
		}
		previous := result.ResourceVersion
		if err := result.Transition(domain.RunCancelling, s.clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateRun(ctx, result, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "run", string(id), "run.cancelling", result.ResourceVersion, nil); err != nil {
			return err
		}
		if err := s.touchCapsuleActivityTx(ctx, tx, capsule.ID, result.UpdatedAt); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, idempotencyKey, result, result.UpdatedAt)
	})
	if err != nil || replayed {
		return result, err
	}
	started := time.Now()
	runtimeRun, err := s.runtime.CancelRun(ctx, capsule.ProviderResourceID, id)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "cancel", operationResult(err), time.Since(started))
	}
	if err != nil {
		// Keep Cancelling: startup/periodic reconciliation safely retries.
		return result, nil
	}
	return s.applyRuntimeState(ctx, id, runtimeRun)
}

func (s *Service) RunEvents(
	ctx context.Context,
	id domain.RunID,
	after int64,
	limit int,
) (RunEventPage, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return RunEventPage{}, err
	}
	if s.runtime != nil {
		if err := s.syncRuntimeEvents(ctx, run); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return RunEventPage{}, err
		}
	}
	var result RunEventPage
	err = s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result.Items, result.More, err = reader.ListRunEvents(ctx, id, after, limit)
		if len(result.Items) > 0 {
			result.NextCursor = result.Items[len(result.Items)-1].Sequence
		} else {
			result.NextCursor = after
		}
		return err
	})
	return result, err
}

func (s *Service) GitStatus(ctx context.Context, capsuleID domain.CapsuleID) (ports.GitResult, error) {
	return s.git(ctx, capsuleID, false)
}

func (s *Service) GitDiff(ctx context.Context, capsuleID domain.CapsuleID) (ports.GitResult, error) {
	return s.git(ctx, capsuleID, true)
}

func (s *Service) git(ctx context.Context, capsuleID domain.CapsuleID, diff bool) (ports.GitResult, error) {
	if s.runtime == nil {
		return ports.GitResult{}, domain.ErrUnsupported
	}
	capsule, err := s.GetCapsule(ctx, capsuleID)
	if err != nil {
		return ports.GitResult{}, err
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return ports.GitResult{}, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	var result ports.GitResult
	if diff {
		result, err = s.runtime.GitDiff(ctx, capsule.ProviderResourceID)
	} else {
		result, err = s.runtime.GitStatus(ctx, capsule.ProviderResourceID)
	}
	if err == nil {
		err = s.TouchCapsuleActivity(ctx, capsuleID)
	}
	return result, err
}

func (s *Service) AttachRun(ctx context.Context, id domain.RunID, after uint64) (ports.RuntimeAttachment, error) {
	if s.runtime == nil {
		return nil, domain.ErrUnsupported
	}
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return nil, err
	}
	capsule, err := s.GetCapsule(ctx, run.CapsuleID)
	if err != nil {
		return nil, err
	}
	if capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return nil, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	started := time.Now()
	attachment, err := s.runtime.AttachRun(ctx, capsule.ProviderResourceID, id, after)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "attach", operationResult(err), time.Since(started))
	}
	if err == nil {
		err = s.TouchCapsuleActivity(ctx, capsule.ID)
	}
	return attachment, err
}

func (s *Service) RecoverRuns(ctx context.Context) error {
	if s.runtime == nil {
		return nil
	}
	var runs []domain.Run
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		runs, err = reader.ListRecoverableRuns(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, run := range runs {
		if run.State == domain.RunQueued {
			if _, err := s.transitionRun(ctx, run.ID, domain.RunFailed, &runCompletion{
				Failure: "Run start was interrupted before reaching the Capsule runtime",
			}); err != nil && !errors.Is(err, domain.ErrConflict) {
				return err
			}
			continue
		}
		capsule, err := s.GetCapsule(ctx, run.CapsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady ||
			capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" {
			continue
		}
		if run.State == domain.RunCancelling {
			runtimeRun, cancelErr := s.runtime.CancelRun(ctx, capsule.ProviderResourceID, run.ID)
			if cancelErr == nil {
				_, err = s.applyRuntimeState(ctx, run.ID, runtimeRun)
			}
		} else {
			runtimeRun, getErr := s.runtime.GetRun(ctx, capsule.ProviderResourceID, run.ID)
			if getErr == nil {
				_, err = s.applyRuntimeState(ctx, run.ID, runtimeRun)
			} else if errors.Is(getErr, domain.ErrNotFound) {
				_, err = s.transitionRun(ctx, run.ID, domain.RunFailed, &runCompletion{
					Failure: "Capsule runtime no longer knows this Run",
				})
			} else {
				err = getErr
			}
		}
		if err != nil && !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return nil
}

type runCompletion struct {
	ExitStatus *int
	Failure    string
}

func (s *Service) applyRuntimeState(
	ctx context.Context,
	id domain.RunID,
	runtimeRun ports.RuntimeRun,
) (domain.Run, error) {
	completion := &runCompletion{Failure: runtimeRun.Failure}
	if runtimeRun.HasExit {
		completion.ExitStatus = &runtimeRun.ExitStatus
	}
	return s.transitionRun(ctx, id, runtimeRun.State, completion)
}

func (s *Service) transitionRun(
	ctx context.Context,
	id domain.RunID,
	state domain.RunState,
	completion *runCompletion,
) (domain.Run, error) {
	var result domain.Run
	var previousState domain.RunState
	var changed bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRun(ctx, id)
		if err != nil {
			return err
		}
		if current.State == state {
			result = current
			return nil
		}
		previousState = current.State
		if current.State == domain.RunCancelling &&
			(state == domain.RunStarting || state == domain.RunRunning) {
			result = current
			return nil
		}
		previous := current.ResourceVersion
		if err := current.Transition(state, s.clock.Now()); err != nil {
			return err
		}
		changed = true
		if completion != nil {
			current.Failure = completion.Failure
			if completion.ExitStatus != nil {
				current.ExitStatus, current.HasExitStatus = *completion.ExitStatus, true
			}
		}
		if err := tx.UpdateRun(ctx, current, previous); err != nil {
			return err
		}
		if err := s.appendEvent(
			ctx, tx, "run", string(id), "run."+strings.ToLower(string(state)),
			current.ResourceVersion, nil,
		); err != nil {
			return err
		}
		if err := s.touchCapsuleActivityTx(
			ctx, tx, current.CapsuleID, current.UpdatedAt,
		); err != nil {
			return err
		}
		result = current
		return nil
	})
	if err == nil && changed && s.observer != nil {
		s.observer.RunTransition(string(previousState), string(state))
	}
	return result, err
}

func (s *Service) syncRuntimeEvents(ctx context.Context, run domain.Run) error {
	capsule, err := s.GetCapsule(ctx, run.CapsuleID)
	if err != nil {
		return err
	}
	events, err := s.runtime.RunEvents(ctx, capsule.ProviderResourceID, run.ID, run.EventCursor)
	if err != nil {
		return err
	}
	if len(events.Items) == 0 && events.NextCursor == run.EventCursor {
		return nil
	}
	unlock := s.lockRunEventSync(run.ID)
	defer unlock()
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
		cursor := current.EventCursor
		changed := false
		if events.Gap {
			if s.observer != nil {
				s.observer.EventGap()
			}
			availableFrom := uint64(0)
			for _, event := range events.Items {
				if event.Sequence > cursor && (availableFrom == 0 || event.Sequence < availableFrom) {
					availableFrom = event.Sequence
				}
			}
			if availableFrom == 0 && events.NextCursor > cursor {
				availableFrom = events.NextCursor + 1
			}
			if availableFrom > cursor+1 {
				metadata, _ := json.Marshal(map[string]any{
					"missingAfter":  cursor,
					"availableFrom": availableFrom,
				})
				if err := s.appendEvent(
					ctx, tx, "run", string(run.ID), "run.runtime_gap",
					current.ResourceVersion, metadata,
				); err != nil {
					return err
				}
				cursor = availableFrom - 1
				changed = true
			}
		}
		for _, event := range events.Items {
			if event.Sequence <= cursor {
				continue
			}
			metadata, _ := json.Marshal(map[string]any{
				"runtimeSequence": event.Sequence,
				"runtimeType":     event.Type,
			})
			if err := s.appendEvent(
				ctx, tx, "run", string(run.ID), "run.runtime_event",
				current.ResourceVersion, metadata,
			); err != nil {
				return err
			}
			cursor = event.Sequence
			changed = true
		}
		if events.NextCursor > cursor {
			cursor = events.NextCursor
			changed = true
		}
		if !changed {
			return nil
		}
		previous := current.ResourceVersion
		current.EventCursor = cursor
		current.UpdatedAt = s.clock.Now().UTC()
		current.ResourceVersion++
		if err := tx.UpdateRun(ctx, current, previous); err != nil {
			return err
		}
		return s.touchCapsuleActivityTx(ctx, tx, current.CapsuleID, current.UpdatedAt)
	})
}

func (s *Service) lockRunEventSync(id domain.RunID) func() {
	const offset64 = uint64(14695981039346656037)
	const prime64 = uint64(1099511628211)
	hash := offset64
	for _, value := range []byte(id) {
		hash ^= uint64(value)
		hash *= prime64
	}
	lock := &s.runSync[hash%uint64(len(s.runSync))]
	lock.Lock()
	return lock.Unlock
}
