package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	fake "github.com/OrlojHQ/meridian/internal/testutil/fakeprovider"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

const stoppedRunFailure = "Capsule runtime stopped while this Run was active"

type restartHarness struct {
	store    *sqlite.Store
	service  *app.Service
	provider *fake.Provider
	runtime  *runRuntime
	recon    *app.Reconciler
	capsule  domain.Capsule
}

// newRestartHarness runs a real reconciler over the fake provider, with the
// daemon's runtime-recovery hook, and returns a Ready Capsule.
func newRestartHarness(t *testing.T) *restartHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(operatorContext())
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	runtime := &runRuntime{}
	service := app.NewService(store, clock, ids, reconciler, runtime)
	reconciler.ConfigureRuntimeRecovery(service.FailStoppedRuntimeRuns)
	reconciler.Start(ctx)
	t.Cleanup(reconciler.Close)
	project, err := service.CreateProject(ctx, "project", "project-restart")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-restart")
	if err != nil {
		t.Fatal(err)
	}
	h := &restartHarness{
		store: store, service: service, provider: provider, runtime: runtime, recon: reconciler,
	}
	h.capsule = h.await(t, capsule.ID, func(c domain.Capsule) bool { return c.State == domain.CapsuleReady })
	return h
}

func (h *restartHarness) resourceID() string { return h.capsule.ProviderResourceID }

