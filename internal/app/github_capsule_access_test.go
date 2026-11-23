package app_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

func TestCapsuleSessionReceivesGitHubAccessAndKeepsTheTokenOutOfTheDatabase(t *testing.T) {
	ctx := operatorContext()
	store, service, runtime, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	secretKey, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secretKey.Zero)
	service.ConfigureSecrets(secretKey)
	service.ConfigureGitHub("Iv1.public", nil, &fakeGitHubOAuth{login: "octocat", scopes: "repo, read:user"})

	insertReady := func(id domain.CapsuleID) {
		t.Helper()
		now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
		if err := store.Transact(ctx, func(tx ports.Transaction) error {
			return tx.InsertCapsule(ctx, domain.Capsule{
				ID: id, ProjectID: "project-thread", Name: string(id), State: domain.CapsuleReady,
				DesiredState: domain.IntentReady, ProviderResourceID: "resource-" + string(id),
				CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}
	insertReady("capsule-github-on")
	insertReady("capsule-github-off")

	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "before",
		Start: true, IdempotencyKey: "github-before",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	beforeToken := runtime.githubToken
	beforeMeta := runtime.lastStart.Frame.Metadata
	runtime.mu.Unlock()
	if beforeToken != "" || strings.Contains(string(beforeMeta), `"github"`) {
		t.Fatalf("session before a connection received GitHub access: %q %s", beforeToken, beforeMeta)
	}

	const token = "gho_CapsuleAccessSecretValue99"
	state, err := service.ConnectGitHubToken(ctx, token, 0, "github-connect")
	if err != nil || state.Login != "octocat" || state.Status != "active" {
		t.Fatalf("connect = %+v, %v", state, err)
	}
	if _, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: "capsule-github-on", Harness: "mock", FirstMessage: "with access",
		Start: true, IdempotencyKey: "github-on",
	}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	grantedToken := runtime.githubToken
	grantedLogin := runtime.githubLogin
	grantedMeta := append([]byte(nil), runtime.lastStart.Frame.Metadata...)
	runtime.mu.Unlock()
	if grantedToken != token || grantedLogin != "octocat" {
		t.Fatalf("granted access = login %q token %q", grantedLogin, grantedToken)
	}
	login, ok := adapterproto.ReadGitHubAccess(grantedMeta)
	if !ok || login != "octocat" || strings.Contains(string(grantedMeta), token) {
		t.Fatalf("metadata = %s", grantedMeta)
	}
	for _, path := range []string{store.Path(), store.Path() + "-wal", store.Path() + "-shm"} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil && os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), token) {
			t.Fatalf("GitHub token was written to %s", path)
		}
	}

	profile, err := service.GetProfile(ctx)
	if err != nil || !profile.CapsuleGitHubAccess {
		t.Fatalf("profile access = %+v, %v", profile.CapsuleGitHubAccess, err)
	}
	off := false
	if _, err := service.UpdateProfile(ctx, app.ProfileUpdate{
		GitName: profile.GitName, GitEmail: profile.GitEmail,
		CapsuleGitHubAccess: &off, ExpectedResourceVersion: profile.ResourceVersion,
	}, "github-off"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: "capsule-github-off", Harness: "mock", FirstMessage: "off",
		Start: true, IdempotencyKey: "github-off-thread",
	}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	deniedToken := runtime.githubToken
	deniedMeta := runtime.lastStart.Frame.Metadata
	runtime.mu.Unlock()
	if deniedToken != "" || strings.Contains(string(deniedMeta), `"github"`) {
		t.Fatalf("turned-off access still granted: %q %s", deniedToken, deniedMeta)
	}
	_ = created
}
