package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type CreateProjectThreadInput struct {
	ProjectID      domain.ProjectID
	Name           string
	Harness        string
	Prompt         string
	IdempotencyKey string
}

type ProjectThreadResult struct {
	Intent domain.ProjectThreadIntent
	Thread *domain.Thread
	Run    *domain.Run
}

func (s *Service) CreateProjectThread(
	ctx context.Context,
	input CreateProjectThreadInput,
) (ProjectThreadResult, error) {
	if s.structured == nil || s.transcriptKey == nil {
		return ProjectThreadResult{}, domain.ErrUnsupported
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name != "" {
		if _, err := requireName(input.Name); err != nil {
			return ProjectThreadResult{}, err
		}
	}
	input.Harness = strings.TrimSpace(input.Harness)
	if input.ProjectID == "" || input.Harness == "" || len(input.Harness) > 128 ||
		strings.ContainsAny(input.Harness, "/\\\x00\r\n") {
		return ProjectThreadResult{}, fmt.Errorf("%w: structured harness is invalid", domain.ErrInvalid)
	}
	if input.Prompt == "" {
		return ProjectThreadResult{}, fmt.Errorf("%w: first prompt is required", domain.ErrInvalid)
	}
	if len(input.Prompt) > adapterproto.MaxContentBytes {
		return ProjectThreadResult{}, fmt.Errorf("%w: message exceeds size limit", domain.ErrResourceExhausted)
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return ProjectThreadResult{}, err
	}

	var result ProjectThreadResult
	var replayed bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		existing, err := tx.GetProjectThreadIntentByKey(
			ctx, input.ProjectID, input.IdempotencyKey,
		)
		if err == nil {
			if err := s.matchProjectThreadReplay(ctx, tx, existing, input); err != nil {
				return err
			}
			result.Intent, replayed = existing, true
			return s.loadProjectThreadResult(ctx, tx, &result)
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if _, err := tx.GetProject(ctx, input.ProjectID); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		intentID := domain.ProjectThreadIntentID(s.ids.NewID())
		capsuleName := input.Name
		if capsuleName == "" {
			suffix := string(intentID)
			if len(suffix) > 12 {
				suffix = suffix[:12]
			}
			capsuleName = "thread-" + suffix
		}
		threadID := domain.ThreadID(s.ids.NewID())
		capsuleID := domain.CapsuleID(s.ids.NewID())
		timelineID := domain.TimelineID(s.ids.NewID())
		runID := domain.RunID(s.ids.NewID())
		messageID := domain.ThreadMessageID(s.ids.NewID())
		dek, err := s.transcriptKey.NewDEK()
		if err != nil {
			return err
		}
		defer wipe(dek)
		wrapped, err := s.transcriptKey.WrapDEK(threadID, dek)
		if err != nil {
			return err
		}
		keyID, keyVersion := s.transcriptKey.Metadata()
		threadEnvelope := domain.Thread{
			ID: threadID, CapsuleID: capsuleID, State: domain.ThreadActive,
			AdapterID: input.Harness, WrappedDEK: wrapped, KEKID: keyID,
			KEKVersion: keyVersion, EnvelopeVersion: transcripts.EnvelopeVersion,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		message, err := s.encryptThreadMessage(
			dek, threadEnvelope, messageID, 1, domain.ThreadRoleUser,
			domain.ThreadMessagePrompt, domain.ThreadBlockText,
			persistedThreadBlock{Content: input.Prompt}, now,
		)
		if err != nil {
			return err
		}
		capsule := domain.Capsule{
			ID: capsuleID, ProjectID: input.ProjectID, TimelineID: timelineID,
			Name: capsuleName, State: domain.CapsuleCreating,
			DesiredState: domain.IntentReady, RestoreComplete: true,
			LastActivityAt: now, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		if err := tx.InsertTimeline(ctx, domain.Timeline{
			ID: timelineID, ProjectID: input.ProjectID, CapsuleID: capsuleID,
			Reason: domain.TimelineRoot, CreatedAt: now,
		}); err != nil {
			return err
		}
		result.Intent = domain.ProjectThreadIntent{
			ID: intentID, ProjectID: input.ProjectID, CapsuleID: capsuleID,
			ThreadID: threadID, RunID: runID, MessageID: messageID,
			CapsuleName: capsuleName, RequestedName: input.Name,
			Harness: input.Harness, IdempotencyKey: input.IdempotencyKey,
			State: domain.ProjectThreadProvisioning, WrappedDEK: wrapped,
			KEKID: keyID, KEKVersion: keyVersion,
			EnvelopeVersion: transcripts.EnvelopeVersion, PendingMessage: message,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProjectThreadIntent(ctx, result.Intent); err != nil {
			return err
		}
		return s.appendEvent(
			ctx, tx, "capsule", string(capsuleID), "capsule.create_requested", 1, nil,
		)
	})
	if err != nil {
		return ProjectThreadResult{}, err
	}
	if result.Intent.State == domain.ProjectThreadProvisioning {
		if err := s.queue.Enqueue(ctx, result.Intent.CapsuleID); err != nil {
			return result, err
		}
	}
	if replayed && result.Intent.State == domain.ProjectThreadReady {
		err = s.retryProjectThreadDelivery(ctx, &result)
	}
	return result, err
}

func (s *Service) GetProjectThreadIntent(
	ctx context.Context,
	id domain.ProjectThreadIntentID,
) (ProjectThreadResult, error) {
	var result ProjectThreadResult
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result.Intent, err = reader.GetProjectThreadIntent(ctx, id)
		if err != nil {
			return err
		}
		return s.loadProjectThreadResult(ctx, reader, &result)
	})
	if err == nil && result.Intent.State == domain.ProjectThreadProvisioning {
		_ = s.queue.Enqueue(ctx, result.Intent.CapsuleID)
	}
	return result, err
}

func (s *Service) matchProjectThreadReplay(
	ctx context.Context,
	reader ports.Reader,
	intent domain.ProjectThreadIntent,
	input CreateProjectThreadInput,
) error {
	if intent.RequestedName != input.Name || intent.Harness != input.Harness {
		return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
	}
	var dek []byte
	var err error
	if len(intent.WrappedDEK) != 0 {
		if !s.transcriptKey.Matches(intent.KEKID, intent.KEKVersion) {
			return domain.ErrKeyMismatch
		}
		dek, err = s.transcriptKey.UnwrapDEK(
			intent.ThreadID, intent.KEKID, intent.KEKVersion, intent.WrappedDEK,
		)
	} else {
		thread, getErr := reader.GetThread(ctx, intent.ThreadID)
		if getErr != nil {
			return getErr
		}
		dek, err = s.unwrapThread(thread)
	}
	if err != nil {
		return err
	}
	defer wipe(dek)
	plaintext, err := transcripts.DecryptMessage(dek, intent.PendingMessage)
	if err != nil {
		return err
	}
	defer transcripts.ZeroPlaintextBlocks(plaintext)
	if len(plaintext) != 1 {
		return domain.ErrTranscriptCorrupt
	}
	var payload persistedThreadBlock
	if err := json.Unmarshal(plaintext[0], &payload); err != nil {
		return domain.ErrTranscriptCorrupt
	}
	if len(payload.Content) != len(input.Prompt) ||
		subtle.ConstantTimeCompare([]byte(payload.Content), []byte(input.Prompt)) != 1 {
		return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
	}
	return nil
}

func (s *Service) loadProjectThreadResult(
	ctx context.Context,
	reader ports.Reader,
	result *ProjectThreadResult,
) error {
	if result.Intent.State != domain.ProjectThreadReady {
		return nil
	}
	thread, err := reader.GetThread(ctx, result.Intent.ThreadID)
	if err != nil {
		return err
	}
	run, err := reader.GetRun(ctx, result.Intent.RunID)
	if err != nil {
		return err
	}
	result.Thread, result.Run = &thread, &run
	return nil
}

// ReconcileProjectThreadCapsule is the narrow coordinator invoked after
// Capsule lifecycle persistence. It never performs provider lifecycle work.
func (s *Service) ReconcileProjectThreadCapsule(
	ctx context.Context,
	capsule domain.Capsule,
) error {
	switch capsule.State {
	case domain.CapsuleReady:
		_, err := s.promoteProjectThread(ctx, capsule.ID, true)
		return err
	case domain.CapsuleFailed:
		return s.failProjectThread(ctx, capsule.ID, "capsule_failed", "Capsule provisioning failed")
	case domain.CapsuleDeleted:
		return s.failProjectThread(ctx, capsule.ID, "capsule_deleted", "Capsule was deleted")
	case domain.CapsuleSealed:
		return s.failProjectThread(ctx, capsule.ID, "capsule_sealed", "Capsule was sealed")
	default:
		return nil
	}
}

func (s *Service) RecoverProjectThreads(ctx context.Context) error {
	var intents []domain.ProjectThreadIntent
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		intents, err = reader.ListProvisioningProjectThreadIntents(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, intent := range intents {
		capsule, err := s.GetCapsule(ctx, intent.CapsuleID)
		if err != nil {
			return err
		}
		switch capsule.State {
		case domain.CapsuleReady:
			// Recovery may restore the durable runtime intent, but does not
			// resend the pending user message.
			if _, err := s.promoteProjectThread(ctx, capsule.ID, false); err != nil {
				return err
			}
		case domain.CapsuleFailed, domain.CapsuleDeleted, domain.CapsuleSealed:
			if err := s.ReconcileProjectThreadCapsule(ctx, capsule); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) promoteProjectThread(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	deliver bool,
) (ProjectThreadResult, error) {
	var result ProjectThreadResult
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		intents, err := tx.ListProvisioningProjectThreadIntents(ctx)
		if err != nil {
			return err
		}
		var intent domain.ProjectThreadIntent
		for _, candidate := range intents {
			if candidate.CapsuleID == capsuleID {
				intent = candidate
				break
			}
		}
		if intent.ID == "" {
			return nil
		}
		capsule, err := tx.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" {
			return nil
		}
		if _, err := tx.GetActiveThread(ctx, capsuleID); err == nil {
			return fmt.Errorf("%w: Capsule already has an active Thread", domain.ErrConflict)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		active, err := tx.HasActiveRun(ctx, capsuleID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w: Capsule already has an active Run", domain.ErrConflict)
		}
		now := s.clock.Now().UTC()
		thread := domain.Thread{
			ID: intent.ThreadID, CapsuleID: capsuleID, State: domain.ThreadActive,
			AdapterID: intent.Harness, WrappedDEK: intent.WrappedDEK,
			KEKID: intent.KEKID, KEKVersion: intent.KEKVersion,
			EnvelopeVersion: intent.EnvelopeVersion, CreatedAt: intent.CreatedAt,
			UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertThread(ctx, thread); err != nil {
			return err
		}
		if err := tx.AppendThreadMessage(ctx, intent.PendingMessage); err != nil {
			return err
		}
		thread, err = tx.GetThread(ctx, intent.ThreadID)
		if err != nil {
			return err
		}
		run := domain.Run{
			ID: intent.RunID, CapsuleID: capsuleID, Harness: intent.Harness,
			State: domain.RunQueued, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		previous := thread.ResourceVersion
		thread.CurrentRunID = run.ID
		thread.UpdatedAt = now
		thread.ResourceVersion++
		if err := tx.UpdateThread(ctx, thread, previous); err != nil {
			return err
		}
		if err := tx.InsertThreadDelivery(ctx, domain.ThreadDelivery{
			ThreadID: thread.ID, MessageID: intent.MessageID, RunID: run.ID,
			ControllerID: string(intent.MessageID), CreatedAt: now,
		}); err != nil {
			return err
		}
		previousIntent := intent.ResourceVersion
		intent.State = domain.ProjectThreadReady
		intent.WrappedDEK = nil
		intent.UpdatedAt = now
		intent.ResourceVersion++
		if err := tx.UpdateProjectThreadIntent(ctx, intent, previousIntent); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "run", string(run.ID), "run.queued", 1, nil); err != nil {
			return err
		}
		if err := s.touchCapsuleActivityTx(ctx, tx, capsuleID, now); err != nil {
			return err
		}
		result.Intent, result.Thread, result.Run = intent, &thread, &run
		return nil
	})
	if err != nil || result.Intent.ID == "" {
		return result, err
	}
	run, startErr := s.startStructuredRuntime(ctx, *result.Thread, *result.Run, false)
	result.Run = &run
	if startErr != nil || !deliver {
		// Runtime failures are represented by the durable Run. A failed
		// provider-side start must not turn a healthy Capsule into Failed.
		return result, nil
	}
	message := result.Intent.PendingMessage
	if err := s.deliverThreadMessage(ctx, *result.Thread, run, message,
		func(saved persistedThreadBlock) adapterproto.Frame {
			return adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
				ID: string(message.ID), SessionID: string(result.Thread.ID),
				Role: adapterproto.RoleUser, Content: saved.Content,
			}
		}); err != nil {
		return result, nil
	}
	return result, nil
}

func (s *Service) retryProjectThreadDelivery(
	ctx context.Context,
	result *ProjectThreadResult,
) error {
	if result.Thread == nil || result.Run == nil {
		return domain.ErrCorrupt
	}
	acknowledged, err := s.threadDeliveryAcknowledged(
		ctx, result.Thread.ID, result.Run.ID, result.Intent.MessageID,
	)
	if err != nil || acknowledged {
		return err
	}
	run := *result.Run
	if run.State == domain.RunQueued || run.State == domain.RunStarting {
		run, err = s.startStructuredRuntime(ctx, *result.Thread, run, false)
		result.Run = &run
	}
	if err != nil || run.State.Terminal() {
		return err
	}
	message := result.Intent.PendingMessage
	return s.deliverThreadMessage(ctx, *result.Thread, run, message,
		func(saved persistedThreadBlock) adapterproto.Frame {
			return adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
				ID: string(message.ID), SessionID: string(result.Thread.ID),
				Role: adapterproto.RoleUser, Content: saved.Content,
			}
		})
}

func (s *Service) failProjectThread(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	code, message string,
) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		intents, err := tx.ListProvisioningProjectThreadIntents(ctx)
		if err != nil {
			return err
		}
		for _, intent := range intents {
			if intent.CapsuleID != capsuleID {
				continue
			}
			previous := intent.ResourceVersion
			intent.State = domain.ProjectThreadFailed
			intent.FailureCode = code
			intent.FailureMessage = message
			intent.UpdatedAt = s.clock.Now().UTC()
			intent.ResourceVersion++
			return tx.UpdateProjectThreadIntent(ctx, intent, previous)
		}
		return nil
	})
}