func (h *restartHarness) await(
	t *testing.T, id domain.CapsuleID, done func(domain.Capsule) bool,
) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		capsule, err := h.service.GetCapsule(operatorContext(), id)
		if err == nil && done(capsule) {
			return capsule
		}
		if time.Now().After(deadline) {
			t.Fatalf("Capsule did not settle: %#v, %v", capsule, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// stopAndRecheck stops the runtime outside Meridian and has the reconciler
// notice, as startup recovery or the live refresh loop would.
func (h *restartHarness) stopAndRecheck(t *testing.T, reason string) {
	t.Helper()
	h.provider.Stop(h.resourceID(), reason)
	if err := h.recon.Enqueue(operatorContext(), h.capsule.ID); err != nil {
		t.Fatal(err)
	}
}

func (h *restartHarness) awaitRestarted(t *testing.T) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for h.provider.State(h.resourceID()) != ports.ProviderReady {
		if time.Now().After(deadline) {
			t.Fatalf("runtime state = %s, want ready", h.provider.State(h.resourceID()))
		}
		time.Sleep(time.Millisecond)
	}
	return h.await(t, h.capsule.ID, func(c domain.Capsule) bool {
		return c.State == domain.CapsuleReady && c.ResourceVersion > h.capsule.ResourceVersion
	})
}

func TestReconcilerRestartsStoppedRuntimeAndFailsItsLostRun(t *testing.T) {
	ctx := operatorContext()
	h := newRestartHarness(t)
	run, err := h.service.StartRun(ctx, h.capsule.ID, "mock", "prompt", "start-restart", 0, 0)
	if err != nil || run.State != domain.RunRunning {
		t.Fatalf("start Run = %#v, %v", run, err)
	}

	h.stopAndRecheck(t, "exit code 0")
	capsule := h.awaitRestarted(t)
	if capsule.Failure != "" {
		t.Fatalf("restarted Capsule failure = %q", capsule.Failure)
	}
	stored, err := h.service.GetRunStored(ctx, run.ID)
	if err != nil || stored.State != domain.RunFailed || stored.Failure != stoppedRunFailure {
		t.Fatalf("Run after restart = %#v, %v", stored, err)
	}
	var restarted bool
	if err := h.store.View(ctx, func(reader ports.Reader) error {
		events, err := reader.ListEvents(ctx, "capsule", string(h.capsule.ID))
		for _, event := range events {
			restarted = restarted || event.Type == "capsule.runtime_restarted"
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !restarted {
		t.Fatal("restart left no capsule.runtime_restarted event")
	}

	// The restarted Capsule takes new work.
	next, err := h.service.StartRun(ctx, h.capsule.ID, "mock", "again", "start-after-restart", 0, 0)
	if err != nil || next.State != domain.RunRunning {
		t.Fatalf("Run after restart = %#v, %v", next, err)
	}
}

func TestReconcilerFailsCapsuleWhoseRuntimeKeepsStopping(t *testing.T) {
	h := newRestartHarness(t)
	for attempt := 0; attempt < 3; attempt++ {
		h.stopAndRecheck(t, "exit code 1")
		h.capsule = h.awaitRestarted(t)
	}

	h.stopAndRecheck(t, "exit code 1")
	failed := h.await(t, h.capsule.ID, func(c domain.Capsule) bool { return c.State == domain.CapsuleFailed })
	if !strings.Contains(failed.Failure, "keeps stopping (exit code 1)") {
		t.Fatalf("crash-loop failure = %q", failed.Failure)
	}
	if state := h.provider.State(h.resourceID()); state != ports.ProviderStopped {
		t.Fatalf("crash-looping runtime state = %s, want stopped", state)
	}
}

func TestReconcilerFailsCapsuleWhoseStoppedRuntimeCannotStart(t *testing.T) {
	h := newRestartHarness(t)
	h.provider.Fail("resume", 1)
	h.stopAndRecheck(t, "exit code 0")
	failed := h.await(t, h.capsule.ID, func(c domain.Capsule) bool { return c.State == domain.CapsuleFailed })
	if !strings.Contains(failed.Failure, "could not be started again") {
		t.Fatalf("failed-start failure = %q", failed.Failure)
	}
}

func TestPausedCapsuleWithStoppedRuntimeStaysPausedAndResumes(t *testing.T) {
	ctx := operatorContext()
	h := newRestartHarness(t)
	paused, err := h.service.PauseCapsule(ctx, h.capsule.ID, h.capsule.ResourceVersion, "pause")
	if err != nil {
		t.Fatal(err)
	}
	paused = h.await(t, paused.ID, func(c domain.Capsule) bool { return c.State == domain.CapsulePaused })

	// An engine restart turns a paused container into a stopped one. That
	// still honours Paused, so the reconciler must not try to pause it.
	h.provider.Stop(h.resourceID(), "exit code 0")
	h.provider.Fail("pause", 10)
	if err := h.recon.Enqueue(ctx, paused.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // past every reconcile retry
	if current := h.await(t, paused.ID, func(domain.Capsule) bool { return true }); current.State != domain.CapsulePaused {
		t.Fatalf("paused Capsule with stopped runtime = %#v", current)
	}

	if _, err := h.service.ResumeCapsule(ctx, paused.ID, paused.ResourceVersion, "resume"); err != nil {
		t.Fatal(err)
	}
	h.await(t, paused.ID, func(c domain.Capsule) bool { return c.State == domain.CapsuleReady })
	if state := h.provider.State(h.resourceID()); state != ports.ProviderReady {
		t.Fatalf("resumed runtime state = %s", state)
	}
}

func TestRuntimeRefreshFailsRunWhoseRuntimeStoppedAndRechecksCapsule(t *testing.T) {
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	capsuleID := insertReadyCapsule(t, ctx, store, clock)
	queue := &recordingQueue{}
	runtime := &stoppedRuntime{runRuntime: &runRuntime{}}
	service := app.NewService(store, clock, &testIDs{}, queue, runtime)
	run, err := service.StartRun(ctx, capsuleID, "mock", "prompt", "start-native", 0, 0)
	if err != nil || run.State != domain.RunRunning {
		t.Fatalf("start Run = %#v, %v", run, err)
	}
	queue.mu.Lock()
	queue.ids = nil
	queue.mu.Unlock()

	runtime.stop()
	if err := service.RefreshLiveRuntime(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	stored, err := service.GetRunStored(ctx, run.ID)
	if err != nil || stored.State != domain.RunFailed || stored.Failure != stoppedRunFailure {
		t.Fatalf("Run after refresh = %#v, %v", stored, err)
	}
	assertQueued(t, queue, capsuleID)
}

func TestRuntimeRefreshFailsStructuredRunWhoseRuntimeStopped(t *testing.T) {
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	capsuleID := insertReadyCapsule(t, ctx, store, clock)
	key, err := transcripts.NewInstallationKey("test-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	queue := &recordingQueue{}
	runtime := &structuredRuntime{}
	service := app.NewService(store, clock, &testIDs{}, queue)
	service.ConfigureThreads(runtime, key)
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "build it",
		Start: true, IdempotencyKey: "create-thread",
	})
	if err != nil {
		t.Fatal(err)
	}

	runtime.mu.Lock()
	runtime.getErr = domain.ErrRuntimeStopped
	runtime.mu.Unlock()
	if err := service.RefreshLiveRuntime(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	run, err := service.GetRunStored(ctx, created.Run.ID)
	if err != nil || run.State != domain.RunFailed || run.Failure != stoppedRunFailure {
		t.Fatalf("structured Run after refresh = %#v, %v", run, err)
	}
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil || thread.State != domain.ThreadPaused {
		t.Fatalf("Thread after refresh = %#v, %v", thread, err)
	}
	assertQueued(t, queue, capsuleID)
}

func insertReadyCapsule(
	t *testing.T, ctx context.Context, store *sqlite.Store, clock *testClock,
) domain.CapsuleID {
	t.Helper()
	now := clock.now
	capsuleID := domain.CapsuleID("capsule-stopped")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		project := domain.Project{
			OwnerUserID: domain.BootstrapOwnerID,
			ID:          "project-stopped", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, domain.Capsule{
			ID: capsuleID, ProjectID: project.ID, Name: "capsule", State: domain.CapsuleReady,
			DesiredState: domain.IntentReady, ProviderResourceID: "resource-stopped",
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	return capsuleID
}

func assertQueued(t *testing.T, queue *recordingQueue, id domain.CapsuleID) {
	t.Helper()
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, queued := range queue.ids {
		if queued == id {
			return
		}
	}
	t.Fatalf("Capsule %s was not queued for a re-check: %v", id, queue.ids)
}

// Agent Sandbox restarts Pods itself and reports the runtime as preparing
// while it comes back; a re-check must not fail the Capsule.
func TestReadyCapsuleStaysReadyWhileProviderRestartsItsRuntime(t *testing.T) {
	ctx := operatorContext()
	h := newRestartHarness(t)
	h.provider.Set(h.resourceID(), ports.ProviderPreparing, "")
	if err := h.recon.Enqueue(ctx, h.capsule.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // past every reconcile retry
	current, err := h.service.GetCapsule(ctx, h.capsule.ID)
	if err != nil || current.State != domain.CapsuleReady || current.Failure != "" {
		t.Fatalf("Capsule while its runtime comes back = %#v, %v", current, err)
	}
	if state := h.provider.State(h.resourceID()); state != ports.ProviderPreparing {
		t.Fatalf("reconciler acted on a provider-managed restart: %s", state)
	}
}
