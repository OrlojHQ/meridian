package app_test

import (
	"context"
	"fmt"
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
)

type scriptedChecks struct {
	mu        sync.Mutex
	calls     int
	failUntil int
	output    string
	lastArgv  []string
}

func (s *scriptedChecks) RunChecks(_ context.Context, _ string, request ports.CheckRequest) (ports.CheckExecution, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	if len(request.Commands) > 0 {
		s.lastArgv = append([]string{}, request.Commands[0].Argv...)
	}
	s.mu.Unlock()
	code := 0
	if call <= s.failUntil {
		code = 1
	}
	results := make([]ports.CheckCommandResult, 0, len(request.Commands))
	for _, command := range request.Commands {
		results = append(results, ports.CheckCommandResult{
			Name: command.Name, Argv: command.Argv, ExitCode: code,
			Output: []byte(s.output), Duration: 20 * time.Millisecond,
		})
	}
	return ports.CheckExecution{
		HeadBefore: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TreeBefore: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		HeadAfter:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TreeAfter:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Results:    results,
	}, nil
}

func (s *scriptedChecks) SuggestChecks(context.Context, string) (ports.CheckSuggestion, error) {
	return ports.CheckSuggestion{Checks: []ports.CheckCommand{{
		Name: "test", Argv: []string{"make", "test"}, Timeout: 30 * time.Second,
	}}}, nil
}

func TestChecksReworkUntilPassAndKeepOutputOutOfEvents(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	secretKey, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer secretKey.Zero()
	checks := &scriptedChecks{failUntil: 1, output: "boom-sentinel\nsecret=ghp_abcdefghijklmnop\n"}
	service.ConfigureSecrets(secretKey)
	service.ConfigureChecks(checks)
	service.SetCheckDebounce(0)
	project, err := service.GetProject(ctx, "project-thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProjectChecks(ctx, project.ID, app.ProjectChecksUpdate{
		Checks:     []app.CheckCommandView{{Name: "test", Argv: []string{"make", "test"}, TimeoutSeconds: 30}},
		AutoRework: true, MaxReworkAttempts: 3, ExpectedResourceVersion: project.ResourceVersion,
	}, "set-checks"); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "do the work",
		Start: true, IdempotencyKey: "thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{{Sequence: 1, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
		}}},
		NextCursor: 1,
	}
	if err := service.SyncThread(ctx, created.Thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.TickChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if checks.calls != 1 {
		t.Fatalf("calls = %d", checks.calls)
	}
	if strings.Join(checks.lastArgv, " ") != "make test" {
		t.Fatalf("argv = %#v", checks.lastArgv)
	}
	var rework string
	for _, frame := range runtime.frames {
		if frame.Type == adapterproto.KindUserMessage && strings.Contains(frame.Content, "Required check") {
			rework = frame.Content
		}
	}
	if !strings.Contains(rework, "Attempt 2 of 3") || !strings.Contains(rework, "boom-sentinel") {
		t.Fatalf("rework = %q", rework)
	}
	if strings.Contains(rework, "ghp_") {
		t.Fatalf("rework leaked a token: %q", rework)
	}
	summary := mustSummary(t, service, ctx, capsuleID)
	if summary.Phase != string(domain.CheckPhaseReworking) {
		t.Fatalf("phase = %#v", summary)
	}
	runtime.events.Items = []ports.RuntimeStructuredEvent{{Sequence: 2, Frame: adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
	}}}
	runtime.events.NextCursor = 2
	if err := service.SyncThread(ctx, created.Thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.TickChecks(ctx); err != nil {
		t.Fatal(err)
	}
	summary = mustSummary(t, service, ctx, capsuleID)
	if summary.Phase != string(domain.CheckPhasePassed) || checks.calls != 2 {
		t.Fatalf("after rework phase=%s calls=%d", summary.Phase, checks.calls)
	}
	assertEventsOmit(t, ctx, store, capsuleID, "boom-sentinel", "ghp_")
	runs, _, err := service.ListCheckRuns(ctx, capsuleID, ports.Page{Limit: 10})
	if err != nil || len(runs) == 0 {
		t.Fatal(err)
	}
	if !strings.Contains(runs[0].PullRequestSection, "Checked tree") || strings.Contains(runs[0].PullRequestSection, "boom-sentinel") {
		t.Fatalf("pull request section = %q", runs[0].PullRequestSection)
	}
}

func TestChecksStopAfterTheBudgetAndWhenTheOperatorInterrupts(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	checks := &scriptedChecks{failUntil: 5, output: "still-broken"}
	service.ConfigureChecks(checks)
	service.SetCheckDebounce(0)
	project, err := service.GetProject(ctx, "project-thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProjectChecks(ctx, project.ID, app.ProjectChecksUpdate{
		Checks:     []app.CheckCommandView{{Name: "test", Argv: []string{"make", "test"}, TimeoutSeconds: 30}},
		AutoRework: true, MaxReworkAttempts: 2, ExpectedResourceVersion: project.ResourceVersion,
	}, "set-checks"); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "try",
		Start: true, IdempotencyKey: "thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	idle := func(sequence uint64) {
		t.Helper()
		runtime.events = ports.RuntimeStructuredEvents{
			Items: []ports.RuntimeStructuredEvent{{Sequence: sequence, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
			}}},
			NextCursor: sequence,
		}
		if err := service.SyncThread(ctx, created.Thread.ID); err != nil {
			t.Fatal(err)
		}
		if err := service.TickChecks(ctx); err != nil {
			t.Fatal(err)
		}
	}
	idle(1)
	if mustSummary(t, service, ctx, capsuleID).Phase != string(domain.CheckPhaseReworking) {
		t.Fatal(mustSummary(t, service, ctx, capsuleID).Phase)
	}
	idle(2)
	summary := mustSummary(t, service, ctx, capsuleID)
	if summary.Phase != string(domain.CheckPhaseFailing) || summary.Attempt != 2 || checks.calls != 2 {
		t.Fatalf("exhausted = %#v calls=%d", summary, checks.calls)
	}
	idle(3)
	if checks.calls != 2 {
		t.Fatalf("budget did not hold, calls=%d", checks.calls)
	}
	if _, err := service.SendThreadMessage(ctx, created.Thread.ID, mustThreadVersion(t, service, ctx, created.Thread.ID), "a new task", "operator", ""); err != nil {
		t.Fatal(err)
	}
	idle(4)
	if checks.calls != 3 {
		t.Fatalf("a new task should start the counter over, calls=%d", checks.calls)
	}
	_ = store
}

