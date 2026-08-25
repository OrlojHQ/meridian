package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type ThreadPage struct {
	Items      []domain.Thread
	NextOffset int
}

type ThreadMutationResult struct {
	Thread    domain.Thread
	Run       *domain.Run
	MessageID domain.ThreadMessageID
}

type CreateThreadInput struct {
	CapsuleID      domain.CapsuleID
	Harness        string
	FirstMessage   string
	Start          bool
	IdempotencyKey string
}

type ThreadBlockView struct {
	ID              domain.ThreadBlockID     `json:"id"`
	MessageID       domain.ThreadMessageID   `json:"messageId"`
	MessageSequence int64                    `json:"messageSequence"`
	Sequence        int64                    `json:"sequence"`
	Role            domain.ThreadMessageRole `json:"role"`
	MessageKind     domain.ThreadMessageKind `json:"messageKind"`
	Kind            domain.ThreadBlockKind   `json:"kind"`
	Content         string                   `json:"content,omitempty"`
	Event           *ThreadAdapterEvent      `json:"event,omitempty"`
	CreatedAt       time.Time                `json:"createdAt"`
}

type ThreadAdapterEvent struct {
	Type            adapterproto.Kind        `json:"type"`
	RuntimeSequence uint64                   `json:"runtimeSequence,omitempty"`
	MessageID       string                   `json:"messageId,omitempty"`
	ResponseTo      string                   `json:"responseTo,omitempty"`
	ToolCallID      string                   `json:"toolCallId,omitempty"`
	ToolName        string                   `json:"toolName,omitempty"`
	Summary         string                   `json:"summary,omitempty"`
	Result          string                   `json:"result,omitempty"`
	Status          adapterproto.Status      `json:"status,omitempty"`
	Code            string                   `json:"code,omitempty"`
	Retryable       bool                     `json:"retryable,omitempty"`
	Reason          string                   `json:"reason,omitempty"`
	Permission      *adapterproto.Permission `json:"permission,omitempty"`
	Input           *adapterproto.Input      `json:"input,omitempty"`
	RequestedAfter  uint64                   `json:"requestedAfter,omitempty"`
	AvailableFrom   uint64                   `json:"availableFrom,omitempty"`
}

type ThreadBlockPage struct {
	Items      []ThreadBlockView   `json:"items"`
	NextCursor int64               `json:"nextCursor"`
	More       bool                `json:"more"`
	Gap        *ThreadAdapterEvent `json:"gap,omitempty"`
}

type threadOutcome struct {
	ThreadID  domain.ThreadID        `json:"threadId"`
	RunID     domain.RunID           `json:"runId,omitempty"`
	MessageID domain.ThreadMessageID `json:"messageId,omitempty"`
}

type persistedThreadBlock struct {
	Content     string              `json:"content,omitempty"`
	Event       *ThreadAdapterEvent `json:"event,omitempty"`
	Metadata    json.RawMessage     `json:"metadata,omitempty"`
	SessionID   string              `json:"sessionId,omitempty"`
	ResumeState string              `json:"resumeState,omitempty"`
}

