package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// inspectStub answers delivery inspection for the no-changes decision.
type inspectStub struct {
	mu         sync.Mutex
	inspection ports.DeliveryInspection
	err        error
}

func (s *inspectStub) InspectDelivery(context.Context, string) (ports.DeliveryInspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspection, s.err
}

func (*inspectStub) CommitDelivery(context.Context, ports.DeliveryCommitRequest) (ports.DeliveryCommitResult, error) {
	return ports.DeliveryCommitResult{}, errLeaked
}

func (*inspectStub) PushDelivery(context.Context, ports.DeliveryPushRequest) (ports.DeliveryPushResult, error) {
	return ports.DeliveryPushResult{}, errLeaked
}

type routineHarness struct {
	ctx     context.Context
	system  context.Context
	store   *sqlite.Store
	service *app.Service
	runtime *structuredRuntime
	clock   *testClock
	inspect *inspectStub
	cursor  uint64
}

const routineProject = domain.ProjectID("project-sched")

func newRoutineHarness(t *testing.T) *routineHarness {
	t.Helper()
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Wednesday 18 November 2026, 10:00 UTC.
	clock := &testClock{now: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertProject(ctx, domain.Project{
			OwnerUserID: domain.BootstrapOwnerID, ID: routineProject, Name: "project",
			CreatedAt: clock.now, UpdatedAt: clock.now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	transcript, err := transcripts.NewInstallationKey("routine-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transcript.Zero)
	secretKey, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secretKey.Zero)
	runtime := &structuredRuntime{}
	inspect := &inspectStub{}
	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	service.ConfigureThreads(runtime, transcript)
	service.ConfigureSecrets(secretKey)
	service.ConfigureDelivery(inspect, nil)
	return &routineHarness{
		ctx: ctx, system: asSystem(ctx), store: store, service: service,
		runtime: runtime, clock: clock, inspect: inspect,
	}
}

func (h *routineHarness) set(at time.Time) {
	h.clock.mu.Lock()
	h.clock.now = at
	h.clock.mu.Unlock()
}

func (h *routineHarness) advance(by time.Duration) {
	h.clock.mu.Lock()
	h.clock.now = h.clock.now.Add(by)
	h.clock.mu.Unlock()
}

func (h *routineHarness) tick(t *testing.T) {
	t.Helper()
	if err := h.service.TickRoutines(h.system); err != nil {
		t.Fatal(err)
	}
}

func (h *routineHarness) create(t *testing.T, input app.RoutineInput) app.RoutineView {
	t.Helper()
	if input.Harness == "" {
		input.Harness = "mock"
	}
	if input.TimeZone == "" {
		input.TimeZone = "UTC"
	}
	view, err := h.service.CreateRoutine(h.ctx, routineProject, input, "create-"+input.Name)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func (h *routineHarness) fires(t *testing.T, id string) []app.RoutineFireView {
	t.Helper()
	items, _, err := h.service.ListRoutineFires(h.ctx, domain.RoutineID(id), ports.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (h *routineHarness) latest(t *testing.T, id string) app.RoutineFireView {
	t.Helper()
	items := h.fires(t, id)
	if len(items) == 0 {
		t.Fatal("no fires")
	}
	return items[0]
}

// ready makes the fire's Capsule Ready, which starts its idle session.
func (h *routineHarness) ready(t *testing.T, fire app.RoutineFireView) {
	t.Helper()
	capsule := setProjectThreadCapsuleState(t, h.ctx, h.store, domain.CapsuleID(fire.CapsuleID), domain.CapsuleReady)
	if err := h.service.ReconcileProjectThreadCapsule(h.system, capsule); err != nil {
		t.Fatal(err)
	}
}

// emit delivers adapter frames to the Thread, as a live session would.
func (h *routineHarness) emit(t *testing.T, thread string, frames ...adapterproto.Frame) {
	t.Helper()
	items := make([]ports.RuntimeStructuredEvent, 0, len(frames))
	for _, frame := range frames {
		h.cursor++
		frame.Protocol = adapterproto.Version
		items = append(items, ports.RuntimeStructuredEvent{Sequence: h.cursor, Frame: frame})
	}
	h.runtime.mu.Lock()
	h.runtime.events = ports.RuntimeStructuredEvents{Items: items, NextCursor: h.cursor}
	h.runtime.mu.Unlock()
	if err := h.service.SyncThread(h.system, domain.ThreadID(thread)); err != nil {
		t.Fatal(err)
	}
}

func (h *routineHarness) sentContaining(text string) int {
	h.runtime.mu.Lock()
	defer h.runtime.mu.Unlock()
	count := 0
	for _, frame := range h.runtime.frames {
		if frame.Type == adapterproto.KindUserMessage && strings.Contains(frame.Content, text) {
			count++
		}
	}
	return count
}

func (h *routineHarness) negotiateModels(threadRun domain.RunID) {
	h.runtime.mu.Lock()
	defer h.runtime.mu.Unlock()
	run := h.runtime.runs[threadRun]
	run.Models, run.Effort = true, true
	h.runtime.runs[threadRun] = run
}

// runToIdle fires the routine, starts the session, sends the prompt, ends
// the agent's turn, and waits out the settle time.
func (h *routineHarness) runToIdle(t *testing.T, routine app.RoutineView) app.RoutineFireView {
	t.Helper()
	h.tick(t)
	fire := h.latest(t, routine.ID)
	if fire.State != string(domain.RoutineFireStarting) || fire.ThreadID == "" {
		t.Fatalf("fire after first tick = %#v", fire)
	}
	h.ready(t, fire)
	h.tick(t) // starting -> selecting
	h.tick(t) // selecting -> running: prompt sent
	fire = h.latest(t, routine.ID)
	if fire.State != string(domain.RoutineFireRunning) {
		t.Fatalf("fire after prompt = %#v", fire)
	}
	h.emit(t, fire.ThreadID, adapterproto.Frame{Type: adapterproto.KindEnd})
	h.tick(t) // idle noticed
	h.advance(domain.RoutineIdleSettle + time.Second)
	h.tick(t) // outcome decided
	return h.latest(t, routine.ID)
}

func TestRoutineRunsOnTimeAndDeletesAnEmptyCapsule(t *testing.T) {
	h := newRoutineHarness(t)
	prompt := "update dependencies sentinel-7f3a"
	routine := h.create(t, app.RoutineInput{Name: "deps-weekly", Prompt: prompt, Cadence: "0 6 * * 1"})
	monday := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
	if routine.NextFireAt == nil || !routine.NextFireAt.Equal(monday) {
		t.Fatalf("next fire = %v", routine.NextFireAt)
	}
	for _, path := range []string{h.store.Path(), h.store.Path() + "-wal"} {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("sentinel-7f3a")) {
			t.Fatalf("plaintext prompt found in %s", path)
		}
	}
	h.tick(t)
	if len(h.fires(t, routine.ID)) != 0 {
		t.Fatal("fired before its time")
	}

	h.set(monday.Add(10 * time.Second))
	fire := h.runToIdle(t, routine)
	if h.sentContaining("sentinel-7f3a") != 1 {
		t.Fatalf("prompt sends = %d", h.sentContaining("sentinel-7f3a"))
	}
	if fire.Trigger != "clock" || fire.Outcome != "no_changes" || fire.State != "cleaning" {
		t.Fatalf("fire = %#v", fire)
	}
	for range 4 {
		h.tick(t)
	}
	fire = h.latest(t, routine.ID)
	if fire.State != "finished" || !fire.CapsuleDeleted {
		t.Fatalf("fire after cleaning = %#v", fire)
	}
	capsule, err := h.service.GetCapsule(h.ctx, domain.CapsuleID(fire.CapsuleID))
	if err != nil {
		t.Fatal(err)
	}
	if capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("capsule desired state = %s", capsule.DesiredState)
	}
	thread, err := h.service.GetThread(h.ctx, domain.ThreadID(fire.ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	if thread.State != domain.ThreadArchived {
		t.Fatalf("thread state = %s", thread.State)
	}
	view, err := h.service.GetRoutine(h.ctx, domain.RoutineID(routine.ID))
	if err != nil {
		t.Fatal(err)
	}
	if view.KeptCapsules != 0 || view.NextFireAt == nil || !view.NextFireAt.Equal(monday.AddDate(0, 0, 7)) {
		t.Fatalf("routine after run = %#v", view)
	}
	notes, err := h.service.CapsuleRoutines(h.ctx, []domain.CapsuleID{capsule.ID})
	if err != nil {
		t.Fatal(err)
	}
	if note := notes[capsule.ID]; note.RoutineName != "deps-weekly" || note.Outcome != "no_changes" {
		t.Fatalf("capsule note = %#v", note)
	}
}

func TestRoutineKeepsChangesAndStopsAtTheBacklog(t *testing.T) {
	h := newRoutineHarness(t)
	h.inspect.inspection = ports.DeliveryInspection{Dirty: true, HasDeliverableChanges: true}
	routine := h.create(t, app.RoutineInput{
		Name: "lint", Prompt: "fix lint", Cadence: "0 * * * *", MaxKept: 1,
	})
	h.set(*routine.NextFireAt)
	fire := h.runToIdle(t, routine)
	if fire.State != "finished" || fire.Outcome != "changed" || fire.CapsuleDeleted {
		t.Fatalf("fire = %#v", fire)
	}
	capsule, err := h.service.GetCapsule(h.ctx, domain.CapsuleID(fire.CapsuleID))
	if err != nil {
		t.Fatal(err)
	}
	if capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("a Capsule with changes was deleted")
	}

	h.set(routine.NextFireAt.Add(time.Hour))
	h.tick(t)
	skipped := h.latest(t, routine.ID)
	if skipped.State != "skipped" || skipped.Reason != domain.RoutineReasonBacklog {
		t.Fatalf("second fire = %#v", skipped)
	}
}

func TestRoutineSkipsAnOverlappingRun(t *testing.T) {
	h := newRoutineHarness(t)
	routine := h.create(t, app.RoutineInput{Name: "slow", Prompt: "work", Cadence: "*/15 * * * *"})
	h.set(*routine.NextFireAt)
	h.tick(t)
	first := h.latest(t, routine.ID)
	if first.State != "starting" {
		t.Fatalf("first = %#v", first)
	}
	h.set(routine.NextFireAt.Add(15 * time.Minute))
	h.tick(t)
	second := h.latest(t, routine.ID)
	if second.State != "skipped" || second.Reason != domain.RoutineReasonOverlap {
		t.Fatalf("second = %#v", second)
	}
	manual, err := h.service.RunRoutine(h.ctx, domain.RoutineID(routine.ID), "run-now")
	if err != nil {
		t.Fatal(err)
	}
	if manual.State != "skipped" || manual.Reason != domain.RoutineReasonOverlap || manual.Trigger != "manual" {
		t.Fatalf("manual = %#v", manual)
	}
}

func TestRoutineTimesOutAndPauses(t *testing.T) {
	h := newRoutineHarness(t)
	routine := h.create(t, app.RoutineInput{
		Name: "long", Prompt: "work", Cadence: "0 6 * * *", MaxRunSeconds: 300,
	})
	h.set(*routine.NextFireAt)
	h.tick(t)
	fire := h.latest(t, routine.ID)
	h.ready(t, fire)
	h.tick(t)
	h.tick(t)
	h.advance(6 * time.Minute)
	h.tick(t)
	fire = h.latest(t, routine.ID)
	if fire.State != "finished" || fire.Outcome != "timed_out" || fire.CapsuleDeleted {
		t.Fatalf("fire = %#v", fire)
	}
	capsule, err := h.service.GetCapsule(h.ctx, domain.CapsuleID(fire.CapsuleID))
	if err != nil {
		t.Fatal(err)
	}
	if capsule.DesiredState != domain.IntentPaused {
		t.Fatalf("desired state = %s", capsule.DesiredState)
	}
}

func TestRoutineAppliesTheModelBeforeThePrompt(t *testing.T) {
	h := newRoutineHarness(t)
	routine := h.create(t, app.RoutineInput{
		Name: "model", Prompt: "prompt-after-model", Cadence: "0 6 * * *", Model: "opus", Effort: "high",
	})
	h.set(*routine.NextFireAt)
	h.tick(t)
	fire := h.latest(t, routine.ID)
	h.ready(t, fire)
	thread, err := h.service.GetThread(h.ctx, domain.ThreadID(fire.ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	h.negotiateModels(thread.CurrentRunID)
	h.tick(t) // selecting
	h.tick(t) // no models reported yet: wait
	if h.sentContaining("prompt-after-model") != 0 {
		t.Fatal("prompt sent before the model was set")
	}
	effort := &adapterproto.EffortSet{Current: "medium", Options: []adapterproto.ModelOption{
		{ID: "medium", Label: "Medium"}, {ID: "high", Label: "High"},
	}}
	h.emit(t, fire.ThreadID, adapterproto.Frame{Type: adapterproto.KindModels, Models: &adapterproto.ModelSet{
		Current: "default", Effort: effort,
		Options: []adapterproto.ModelOption{{ID: "default", Label: "Default"}, {ID: "opus", Label: "Opus"}},
	}})
	h.tick(t) // set_model sent
	if !h.sentFrame(adapterproto.KindSetModel, "opus") {
		t.Fatal("set_model was not sent")
	}
	if h.sentContaining("prompt-after-model") != 0 {
		t.Fatal("prompt sent before the switch was confirmed")
	}
	h.emit(t, fire.ThreadID, adapterproto.Frame{Type: adapterproto.KindModels, Models: &adapterproto.ModelSet{
		Current: "opus", Effort: effort,
		Options: []adapterproto.ModelOption{{ID: "default", Label: "Default"}, {ID: "opus", Label: "Opus"}},
	}})
	h.tick(t) // set_effort sent
	if !h.sentFrame(adapterproto.KindSetEffort, "high") {
		t.Fatal("set_effort was not sent")
	}
	h.emit(t, fire.ThreadID, adapterproto.Frame{Type: adapterproto.KindModels, Models: &adapterproto.ModelSet{
		Current: "opus", Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus"}},
		Effort: &adapterproto.EffortSet{Current: "high", Options: effort.Options},
	}})
	h.tick(t) // prompt sent
	if h.sentContaining("prompt-after-model") != 1 {
		t.Fatalf("prompt sends = %d", h.sentContaining("prompt-after-model"))
	}
	if fire := h.latest(t, routine.ID); fire.State != "running" {
		t.Fatalf("fire = %#v", fire)
	}
}

func (h *routineHarness) sentFrame(kind adapterproto.Kind, value string) bool {
	h.runtime.mu.Lock()
	defer h.runtime.mu.Unlock()
	for _, frame := range h.runtime.frames {
		if frame.Type == kind && (frame.Model == value || frame.Effort == value) {
			return true
		}
	}
	return false
}

func TestRoutineFailsOnAnUnavailableModelAndTurnsOffAfterThree(t *testing.T) {
	h := newRoutineHarness(t)
	routine := h.create(t, app.RoutineInput{
		Name: "missing", Prompt: "never-sent", Cadence: "0 * * * *", Model: "nope", MaxKept: 20,
	})
	at := *routine.NextFireAt
	for i := range 3 {
		h.set(at.Add(time.Duration(i) * time.Hour))
		h.tick(t)
		fire := h.latest(t, routine.ID)
		h.ready(t, fire)
		thread, err := h.service.GetThread(h.ctx, domain.ThreadID(fire.ThreadID))
		if err != nil {
			t.Fatal(err)
		}
		h.negotiateModels(thread.CurrentRunID)
		h.tick(t)
		h.emit(t, fire.ThreadID, adapterproto.Frame{Type: adapterproto.KindModels, Models: &adapterproto.ModelSet{
			Current: "default", Options: []adapterproto.ModelOption{{ID: "default", Label: "Default"}},
		}})
		h.tick(t)
		fire = h.latest(t, routine.ID)
		if fire.Outcome != "failed" || fire.Reason != domain.RoutineReasonModelAbsent {
			t.Fatalf("fire %d = %#v", i, fire)
		}
	}
	if h.sentContaining("never-sent") != 0 {
		t.Fatal("prompt sent on a model the operator did not pick")
	}
	view, err := h.service.GetRoutine(h.ctx, domain.RoutineID(routine.ID))
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || view.DisabledReason != domain.RoutineDisabledStartFailures || view.NextFireAt != nil {
		t.Fatalf("routine = %#v", view)
	}
	enabled := true
	view, err = h.service.UpdateRoutine(h.ctx, domain.RoutineID(routine.ID), app.RoutineUpdate{
		Enabled: &enabled, ExpectedResourceVersion: view.ResourceVersion,
	}, "turn-on")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.DisabledReason != "" || view.NextFireAt == nil {
		t.Fatalf("turned on = %#v", view)
	}
}

func TestRoutineCatchUpAndMissedFires(t *testing.T) {
	h := newRoutineHarness(t)
	routine := h.create(t, app.RoutineInput{Name: "hourly", Prompt: "work", Cadence: "0 * * * *"})
	first := *routine.NextFireAt
	// Asleep for four and a half hours: four missed, the latest caught up.
	h.set(first.Add(4*time.Hour + 30*time.Minute))
	h.tick(t)
	fires := h.fires(t, routine.ID)
	var missed, caught int
	for _, fire := range fires {
		switch {
		case fire.State == "missed":
			missed++
		case fire.Trigger == "catch_up":
			caught++
			if !fire.ScheduledFor.Equal(first.Add(4 * time.Hour)) {
				t.Fatalf("caught up %v", fire.ScheduledFor)
			}
		}
	}
	if missed != 4 || caught != 1 {
		t.Fatalf("missed=%d caught=%d: %#v", missed, caught, fires)
	}
	view, err := h.service.GetRoutine(h.ctx, domain.RoutineID(routine.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !view.NextFireAt.Equal(first.Add(5 * time.Hour)) {
		t.Fatalf("next = %v", view.NextFireAt)
	}

	// A catch-up window of zero misses everything late.
	zero := 0
	late := h.create(t, app.RoutineInput{Name: "strict", Prompt: "work", Cadence: "30 * * * *", CatchUpSeconds: &zero})
	h.set(late.NextFireAt.Add(10 * time.Minute))
	h.tick(t)
	if fire := h.latest(t, late.ID); fire.State != "missed" {
		t.Fatalf("strict fire = %#v", fire)
	}
}

func TestRoutineValidation(t *testing.T) {
	h := newRoutineHarness(t)
	for name, input := range map[string]app.RoutineInput{
		"too often":     {Name: "often", Prompt: "p", Cadence: "*/10 * * * *", Harness: "mock", TimeZone: "UTC"},
		"no prompt":     {Name: "empty", Prompt: "  ", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "UTC"},
		"bad name":      {Name: "Bad Name", Prompt: "p", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "UTC"},
		"bad zone":      {Name: "zone", Prompt: "p", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "Nowhere/Here"},
		"run too short": {Name: "short", Prompt: "p", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "UTC", MaxRunSeconds: 60},
	} {
		if _, err := h.service.CreateRoutine(h.ctx, routineProject, input, "bad-"+input.Name); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	h.create(t, app.RoutineInput{Name: "taken", Prompt: "p", Cadence: "0 6 * * *"})
	if _, err := h.service.CreateRoutine(h.ctx, routineProject, app.RoutineInput{
		Name: "taken", Prompt: "p", Cadence: "0 7 * * *", Harness: "mock", TimeZone: "UTC",
	}, "dup"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate name err = %v", err)
	}
}

func TestRoutineUpdateAndDelete(t *testing.T) {
	h := newRoutineHarness(t)
	routine := h.create(t, app.RoutineInput{Name: "edit-me", Prompt: "old prompt", Cadence: "0 6 * * *"})
	prompt, cadence, enabled := "new prompt", "0 9 * * 1-5", false
	updated, err := h.service.UpdateRoutine(h.ctx, domain.RoutineID(routine.ID), app.RoutineUpdate{
		Prompt: &prompt, Cadence: &cadence, Enabled: &enabled, ExpectedResourceVersion: routine.ResourceVersion,
	}, "edit")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Prompt != "new prompt" || updated.Cadence != cadence || updated.Enabled || updated.NextFireAt != nil {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := h.service.UpdateRoutine(h.ctx, domain.RoutineID(routine.ID), app.RoutineUpdate{
		Prompt: &prompt, ExpectedResourceVersion: routine.ResourceVersion,
	}, "stale"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	if err := h.service.DeleteRoutine(h.ctx, domain.RoutineID(routine.ID), updated.ResourceVersion, "delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.GetRoutine(h.ctx, domain.RoutineID(routine.ID)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
}

func TestRoutineListOwnedCoversOnlyTheCaller(t *testing.T) {
	h := newRoutineHarness(t)
	h.create(t, app.RoutineInput{Name: "nightly", Prompt: "triage", Cadence: "0 3 * * *"})
	mine, err := h.service.ListOwnedRoutines(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Name != "nightly" || mine[0].ProjectID != string(routineProject) {
		t.Fatalf("owned = %#v", mine)
	}
	bob, ok := principal.User("usr_bob")
	if !ok {
		t.Fatal("principal.User refused usr_bob")
	}
	theirs, err := h.service.ListOwnedRoutines(principal.With(context.Background(), bob))
	if err != nil {
		t.Fatal(err)
	}
	if len(theirs) != 0 {
		t.Fatalf("another user listed %d routines", len(theirs))
	}
}
