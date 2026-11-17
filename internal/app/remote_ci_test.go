package app_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubauth"
	"github.com/OrlojHQ/meridian/internal/githubci"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type scriptCI struct {
	mu        sync.Mutex
	snapshots []githubci.Snapshot
	err       error
	calls     int
	section   string
	tokenSeen string
}

func (s *scriptCI) ReadCommit(_ context.Context, token, _, _, _ string) (githubci.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.tokenSeen = token
	if s.err != nil {
		return githubci.Snapshot{}, s.err
	}
	if len(s.snapshots) == 0 {
		return githubci.Snapshot{}, nil
	}
	snap := s.snapshots[0]
	s.snapshots = s.snapshots[1:]
	return snap, nil
}

func (s *scriptCI) WriteChecksSection(_ context.Context, token, _, _ string, _ int64, section string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.section = section
	if strings.Contains(section, token) || strings.Contains(section, "boom-log") || strings.Contains(section, "ghp_") {
		return errLeaked
	}
	return nil
}

type pushSpy struct{ pushes int }

func (p *pushSpy) InspectDelivery(context.Context, string) (ports.DeliveryInspection, error) {
	return ports.DeliveryInspection{}, nil
}
func (p *pushSpy) CommitDelivery(context.Context, ports.DeliveryCommitRequest) (ports.DeliveryCommitResult, error) {
	return ports.DeliveryCommitResult{}, errLeaked
}
func (p *pushSpy) PushDelivery(context.Context, ports.DeliveryPushRequest) (ports.DeliveryPushResult, error) {
	p.pushes++
	return ports.DeliveryPushResult{}, errLeaked
}

var errLeaked = errString("leaked")

type errString string

func (e errString) Error() string { return string(e) }