func (s *Service) CreateThread(ctx context.Context, input CreateThreadInput) (ThreadMutationResult, error) {
	if s.structured == nil || s.transcriptKey == nil {
		return ThreadMutationResult{}, domain.ErrUnsupported
	}
	input.Harness = strings.TrimSpace(input.Harness)
	if input.CapsuleID == "" || input.Harness == "" || len(input.Harness) > 128 ||
		strings.ContainsAny(input.Harness, "/\\\x00\r\n") {
		return ThreadMutationResult{}, fmt.Errorf("%w: structured harness is invalid", domain.ErrInvalid)
	}
	if len(input.FirstMessage) > adapterproto.MaxContentBytes {
		return ThreadMutationResult{}, fmt.Errorf("%w: message exceeds size limit", domain.ErrResourceExhausted)
	}
	if input.Start && input.FirstMessage == "" {
		return ThreadMutationResult{}, fmt.Errorf("%w: an atomic start requires a first message", domain.ErrInvalid)
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return ThreadMutationResult{}, err
	}
	scope := "thread:create:" + string(input.CapsuleID)
	var outcome threadOutcome
	var result ThreadMutationResult
	var replayed bool
	var firstMessage domain.ThreadMessage
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[threadOutcome](ctx, tx, scope, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if ok {
			outcome, replayed = replay, true
			result.Thread, err = tx.GetThread(ctx, replay.ThreadID)
			if err != nil {
				return err
			}
			if replay.RunID != "" {
				run, runErr := tx.GetRun(ctx, replay.RunID)
				if runErr != nil {
					return runErr
				}
				result.Run = &run
			}
			result.MessageID = replay.MessageID
			return nil
		}
		capsule, err := tx.GetCapsule(ctx, input.CapsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
			return fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
		}
		if capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule maintenance is active", domain.ErrConflict)
		}
		if _, err := tx.GetActiveThread(ctx, input.CapsuleID); err == nil {
			return fmt.Errorf("%w: Capsule already has an active Thread", domain.ErrConflict)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if input.Start {
			active, err := tx.HasActiveRun(ctx, input.CapsuleID)
			if err != nil {
				return err
			}
			if active {
				return fmt.Errorf("%w: Capsule already has an active Run", domain.ErrConflict)
			}
		}
		now := s.clock.Now().UTC()
		threadID := domain.ThreadID(s.ids.NewID())
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
		result.Thread = domain.Thread{
			ID: threadID, CapsuleID: input.CapsuleID, State: domain.ThreadActive,
			AdapterID: input.Harness, WrappedDEK: wrapped, KEKID: keyID,
			KEKVersion: keyVersion, EnvelopeVersion: transcripts.EnvelopeVersion,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertThread(ctx, result.Thread); err != nil {
			return err
		}
		if input.FirstMessage != "" {
			messageID := domain.ThreadMessageID(s.ids.NewID())
			firstMessage, err = s.encryptThreadMessage(dek, result.Thread, messageID, 1,
				domain.ThreadRoleUser, domain.ThreadMessagePrompt, domain.ThreadBlockText,
				persistedThreadBlock{Content: input.FirstMessage}, now)
			if err != nil {
				return err
			}
			if err := tx.AppendThreadMessage(ctx, firstMessage); err != nil {
				return err
			}
			outcome.MessageID, result.MessageID = messageID, messageID
		}
		if input.Start {
			run := domain.Run{
				ID: domain.RunID(s.ids.NewID()), CapsuleID: input.CapsuleID,
				Harness: input.Harness, State: domain.RunQueued, CreatedAt: now,
				UpdatedAt: now, ResourceVersion: 1,
			}
			if err := tx.InsertRun(ctx, run); err != nil {
				return err
			}
			current, err := tx.GetThread(ctx, threadID)
			if err != nil {
				return err
			}
			previous := current.ResourceVersion
			current.CurrentRunID = run.ID
			current.UpdatedAt = now
			current.ResourceVersion++
			if err := tx.UpdateThread(ctx, current, previous); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, "run", string(run.ID), "run.queued", 1, nil); err != nil {
				return err
			}
			result.Thread, result.Run = current, &run
			outcome.RunID = run.ID
			if firstMessage.ID != "" {
				if err := tx.InsertThreadDelivery(ctx, domain.ThreadDelivery{
					ThreadID: threadID, MessageID: firstMessage.ID, RunID: run.ID,
					ControllerID: string(firstMessage.ID), CreatedAt: now,
				}); err != nil {
					return err
				}
			}
		} else {
			result.Thread, err = tx.GetThread(ctx, threadID)
			if err != nil {
				return err
			}
		}
		outcome.ThreadID = threadID
		if err := s.touchCapsuleActivityTx(ctx, tx, input.CapsuleID, now); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, input.IdempotencyKey, outcome, now)
	})
	if err != nil {
		return ThreadMutationResult{}, err
	}
	if input.Start {
		if replayed && outcome.MessageID != "" {
			firstMessage, err = s.findThreadMessage(ctx, outcome.ThreadID, outcome.MessageID)
			if err != nil {
				return result, err
			}
		}
		run := *result.Run
		var startErr error
		acknowledged := firstMessage.ID == ""
		if firstMessage.ID != "" {
			acknowledged, startErr = s.threadDeliveryAcknowledged(
				ctx, result.Thread.ID, run.ID, firstMessage.ID,
			)
		}
		if startErr == nil && (!replayed || run.State == domain.RunQueued ||
			run.State == domain.RunStarting) {
			run, startErr = s.startStructuredRuntime(ctx, result.Thread, run, false)
		}
		result.Run = &run
		if startErr == nil && firstMessage.ID != "" && !acknowledged {
			startErr = s.deliverThreadMessage(ctx, result.Thread, run, firstMessage,
				func(saved persistedThreadBlock) adapterproto.Frame {
					return adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
						ID: string(firstMessage.ID), SessionID: string(result.Thread.ID),
						Role: adapterproto.RoleUser, Content: saved.Content,
					}
				})
		}
		if fresh, getErr := s.GetThread(ctx, result.Thread.ID); getErr == nil {
			result.Thread = fresh
		}
		return result, startErr
	}
	return result, nil
}

func (s *Service) GetThread(ctx context.Context, id domain.ThreadID) (domain.Thread, error) {
	var thread domain.Thread
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		thread, err = reader.GetThread(ctx, id)
		return err
	})
	return thread, err
}

func (s *Service) ListThreads(ctx context.Context, capsuleID domain.CapsuleID, offset, limit int) (ThreadPage, error) {
	page := normalizePage(offset, limit)
	var result ThreadPage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetCapsule(ctx, capsuleID); err != nil {
			return err
		}
		var more bool
		var err error
		result.Items, more, err = reader.ListThreads(ctx, capsuleID, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) ArchiveThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (domain.Thread, error) {
	return s.mutateThreadState(ctx, id, expected, key, "archive", domain.ThreadArchived)
}

func (s *Service) mutateThreadState(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion,
	key, operation string, state domain.ThreadState,
) (domain.Thread, error) {
	if expected <= 0 {
		return domain.Thread{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Thread{}, err
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
		if current.CurrentRunID != "" {
			run, err := tx.GetRun(ctx, current.CurrentRunID)
			if err != nil {
				return err
			}
			if !run.State.Terminal() {
				return fmt.Errorf("%w: cancel the active Thread session first", domain.ErrConflict)
			}
			current.CurrentRunID = ""
		}
		previous := current.ResourceVersion
		if err := current.Transition(state, s.clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateThread(ctx, current, previous); err != nil {
			return err
		}
		result = current
		return putReplay(ctx, tx, scope, key, threadOutcome{ThreadID: id}, current.UpdatedAt)
	})
	return result, err
}

func (s *Service) DeleteThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (domain.Thread, error) {
	if expected <= 0 {
		return domain.Thread{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Thread{}, err
	}
	scope := "thread:" + string(id) + ":delete"
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
		if current.CurrentRunID != "" {
			run, err := tx.GetRun(ctx, current.CurrentRunID)
			if err != nil {
				return err
			}
			if !run.State.Terminal() {
				return fmt.Errorf("%w: cancel the active Thread session before crypto-shred", domain.ErrConflict)
			}
		}
		now := s.clock.Now().UTC()
		if err := tx.CryptoShredThread(ctx, id, expected, now); err != nil {
			return err
		}
		result, err = tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, threadOutcome{ThreadID: id}, now)
	})
	return result, err
}

