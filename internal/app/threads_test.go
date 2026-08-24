package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type structuredRuntime struct {
	mu           sync.Mutex
	runs         map[domain.RunID]ports.RuntimeRun
	starts       map[domain.RunID]int
	sends        map[string]int
	frames       map[string]adapterproto.Frame
	sendFailures int
	events       ports.RuntimeStructuredEvents
	cancels      int
	cancelErr    error
}

func (r *structuredRuntime) StartStructured(
	_ context.Context, request ports.RuntimeStructuredStartRequest,
) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs == nil {
		r.runs = make(map[domain.RunID]ports.RuntimeRun)
		r.starts = make(map[domain.RunID]int)
		r.sends = make(map[string]int)
		r.frames = make(map[string]adapterproto.Frame)
	}
	r.starts[request.RunID]++
	run := ports.RuntimeRun{State: domain.RunRunning, Structured: true}
	r.runs[request.RunID] = run
	return run, nil
}

func (r *structuredRuntime) GetStructured(
	_ context.Context, _ string, id domain.RunID,
) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok {
		return ports.RuntimeRun{}, domain.ErrNotFound
	}
	return run, nil
}

func (r *structuredRuntime) SendStructured(
	_ context.Context, request ports.RuntimeStructuredSendRequest,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends[request.Frame.ID]++
	r.frames[request.Frame.ID] = request.Frame
	if r.sendFailures > 0 {
		r.sendFailures--
		return errors.New("injected send failure")
	}
	return nil
}

func (r *structuredRuntime) StructuredEvents(
	context.Context, string, domain.RunID, uint64,
) (ports.RuntimeStructuredEvents, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events, nil
}

func (r *structuredRuntime) CancelStructured(
	_ context.Context, _ string, id domain.RunID,
) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels++
	if r.cancelErr != nil {
		return ports.RuntimeRun{}, r.cancelErr
	}
	run := ports.RuntimeRun{State: domain.RunCancelled, Structured: true}
	r.runs[id] = run
	return run, nil
}

func (*structuredRuntime) StructuredProfiles(
	context.Context, string,
) ([]ports.RuntimeHarnessProfile, error) {
	return []ports.RuntimeHarnessProfile{{
		Name: "mock", Structured: true, AdapterKind: "mock", Protocol: adapterproto.Version,
	}}, nil
}

