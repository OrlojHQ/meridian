package app_test

import (
	"path/filepath"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

func idleFrame(sequence uint64) ports.RuntimeStructuredEvent {
	return ports.RuntimeStructuredEvent{Sequence: sequence, Frame: adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusIdle, SessionID: "harness-session",
		ResumeState: `{"driver":"codex","thread":"harness-session"}`,
	}}
}

func runningFrame(sequence uint64) ports.RuntimeStructuredEvent {
	return ports.RuntimeStructuredEvent{Sequence: sequence, Frame: adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusRunning,
	}}
}

// A session started by an earlier daemon process holds a lease, CA, and
// gateway port that died with it. The next prompt resumes the harness in a
// new Run with a fresh connection instead of reaching the dead one.
func TestPromptRenewsSessionFromEarlierDaemon(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	secretKey, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer secretKey.Zero()
	configure := func(service *app.Service) {
		t.Helper()
		service.ConfigureSecrets(secretKey)
		if err := service.ConfigureLocalProviderGateway(
			"https://provider-gateway.meridian.internal:4433",
			func() (string, error) { return "-----BEGIN CERTIFICATE-----\n", nil }, false,
		); err != nil {
			t.Fatal(err)
		}
	}
	configure(service)
	defer service.CloseProviderGateway()
	capsule, err := service.GetCapsule(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := service.PutProviderConnection(ctx, "", app.ConnectionInput{
		Name: "OpenAI", Provider: "openai", APIKey: "upstream-private-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.GrantProviderConnection(ctx, capsule.ProjectID, "codex", connection.ID); err != nil {
		t.Fatal(err)
	}

	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{runningFrame(1), idleFrame(2)},
		NextCursor: 2,
	}
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "codex", FirstMessage: "hello",
		Start: true, IdempotencyKey: "create-renewal",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRun := created.Run.ID
	if err := service.SyncThread(ctx, created.Thread.ID); err != nil {
		t.Fatal(err)
	}

	// This process issued the lease, so a prompt goes to the same session.
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := service.SendThreadMessage(ctx, thread.ID, thread.ResourceVersion, "again", "same-daemon", "")
	if err != nil {
		t.Fatal(err)
	}
	if sent.Run.ID != firstRun || runtime.cancels != 0 || len(runtime.startFrames) != 1 {
		t.Fatalf("fresh lease renewed: run %s, cancels %d, starts %d", sent.Run.ID, runtime.cancels, len(runtime.startFrames))
	}

	// A new daemon process: same store, new gateway, no memory of the lease.
	restarted := app.NewService(store, &testClock{now: thread.UpdatedAt}, &testIDs{next: 500}, &recordingQueue{})
	restarted.ConfigureThreads(runtime, key)
	configure(restarted)
	defer restarted.CloseProviderGateway()

	// Mid-turn, the session is left alone; renewing would drop the turn.
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{runningFrame(3)},
		NextCursor: 3,
	}
	runtime.mu.Unlock()
	if err := restarted.SyncThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, err = restarted.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	busy, err := restarted.SendThreadMessage(ctx, thread.ID, thread.ResourceVersion, "queued", "mid-turn", "")
	if err != nil {
		t.Fatal(err)
	}
	if busy.Run.ID != firstRun || runtime.cancels != 0 {
		t.Fatalf("mid-turn session renewed: run %s, cancels %d", busy.Run.ID, runtime.cancels)
	}

	// Between turns, the prompt renews the session and lands in the new Run.
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{idleFrame(4)},
		NextCursor: 4,
	}
	runtime.mu.Unlock()
	if err := restarted.SyncThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, err = restarted.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := restarted.SendThreadMessage(ctx, thread.ID, thread.ResourceVersion, "after restart", "after-restart", "")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Run.ID == firstRun || runtime.cancels != 1 {
		t.Fatalf("stale session not renewed: run %s, cancels %d", renewed.Run.ID, runtime.cancels)
	}
	runtime.mu.Lock()
	resume := runtime.startFrames[len(runtime.startFrames)-1]
	delivered := runtime.sends[string(renewed.MessageID)]
	runtime.mu.Unlock()
	if resume.Type != adapterproto.KindResume || resume.SessionID != "harness-session" ||
		resume.ResumeState != `{"driver":"codex","thread":"harness-session"}` {
		t.Fatalf("renewal start frame = %#v, want resume of the harness session", resume)
	}
	if delivered != 1 {
		t.Fatalf("prompt delivered %d times to the renewed session", delivered)
	}
	old, err := restarted.GetRunStored(ctx, firstRun)
	if err != nil {
		t.Fatal(err)
	}
	if old.State != domain.RunCancelled {
		t.Fatalf("superseded run state = %s, want Cancelled", old.State)
	}
	thread, err = restarted.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.State != domain.ThreadActive || thread.CurrentRunID != renewed.Run.ID {
		t.Fatalf("thread %s on run %s, want active on %s", thread.State, thread.CurrentRunID, renewed.Run.ID)
	}
	page, err := restarted.ThreadBlocks(ctx, thread.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range page.Items {
		if block.Event != nil && block.Event.Type == adapterproto.KindEnd {
			t.Fatalf("renewal left a stop in the transcript: %#v", block.Event)
		}
	}

	// A replayed request does not renew again.
	again, err := restarted.SendThreadMessage(ctx, thread.ID, thread.ResourceVersion, "after restart", "after-restart", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.MessageID != renewed.MessageID || runtime.cancels != 1 {
		t.Fatalf("replay renewed: message %s, cancels %d", again.MessageID, runtime.cancels)
	}
}
