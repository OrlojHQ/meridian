package app_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubauth"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/testutil/fakeprovider"
)

type fakeGitHubOAuth struct {
	login       string
	scopes      string
	token       string
	dead        string
	last        string
	device      githubauth.DeviceCode
	poll        githubauth.DevicePoll
	repos       []githubauth.Repo
	listErr     error
	users       int
	probeStatus int
}

func (f *fakeGitHubOAuth) RequestDeviceCode(context.Context, string) (githubauth.DeviceCode, error) {
	return f.device, nil
}

func (f *fakeGitHubOAuth) PollDeviceToken(context.Context, string, string) (githubauth.DevicePoll, error) {
	return f.poll, nil
}

func (f *fakeGitHubOAuth) CurrentUser(_ context.Context, token string) (githubauth.Account, error) {
	f.users++
	f.last = token
	if f.dead != "" && token == f.dead {
		return githubauth.Account{}, &githubauth.StatusError{Status: http.StatusUnauthorized, Reason: "expired"}
	}
	return githubauth.Account{Login: f.login, Scopes: f.scopes}, nil
}

func (f *fakeGitHubOAuth) ListRepos(context.Context, string, string) ([]githubauth.Repo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.repos, nil
}

func (f *fakeGitHubOAuth) ProbeRepository(context.Context, string, string, string) (int, bool, error) {
	if f.probeStatus != 0 {
		return f.probeStatus, false, nil
	}
	return 200, false, nil
}

type fakeGitHubHost struct {
	login string
	token string
	reads int
}

func (f *fakeGitHubHost) Login(context.Context) (string, error) { return f.login, nil }
func (f *fakeGitHubHost) Token(context.Context) (string, error) {
	f.reads++
	return f.token, nil
}