func TestThreadAtomicIdempotencySyncAndCryptoShred(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{
			{Sequence: 1, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindAssistantDelta,
				Role: adapterproto.RoleAssistant, MessageID: "assistant-1", Content: "partial",
			}},
			{Sequence: 2, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindToolStart,
				Role: adapterproto.RoleAssistant, ToolCallID: "tool-1",
				ToolName: "mock", Summary: "bounded",
			}},
			{Sequence: 3, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindToolResult,
				Role: adapterproto.RoleTool, ToolCallID: "tool-1", Result: "ok",
			}},
			{Sequence: 4, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
				Role: adapterproto.RoleAssistant, MessageID: "assistant-1", Content: "final",
			}},
			{Sequence: 5, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest,
				ID: "permission-1", Permission: &adapterproto.Permission{
					Kind: "mock", Summary: "allow?", Options: []string{"allow", "deny"},
				},
			}},
		},
		NextCursor: 5,
	}
	input := app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "secret prompt",
		Start: true, IdempotencyKey: "create-thread",
	}
	created, err := service.CreateThread(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateThread(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Thread.ID != created.Thread.ID || replayed.MessageID != created.MessageID ||
		replayed.Run.ID != created.Run.ID {
		t.Fatalf("replay = %#v, want resource references from %#v", replayed, created)
	}
	runtime.mu.Lock()
	if len(runtime.starts) != 1 || runtime.sends[string(created.MessageID)] != 1 {
		t.Fatalf("runtime starts/sends = %#v/%#v", runtime.starts, runtime.sends)
	}
	runtime.mu.Unlock()

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := service.ThreadBlocks(ctx, created.Thread.ID, 0, 100)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.ThreadBlocks(ctx, created.Thread.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 6 || page.Items[0].Content != "secret prompt" ||
		page.Items[4].Content != "final" {
		t.Fatalf("decrypted ordered blocks = %#v", page.Items)
	}
	if page.Items[5].Event == nil || page.Items[5].Event.MessageID != "permission-1" {
		t.Fatalf("permission correlation ID = %#v", page.Items[5].Event)
	}
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.MessageCount != 6 {
		t.Fatalf("deduplicated message count = %d, want 6", thread.MessageCount)
	}
	choice := "allow"
	if _, err := service.RespondThread(
		ctx, thread.ID, thread.ResourceVersion, "permission-1", &choice, nil, "permission-response",
	); err != nil {
		t.Fatal(err)
	}
	thread, _ = service.GetThread(ctx, thread.ID)
	if _, err := service.CancelThread(
		ctx, thread.ID, thread.ResourceVersion, "cancel-thread",
	); err != nil {
		t.Fatal(err)
	}
	thread, _ = service.GetThread(ctx, thread.ID)
	if thread.State != domain.ThreadPaused {
		t.Fatalf("terminal structured session left Thread %s, want paused", thread.State)
	}
	thread, err = service.ArchiveThread(ctx, thread.ID, thread.ResourceVersion, "archive-thread")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.DeleteThread(ctx, thread.ID, thread.ResourceVersion, "delete-thread")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.State != domain.ThreadDeleted || len(deleted.WrappedDEK) != 0 {
		t.Fatalf("crypto-shredded Thread = %#v", deleted)
	}
	listed, err := service.ListThreads(ctx, capsuleID, 0, 10)
	if err != nil || len(listed.Items) != 0 {
		t.Fatalf("normal list after crypto-shred = %#v, %v", listed, err)
	}
	explicit, err := service.GetThread(ctx, thread.ID)
	if err != nil || explicit.State != domain.ThreadDeleted {
		t.Fatalf("explicit deleted Thread lookup = %#v, %v", explicit, err)
	}
	if _, err := service.ThreadBlocks(ctx, thread.ID, 0, 10); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("read after crypto-shred = %v", err)
	}
}

func TestTerminalThreadFreesActiveSlotAndPausedResumeConflicts(t *testing.T) {
	ctx := context.Background()
	store, service, _, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()

	first, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "first",
		Start: true, IdempotencyKey: "first-thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelThread(
		ctx, first.Thread.ID, first.Thread.ResourceVersion, "cancel-first",
	); err != nil {
		t.Fatal(err)
	}
	first.Thread, err = service.GetThread(ctx, first.Thread.ID)
	if err != nil || first.Thread.State != domain.ThreadPaused {
		t.Fatalf("completed Thread = %#v, %v", first.Thread, err)
	}

	second, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", IdempotencyKey: "second-thread",
	})
	if err != nil {
		t.Fatalf("new active Thread after terminal session: %v", err)
	}
	if _, err := service.ResumeThread(
		ctx, first.Thread.ID, first.Thread.ResourceVersion, "resume-first",
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("resume beside another active Thread = %v", err)
	}
	if second.Thread.State != domain.ThreadActive {
		t.Fatalf("second Thread state = %s", second.Thread.State)
	}
}

func TestThreadGapWrongKeyAndRecoveryDoesNotSend(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "prompt",
		Start: true, IdempotencyKey: "gap-thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.events = ports.RuntimeStructuredEvents{
		Gap: true, NextCursor: 5,
		Items: []ports.RuntimeStructuredEvent{{Sequence: 5, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
			Status: adapterproto.StatusIdle,
		}}},
	}
	page, err := service.ThreadBlocks(ctx, created.Thread.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if page.Gap == nil || page.Gap.AvailableFrom != 5 {
		t.Fatalf("gap = %#v", page.Gap)
	}
	runtime.mu.Lock()
	sendsBefore := len(runtime.sends)
	runtime.mu.Unlock()
	if err := service.RecoverThreads(ctx); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	sendsAfter := len(runtime.sends)
	runtime.mu.Unlock()
	if sendsAfter != sendsBefore {
		t.Fatalf("recovery auto-sent messages: %d -> %d", sendsBefore, sendsAfter)
	}
	wrong, err := transcripts.NewInstallationKey("wrong", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Zero()
	wrongService := app.NewService(store, &testClock{now: time.Now()}, &testIDs{}, &recordingQueue{})
	wrongService.ConfigureThreads(runtime, wrong)
	if _, err := wrongService.ThreadBlocks(ctx, created.Thread.ID, 0, 10); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("wrong-key read = %v", err)
	}
}