func (s *Service) StartThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (ThreadMutationResult, error) {
	return s.beginThreadSession(ctx, id, expected, key, false)
}

func (s *Service) ResumeThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (ThreadMutationResult, error) {
	return s.beginThreadSession(ctx, id, expected, key, true)
}

func (s *Service) beginThreadSession(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string, resume bool,
) (ThreadMutationResult, error) {
	if s.structured == nil || s.transcriptKey == nil {
		return ThreadMutationResult{}, domain.ErrUnsupported
	}
	if expected <= 0 {
		return ThreadMutationResult{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return ThreadMutationResult{}, err
	}
	operation := "start"
	if resume {
		operation = "resume"
	}
	scope := "thread:" + string(id) + ":" + operation
	var result ThreadMutationResult
	var replayed bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[threadOutcome](ctx, tx, scope, key)
		if err != nil {
			return err
		}
		if ok {
			replayed = true
			result.Thread, err = tx.GetThread(ctx, replay.ThreadID)
			if err != nil {
				return err
			}
			run, err := tx.GetRun(ctx, replay.RunID)
			result.Run, result.MessageID = &run, replay.MessageID
			return err
		}
		thread, err := tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		if thread.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if thread.State != domain.ThreadActive && !(resume && thread.State == domain.ThreadPaused) {
			return fmt.Errorf("%w: Thread cannot %s from %s", domain.ErrIllegalTransition, operation, thread.State)
		}
		capsule, err := tx.GetCapsule(ctx, thread.CapsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady ||
			capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule is not available for a Thread session", domain.ErrIllegalTransition)
		}
		active, err := tx.HasActiveRun(ctx, thread.CapsuleID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w: Capsule already has an active Run", domain.ErrConflict)
		}
		now := s.clock.Now().UTC()
		run := domain.Run{
			ID: domain.RunID(s.ids.NewID()), CapsuleID: thread.CapsuleID, Harness: thread.AdapterID,
			State: domain.RunQueued, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		previous := thread.ResourceVersion
		thread.CurrentRunID = run.ID
		if thread.State == domain.ThreadPaused {
			if err := thread.Transition(domain.ThreadActive, now); err != nil {
				return err
			}
		} else {
			thread.UpdatedAt, thread.ResourceVersion = now, thread.ResourceVersion+1
		}
		if err := tx.UpdateThread(ctx, thread, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "run", string(run.ID), "run.queued", 1, nil); err != nil {
			return err
		}
		var messageID domain.ThreadMessageID
		if !resume {
			message, messageErr := tx.FindUndeliveredUserMessage(ctx, id)
			if messageErr == nil {
				messageID = message.ID
				if err := tx.InsertThreadDelivery(ctx, domain.ThreadDelivery{
					ThreadID: id, MessageID: message.ID, RunID: run.ID,
					ControllerID: string(message.ID), CreatedAt: now,
				}); err != nil {
					return err
				}
			} else if !errors.Is(messageErr, domain.ErrNotFound) {
				return messageErr
			}
		}
		result.Thread, result.Run = thread, &run
		result.MessageID = messageID
		if err := s.touchCapsuleActivityTx(ctx, tx, thread.CapsuleID, now); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key,
			threadOutcome{ThreadID: id, RunID: run.ID, MessageID: messageID}, now)
	})
	if err != nil {
		return ThreadMutationResult{}, err
	}
	run := *result.Run
	acknowledged := result.MessageID == ""
	if result.MessageID != "" {
		acknowledged, err = s.threadDeliveryAcknowledged(
			ctx, result.Thread.ID, run.ID, result.MessageID,
		)
	}
	if err == nil && (!replayed || run.State == domain.RunQueued || run.State == domain.RunStarting) {
		run, err = s.startStructuredRuntime(ctx, result.Thread, run, resume)
	}
	result.Run = &run
	if err == nil && result.MessageID != "" && !acknowledged {
		message, findErr := s.findThreadMessage(ctx, id, result.MessageID)
		if findErr != nil {
			err = findErr
		} else {
			err = s.deliverThreadMessage(ctx, result.Thread, run, message,
				func(saved persistedThreadBlock) adapterproto.Frame {
					return adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
						ID: string(message.ID), SessionID: string(result.Thread.ID),
						Role: adapterproto.RoleUser, Content: saved.Content,
					}
				})
		}
	}
	if fresh, getErr := s.GetThread(ctx, id); getErr == nil {
		result.Thread = fresh
	}
	return result, err
}

func (s *Service) SendThreadMessage(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion,
	content, key string,
) (ThreadMutationResult, error) {
	if content == "" || len(content) > adapterproto.MaxContentBytes {
		return ThreadMutationResult{}, fmt.Errorf("%w: message must be non-empty and bounded", domain.ErrInvalid)
	}
	frame := func(
		messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock,
	) adapterproto.Frame {
		return adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: string(messageID), SessionID: string(thread.ID), Role: adapterproto.RoleUser,
			Content: saved.Content,
		}
	}
	return s.appendAndSend(ctx, id, expected, key, "message", domain.ThreadRoleUser,
		domain.ThreadMessagePrompt, domain.ThreadBlockText, persistedThreadBlock{Content: content}, frame)
}