func TestRunningCheckIsNotLeftRunningAcrossRestart(t *testing.T) {
	ctx := operatorContext()
	store, service, _, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	started := make(chan struct{})
	release := make(chan struct{})
	checks := &blockingChecks{started: started, release: release}
	service.ConfigureChecks(checks)
	service.SetCheckDebounce(0)
	project, err := service.GetProject(ctx, "project-thread")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProjectChecks(ctx, project.ID, app.ProjectChecksUpdate{
		Checks:     []app.CheckCommandView{{Name: "test", Argv: []string{"make", "test"}, TimeoutSeconds: 30}},
		AutoRework: false, MaxReworkAttempts: 0, ExpectedResourceVersion: project.ResourceVersion,
	}, "set-checks"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCheckRun(ctx, capsuleID, "manual"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.TickChecks(ctx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("check did not start")
	}
	during, _, err := service.ListCheckRuns(ctx, capsuleID, ports.Page{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(during) != 1 || during[0].State != string(domain.CheckRunning) {
		t.Fatalf("during = %#v", during)
	}
	restarted := app.NewService(store, &testClock{now: time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)}, &prefixedIDs{}, &recordingQueue{})
	restarted.ConfigureChecks(checks)
	restarted.SetCheckDebounce(0)
	if err := restarted.TickChecks(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil && !strings.Contains(err.Error(), "conflict") {
		t.Fatal(err)
	}
	summary := mustSummary(t, restarted, ctx, capsuleID)
	if summary.Phase == string(domain.CheckPhaseRunning) || summary.Phase == string(domain.CheckPhaseQueued) && summary.RunID == "" {
		t.Fatalf("stuck summary %#v", summary)
	}
	runs, _, err := restarted.ListCheckRuns(ctx, capsuleID, ports.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var sawError bool
	for _, run := range runs {
		if run.State == string(domain.CheckError) {
			sawError = true
		}
		if run.State == string(domain.CheckRunning) {
			t.Fatalf("running row survived restart: %#v", runs)
		}
	}
	if !sawError {
		t.Fatalf("runs = %#v", runs)
	}
}

type prefixedIDs struct{ n int }

func (p *prefixedIDs) NewID() string {
	p.n++
	return fmt.Sprintf("restart-%03d", p.n)
}

type blockingChecks struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (b *blockingChecks) RunChecks(ctx context.Context, _ string, request ports.CheckRequest) (ports.CheckExecution, error) {
	b.mu.Lock()
	b.calls++
	call := b.calls
	b.mu.Unlock()
	passed := ports.CheckExecution{
		HeadBefore: "head", TreeBefore: "tree", HeadAfter: "head", TreeAfter: "tree",
		Results: []ports.CheckCommandResult{{
			Name: request.Commands[0].Name, Argv: request.Commands[0].Argv, ExitCode: 0,
		}},
	}
	if call > 1 {
		return passed, nil
	}
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ports.CheckExecution{}, ctx.Err()
	}
	return passed, nil
}

func (b *blockingChecks) SuggestChecks(context.Context, string) (ports.CheckSuggestion, error) {
	return ports.CheckSuggestion{}, nil
}

func mustSummary(t *testing.T, service *app.Service, ctx context.Context, id domain.CapsuleID) app.ChecksSummaryView {
	t.Helper()
	summaries, err := service.CheckSummaries(ctx, []domain.CapsuleID{id})
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := summaries[id]
	if !ok {
		t.Fatal("missing summary")
	}
	return summary
}

func mustThreadVersion(t *testing.T, service *app.Service, ctx context.Context, id domain.ThreadID) domain.ResourceVersion {
	t.Helper()
	thread, err := service.GetThread(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return thread.ResourceVersion
}

func assertEventsOmit(t *testing.T, ctx context.Context, store interface {
	View(context.Context, func(ports.Reader) error) error
}, capsuleID domain.CapsuleID, forbidden ...string) {
	t.Helper()
	err := store.View(ctx, func(reader ports.Reader) error {
		events, err := reader.ListEvents(ctx, "capsule", string(capsuleID))
		if err != nil {
			return err
		}
		for _, event := range events {
			for _, word := range forbidden {
				if strings.Contains(string(event.Data), word) || strings.Contains(event.Type, word) {
					t.Fatalf("event %s leaked %q in %s", event.Type, word, event.Data)
				}
			}
		}
		records, _, err := reader.ListAuditRecords(ctx, "", 0, 50)
		if err != nil {
			return err
		}
		for _, record := range records {
			if strings.Contains(string(record.Action), "make test") {
				t.Fatal("audit recorded a command")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