func TestDeferredFirstMessageStartDeliversEarliestOnce(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "deferred-first",
		Start: false, IdempotencyKey: "deferred-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	if len(runtime.sends) != 0 || len(runtime.starts) != 0 {
		t.Fatalf("deferred create touched runtime: starts=%v sends=%v", runtime.starts, runtime.sends)
	}
	runtime.mu.Unlock()
	started, err := service.StartThread(
		ctx, created.Thread.ID, created.Thread.ResourceVersion, "deferred-start",
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.MessageID != created.MessageID {
		t.Fatalf("started message = %q, want %q", started.MessageID, created.MessageID)
	}
	if _, err := service.StartThread(
		ctx, created.Thread.ID, created.Thread.ResourceVersion, "deferred-start",
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.starts[started.Run.ID] != 1 || runtime.sends[string(created.MessageID)] != 1 ||
		runtime.frames[string(created.MessageID)].Content != "deferred-first" {
		t.Fatalf("deferred delivery = starts=%v sends=%v frames=%v",
			runtime.starts, runtime.sends, runtime.frames)
	}
}

func TestThreadDeliveryRetriesCrashBeforeSendAndAcknowledgesSuccess(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	runtime.sendFailures = 1
	input := app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "crash-boundary",
		Start: true, IdempotencyKey: "create-crash",
	}
	first, err := service.CreateThread(ctx, input)
	if err == nil {
		t.Fatal("injected create+start delivery failure succeeded")
	}
	if _, err := service.CreateThread(ctx, input); err != nil {
		t.Fatalf("retry after crash-before-send: %v", err)
	}
	if _, err := service.CreateThread(ctx, input); err != nil {
		t.Fatalf("successful replay: %v", err)
	}
	runtime.mu.Lock()
	if runtime.starts[first.Run.ID] != 1 || runtime.sends[string(first.MessageID)] != 2 {
		t.Fatalf("create replay runtime work = starts=%v sends=%v", runtime.starts, runtime.sends)
	}
	runtime.mu.Unlock()

	thread, err := service.GetThread(ctx, first.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.sendFailures = 1
	sent, err := service.SendThreadMessage(
		ctx, thread.ID, thread.ResourceVersion, "second", "send-crash",
	)
	if err == nil {
		t.Fatal("injected message delivery failure succeeded")
	}
	if _, err := service.SendThreadMessage(
		ctx, thread.ID, thread.ResourceVersion, "ignored-on-replay", "send-crash",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendThreadMessage(
		ctx, thread.ID, thread.ResourceVersion, "ignored-again", "send-crash",
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	if runtime.sends[string(sent.MessageID)] != 2 {
		t.Fatalf("send delivery attempts = %v", runtime.sends)
	}
	runtime.mu.Unlock()

	thread, _ = service.GetThread(ctx, thread.ID)
	choice := "allow"
	runtime.sendFailures = 1
	responded, err := service.RespondThread(
		ctx, thread.ID, thread.ResourceVersion, "request-1", &choice, nil, "respond-crash",
	)
	if err == nil {
		t.Fatal("injected response delivery failure succeeded")
	}
	if _, err := service.RespondThread(
		ctx, thread.ID, thread.ResourceVersion, "request-1", &choice, nil, "respond-crash",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RespondThread(
		ctx, thread.ID, thread.ResourceVersion, "request-1", &choice, nil, "respond-crash",
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	if runtime.sends[string(responded.MessageID)] != 2 {
		t.Fatalf("response delivery attempts = %v", runtime.sends)
	}
	runtime.mu.Unlock()
}

func TestCancelThreadReturnsRuntimeFailureAndRetriesCancelling(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "cancel",
		Start: true, IdempotencyKey: "cancel-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.cancelErr = errors.New("runtime cancellation failed")
	if _, err := service.CancelThread(
		ctx, created.Thread.ID, created.Thread.ResourceVersion, "cancel-retry",
	); err == nil || err.Error() != "runtime cancellation failed" {
		t.Fatalf("cancel error = %v", err)
	}
	run, err := service.GetRunStored(ctx, created.Run.ID)
	if err != nil || run.State != domain.RunCancelling {
		t.Fatalf("durable cancelling run = %#v, %v", run, err)
	}
	runtime.cancelErr = nil
	if _, err := service.CancelThread(
		ctx, created.Thread.ID, created.Thread.ResourceVersion, "cancel-retry",
	); err != nil {
		t.Fatalf("cancel retry: %v", err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.cancels != 2 {
		t.Fatalf("runtime cancel attempts = %d", runtime.cancels)
	}
}

func TestThreadMetadataEncryptedAndPathPlaintextDoesNotLeak(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	sentinel := "/distinctive/private/repository/thread-path-sentinel"
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: sentinel,
		Start: true, IdempotencyKey: "leak-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{{Sequence: 1, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindToolStart,
			Role: adapterproto.RoleAssistant, ToolCallID: "tool-metadata",
			ToolName: "test", Metadata: []byte(`{"display":"metadata-round-trip"}`),
		}}},
		NextCursor: 1,
	}
	if err := service.SyncThread(ctx, created.Thread.ID); err != nil {
		t.Fatal(err)
	}
	var messages []domain.ThreadMessage
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, created.Thread.ID, 1, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dek, err := key.UnwrapDEK(
		created.Thread.ID, created.Thread.KEKID, created.Thread.KEKVersion,
		created.Thread.WrappedDEK,
	)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := transcripts.DecryptMessage(dek, messages[0])
	for index := range dek {
		dek[index] = 0
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plaintext[0], []byte(`"metadata":{"display":"metadata-round-trip"}`)) {
		t.Fatalf("encrypted payload did not preserve metadata: %s", plaintext[0])
	}
	transcripts.ZeroPlaintextBlocks(plaintext)
	for _, path := range []string{store.Path(), store.Path() + "-wal"} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil && errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(raw, []byte(sentinel)) {
			t.Fatalf("Thread path leaked into %s", path)
		}
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		events, err := reader.ListEvents(ctx, "run", string(created.Run.ID))
		if err != nil {
			return err
		}
		for _, event := range events {
			if bytes.Contains(event.Data, []byte(sentinel)) {
				t.Fatal("Thread path leaked into lifecycle event")
			}
		}
		record, err := reader.GetIdempotency(ctx, "thread:create:"+string(capsuleID), "leak-create")
		if err != nil {
			return err
		}
		if bytes.Contains(record.Outcome, []byte(sentinel)) {
			t.Fatal("Thread path leaked into idempotency outcome")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func threadService(
	t *testing.T, ctx context.Context,
) (*sqlite.Store, *app.Service, *structuredRuntime, *transcripts.InstallationKey, domain.CapsuleID) {
	t.Helper()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	capsuleID := domain.CapsuleID("capsule-thread")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		project := domain.Project{
			ID: "project-thread", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, domain.Capsule{
			ID: capsuleID, ProjectID: project.ID, Name: "capsule", State: domain.CapsuleReady,
			DesiredState: domain.IntentReady, ProviderResourceID: "resource-thread",
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	key, err := transcripts.NewInstallationKey("test-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &structuredRuntime{}
	service := app.NewService(
		store, &testClock{now: now}, &testIDs{}, &recordingQueue{})
	service.ConfigureThreads(runtime, key)
	return store, service, runtime, key, capsuleID
}
