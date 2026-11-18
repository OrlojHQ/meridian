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

type scheduleHarness struct {
	ctx     context.Context
	system  context.Context
	store   *sqlite.Store
	service *app.Service
	runtime *structuredRuntime
	clock   *testClock
	inspect *inspectStub
	cursor  uint64
}

const scheduleProject = domain.ProjectID("project-sched")

func newScheduleHarness(t *testing.T) *scheduleHarness {
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
			OwnerUserID: domain.BootstrapOwnerID, ID: scheduleProject, Name: "project",
			CreatedAt: clock.now, UpdatedAt: clock.now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	transcript, err := transcripts.NewInstallationKey("schedule-test", 1)
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
	return &scheduleHarness{
		ctx: ctx, system: asSystem(ctx), store: store, service: service,
		runtime: runtime, clock: clock, inspect: inspect,
	}
}

func (h *scheduleHarness) set(at time.Time) {
	h.clock.mu.Lock()
	h.clock.now = at
	h.clock.mu.Unlock()
}

func (h *scheduleHarness) advance(by time.Duration) {
	h.clock.mu.Lock()
	h.clock.now = h.clock.now.Add(by)
	h.clock.mu.Unlock()
}

func (h *scheduleHarness) tick(t *testing.T) {
	t.Helper()
	if err := h.service.TickSchedules(h.system); err != nil {
		t.Fatal(err)
	}
}