func (s *Service) RespondThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion,
	responseTo string, choice, inputValue *string, key string,
) (ThreadMutationResult, error) {
	if responseTo == "" || (choice == nil) == (inputValue == nil) {
		return ThreadMutationResult{}, fmt.Errorf("%w: exactly one permission choice or input value is required", domain.ErrInvalid)
	}
	event := &ThreadAdapterEvent{
		Type: adapterproto.KindPermissionResponse, ResponseTo: responseTo,
	}
	if choice != nil {
		event.Permission = &adapterproto.Permission{Kind: "permission", Choice: *choice}
	} else {
		event.Type = adapterproto.KindInputResponse
		event.Input = &adapterproto.Input{Value: *inputValue}
	}
	payload := persistedThreadBlock{Event: event}
	frame := func(
		messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock,
	) adapterproto.Frame {
		result := adapterproto.Frame{
			Protocol: adapterproto.Version, ID: string(messageID),
			SessionID: string(thread.ID),
		}
		if saved.Event != nil {
			result.ResponseTo = saved.Event.ResponseTo
		}
		if saved.Event != nil && saved.Event.Type == adapterproto.KindPermissionResponse {
			result.Type = adapterproto.KindPermissionResponse
			result.Permission = saved.Event.Permission
		} else {
			result.Type = adapterproto.KindInputResponse
			if saved.Event != nil {
				result.Input = saved.Event.Input
			}
		}
		return result
	}
	return s.appendAndSend(ctx, id, expected, key, "respond", domain.ThreadRoleUser,
		domain.ThreadMessageStatus, domain.ThreadBlockJSON, payload, frame)
}

func (s *Service) appendAndSend(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key, operation string,
	role domain.ThreadMessageRole, messageKind domain.ThreadMessageKind, blockKind domain.ThreadBlockKind,
	payload persistedThreadBlock,
	makeFrame func(domain.ThreadMessageID, domain.Thread, persistedThreadBlock) adapterproto.Frame,
) (ThreadMutationResult, error) {
	if s.structured == nil || s.transcriptKey == nil {
		return ThreadMutationResult{}, domain.ErrUnsupported
	}
	if expected <= 0 {
		return ThreadMutationResult{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return ThreadMutationResult{}, err
	}
	scope := "thread:" + string(id) + ":" + operation
	var result ThreadMutationResult
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[threadOutcome](ctx, tx, scope, key)
		if err != nil {
			return err
		}
		if ok {
			result.Thread, err = tx.GetThread(ctx, replay.ThreadID)
			if err != nil {
				return err
			}
			run, err := tx.GetRun(ctx, replay.RunID)
			result.Run, result.MessageID = &run, replay.MessageID
			return err
		}
		thread, err := tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		if thread.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
			return fmt.Errorf("%w: Thread has no active structured session", domain.ErrIllegalTransition)
		}
		run, err := tx.GetRun(ctx, thread.CurrentRunID)
		if err != nil {
			return err
		}
		if run.State.Terminal() || run.State == domain.RunCancelling {
			return fmt.Errorf("%w: Thread session is not accepting input", domain.ErrIllegalTransition)
		}
		dek, err := s.unwrapThread(thread)
		if err != nil {
			return err
		}
		defer wipe(dek)
		now := s.clock.Now().UTC()
		messageID := domain.ThreadMessageID(s.ids.NewID())
		message, err := s.encryptThreadMessage(dek, thread, messageID, thread.MessageCount+1,
			role, messageKind, blockKind, payload, now)
		if err != nil {
			return err
		}
		if err := tx.AppendThreadMessage(ctx, message); err != nil {
			return err
		}
		if err := tx.InsertThreadDelivery(ctx, domain.ThreadDelivery{
			ThreadID: id, MessageID: messageID, RunID: run.ID,
			ControllerID: string(messageID), CreatedAt: now,
		}); err != nil {
			return err
		}
		result.Thread, err = tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		result.Run, result.MessageID = &run, messageID
		if err := s.touchCapsuleActivityTx(ctx, tx, thread.CapsuleID, now); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key,
			threadOutcome{ThreadID: id, RunID: run.ID, MessageID: messageID}, now)
	})
	if err != nil {
		return ThreadMutationResult{}, err
	}
	acknowledged, err := s.threadDeliveryAcknowledged(
		ctx, result.Thread.ID, result.Run.ID, result.MessageID,
	)
	if err != nil || acknowledged {
		return result, err
	}
	savedMessage, err := s.findThreadMessage(ctx, id, result.MessageID)
	if err != nil {
		return result, err
	}
	savedPayload, err := s.decryptPersistedMessage(result.Thread, savedMessage)
	if err != nil {
		return result, err
	}
	frame := makeFrame(result.MessageID, result.Thread, savedPayload)
	err = s.deliverThreadMessage(ctx, result.Thread, *result.Run, savedMessage,
		func(persistedThreadBlock) adapterproto.Frame { return frame })
	return result, err
}