func TestGitHubConnectionCoversDeliveryAndNamedSecretsWin(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	oauth := &fakeGitHubOAuth{login: "octocat", scopes: "repo read:user"}
	service.ConfigureGitHub("Iv1.public", nil, oauth)
	runtime := &deliveryRuntime{inspection: ports.DeliveryInspection{
		HEAD: deliveryHead, Tree: deliveryTree, Dirty: true,
		Branch: "feature/local", DefaultBranch: "main",
		OriginURL: "https://github.com/octocat/hello.git",
	}}
	github := &deliveryGitHub{}
	service.ConfigureDelivery(runtime, github)

	const connectionToken = "gho_connectiontokenvalue"
	state, err := service.ConnectGitHubToken(ctx, connectionToken, 0, "connect-1")
	if err != nil || !state.Connected || state.Login != "octocat" || state.Status != domain.GitHubConnectionActive {
		t.Fatalf("connect = %+v, %v", state, err)
	}
	project, err := service.CreateProjectConfigured(ctx, "hello", app.ProjectConfiguration{
		RepositoryURL: "https://github.com/octocat/hello", DefaultBaseBranch: "main",
		CommitAuthorName: "Octo", CommitAuthorEmail: "octo@example.test",
	}, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if project.GitSecretName != "" || project.GitPushSecretName != "" || project.GitHubAPISecretName != "" {
		t.Fatalf("project named secrets = %q %q %q", project.GitSecretName, project.GitPushSecretName, project.GitHubAPISecretName)
	}
	now := clock.Now()
	capsule := domain.Capsule{
		ID: "cap-1", ProjectID: project.ID, TimelineID: "tl-1", Name: "hello",
		State: domain.CapsuleReady, DesiredState: domain.IntentReady,
		ProviderResourceID: "provider-1", RestoreComplete: true,
		LastActivityAt: now, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		return tx.InsertTimeline(ctx, domain.Timeline{
			ID: capsule.TimelineID, ProjectID: project.ID, CapsuleID: capsule.ID,
			Reason: domain.TimelineRoot, CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	input := deliveryInput(deliveryFixture{project: project, capsule: capsule}, "delivery-connection")
	input.Action = domain.DeliveryOpenPullRequest
	input.PullRequestTitle = "Hello"
	if _, err := service.CreateDelivery(ctx, input); err != nil {
		t.Fatal(err)
	}
	github.mu.Lock()
	got := github.token
	github.mu.Unlock()
	if got != connectionToken {
		t.Fatalf("pull request token = %q", got)
	}
	runtime.mu.Lock()
	pushed := runtime.push.GitCredential.Password
	runtime.mu.Unlock()
	if pushed != connectionToken {
		t.Fatalf("push token = %q", pushed)
	}

	if _, err := service.PutSecret(ctx, "delivery_push", app.SecretInput{
		Purpose: domain.SecretGitPush, Username: "git-user", Password: "explicit-push-token",
	}, 0, "push-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PutSecret(ctx, "delivery_github", app.SecretInput{
		Purpose: domain.SecretGitHubAPI, Value: "explicit-api-token",
	}, 0, "api-secret"); err != nil {
		t.Fatal(err)
	}
	named, err := service.CreateProjectConfigured(ctx, "named", app.ProjectConfiguration{
		RepositoryURL: "https://github.com/octocat/hello", DefaultBaseBranch: "main",
		GitPushSecretName: "delivery_push", GitHubAPISecretName: "delivery_github",
		CommitAuthorName: "Octo", CommitAuthorEmail: "octo@example.test",
	}, "project-2")
	if err != nil {
		t.Fatal(err)
	}
	namedCapsule := capsule
	namedCapsule.ID = "cap-2"
	namedCapsule.ProjectID = named.ID
	namedCapsule.TimelineID = "tl-2"
	namedCapsule.ProviderResourceID = "provider-2"
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, namedCapsule); err != nil {
			return err
		}
		return tx.InsertTimeline(ctx, domain.Timeline{
			ID: namedCapsule.TimelineID, ProjectID: named.ID, CapsuleID: namedCapsule.ID,
			Reason: domain.TimelineRoot, CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.inspection.HEAD = deliveryHead
	runtime.inspection.Tree = deliveryTree
	runtime.inspection.Dirty = true
	runtime.mu.Unlock()
	namedInput := deliveryInput(deliveryFixture{project: named, capsule: namedCapsule}, "delivery-named")
	namedInput.Action = domain.DeliveryOpenPullRequest
	namedInput.PullRequestTitle = "Named"
	if _, err := service.CreateDelivery(ctx, namedInput); err != nil {
		t.Fatal(err)
	}
	github.mu.Lock()
	got = github.token
	github.mu.Unlock()
	if got != "explicit-api-token" {
		t.Fatalf("named secret did not win, token = %q", got)
	}
}

func TestHostGitHubIsConfirmedAndLocal(t *testing.T) {
	if app.HostGitHubAllowed(domain.TeamMode, true) || app.HostGitHubAllowed(domain.SingleUserMode, false) {
		t.Fatal("host GitHub was allowed for team mode or a remote request")
	}
	if !app.HostGitHubAllowed(domain.SingleUserMode, true) {
		t.Fatal("local single-user station refused host GitHub")
	}
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	host := &fakeGitHubHost{login: "octocat", token: "gho_hosttokenvalue"}
	service.ConfigureGitHub("", host, &fakeGitHubOAuth{login: "octocat", scopes: "repo"})
	login, available, err := service.HostGitHubLogin(ctx, false)
	if err != nil || available || login != "" || host.reads != 0 {
		t.Fatalf("remote probe = %q %v %v reads=%d", login, available, err, host.reads)
	}
	if _, err := service.UseHostGitHub(ctx, false, 0, "host-remote"); !errors.Is(err, domain.ErrUnsupported) || host.reads != 0 {
		t.Fatalf("remote confirm = %v reads=%d", err, host.reads)
	}
	login, available, err = service.HostGitHubLogin(ctx, true)
	if err != nil || !available || login != "octocat" || host.reads != 0 {
		t.Fatalf("local probe = %q %v %v reads=%d", login, available, err, host.reads)
	}
	state, err := service.UseHostGitHub(ctx, true, 0, "host-local")
	if err != nil || state.Login != "octocat" || host.reads != 1 {
		t.Fatalf("local confirm = %+v %v reads=%d", state, err, host.reads)
	}

	db, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE installation SET mode = 'team', oidc_issuer = 'https://idp.example', claimed_at = '2026-11-16T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	before := host.reads
	if _, err := service.UseHostGitHub(ctx, true, state.ResourceVersion, "host-team"); !errors.Is(err, domain.ErrUnsupported) || host.reads != before {
		t.Fatalf("team confirm = %v reads=%d", err, host.reads)
	}
}

func TestHostGitHubRefreshesAfterReauthentication(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	host := &fakeGitHubHost{login: "octocat", token: "gho_hosttokenvalue"}
	oauth := &fakeGitHubOAuth{login: "octocat", scopes: "repo"}
	service.ConfigureGitHub("", host, oauth)
	if _, err := service.UseHostGitHub(ctx, true, 0, "host-first"); err != nil {
		t.Fatal(err)
	}
	host.token = "gho_refreshedtokenvalue"
	oauth.dead = "gho_hosttokenvalue"
	before := host.reads
	remote, err := service.CheckGitHubConnection(ctx, false)
	if err != nil || remote.Status != domain.GitHubConnectionNeedsReconnect || host.reads != before {
		t.Fatalf("remote check = %+v %v reads=%d", remote, err, host.reads)
	}
	refreshed, err := service.CheckGitHubConnection(ctx, true)
	if err != nil || refreshed.Status != domain.GitHubConnectionActive || refreshed.Login != "octocat" {
		t.Fatalf("refresh = %+v %v", refreshed, err)
	}
	if oauth.last != "gho_refreshedtokenvalue" || host.reads != before+1 {
		t.Fatalf("last token = %q reads=%d", oauth.last, host.reads)
	}
}

func TestOrganizationSSODoesNotRetireTheLogin(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	oauth := &fakeGitHubOAuth{
		login: "octocat", scopes: "repo",
		listErr: &githubauth.StatusError{Status: http.StatusForbidden},
	}
	service.ConfigureGitHub("", nil, oauth)
	if _, err := service.ConnectGitHubToken(ctx, "gho_approvedtokenvalue", 0, "sso-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SearchGitHubRepos(ctx, ""); err == nil {
		t.Fatal("organization SSO response was treated as a repository list")
	}
	state, err := service.GitHubConnection(ctx)
	if err != nil || state.Status != domain.GitHubConnectionActive {
		t.Fatalf("connection = %+v %v", state, err)
	}
}

func TestGitHubDeviceFlowStoresOneConnection(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	oauth := &fakeGitHubOAuth{
		login: "octocat", scopes: "repo",
		device: githubauth.DeviceCode{
			DeviceCode: "device-secret", UserCode: "ABCD-EFGH",
			VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5,
		},
		poll: githubauth.DevicePoll{Token: "gho_devicetokenvalue", Scope: "repo"},
	}
	service.ConfigureGitHub("Iv1.public", nil, oauth)
	started, err := service.BeginGitHubDevice(ctx, 0)
	if err != nil || started.UserCode != "ABCD-EFGH" || started.VerificationURI != "https://github.com/login/device" {
		t.Fatalf("start = %+v, %v", started, err)
	}
	polled, err := service.PollGitHubDevice(ctx)
	if err != nil || polled.Status != "connected" || polled.Connection.Login != "octocat" {
		t.Fatalf("poll = %+v, %v", polled, err)
	}
	repos, err := service.SearchGitHubRepos(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Fatalf("repos = %+v", repos)
	}
	oauth.repos = []githubauth.Repo{{
		FullName: "octocat/hello", Private: true, Description: "hi", DefaultBranch: "main",
		URL: "https://github.com/octocat/hello",
	}}
	repos, err = service.SearchGitHubRepos(ctx, "hello")
	if err != nil || len(repos) != 1 || !repos[0].Private || repos[0].URL != "https://github.com/octocat/hello" {
		t.Fatalf("repos = %+v, %v", repos, err)
	}
}

func TestMissingRepoScopeNeedsReconnect(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	service.ConfigureGitHub("", nil, &fakeGitHubOAuth{login: "octocat", scopes: "read:user"})
	state, err := service.ConnectGitHubToken(ctx, "gho_limitedtokenvalue", 0, "limited")
	if err != nil || state.Status != domain.GitHubConnectionNeedsReconnect || state.Reason != domain.GitHubReasonMissingAccess {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestPrivateGitHubRepositoryIsRefusedBeforeTheProjectExists(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	service.ConfigureGitHub("", nil, &fakeGitHubOAuth{probeStatus: http.StatusNotFound})
	_, err = service.CreateProjectConfigured(ctx, "hidden", app.ProjectConfiguration{
		RepositoryURL: "https://github.com/octocat/hidden",
	}, "hidden-project")
	var access *domain.RepositoryAccessError
	if !errors.As(err, &access) || access.Error() != "Meridian couldn't access octocat/hidden. It may be private." {
		t.Fatalf("create = %v", err)
	}
	if strings.Contains(err.Error(), "://") {
		t.Fatal("access error included a URL")
	}
	page, err := service.ListProjects(ctx, 0, 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("projects = %+v, %v", page.Items, err)
	}
}

func TestRepositoryAccessIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(operatorContext())
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fakeprovider.New(fakeprovider.Options{
		CreateHook: func(context.Context, ports.CreateCapsuleRequest) error {
			return domain.RepositoryAccessFromURL("https://github.com/octocat/hidden.git")
		},
	})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	t.Cleanup(reconciler.Close)
	service := app.NewService(store, clock, ids, reconciler)
	now := clock.Now()
	project := domain.Project{
		OwnerUserID: domain.BootstrapOwnerID, ID: "project-hidden", Name: "hidden",
		RepositoryURL: "https://github.com/octocat/hidden",
		CreatedAt:     now, UpdatedAt: now, ResourceVersion: 1,
	}
	capsule := domain.Capsule{
		ID: "capsule-hidden", ProjectID: project.ID, Name: "hidden",
		State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, capsule)
	}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Enqueue(ctx, capsule.ID); err != nil {
		t.Fatal(err)
	}
	failed := awaitCapsuleState(t, service, capsule.ID, func(item domain.Capsule) bool {
		return item.State == domain.CapsuleFailed
	})
	if failed.FailureCode != domain.RepositoryAccess ||
		failed.Failure != "Meridian couldn't access octocat/hidden. It may be private." {
		t.Fatalf("failure = %q %q", failed.FailureCode, failed.Failure)
	}
	if strings.Contains(failed.Failure, "://") || strings.Contains(failed.Failure, "@") {
		t.Fatalf("failure leaked a credential or URL: %q", failed.Failure)
	}
	time.Sleep(80 * time.Millisecond)
	if calls := provider.Calls("create"); calls != 1 {
		t.Fatalf("create calls = %d, want 1", calls)
	}
}
