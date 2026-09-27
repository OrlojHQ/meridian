package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func structuredEvent(sequence uint64, frame adapterproto.Frame) ports.RuntimeStructuredEvent {
	frame.Protocol = adapterproto.Version
	return ports.RuntimeStructuredEvent{Sequence: sequence, Frame: frame}
}

func setStructuredEvents(runtime *structuredRuntime, events ...ports.RuntimeStructuredEvent) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items: events, NextCursor: events[len(events)-1].Sequence,
	}
}

func awaitingKind(t *testing.T, ctx context.Context, service *app.Service, id domain.ThreadID) string {
	t.Helper()
	thread, err := service.GetThread(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := service.ThreadAwaiting(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if awaiting == nil {
		return ""
	}
	if awaiting.Since.IsZero() {
		t.Fatalf("awaiting %s has no start time", awaiting.Kind)
	}
	return string(awaiting.Kind)
}

func TestRuntimeRefreshSurfacesUnansweredRequestsWithoutAViewer(t *testing.T) {
	ctx := context.Background()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	permission := structuredEvent(1, adapterproto.Frame{
		Type: adapterproto.KindPermissionRequest, ID: "permission-1",
		Permission: &adapterproto.Permission{Kind: "shell", Summary: "rm -rf build"},
	})
	setStructuredEvents(runtime, permission)
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "clean up",
		Start: true, IdempotencyKey: "create-thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind := awaitingKind(t, ctx, service, created.Thread.ID); kind != "" {
		t.Fatalf("awaiting before the request was ingested = %q", kind)
	}

	if err := service.RefreshLiveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if kind := awaitingKind(t, ctx, service, created.Thread.ID); kind != "permission" {
		t.Fatalf("awaiting after unwatched refresh = %q, want permission", kind)
	}

	thread, _ := service.GetThread(ctx, created.Thread.ID)
	choice := "deny"
	if _, err := service.RespondThread(
		ctx, thread.ID, thread.ResourceVersion, "permission-1", &choice, nil, "respond",
	); err != nil {
		t.Fatal(err)
	}
	if kind := awaitingKind(t, ctx, service, created.Thread.ID); kind != "" {
		t.Fatalf("awaiting after response = %q", kind)
	}

	setStructuredEvents(runtime, permission, structuredEvent(2, adapterproto.Frame{
		Type: adapterproto.KindInputRequest, ID: "input-1",
		Input: &adapterproto.Input{Prompt: "branch name?"},
	}))
	if err := service.RefreshLiveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if kind := awaitingKind(t, ctx, service, created.Thread.ID); kind != "input" {
		t.Fatalf("awaiting after input request = %q, want input", kind)
	}

	runtime.mu.Lock()
	runtime.runs[created.Run.ID] = ports.RuntimeRun{State: domain.RunSucceeded, Structured: true}
	runtime.mu.Unlock()
	if err := service.RefreshLiveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := service.GetRunStored(ctx, created.Run.ID)
	if err != nil || run.State != domain.RunSucceeded {
		t.Fatalf("refreshed structured Run = %#v, %v", run, err)
	}
	if kind := awaitingKind(t, ctx, service, created.Thread.ID); kind != "" {
		t.Fatalf("awaiting after the session ended = %q", kind)
	}
}

func TestRuntimeRefreshIgnoresHeartbeatsForIdleActivity(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	store, service, runtime, key, capsuleID := threadServiceWithClock(t, ctx, clock)
	defer store.Close()
	defer key.Zero()
	heartbeat := structuredEvent(1, adapterproto.Frame{Type: adapterproto.KindHeartbeat})
	setStructuredEvents(runtime, heartbeat)
	if _, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "wait",
		Start: true, IdempotencyKey: "create-thread",
	}); err != nil {
		t.Fatal(err)
	}
	lastActivity := func() time.Time {
		t.Helper()
		var capsule domain.Capsule
		if err := store.View(ctx, func(reader ports.Reader) error {
			var err error
			capsule, err = reader.GetCapsule(ctx, capsuleID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return capsule.LastActivityAt
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(time.Hour + 500*time.Millisecond)
	clock.mu.Unlock()
	before := lastActivity()
	if err := service.RefreshLiveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if after := lastActivity(); !after.Equal(before) {
		t.Fatalf("heartbeat moved Capsule activity from %s to %s", before, after)
	}

	setStructuredEvents(runtime, heartbeat, structuredEvent(2, adapterproto.Frame{
		Type: adapterproto.KindAssistantMessage, Role: adapterproto.RoleAssistant,
		MessageID: "assistant-1", Content: "done",
	}))
	if err := service.RefreshLiveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	if after := lastActivity(); !after.After(before) {
		t.Fatalf("agent output did not record Capsule activity (still %s)", after)
	}
}

func TestRuntimeRefreshReconcilesUnwatchedNativeRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	reconciler := app.NewReconciler(store, fake.New(fake.Options{}), clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	runtime := &runRuntime{}
	service := app.NewService(store, clock, ids, reconciler, runtime)
	project, err := service.CreateProject(ctx, "project", "project-refresh")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-refresh")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for capsule.State != domain.CapsuleReady {
		capsule, err = service.GetCapsule(ctx, capsule.ID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("Capsule readiness = %#v, %v", capsule, err)
		}
		time.Sleep(time.Millisecond)
	}
	run, err := service.StartRun(ctx, capsule.ID, "mock", "prompt", "start-refresh", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.runs[run.ID] = ports.RuntimeRun{State: domain.RunSucceeded, HasExit: true, PTY: true}
	runtime.mu.Unlock()

	listed, err := service.ListRuns(ctx, capsule.ID, 0, 10)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].State == domain.RunSucceeded {
		t.Fatalf("Runs before refresh = %#v, %v", listed, err)
	}
	if err := service.RefreshLiveRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	listed, err = service.ListRuns(ctx, capsule.ID, 0, 10)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].State != domain.RunSucceeded {
		t.Fatalf("Runs after refresh = %#v, %v", listed, err)
	}
}