func (s *Service) CancelThread(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, key string,
) (ThreadMutationResult, error) {
	if s.structured == nil {
		return ThreadMutationResult{}, domain.ErrUnsupported
	}
	if expected <= 0 {
		return ThreadMutationResult{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return ThreadMutationResult{}, err
	}
	scope := "thread:" + string(id) + ":cancel"
	var result ThreadMutationResult
	var capsule domain.Capsule
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[threadOutcome](ctx, tx, scope, key)
		if err != nil {
			return err
		}
		if ok {
			result.Thread, err = tx.GetThread(ctx, replay.ThreadID)
			if err != nil {
				return err
			}
			run, err := tx.GetRun(ctx, replay.RunID)
			result.Run = &run
			capsule, _ = tx.GetCapsule(ctx, result.Thread.CapsuleID)
			return err
		}
		thread, err := tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		if thread.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if thread.CurrentRunID == "" {
			return fmt.Errorf("%w: Thread has no current session", domain.ErrIllegalTransition)
		}
		run, err := tx.GetRun(ctx, thread.CurrentRunID)
		if err != nil {
			return err
		}
		if run.State.Terminal() {
			return fmt.Errorf("%w: Thread session is already terminal", domain.ErrIllegalTransition)
		}
		capsule, err = tx.GetCapsule(ctx, thread.CapsuleID)
		if err != nil {
			return err
		}
		previous := run.ResourceVersion
		if run.State != domain.RunCancelling {
			if err := run.Transition(domain.RunCancelling, s.clock.Now()); err != nil {
				return err
			}
			if err := tx.UpdateRun(ctx, run, previous); err != nil {
				return err
			}
		}
		result.Thread, result.Run = thread, &run
		return putReplay(ctx, tx, scope, key, threadOutcome{ThreadID: id, RunID: run.ID}, s.clock.Now())
	})
	if err != nil {
		return ThreadMutationResult{}, err
	}
	runtimeRun, cancelErr := s.structured.CancelStructured(
		ctx, capsule.ProviderResourceID, result.Run.ID)
	if cancelErr == nil {
		run, applyErr := s.applyRuntimeState(ctx, result.Run.ID, runtimeRun)
		result.Run = &run
		if applyErr != nil {
			return result, applyErr
		}
		if run.State.Terminal() {
			if pauseErr := s.pauseCompletedThread(ctx, id, run.ID); pauseErr != nil {
				return result, pauseErr
			}
			if fresh, getErr := s.GetThread(ctx, id); getErr == nil {
				result.Thread = fresh
			}
		}
		return result, nil
	}
	return result, cancelErr
}

func (s *Service) ThreadBlocks(
	ctx context.Context, id domain.ThreadID, after int64, limit int,
) (ThreadBlockPage, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return ThreadBlockPage{}, fmt.Errorf("%w: invalid Thread block page", domain.ErrInvalid)
	}
	thread, err := s.GetThread(ctx, id)
	if err != nil {
		return ThreadBlockPage{}, err
	}
	if thread.State != domain.ThreadDeleted && thread.CurrentRunID != "" && s.structured != nil {
		if err := s.SyncThread(ctx, id); err != nil &&
			!errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrIllegalTransition) {
			return ThreadBlockPage{}, err
		}
		thread, err = s.GetThread(ctx, id)
		if err != nil {
			return ThreadBlockPage{}, err
		}
	}
	dek, err := s.unwrapThread(thread)
	if err != nil {
		return ThreadBlockPage{}, err
	}
	defer wipe(dek)
	var messages []domain.ThreadMessage
	var more bool
	err = s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, more, err = reader.ListThreadMessages(ctx, id, after, limit)
		return err
	})
	if err != nil {
		return ThreadBlockPage{}, err
	}
	result := ThreadBlockPage{More: more, NextCursor: after}
	for _, message := range messages {
		plaintext, err := transcripts.DecryptMessage(dek, message)
		if err != nil {
			return ThreadBlockPage{}, err
		}
		for index, raw := range plaintext {
			var payload persistedThreadBlock
			if err := json.Unmarshal(raw, &payload); err != nil {
				transcripts.ZeroPlaintextBlocks(plaintext)
				return ThreadBlockPage{}, domain.ErrTranscriptCorrupt
			}
			block := message.Blocks[index]
			view := ThreadBlockView{
				ID: block.ID, MessageID: message.ID, MessageSequence: message.Sequence,
				Sequence: block.Sequence, Role: message.Role, MessageKind: message.Kind,
				Kind: block.Kind, Content: payload.Content, Event: payload.Event,
				CreatedAt: block.CreatedAt,
			}
			if payload.Event != nil && payload.Event.Type == adapterproto.KindGap {
				result.Gap = payload.Event
			}
			result.Items = append(result.Items, view)
		}
		transcripts.ZeroPlaintextBlocks(plaintext)
		result.NextCursor = message.Sequence
	}
	return result, nil
}