func ciService(t *testing.T) (context.Context, string, *sqlite.Store, *app.Service, *structuredRuntime, domain.CapsuleID) {
	t.Helper()
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	capsuleID := domain.CapsuleID("capsule-ci")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		project := domain.Project{
			OwnerUserID: domain.BootstrapOwnerID, ID: "project-ci", Name: "project",
			RepositoryURL: "https://github.com/orloj/meridian",
			CreatedAt:     now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, domain.Capsule{
			ID: capsuleID, ProjectID: project.ID, Name: "capsule", State: domain.CapsuleReady,
			DesiredState: domain.IntentReady, ProviderResourceID: "resource-ci",
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "secret"), true)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := transcripts.NewInstallationKey("test-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &structuredRuntime{}
	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	service.ConfigureThreads(runtime, transcript)
	service.ConfigureSecrets(key)
	service.ConfigureGitHub("client", nil, &fakeGitHubOAuth{login: "octocat", scopes: "repo"})
	service.SetRemoteCIImmediatePoll(true)
	t.Cleanup(func() {
		store.Close()
		key.Zero()
		transcript.Zero()
	})
	return ctx, dir, store, service, runtime, capsuleID
}

func TestRemoteCIFailureReworksWithoutPushing(t *testing.T) {
	ctx, dir, _, service, runtime, capsuleID := ciService(t)
	if _, err := service.ConnectGitHubToken(ctx, "ghp_testtokenvalue", 0, "connect-ci"); err != nil {
		t.Fatal(err)
	}
	saveCIChecks(t, ctx, service)
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "do the work",
		Start: true, IdempotencyKey: "thread-ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := &scriptCI{snapshots: []githubci.Snapshot{{Checks: []githubci.Check{{
		Name: "build", Source: "check_run", Conclusion: "failure",
		URL:    "https://github.com/orloj/meridian/runs/9",
		Detail: "boom-log\nsecret=ghp_abcdefghijklmnop\n",
	}}}}}
	pushes := &pushSpy{}
	service.ConfigureDelivery(pushes, nil)
	service.ConfigureRemoteCI(reader)
	head := strings.Repeat("a", 40)
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 7,
		URL: "https://github.com/orloj/meridian/pull/7", Head: head, Tree: strings.Repeat("b", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	// The agent is still on its first turn, so the CI output waits.
	view, err := service.RemoteCI(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "failing" || view.ReworkSent {
		t.Fatalf("mid-turn view = %#v", view)
	}
	for _, frame := range runtime.frames {
		if strings.Contains(frame.Content, "GitHub CI failed") {
			t.Fatal("CI rework was sent while the agent was mid-turn")
		}
	}
	endTurn(t, ctx, service, runtime, created.Thread.ID)
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = service.RemoteCI(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "failing" || !strings.Contains(view.Label, "CI failing (1/3)") || !view.PushReview || !view.ReworkSent {
		t.Fatalf("view = %#v", view)
	}
	if !strings.Contains(view.Label, "Review the fix before it can be pushed.") {
		t.Fatalf("label = %q", view.Label)
	}
	if view.Checks[0].Output != "boom-log\nsecret=[redacted]\n" && !strings.Contains(view.Checks[0].Output, "[redacted]") {
		t.Fatalf("output = %q", view.Checks[0].Output)
	}
	if strings.Contains(view.Checks[0].Output, "ghp_") {
		t.Fatalf("output leaked a token: %q", view.Checks[0].Output)
	}
	var rework string
	for _, frame := range runtime.frames {
		if strings.Contains(frame.Content, "GitHub CI failed") {
			rework = frame.Content
		}
	}
	if !strings.Contains(rework, "Attempt 2 of 3") || !strings.Contains(rework, "will not push") {
		t.Fatalf("rework = %q", rework)
	}
	if strings.Contains(rework, "ghp_") {
		t.Fatalf("rework leaked a token: %q", rework)
	}
	if pushes.pushes != 0 {
		t.Fatalf("pushes = %d", pushes.pushes)
	}
	if !strings.Contains(reader.section, "failure `build`") || strings.Contains(reader.section, "boom-log") {
		t.Fatalf("section = %q", reader.section)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "boom-log") || strings.Contains(string(raw), "ghp_abcdefghijklmnop") {
		t.Fatal("sqlite stored CI plaintext")
	}
	state, err := service.GitHubConnection(ctx)
	if err != nil || state.Status != domain.GitHubConnectionActive {
		t.Fatalf("connection = %#v %v", state, err)
	}
}

func TestRemoteCI403DoesNotRetireTheLogin(t *testing.T) {
	ctx, _, _, service, _, capsuleID := ciService(t)
	if _, err := service.ConnectGitHubToken(ctx, "ghp_testtokenvalue", 0, "connect-403"); err != nil {
		t.Fatal(err)
	}
	service.ConfigureRemoteCI(&scriptCI{err: &githubauth.StatusError{Status: 403}})
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 8,
		Head: strings.Repeat("c", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := service.RemoteCI(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Label != "Can't read CI for this repository" {
		t.Fatalf("label = %q", view.Label)
	}
	state, err := service.GitHubConnection(ctx)
	if err != nil || state.Status != domain.GitHubConnectionActive {
		t.Fatalf("connection = %#v %v", state, err)
	}
}

func TestRemoteCI401AsksToReconnect(t *testing.T) {
	ctx, _, _, service, _, capsuleID := ciService(t)
	if _, err := service.ConnectGitHubToken(ctx, "ghp_testtokenvalue", 0, "connect-401"); err != nil {
		t.Fatal(err)
	}
	service.ConfigureRemoteCI(&scriptCI{err: &githubauth.StatusError{Status: 401, Reason: "invalid"}})
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 9,
		Head: strings.Repeat("d", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := service.RemoteCI(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Label != "Can't read CI: Reconnect GitHub" {
		t.Fatalf("label = %q", view.Label)
	}
	state, err := service.GitHubConnection(ctx)
	if err != nil || state.Status != domain.GitHubConnectionNeedsReconnect {
		t.Fatalf("connection = %#v %v", state, err)
	}
}

func TestRemoteCIWithoutConnectionStillRunsLocalChecks(t *testing.T) {
	ctx, _, _, service, runtime, capsuleID := ciService(t)
	checks := &scriptedChecks{}
	service.ConfigureChecks(checks)
	service.SetCheckDebounce(0)
	project, err := service.GetProject(ctx, "project-ci")
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
		Start: true, IdempotencyKey: "thread-local",
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
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 4,
		Head: strings.Repeat("e", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := service.RemoteCI(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Label != "Connect GitHub to read CI" || view.State != "connect" {
		t.Fatalf("view = %#v", view)
	}
	if err := service.SyncThread(ctx, created.Thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.TickChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if checks.calls == 0 {
		t.Fatal("local checks did not run")
	}
}

func TestRemoteCIWaitsThenPasses(t *testing.T) {
	ctx, _, _, service, _, capsuleID := ciService(t)
	if _, err := service.ConnectGitHubToken(ctx, "ghp_testtokenvalue", 0, "connect-pass"); err != nil {
		t.Fatal(err)
	}
	reader := &scriptCI{snapshots: []githubci.Snapshot{
		{Checks: []githubci.Check{{Name: "build", Source: "check_run", Conclusion: "pending"}}},
		{Checks: []githubci.Check{{Name: "build", Source: "check_run", Conclusion: "success", URL: "https://github.com/orloj/meridian/runs/1"}}},
	}}
	service.ConfigureRemoteCI(reader)
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 3,
		Head: strings.Repeat("f", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := service.RemoteCI(ctx, capsuleID)
	if err != nil || view.Label != "Waiting on CI" {
		t.Fatalf("view = %#v %v", view, err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = service.RemoteCI(ctx, capsuleID)
	if err != nil || view.Label != "CI passed" || view.State != "passed" {
		t.Fatalf("view = %#v %v", view, err)
	}
}

func saveCIChecks(t *testing.T, ctx context.Context, service *app.Service) {
	t.Helper()
	project, err := service.GetProject(ctx, "project-ci")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProjectChecks(ctx, project.ID, app.ProjectChecksUpdate{
		Checks:     []app.CheckCommandView{{Name: "test", Argv: []string{"make", "test"}, TimeoutSeconds: 30}},
		AutoRework: true, MaxReworkAttempts: 3, ExpectedResourceVersion: project.ResourceVersion,
	}, "save-ci-checks"); err != nil {
		t.Fatal(err)
	}
}

func endTurn(t *testing.T, ctx context.Context, service *app.Service, runtime *structuredRuntime, thread domain.ThreadID) {
	t.Helper()
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{{Sequence: 1, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
		}}},
		NextCursor: 1,
	}
	if err := service.SyncThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
}

func failingCI() *scriptCI {
	return &scriptCI{snapshots: []githubci.Snapshot{{Checks: []githubci.Check{{
		Name: "build", Source: "check_run", Conclusion: "failure",
		URL: "https://github.com/orloj/meridian/runs/9", Detail: "boom-log\n",
	}}}}}
}

// A Project that never saved its checks settings is watched and shown, but
// its agent is not sent CI output.
func TestRemoteCIReworkIsOptIn(t *testing.T) {
	ctx, _, _, service, runtime, capsuleID := ciService(t)
	if _, err := service.ConnectGitHubToken(ctx, "ghp_testtokenvalue", 0, "connect-ci"); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "do the work",
		Start: true, IdempotencyKey: "thread-ci",
	})
	if err != nil {
		t.Fatal(err)
	}
	endTurn(t, ctx, service, runtime, created.Thread.ID)
	service.ConfigureRemoteCI(failingCI())
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 7,
		Head: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := service.TickRemoteCI(ctx); err != nil {
			t.Fatal(err)
		}
	}
	view, err := service.RemoteCI(ctx, capsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "failing" || view.ReworkSent {
		t.Fatalf("view = %#v", view)
	}
	for _, frame := range runtime.frames {
		if strings.Contains(frame.Content, "GitHub CI failed") {
			t.Fatal("CI rework was sent to a Project that did not opt in")
		}
	}
}

// A watch whose Capsule can no longer use the result stops polling.
func TestRemoteCIStopsPollingForASealedCapsule(t *testing.T) {
	ctx, _, store, service, _, capsuleID := ciService(t)
	if err := service.RecordRemoteCI(ctx, app.RemoteCIOrigin{
		CapsuleID: capsuleID, Owner: "orloj", Repository: "meridian", Number: 7,
		Head: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	watch := latestWatch(t, ctx, store, capsuleID)
	if watch.State != domain.RemoteCIConnect || watch.NextPollAt.IsZero() {
		t.Fatalf("watch before sealing = %#v", watch)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.State = domain.CapsuleSealed
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, previous)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TickRemoteCI(ctx); err != nil {
		t.Fatal(err)
	}
	watch = latestWatch(t, ctx, store, capsuleID)
	if !watch.NextPollAt.IsZero() || watch.FinishedAt.IsZero() || watch.State != domain.RemoteCIConnect {
		t.Fatalf("watch after sealing = %#v", watch)
	}
}

func latestWatch(t *testing.T, ctx context.Context, store *sqlite.Store, id domain.CapsuleID) domain.RemoteCIWatch {
	t.Helper()
	var watch domain.RemoteCIWatch
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		watch, err = reader.LatestRemoteCI(ctx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return watch
}

func TestRemoteCIStalledDelayIsBounded(t *testing.T) {
	for _, item := range []struct {
		age, want time.Duration
	}{
		{0, time.Minute}, {5 * time.Minute, time.Minute}, {time.Hour, 6 * time.Minute},
		{24 * time.Hour, time.Hour}, {30 * 24 * time.Hour, time.Hour},
	} {
		if got := domain.RemoteCIStalledDelay(item.age); got != item.want {
			t.Fatalf("delay(%s) = %s, want %s", item.age, got, item.want)
		}
	}
}
