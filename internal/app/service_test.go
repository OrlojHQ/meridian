package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

type testIDs struct {
	mu   sync.Mutex
	next int
}

func (i *testIDs) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("id-%03d", i.next)
}

type recordingQueue struct {
	mu  sync.Mutex
	ids []domain.CapsuleID
}

type runRuntime struct {
	mu         sync.Mutex
	starts     int
	startState domain.RunState
	runs       map[domain.RunID]ports.RuntimeRun
	eventCalls int
	eventErr   error
}

type nativeProfiles struct{}

func (nativeProfiles) HarnessProfiles(
	context.Context, string,
) ([]ports.RuntimeHarnessProfile, error) {
	return []ports.RuntimeHarnessProfile{{Name: "opencode", PTY: true}}, nil
}

type concurrentEventRuntime struct {
	*runRuntime
	mu       sync.Mutex
	response ports.RuntimeEvents
	arrived  chan struct{}
	release  chan struct{}
}

func (r *concurrentEventRuntime) RunEvents(
	context.Context,
	string,
	domain.RunID,
	uint64,
) (ports.RuntimeEvents, error) {
	r.mu.Lock()
	response := r.response
	arrived, release := r.arrived, r.release
	r.mu.Unlock()
	if arrived != nil {
		arrived <- struct{}{}
		<-release
	}
	return response, nil
}

func (r *runRuntime) StartRun(_ context.Context, request ports.RuntimeRunRequest) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts++
	if r.runs == nil {
		r.runs = make(map[domain.RunID]ports.RuntimeRun)
	}
	state := r.startState
	if state == "" {
		state = domain.RunRunning
	}
	value := ports.RuntimeRun{State: state, Cursor: 2, PTY: true}
	r.runs[request.RunID] = value
	return value, nil
}

func (r *runRuntime) GetRun(_ context.Context, _ string, id domain.RunID) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.runs[id]
	if !ok {
		return ports.RuntimeRun{}, domain.ErrNotFound
	}
	return value, nil
}

func (r *runRuntime) CancelRun(_ context.Context, _ string, id domain.RunID) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := ports.RuntimeRun{State: domain.RunCancelled, Cursor: 3}
	r.runs[id] = value
	return value, nil
}

func (r *runRuntime) RunEvents(_ context.Context, _ string, _ domain.RunID, after uint64) (ports.RuntimeEvents, error) {
	r.mu.Lock()
	r.eventCalls++
	eventErr := r.eventErr
	r.mu.Unlock()
	if eventErr != nil {
		return ports.RuntimeEvents{}, eventErr
	}
	if after >= 2 {
		return ports.RuntimeEvents{NextCursor: after}, nil
	}
	return ports.RuntimeEvents{
		Items:      []ports.RuntimeEvent{{Sequence: 1, Type: "output"}, {Sequence: 2, Type: "record"}},
		NextCursor: 2,
	}, nil
}

func (*runRuntime) AttachRun(context.Context, string, domain.RunID, uint64) (ports.RuntimeAttachment, error) {
	return nil, domain.ErrUnsupported
}
func (*runRuntime) GitStatus(context.Context, string) (ports.GitResult, error) {
	return ports.GitResult{Content: " M result.txt\n"}, nil
}
func (*runRuntime) GitDiff(context.Context, string) (ports.GitResult, error) {
	return ports.GitResult{Content: "diff"}, nil
}

func (q *recordingQueue) Enqueue(_ context.Context, id domain.CapsuleID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ids = append(q.ids, id)
	return nil
}