func (s *Service) SyncThread(ctx context.Context, id domain.ThreadID) error {
	if s.structured == nil || s.transcriptKey == nil {
		return domain.ErrUnsupported
	}
	unlock := s.lockThreadSync(id)
	defer unlock()
	thread, err := s.GetThread(ctx, id)
	if err != nil {
		return err
	}
	if thread.State == domain.ThreadDeleted || thread.CurrentRunID == "" {
		return nil
	}
	dek, err := s.unwrapThread(thread)
	if err != nil {
		return err
	}
	defer wipe(dek)
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	if err != nil {
		return err
	}
	capsule, err := s.GetCapsule(ctx, thread.CapsuleID)
	if err != nil {
		return err
	}
	if capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	events, err := s.structured.StructuredEvents(
		ctx, capsule.ProviderResourceID, run.ID, run.EventCursor)
	if err != nil {
		return err
	}
	if len(events.Items) == 0 && events.NextCursor == run.EventCursor && !events.Gap {
		return nil
	}
	var endStatus adapterproto.Status
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		currentThread, err := tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		currentRun, err := tx.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
		cursor := currentRun.EventCursor
		messageSequence := currentThread.MessageCount
		now := s.clock.Now().UTC()
		appendGap := func(requested, available uint64) error {
			if available <= requested+1 {
				return nil
			}
			messageSequence++
			payload := persistedThreadBlock{Event: &ThreadAdapterEvent{
				Type: adapterproto.KindGap, RequestedAfter: requested, AvailableFrom: available,
			}}
			message, err := s.encryptThreadMessage(dek, currentThread,
				domain.ThreadMessageID(s.ids.NewID()), messageSequence,
				domain.ThreadRoleSystem, domain.ThreadMessageStatus,
				domain.ThreadBlockError, payload, now)
			if err != nil {
				return err
			}
			if err := tx.AppendThreadMessage(ctx, message); err != nil {
				return err
			}
			if s.observer != nil {
				s.observer.EventGap()
			}
			return nil
		}
		if events.Gap {
			available := events.NextCursor + 1
			if len(events.Items) > 0 {
				available = events.Items[0].Sequence
			}
			if available > cursor+1 {
				if err := appendGap(cursor, available); err != nil {
					return err
				}
				cursor = available - 1
			}
		}
		for _, event := range events.Items {
			if event.Sequence <= cursor {
				continue
			}
			if event.Sequence > cursor+1 {
				if err := appendGap(cursor, event.Sequence); err != nil {
					return err
				}
				cursor = event.Sequence - 1
			}
			role, messageKind, blockKind, payload, persist := mapStructuredEvent(event)
			if persist {
				messageSequence++
				message, err := s.encryptThreadMessage(dek, currentThread,
					domain.ThreadMessageID(s.ids.NewID()), messageSequence,
					role, messageKind, blockKind, payload, now)
				if err != nil {
					return err
				}
				if err := tx.AppendThreadMessage(ctx, message); err != nil {
					return err
				}
			}
			if event.Frame.Type == adapterproto.KindEnd {
				endStatus = event.Frame.Status
			}
			cursor = event.Sequence
		}
		if events.NextCursor > cursor {
			if err := appendGap(cursor, events.NextCursor+1); err != nil {
				return err
			}
			cursor = events.NextCursor
		}
		if cursor == currentRun.EventCursor {
			return nil
		}
		previous := currentRun.ResourceVersion
		currentRun.EventCursor = cursor
		currentRun.UpdatedAt = now
		currentRun.ResourceVersion++
		if err := tx.UpdateRun(ctx, currentRun, previous); err != nil {
			return err
		}
		return s.touchCapsuleActivityTx(ctx, tx, currentThread.CapsuleID, now)
	})
	if err != nil {
		return err
	}
	if endStatus != "" {
		status := domain.RunFailed
		switch endStatus {
		case adapterproto.StatusSucceeded:
			status = domain.RunSucceeded
		case adapterproto.StatusCancelled:
			status = domain.RunCancelled
		}
		_, err = s.transitionRun(ctx, run.ID, status, &runCompletion{})
		if err == nil {
			err = s.pauseCompletedThread(ctx, id, run.ID)
		}
	}
	return err
}

func (s *Service) RecoverThreads(ctx context.Context) error {
	if s.structured == nil {
		return nil
	}
	var threads []domain.Thread
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		threads, err = reader.ListRecoverableThreads(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, thread := range threads {
		capsule, err := s.GetCapsule(ctx, thread.CapsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady ||
			capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" {
			continue
		}
		run, err := s.GetRunStored(ctx, thread.CurrentRunID)
		if err != nil {
			return err
		}
		if run.State == domain.RunQueued {
			// Reconcile only the durable session intent. The first/user messages
			// remain unsent until the caller retries their idempotent mutation.
			_, _ = s.startStructuredRuntime(ctx, thread, run, false)
			continue
		}
		runtimeRun, getErr := s.structured.GetStructured(ctx, capsule.ProviderResourceID, run.ID)
		if getErr == nil {
			if _, err := s.applyRuntimeState(ctx, run.ID, runtimeRun); err != nil &&
				!errors.Is(err, domain.ErrConflict) {
				return err
			}
			if err := s.SyncThread(ctx, thread.ID); err != nil &&
				!errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		} else if errors.Is(getErr, domain.ErrNotFound) {
			if _, err := s.transitionRun(ctx, run.ID, domain.RunFailed, &runCompletion{
				Failure: "Capsule runtime no longer knows this structured session",
			}); err != nil && !errors.Is(err, domain.ErrConflict) {
				return err
			}
			if err := s.pauseCompletedThread(ctx, thread.ID, run.ID); err != nil &&
				!errors.Is(err, domain.ErrConflict) {
				return err
			}
		}
	}
	return nil
}

func (s *Service) ListHarnessProfiles(
	ctx context.Context, capsuleID domain.CapsuleID,
) ([]ports.RuntimeHarnessProfile, error) {
	if s.profiles == nil {
		return nil, domain.ErrUnsupported
	}
	capsule, err := s.GetCapsule(ctx, capsuleID)
	if err != nil {
		return nil, err
	}
	if capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return nil, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	return s.profiles.HarnessProfiles(ctx, capsule.ProviderResourceID)
}

func (s *Service) GetRunStored(ctx context.Context, id domain.RunID) (domain.Run, error) {
	var run domain.Run
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		run, err = reader.GetRun(ctx, id)
		return err
	})
	return run, err
}