func (h *scheduleHarness) create(t *testing.T, input app.ScheduleInput) app.ScheduleView {
	t.Helper()
	if input.Harness == "" {
		input.Harness = "mock"
	}
	if input.TimeZone == "" {
		input.TimeZone = "UTC"
	}
	view, err := h.service.CreateSchedule(h.ctx, scheduleProject, input, "create-"+input.Name)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func (h *scheduleHarness) fires(t *testing.T, id string) []app.ScheduleFireView {
	t.Helper()
	items, _, err := h.service.ListScheduleFires(h.ctx, domain.ScheduleID(id), ports.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (h *scheduleHarness) latest(t *testing.T, id string) app.ScheduleFireView {
	t.Helper()
	items := h.fires(t, id)
	if len(items) == 0 {
		t.Fatal("no fires")
	}
	return items[0]
}

// ready makes the fire's Capsule Ready, which starts its idle session.
func (h *scheduleHarness) ready(t *testing.T, fire app.ScheduleFireView) {
	t.Helper()
	capsule := setProjectThreadCapsuleState(t, h.ctx, h.store, domain.CapsuleID(fire.CapsuleID), domain.CapsuleReady)
	if err := h.service.ReconcileProjectThreadCapsule(h.system, capsule); err != nil {
		t.Fatal(err)
	}
}

// emit delivers adapter frames to the Thread, as a live session would.
func (h *scheduleHarness) emit(t *testing.T, thread string, frames ...adapterproto.Frame) {
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

func (h *scheduleHarness) sentContaining(text string) int {
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

func (h *scheduleHarness) negotiateModels(threadRun domain.RunID) {
	h.runtime.mu.Lock()
	defer h.runtime.mu.Unlock()
	run := h.runtime.runs[threadRun]
	run.Models, run.Effort = true, true
	h.runtime.runs[threadRun] = run
}

// runToIdle fires the schedule, starts the session, sends the prompt, ends
// the agent's turn, and waits out the settle time.
func (h *scheduleHarness) runToIdle(t *testing.T, schedule app.ScheduleView) app.ScheduleFireView {
	t.Helper()
	h.tick(t)
	fire := h.latest(t, schedule.ID)
	if fire.State != string(domain.ScheduleFireStarting) || fire.ThreadID == "" {
		t.Fatalf("fire after first tick = %#v", fire)
	}
	h.ready(t, fire)
	h.tick(t) // starting -> selecting
	h.tick(t) // selecting -> running: prompt sent
	fire = h.latest(t, schedule.ID)
	if fire.State != string(domain.ScheduleFireRunning) {
		t.Fatalf("fire after prompt = %#v", fire)
	}
	h.emit(t, fire.ThreadID, adapterproto.Frame{Type: adapterproto.KindEnd})
	h.tick(t) // idle noticed
	h.advance(domain.ScheduleIdleSettle + time.Second)
	h.tick(t) // outcome decided
	return h.latest(t, schedule.ID)
}

func TestScheduleRunsOnTimeAndDeletesAnEmptyCapsule(t *testing.T) {
	h := newScheduleHarness(t)
	prompt := "update dependencies sentinel-7f3a"
	schedule := h.create(t, app.ScheduleInput{Name: "deps-weekly", Prompt: prompt, Cadence: "0 6 * * 1"})
	monday := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
	if schedule.NextFireAt == nil || !schedule.NextFireAt.Equal(monday) {
		t.Fatalf("next fire = %v", schedule.NextFireAt)
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
	if len(h.fires(t, schedule.ID)) != 0 {
		t.Fatal("fired before its time")
	}

	h.set(monday.Add(10 * time.Second))
	fire := h.runToIdle(t, schedule)
	if h.sentContaining("sentinel-7f3a") != 1 {
		t.Fatalf("prompt sends = %d", h.sentContaining("sentinel-7f3a"))
	}
	if fire.Trigger != "clock" || fire.Outcome != "no_changes" || fire.State != "cleaning" {
		t.Fatalf("fire = %#v", fire)
	}
	for range 4 {
		h.tick(t)
	}
	fire = h.latest(t, schedule.ID)
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
	view, err := h.service.GetSchedule(h.ctx, domain.ScheduleID(schedule.ID))
	if err != nil {
		t.Fatal(err)
	}
	if view.KeptCapsules != 0 || view.NextFireAt == nil || !view.NextFireAt.Equal(monday.AddDate(0, 0, 7)) {
		t.Fatalf("schedule after run = %#v", view)
	}
	notes, err := h.service.CapsuleSchedules(h.ctx, []domain.CapsuleID{capsule.ID})
	if err != nil {
		t.Fatal(err)
	}
	if note := notes[capsule.ID]; note.ScheduleName != "deps-weekly" || note.Outcome != "no_changes" {
		t.Fatalf("capsule note = %#v", note)
	}
}

func TestScheduleKeepsChangesAndStopsAtTheBacklog(t *testing.T) {
	h := newScheduleHarness(t)
	h.inspect.inspection = ports.DeliveryInspection{Dirty: true, HasDeliverableChanges: true}
	schedule := h.create(t, app.ScheduleInput{
		Name: "lint", Prompt: "fix lint", Cadence: "0 * * * *", MaxKept: 1,
	})
	h.set(*schedule.NextFireAt)
	fire := h.runToIdle(t, schedule)
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

	h.set(schedule.NextFireAt.Add(time.Hour))
	h.tick(t)
	skipped := h.latest(t, schedule.ID)
	if skipped.State != "skipped" || skipped.Reason != domain.ScheduleReasonBacklog {
		t.Fatalf("second fire = %#v", skipped)
	}
}

func TestScheduleSkipsAnOverlappingRun(t *testing.T) {
	h := newScheduleHarness(t)
	schedule := h.create(t, app.ScheduleInput{Name: "slow", Prompt: "work", Cadence: "*/15 * * * *"})
	h.set(*schedule.NextFireAt)
	h.tick(t)
	first := h.latest(t, schedule.ID)
	if first.State != "starting" {
		t.Fatalf("first = %#v", first)
	}
	h.set(schedule.NextFireAt.Add(15 * time.Minute))
	h.tick(t)
	second := h.latest(t, schedule.ID)
	if second.State != "skipped" || second.Reason != domain.ScheduleReasonOverlap {
		t.Fatalf("second = %#v", second)
	}
	manual, err := h.service.RunSchedule(h.ctx, domain.ScheduleID(schedule.ID), "run-now")
	if err != nil {
		t.Fatal(err)
	}
	if manual.State != "skipped" || manual.Reason != domain.ScheduleReasonOverlap || manual.Trigger != "manual" {
		t.Fatalf("manual = %#v", manual)
	}
}

func TestScheduleTimesOutAndPauses(t *testing.T) {
	h := newScheduleHarness(t)
	schedule := h.create(t, app.ScheduleInput{
		Name: "long", Prompt: "work", Cadence: "0 6 * * *", MaxRunSeconds: 300,
	})
	h.set(*schedule.NextFireAt)
	h.tick(t)
	fire := h.latest(t, schedule.ID)
	h.ready(t, fire)
	h.tick(t)
	h.tick(t)
	h.advance(6 * time.Minute)
	h.tick(t)
	fire = h.latest(t, schedule.ID)
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

func TestScheduleAppliesTheModelBeforeThePrompt(t *testing.T) {
	h := newScheduleHarness(t)
	schedule := h.create(t, app.ScheduleInput{
		Name: "model", Prompt: "prompt-after-model", Cadence: "0 6 * * *", Model: "opus", Effort: "high",
	})
	h.set(*schedule.NextFireAt)
	h.tick(t)
	fire := h.latest(t, schedule.ID)
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
	if fire := h.latest(t, schedule.ID); fire.State != "running" {
		t.Fatalf("fire = %#v", fire)
	}
}

func (h *scheduleHarness) sentFrame(kind adapterproto.Kind, value string) bool {
	h.runtime.mu.Lock()
	defer h.runtime.mu.Unlock()
	for _, frame := range h.runtime.frames {
		if frame.Type == kind && (frame.Model == value || frame.Effort == value) {
			return true
		}
	}
	return false
}

func TestScheduleFailsOnAnUnavailableModelAndTurnsOffAfterThree(t *testing.T) {
	h := newScheduleHarness(t)
	schedule := h.create(t, app.ScheduleInput{
		Name: "missing", Prompt: "never-sent", Cadence: "0 * * * *", Model: "nope", MaxKept: 20,
	})
	at := *schedule.NextFireAt
	for i := range 3 {
		h.set(at.Add(time.Duration(i) * time.Hour))
		h.tick(t)
		fire := h.latest(t, schedule.ID)
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
		fire = h.latest(t, schedule.ID)
		if fire.Outcome != "failed" || fire.Reason != domain.ScheduleReasonModelAbsent {
			t.Fatalf("fire %d = %#v", i, fire)
		}
	}
	if h.sentContaining("never-sent") != 0 {
		t.Fatal("prompt sent on a model the operator did not pick")
	}
	view, err := h.service.GetSchedule(h.ctx, domain.ScheduleID(schedule.ID))
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || view.DisabledReason != domain.ScheduleDisabledStartFailures || view.NextFireAt != nil {
		t.Fatalf("schedule = %#v", view)
	}
	enabled := true
	view, err = h.service.UpdateSchedule(h.ctx, domain.ScheduleID(schedule.ID), app.ScheduleUpdate{
		Enabled: &enabled, ExpectedResourceVersion: view.ResourceVersion,
	}, "turn-on")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.DisabledReason != "" || view.NextFireAt == nil {
		t.Fatalf("turned on = %#v", view)
	}
}

func TestScheduleCatchUpAndMissedFires(t *testing.T) {
	h := newScheduleHarness(t)
	schedule := h.create(t, app.ScheduleInput{Name: "hourly", Prompt: "work", Cadence: "0 * * * *"})
	first := *schedule.NextFireAt
	// Asleep for four and a half hours: four missed, the latest caught up.
	h.set(first.Add(4*time.Hour + 30*time.Minute))
	h.tick(t)
	fires := h.fires(t, schedule.ID)
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
	view, err := h.service.GetSchedule(h.ctx, domain.ScheduleID(schedule.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !view.NextFireAt.Equal(first.Add(5 * time.Hour)) {
		t.Fatalf("next = %v", view.NextFireAt)
	}

	// A catch-up window of zero misses everything late.
	zero := 0
	late := h.create(t, app.ScheduleInput{Name: "strict", Prompt: "work", Cadence: "30 * * * *", CatchUpSeconds: &zero})
	h.set(late.NextFireAt.Add(10 * time.Minute))
	h.tick(t)
	if fire := h.latest(t, late.ID); fire.State != "missed" {
		t.Fatalf("strict fire = %#v", fire)
	}
}

func TestScheduleValidation(t *testing.T) {
	h := newScheduleHarness(t)
	for name, input := range map[string]app.ScheduleInput{
		"too often":     {Name: "often", Prompt: "p", Cadence: "*/10 * * * *", Harness: "mock", TimeZone: "UTC"},
		"no prompt":     {Name: "empty", Prompt: "  ", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "UTC"},
		"bad name":      {Name: "Bad Name", Prompt: "p", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "UTC"},
		"bad zone":      {Name: "zone", Prompt: "p", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "Nowhere/Here"},
		"run too short": {Name: "short", Prompt: "p", Cadence: "0 6 * * *", Harness: "mock", TimeZone: "UTC", MaxRunSeconds: 60},
	} {
		if _, err := h.service.CreateSchedule(h.ctx, scheduleProject, input, "bad-"+input.Name); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	h.create(t, app.ScheduleInput{Name: "taken", Prompt: "p", Cadence: "0 6 * * *"})
	if _, err := h.service.CreateSchedule(h.ctx, scheduleProject, app.ScheduleInput{
		Name: "taken", Prompt: "p", Cadence: "0 7 * * *", Harness: "mock", TimeZone: "UTC",
	}, "dup"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate name err = %v", err)
	}
}

func TestScheduleUpdateAndDelete(t *testing.T) {
	h := newScheduleHarness(t)
	schedule := h.create(t, app.ScheduleInput{Name: "edit-me", Prompt: "old prompt", Cadence: "0 6 * * *"})
	prompt, cadence, enabled := "new prompt", "0 9 * * 1-5", false
	updated, err := h.service.UpdateSchedule(h.ctx, domain.ScheduleID(schedule.ID), app.ScheduleUpdate{
		Prompt: &prompt, Cadence: &cadence, Enabled: &enabled, ExpectedResourceVersion: schedule.ResourceVersion,
	}, "edit")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Prompt != "new prompt" || updated.Cadence != cadence || updated.Enabled || updated.NextFireAt != nil {
		t.Fatalf("updated = %#v", updated)
	}
	if _, err := h.service.UpdateSchedule(h.ctx, domain.ScheduleID(schedule.ID), app.ScheduleUpdate{
		Prompt: &prompt, ExpectedResourceVersion: schedule.ResourceVersion,
	}, "stale"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	if err := h.service.DeleteSchedule(h.ctx, domain.ScheduleID(schedule.ID), updated.ResourceVersion, "delete"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.GetSchedule(h.ctx, domain.ScheduleID(schedule.ID)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
}