func TestServiceIdempotencyConflictsAndIllegalMutations(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	queue := &recordingQueue{}
	service := app.NewService(store, clock, ids, queue)

	project, err := service.CreateProject(ctx, "project", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.CreateProject(ctx, "ignored on replay", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay, project) {
		t.Fatalf("project replay = %#v, want %#v", replay, project)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}
	capsuleReplay, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}
	if capsuleReplay != capsule {
		t.Fatalf("capsule replay = %#v, want %#v", capsuleReplay, capsule)
	}
	if _, err := service.CreateCapsule(
		ctx, project.ID, "different", "capsule-key",
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Capsule replay mismatch = %v", err)
	}
	if _, err := service.PauseCapsule(ctx, capsule.ID, 99, "conflict"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("pause conflict = %v", err)
	}
	if _, err := service.PauseCapsule(
		ctx, capsule.ID, capsule.ResourceVersion, "illegal",
	); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("pause Creating capsule = %v", err)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		projectEvents, err := reader.ListEvents(ctx, "project", string(project.ID))
		if err != nil {
			return err
		}
		capsuleEvents, err := reader.ListEvents(ctx, "capsule", string(capsule.ID))
		if err != nil {
			return err
		}
		if len(projectEvents) != 1 || len(capsuleEvents) != 1 {
			t.Fatalf("idempotent event counts = %d, %d", len(projectEvents), len(capsuleEvents))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcilerRetriesAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{Failures: map[string]int{"create": 2}})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	service := app.NewService(store, clock, ids, reconciler)
	project, err := service.CreateProject(ctx, "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		capsule, err = service.GetCapsule(ctx, capsule.ID)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == domain.CapsuleReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capsule did not reconcile: %#v", capsule)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() {
		reconciler.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconciler did not stop after cancellation")
	}
}

func TestRunUseCasesIdempotencyExclusivityEventsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	runtime := &runRuntime{}
	service := app.NewService(store, clock, ids, reconciler, runtime)
	project, err := service.CreateProject(ctx, "project", "project-run")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-run")
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
	run, err := service.StartRun(ctx, capsule.ID, "mock", "sensitive prompt", "start-key", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.StartRun(ctx, capsule.ID, "ignored", "different", "start-key", 0, 0)
	if err != nil || replay.ID != run.ID || runtime.starts != 1 {
		t.Fatalf("replay = %#v, starts = %d, err = %v", replay, runtime.starts, err)
	}
	if _, err := service.StartRun(ctx, capsule.ID, "mock", "second", "second-key", 0, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second active Run = %v", err)
	}
	events, err := service.RunEvents(ctx, run.ID, 0, 100)
	if err != nil || len(events.Items) < 3 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	for _, event := range events.Items {
		if strings.Contains(string(event.Data), "sensitive prompt") {
			t.Fatal("prompt persisted in Run event")
		}
	}
	run, err = service.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.CancelRun(ctx, run.ID, run.ResourceVersion, "cancel-key")
	if err != nil || cancelled.State != domain.RunCancelled {
		t.Fatalf("cancel = %#v, %v", cancelled, err)
	}
	runtime.mu.Lock()
	eventCalls := runtime.eventCalls
	runtime.eventErr = errors.New("terminal runtime events unavailable")
	runtime.mu.Unlock()
	if _, err := service.RunEvents(ctx, run.ID, 0, 100); err != nil {
		t.Fatalf("durable terminal Run events = %v", err)
	}
	runtime.mu.Lock()
	if runtime.eventCalls != eventCalls {
		t.Fatal("terminal Run events unexpectedly contacted the runtime")
	}
	runtime.mu.Unlock()
	status, err := service.GitStatus(ctx, capsule.ID)
	if err != nil || status.Content == "" {
		t.Fatalf("Git status = %#v, %v", status, err)
	}
}