func (s *Service) startStructuredRuntime(
	ctx context.Context, thread domain.Thread, run domain.Run, resume bool,
) (domain.Run, error) {
	if run.State == domain.RunQueued {
		var err error
		run, err = s.transitionRun(ctx, run.ID, domain.RunStarting, nil)
		if err != nil {
			return domain.Run{}, err
		}
	}
	capsule, err := s.GetCapsule(ctx, thread.CapsuleID)
	if err != nil {
		return run, err
	}
	if capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return run, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	frame := adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStart,
		ID: "start-" + string(thread.ID), SessionID: string(thread.ID),
	}
	project, err := s.GetProject(ctx, capsule.ProjectID)
	if err != nil {
		return run, err
	}
	secretValues, err := s.resolveHarnessSecrets(ctx, project)
	if err != nil {
		return run, err
	}
	if resume {
		state, sessionID, err := s.latestResumeState(ctx, thread)
		if err != nil {
			return run, err
		}
		frame.Type, frame.ResumeState = adapterproto.KindResume, state
		if sessionID != "" {
			frame.SessionID = sessionID
		}
	}
	runtimeRun, err := s.structured.StartStructured(ctx, ports.RuntimeStructuredStartRequest{
		RunID: run.ID, ResourceID: capsule.ProviderResourceID, Harness: thread.AdapterID, Frame: frame,
		Secrets: cloneStringMap(secretValues),
	})
	clearStringMap(secretValues)
	if err != nil {
		failed, transitionErr := s.transitionRun(ctx, run.ID, domain.RunFailed, &runCompletion{
			Failure: "Capsule runtime rejected structured session start",
		})
		if transitionErr != nil {
			return run, transitionErr
		}
		if pauseErr := s.pauseCompletedThread(ctx, thread.ID, run.ID); pauseErr != nil {
			return failed, pauseErr
		}
		return failed, err
	}
	run, err = s.applyRuntimeState(ctx, run.ID, runtimeRun)
	if err != nil {
		return run, err
	}
	if run.State.Terminal() {
		if err := s.pauseCompletedThread(ctx, thread.ID, run.ID); err != nil {
			return run, err
		}
		return run, nil
	}
	return run, nil
}

func (s *Service) deliverThreadMessage(
	ctx context.Context,
	thread domain.Thread,
	run domain.Run,
	message domain.ThreadMessage,
	makeFrame func(persistedThreadBlock) adapterproto.Frame,
) error {
	var delivery domain.ThreadDelivery
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		delivery, err = reader.GetThreadDelivery(ctx, message.ID)
		return err
	})
	if err != nil {
		return err
	}
	if !delivery.DeliveredAt.IsZero() {
		return nil
	}
	if delivery.ThreadID != thread.ID || delivery.RunID != run.ID ||
		delivery.ControllerID != string(message.ID) {
		return domain.ErrTranscriptCorrupt
	}
	payload, err := s.decryptPersistedMessage(thread, message)
	if err != nil {
		return err
	}
	frame := makeFrame(payload)
	if frame.ID != delivery.ControllerID {
		return domain.ErrTranscriptCorrupt
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	capsule, err := s.GetCapsule(ctx, thread.CapsuleID)
	if err != nil {
		return err
	}
	if capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" {
		return fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	if err := s.structured.SendStructured(ctx, ports.RuntimeStructuredSendRequest{
		ResourceID: capsule.ProviderResourceID, RunID: run.ID, Frame: frame,
	}); err != nil {
		return err
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		now := s.clock.Now().UTC()
		if err := tx.AcknowledgeThreadDelivery(ctx, message.ID, now); err != nil {
			return err
		}
		return s.touchCapsuleActivityTx(ctx, tx, thread.CapsuleID, now)
	})
}

func (s *Service) threadDeliveryAcknowledged(
	ctx context.Context,
	threadID domain.ThreadID,
	runID domain.RunID,
	messageID domain.ThreadMessageID,
) (bool, error) {
	var delivery domain.ThreadDelivery
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		delivery, err = reader.GetThreadDelivery(ctx, messageID)
		return err
	})
	if err != nil {
		return false, err
	}
	if delivery.ThreadID != threadID || delivery.RunID != runID ||
		delivery.ControllerID != string(messageID) {
		return false, domain.ErrTranscriptCorrupt
	}
	return !delivery.DeliveredAt.IsZero(), nil
}

func (s *Service) encryptThreadMessage(
	dek []byte, thread domain.Thread, messageID domain.ThreadMessageID, sequence int64,
	role domain.ThreadMessageRole, messageKind domain.ThreadMessageKind,
	blockKind domain.ThreadBlockKind, payload persistedThreadBlock, now time.Time,
) (domain.ThreadMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return domain.ThreadMessage{}, err
	}
	defer wipe(encoded)
	return transcripts.EncryptMessage(dek, transcripts.MessageInput{
		ID: messageID, ThreadID: thread.ID, Sequence: sequence, Role: role,
		Kind: messageKind, CreatedAt: now,
		Blocks: []transcripts.PlaintextBlock{{
			ID: domain.ThreadBlockID(s.ids.NewID()), Kind: blockKind, Content: encoded,
		}},
	})
}

func (s *Service) unwrapThread(thread domain.Thread) ([]byte, error) {
	if thread.State == domain.ThreadDeleted || len(thread.WrappedDEK) == 0 {
		return nil, domain.ErrKeyMismatch
	}
	if s.transcriptKey == nil || !s.transcriptKey.Matches(thread.KEKID, thread.KEKVersion) {
		return nil, domain.ErrKeyMismatch
	}
	return s.transcriptKey.UnwrapDEK(thread.ID, thread.KEKID, thread.KEKVersion, thread.WrappedDEK)
}

func (s *Service) decryptPersistedMessage(
	thread domain.Thread,
	message domain.ThreadMessage,
) (persistedThreadBlock, error) {
	dek, err := s.unwrapThread(thread)
	if err != nil {
		return persistedThreadBlock{}, err
	}
	defer wipe(dek)
	plaintext, err := transcripts.DecryptMessage(dek, message)
	if err != nil {
		return persistedThreadBlock{}, err
	}
	defer transcripts.ZeroPlaintextBlocks(plaintext)
	if len(plaintext) != 1 {
		return persistedThreadBlock{}, domain.ErrTranscriptCorrupt
	}
	var payload persistedThreadBlock
	if err := json.Unmarshal(plaintext[0], &payload); err != nil {
		return persistedThreadBlock{}, domain.ErrTranscriptCorrupt
	}
	return payload, nil
}

