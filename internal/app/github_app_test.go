package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubapp"
	"github.com/OrlojHQ/meridian/internal/githubauth"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type ghAppClock struct{ now time.Time }

func (c ghAppClock) Now() time.Time { return c.now }

type ghAppIDs struct{ n int }

func (s *ghAppIDs) NewID() string {
	s.n++
	return fmt.Sprintf("id-%d", s.n)
}

type ghAppQueue struct{}

func (ghAppQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

type ghAppUser struct{}

func (ghAppUser) RequestDeviceCode(context.Context, string) (githubauth.DeviceCode, error) {
	return githubauth.DeviceCode{}, nil
}
func (ghAppUser) PollDeviceToken(context.Context, string, string) (githubauth.DevicePoll, error) {
	return githubauth.DevicePoll{}, nil
}
func (ghAppUser) CurrentUser(context.Context, string) (githubauth.Account, error) {
	return githubauth.Account{Login: "octocat", Scopes: "repo"}, nil
}
func (ghAppUser) ListRepos(context.Context, string, string) ([]githubauth.Repo, error) {
	return nil, nil
}

func testAppKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubAppTokensStayOnTheStationExceptTheInstallationToken(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	pemKey := testAppKey(t)
	const (
		clientSecret  = "client-secret-value"
		webhookSecret = "webhook-secret-value"
		userToken     = "gho_user_connection_token"
		installToken  = "ghs_installationtokenvalue"
	)
	var tokenBody map[string]any
	installed := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/conversions"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 42, "slug": "meridian", "name": "Meridian",
				"client_id": "Iv1.public", "client_secret": clientSecret,
				"webhook_secret": webhookSecret, "pem": pemKey,
				"html_url": "https://github.com/apps/meridian",
				"owner":    map[string]string{"login": "octocat", "type": "User"},
			})
		case r.URL.Path == "/repos/octocat/hello/installation":
			if !installed {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 99, "repository_selection": "all",
				"account": map[string]string{"login": "octocat", "type": "User"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/99/access_tokens":
			if err := json.NewDecoder(r.Body).Decode(&tokenBody); err != nil {
				t.Errorf("token body: %v", err)
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("missing app jwt")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": installToken, "expires_at": time.Now().Add(time.Hour).UTC(),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	service := NewService(store, ghAppClock{now: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)}, &ghAppIDs{}, ghAppQueue{})
	service.ConfigureSecrets(key)
	service.ConfigureGitHubApp("", githubapp.NewClient(server.Client()).WithBase(server.URL))
	service.ConfigureGitHub("", nil, ghAppUser{})

	setup, err := service.BeginGitHubAppSetup(ctx, "", "http://127.0.0.1:7777")
	if err != nil {
		t.Fatal(err)
	}
	if setup.ActionURL != "https://github.com/settings/apps/new" {
		t.Fatalf("action = %s", setup.ActionURL)
	}
	if strings.Contains(setup.Manifest, "PRIVATE KEY") || strings.Contains(setup.Manifest, pemKey) {
		t.Fatal("manifest contains a key")
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(setup.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	perms, _ := manifest["default_permissions"].(map[string]any)
	if perms["contents"] != "write" || perms["pull_requests"] != "write" || perms["checks"] != "read" || perms["issues"] != "write" {
		t.Fatalf("permissions = %#v", perms)
	}
	if _, ok := manifest["default_events"]; ok {
		t.Fatal("webhook events were requested without a public https address")
	}
	redirect, _ := manifest["redirect_url"].(string)
	state := ""
	if i := strings.Index(redirect, "state="); i >= 0 {
		state = redirect[i+len("state="):]
	}
	if err := service.CompleteGitHubAppSetup(context.Background(), state, "one-time-code"); err != nil {
		t.Fatal(err)
	}
	view, err := service.GitHubApp(ctx)
	if err != nil || !view.Configured || view.Slug != "meridian" || view.CanManage != true {
		t.Fatalf("view = %+v %v", view, err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), pemKey) || strings.Contains(string(encoded), clientSecret) || strings.Contains(string(encoded), webhookSecret) || strings.Contains(string(encoded), "PRIVATE KEY") {
		t.Fatal("API view contains an App secret")
	}
	db, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), pemKey) || strings.Contains(string(db), "PRIVATE KEY") || strings.Contains(string(db), clientSecret) {
		t.Fatal("database contains the App key")
	}

	access, err := service.runtimeGitHubAccess(ctx, domain.BootstrapOwnerID, "https://github.com/octocat/hello")
	if err != nil || access == nil || access.Token != installToken {
		t.Fatalf("session access = %+v %v", access, err)
	}
	if access.Token == userToken {
		t.Fatal("session received the user token")
	}
	repos, _ := tokenBody["repositories"].([]any)
	if len(repos) != 1 || repos[0] != "hello" {
		t.Fatalf("token repositories = %#v", tokenBody["repositories"])
	}
	asked, _ := tokenBody["permissions"].(map[string]any)
	if asked["contents"] != "write" || asked["metadata"] != "read" || asked["statuses"] != "read" {
		t.Fatalf("token permissions = %#v", asked)
	}

	if _, err := service.ConnectGitHubToken(ctx, userToken, 0, "connect-user"); err != nil {
		t.Fatal(err)
	}
	push, fromConnection, err := service.resolveDeliveryCredential(ctx, domain.BootstrapOwnerID, "https://github.com/octocat/hello", "", domain.SecretGitPush)
	if err != nil || fromConnection || push.Password != installToken {
		t.Fatalf("push credential = %q fromConnection=%v %v", push.Password, fromConnection, err)
	}
	apiToken, fromConnection, err := service.resolveDeliveryCredential(ctx, domain.BootstrapOwnerID, "https://github.com/octocat/hello", "", domain.SecretGitHubAPI)
	if err != nil || !fromConnection || apiToken.Value != userToken {
		t.Fatalf("pull request credential = %q fromConnection=%v %v", apiToken.Value, fromConnection, err)
	}

	service.clearGitHubAppCache()
	installed = false
	again, err := service.runtimeGitHubAccess(ctx, domain.BootstrapOwnerID, "https://github.com/octocat/hello")
	if err != nil || again != nil {
		t.Fatalf("missing install access = %+v %v", again, err)
	}
	project, err := service.CreateProjectConfigured(ctx, "hello", ProjectConfiguration{
		RepositoryURL: "https://github.com/octocat/hello", DefaultBaseBranch: "main",
		CommitAuthorName: "Octo", CommitAuthorEmail: "octo@example.test",
	}, "project-app")
	if err != nil {
		t.Fatal(err)
	}
	status, err := service.GitHubAppAccess(ctx, project.ID)
	if err != nil || status.State != "not_installed" || !strings.Contains(status.Message, "octocat/hello") {
		t.Fatalf("access = %+v %v", status, err)
	}

	off := false
	if _, err := service.UpdateProfile(ctx, ProfileUpdate{
		CapsuleGitHubAccess: &off, ExpectedResourceVersion: 0,
	}, "profile-off"); err != nil {
		t.Fatal(err)
	}
	service.clearGitHubAppCache()
	installed = true
	if access, err := service.runtimeGitHubAccess(ctx, domain.BootstrapOwnerID, "https://github.com/octocat/hello"); err != nil || access != nil {
		t.Fatalf("switch off = %+v %v", access, err)
	}
	status, err = service.GitHubAppAccess(ctx, project.ID)
	if err != nil || status.State != "off" {
		t.Fatalf("off access = %+v %v", status, err)
	}
}

func TestGitHubAppWebhookVerifiesTheSignature(t *testing.T) {
	ctx := operatorContext()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dir), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	const webhookSecret = "webhook-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 7, "slug": "meridian", "name": "Meridian", "client_id": "Iv1.public",
			"client_secret": "client-secret-value", "webhook_secret": webhookSecret,
			"pem":      testAppKey(t),
			"html_url": "https://github.com/apps/meridian",
			"owner":    map[string]string{"login": "octocat", "type": "User"},
		})
	}))
	t.Cleanup(server.Close)
	service := NewService(store, ghAppClock{now: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)}, &ghAppIDs{}, ghAppQueue{})
	service.ConfigureSecrets(key)
	service.ConfigureGitHubApp("https://station.example", githubapp.NewClient(server.Client()).WithBase(server.URL))
	setup, err := service.BeginGitHubAppSetup(ctx, "acme", "https://station.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(setup.ActionURL, "https://github.com/organizations/acme/") {
		t.Fatalf("org action = %s", setup.ActionURL)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(setup.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	events, _ := manifest["default_events"].([]any)
	if len(events) == 0 {
		t.Fatal("public https station did not request webhook events")
	}
	redirect, _ := manifest["redirect_url"].(string)
	state := redirect[strings.Index(redirect, "state=")+len("state="):]
	if err := service.CompleteGitHubAppSetup(principal.WithSystem(context.Background()), state, "code"); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"action":"created","installation":{"id":5,"repository_selection":"all","account":{"login":"acme","type":"Organization"}}}`)
	if err := service.ReceiveGitHubAppWebhook(context.Background(), "installation", "sha256=dead", body); err == nil {
		t.Fatal("bad signature was accepted")
	}
	mac := githubappSignature(webhookSecret, body)
	if err := service.ReceiveGitHubAppWebhook(context.Background(), "installation", mac, body); err != nil {
		t.Fatal(err)
	}
	view, err := service.GitHubApp(ctx)
	if err != nil || len(view.Installations) != 1 || view.Installations[0].AccountLogin != "acme" || view.Installations[0].RepositorySelection != "all" {
		t.Fatalf("installations = %+v %v", view.Installations, err)
	}
	select {
	case <-service.ciWake:
		t.Fatal("installation event woke CI")
	default:
	}
	checkBody := []byte(`{"action":"completed"}`)
	if err := service.ReceiveGitHubAppWebhook(context.Background(), "check_run", githubappSignature(webhookSecret, checkBody), checkBody); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.ciWake:
	default:
		t.Fatal("check event did not wake CI")
	}
}

func githubappSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubRefreshWaitIsBeforeExpiry(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	if got := githubRefreshWait(now, now.Add(time.Hour)); got != 50*time.Minute {
		t.Fatalf("wait = %s", got)
	}
	if got := githubRefreshWait(now, now.Add(5*time.Minute)); got != time.Minute {
		t.Fatalf("short wait = %s", got)
	}
}

type recordingPush struct{ token string }

func (r *recordingPush) RefreshGitHubToken(_ context.Context, _, token string) error {
	r.token = token
	return nil
}

func TestPushGitHubTokenUsesTheRuntime(t *testing.T) {
	service := NewService(struct{ ports.Store }{}, ghAppClock{}, &ghAppIDs{}, ghAppQueue{})
	push := &recordingPush{}
	service.githubPush = push
	if err := service.pushGitHubToken(principal.WithSystem(context.Background()), "capsule", "ghs_next"); err != nil {
		t.Fatal(err)
	}
	if push.token != "ghs_next" {
		t.Fatalf("pushed %q", push.token)
	}
}