func TestRuntimeEventSynchronizationIsConcurrentMonotonicAndIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	runtime := &concurrentEventRuntime{runRuntime: &runRuntime{}}
	service := app.NewService(store, clock, ids, reconciler, runtime)
	project, err := service.CreateProject(ctx, "project", "sync-project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "sync-capsule")
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
	run, err := service.StartRun(ctx, capsule.ID, "mock", "prompt", "sync-run", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.response = ports.RuntimeEvents{
		Items: []ports.RuntimeEvent{
			{Sequence: 3, Type: "first"},
			{Sequence: 4, Type: "second"},
		},
		NextCursor: 4,
		Gap:        true,
	}
	runtime.arrived = make(chan struct{}, 2)
	runtime.release = make(chan struct{})
	arrived, release := runtime.arrived, runtime.release
	runtime.mu.Unlock()

	errorsChannel := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := service.RunEvents(ctx, run.ID, 0, 100)
			errorsChannel <- err
		}()
	}
	<-arrived
	<-arrived
	close(release)
	for range 2 {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}

	runtime.mu.Lock()
	runtime.arrived, runtime.release = nil, nil
	runtime.response = ports.RuntimeEvents{NextCursor: 2}
	runtime.mu.Unlock()
	if _, err := service.RunEvents(ctx, run.ID, 0, 100); err != nil {
		t.Fatal(err)
	}
	current, err := service.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.EventCursor != 4 {
		t.Fatalf("EventCursor regressed to %d", current.EventCursor)
	}

	counts := map[uint64]int{}
	gaps := 0
	if err := store.View(ctx, func(reader ports.Reader) error {
		events, _, err := reader.ListRunEvents(ctx, run.ID, 0, 100)
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Type == "run.runtime_gap" {
				gaps++
			}
			if event.Type != "run.runtime_event" {
				continue
			}
			var metadata struct {
				RuntimeSequence uint64 `json:"runtimeSequence"`
			}
			if err := json.Unmarshal(event.Data, &metadata); err != nil {
				return err
			}
			counts[metadata.RuntimeSequence]++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if counts[3] != 1 || counts[4] != 1 {
		t.Fatalf("runtime sequence counts = %#v", counts)
	}
	if gaps != 1 {
		t.Fatalf("gap event count = %d", gaps)
	}
}

func TestCreateProjectConfiguredHarnessImages(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := app.NewService(store, &testClock{now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	if _, err := service.CreateProjectConfigured(ctx, "dupes", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{
			{Name: "mock", ImageReference: "meridian-capsule:dev"},
			{Name: "mock", ImageReference: "meridian-capsule:other"},
		},
	}, "project-dupes"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("duplicate harness image error = %v", err)
	}
	project, err := service.CreateProjectConfigured(ctx, "images", app.ProjectConfiguration{
		ImageReference: "meridian-capsule:dev",
		HarnessImages: []domain.HarnessImage{
			{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
		},
	}, "project-images")
	if err != nil || len(project.HarnessImages) != 1 || project.HarnessImages[0].Name != "opencode" {
		t.Fatalf("created project = %#v, %v", project, err)
	}
	capsule, err := service.CreateCapsuleForHarness(
		ctx, project.ID, "native", "opencode", "capsule-opencode",
	)
	if err != nil || capsule.LauncherHarness != "opencode" ||
		capsule.ImageReference != "meridian-capsule-opencode:dev" {
		t.Fatalf("harness-selected Capsule = %#v, %v", capsule, err)
	}
	if _, err := service.CreateCapsuleForHarness(
		ctx, project.ID, "native", "", "capsule-opencode",
	); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("launcher harness replay mismatch = %v", err)
	}
	if _, err := service.CreateCapsuleForHarness(
		ctx, project.ID, "unknown", "pi", "capsule-pi",
	); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unallowlisted Capsule harness error = %v", err)
	}
	updated, err := service.UpdateProjectHarnessImages(ctx, project.ID, []domain.HarnessImage{
		{Name: "mock", ImageReference: "meridian-capsule:dev"},
		{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
	}, project.ResourceVersion)
	if err != nil || len(updated.HarnessImages) != 2 || updated.ResourceVersion != project.ResourceVersion+1 {
		t.Fatalf("updated project = %#v, %v", updated, err)
	}
	if _, err := service.UpdateProjectHarnessImages(ctx, project.ID, nil, project.ResourceVersion); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale apply error = %v", err)
	}
}

func TestCapsuleLauncherStartsOnceAndDoesNotRestartFailedRun(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	capsule := domain.Capsule{
		ID: "launcher-capsule", ProjectID: "launcher-project", Name: "launcher",
		LauncherHarness: "opencode", State: domain.CapsuleReady, DesiredState: domain.IntentReady,
		ProviderResourceID: "resource", RestoreComplete: true, LastActivityAt: now,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, domain.Project{
			ID: "launcher-project", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, capsule)
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &runRuntime{startState: domain.RunFailed}
	service := app.NewService(
		store, &testClock{now: now}, &testIDs{}, &recordingQueue{}, runtime,
	)
	service.ConfigureHarnessProfiles(nativeProfiles{})
	if err := service.ReconcileCapsuleLauncher(ctx, capsule); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileCapsuleLauncher(ctx, capsule); err != nil {
		t.Fatal(err)
	}
	if runtime.starts != 1 {
		t.Fatalf("launcher starts = %d, want 1", runtime.starts)
	}
	runs, err := service.ListRuns(ctx, capsule.ID, 0, 10)
	if err != nil || len(runs.Items) != 1 || runs.Items[0].State != domain.RunFailed {
		t.Fatalf("launcher Run = %#v, %v", runs.Items, err)
	}
}