func (s *Service) findThreadMessage(
	ctx context.Context, threadID domain.ThreadID, messageID domain.ThreadMessageID,
) (domain.ThreadMessage, error) {
	var after int64
	for {
		var messages []domain.ThreadMessage
		var more bool
		err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			messages, more, err = reader.ListThreadMessages(ctx, threadID, after, 100)
			return err
		})
		if err != nil {
			return domain.ThreadMessage{}, err
		}
		for _, message := range messages {
			if message.ID == messageID {
				return message, nil
			}
			after = message.Sequence
		}
		if !more {
			return domain.ThreadMessage{}, domain.ErrNotFound
		}
	}
}

func (s *Service) latestResumeState(
	ctx context.Context, thread domain.Thread,
) (string, string, error) {
	dek, err := s.unwrapThread(thread)
	if err != nil {
		return "", "", err
	}
	defer wipe(dek)
	var after int64
	var state, sessionID string
	for {
		var messages []domain.ThreadMessage
		var more bool
		err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			messages, more, err = reader.ListThreadMessages(ctx, thread.ID, after, 100)
			return err
		})
		if err != nil {
			return "", "", err
		}
		for _, message := range messages {
			plaintext, err := transcripts.DecryptMessage(dek, message)
			if err != nil {
				return "", "", err
			}
			for _, value := range plaintext {
				var payload persistedThreadBlock
				if json.Unmarshal(value, &payload) != nil {
					transcripts.ZeroPlaintextBlocks(plaintext)
					return "", "", domain.ErrTranscriptCorrupt
				}
				if payload.ResumeState != "" {
					state, sessionID = payload.ResumeState, payload.SessionID
				}
			}
			transcripts.ZeroPlaintextBlocks(plaintext)
			after = message.Sequence
		}
		if !more {
			break
		}
	}
	if state == "" {
		return "", "", fmt.Errorf("%w: Thread has no adapter resume state", domain.ErrUnsupported)
	}
	return state, sessionID, nil
}

func mapStructuredEvent(event ports.RuntimeStructuredEvent) (
	domain.ThreadMessageRole, domain.ThreadMessageKind, domain.ThreadBlockKind,
	persistedThreadBlock, bool,
) {
	frame := event.Frame
	public := &ThreadAdapterEvent{
		Type: frame.Type, RuntimeSequence: event.Sequence, MessageID: frame.MessageID,
		ResponseTo: frame.ResponseTo,
		ToolCallID: frame.ToolCallID, ToolName: frame.ToolName, Summary: frame.Summary,
		Result: frame.Result, Status: frame.Status, Code: frame.Code,
		Retryable: frame.Retryable, Reason: frame.Reason,
		Permission: frame.Permission, Input: frame.Input,
	}
	if (frame.Type == adapterproto.KindPermissionRequest ||
		frame.Type == adapterproto.KindInputRequest) && frame.ID != "" {
		public.MessageID = frame.ID
	}
	payload := persistedThreadBlock{
		Event: public, Metadata: frame.Metadata,
		SessionID: frame.SessionID, ResumeState: frame.ResumeState,
	}
	switch frame.Type {
	case adapterproto.KindAssistantDelta:
		public.Summary = frame.Content
		return domain.ThreadRoleAssistant, domain.ThreadMessageStatus, domain.ThreadBlockText, payload, true
	case adapterproto.KindAssistantMessage:
		payload.Content = frame.Content
		return domain.ThreadRoleAssistant, domain.ThreadMessageResponse, domain.ThreadBlockText, payload, true
	case adapterproto.KindToolStart:
		return domain.ThreadRoleAssistant, domain.ThreadMessageToolCall, domain.ThreadBlockToolCall, payload, true
	case adapterproto.KindToolResult:
		return domain.ThreadRoleTool, domain.ThreadMessageToolResult, domain.ThreadBlockToolResult, payload, true
	case adapterproto.KindError:
		return domain.ThreadRoleSystem, domain.ThreadMessageStatus, domain.ThreadBlockError, payload, true
	case adapterproto.KindStatus, adapterproto.KindPermissionRequest,
		adapterproto.KindInputRequest, adapterproto.KindEnd:
		return domain.ThreadRoleSystem, domain.ThreadMessageStatus, domain.ThreadBlockJSON, payload, true
	default:
		return "", "", "", persistedThreadBlock{}, false
	}
}

func (s *Service) lockThreadSync(id domain.ThreadID) func() {
	const offset64 = uint64(14695981039346656037)
	const prime64 = uint64(1099511628211)
	hash := offset64
	for _, value := range []byte(id) {
		hash ^= uint64(value)
		hash *= prime64
	}
	lock := &s.threadSync[hash%uint64(len(s.threadSync))]
	lock.Lock()
	return lock.Unlock
}

func (s *Service) pauseCompletedThread(
	ctx context.Context,
	id domain.ThreadID,
	runID domain.RunID,
) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		thread, err := tx.GetThread(ctx, id)
		if err != nil {
			return err
		}
		if thread.State != domain.ThreadActive || thread.CurrentRunID != runID {
			return nil
		}
		run, err := tx.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if !run.State.Terminal() {
			return nil
		}
		previous := thread.ResourceVersion
		if err := thread.Transition(domain.ThreadPaused, s.clock.Now()); err != nil {
			return err
		}
		return tx.UpdateThread(ctx, thread, previous)
	})
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
