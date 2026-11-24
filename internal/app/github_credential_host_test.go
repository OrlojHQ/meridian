package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

// The GitHub connection's token must only reach github.com. A Project on any
// other host with no named secret clones anonymously and cannot push.
func TestGitHubConnectionTokenStaysOnGitHub(t *testing.T) {
	ctx := principal.WithSystem(operatorContext())
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	const token = "gho_connectiontokenvalue"
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	connection := domain.GitHubConnection{
		ID: "ghc-1", OwnerUserID: domain.BootstrapOwnerID, Login: "octocat",
		Status: domain.GitHubConnectionActive, Source: domain.GitHubSourceToken,
		EnvelopeVersion: secrets.GitHubEnvelopeVersion(), KEKID: key.ID, KEKVersion: key.Version,
		CreatedAt: now, UpdatedAt: now, CheckedAt: now, ResourceVersion: 1,
	}
	connection.Nonce, connection.Ciphertext, err = key.SealGitHub(connection, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertGitHubConnection(ctx, connection)
	}); err != nil {
		t.Fatal(err)
	}
	scoped := newScopedStore(store, &fixedClock{now: now})
	system := principal.WithSystem(context.Background())
	credential, err := openCloneCredential(system, scoped, key, domain.BootstrapOwnerID, "https://github.com/octocat/hello.git", "", nil)
	if err != nil || credential == nil || credential.Username != gitHubHTTPSUser || credential.Password != token {
		t.Fatalf("system clone credential = %+v, %v", credential, err)
	}
	other, ok := principal.User("usr_other")
	if !ok {
		t.Fatal("other principal")
	}
	denied, err := openCloneCredential(principal.With(context.Background(), other), scoped, key, domain.BootstrapOwnerID, "https://github.com/octocat/hello.git", "", nil)
	if err != nil || denied != nil {
		t.Fatalf("other owner clone = %+v, %v", denied, err)
	}
	service := &Service{store: store, secretKey: key}

	for _, repository := range []string{
		"https://github.com/octocat/hello", "https://GitHub.com/octocat/hello.git",
	} {
		credential, err := openCloneCredential(ctx, store, key, domain.BootstrapOwnerID, repository, "", nil)
		if err != nil || credential == nil || credential.Password != token {
			t.Fatalf("clone %s = %+v, %v", repository, credential, err)
		}
		payload, fromConnection, err := service.resolveDeliveryCredential(
			ctx, domain.BootstrapOwnerID, repository, "", domain.SecretGitPush)
		if err != nil || !fromConnection || payload.Password != token {
			t.Fatalf("push %s = %v, %v", repository, fromConnection, err)
		}
	}
	for _, repository := range []string{
		"https://gitlab.example.com/octocat/hello.git",
		"https://github.com.example.com/octocat/hello",
		"https://github.com:8443/octocat/hello",
		"http://github.com/octocat/hello",
		"ssh://git@github.com/octocat/hello.git",
		"/srv/repos/hello",
	} {
		credential, err := openCloneCredential(ctx, store, key, domain.BootstrapOwnerID, repository, "", nil)
		if err != nil || credential != nil {
			t.Fatalf("clone %s handed the GitHub token out: %+v, %v", repository, credential, err)
		}
		for _, purpose := range []domain.SecretPurpose{domain.SecretGitPush, domain.SecretGitHubAPI} {
			payload, _, err := service.resolveDeliveryCredential(
				ctx, domain.BootstrapOwnerID, repository, "", purpose)
			if !errors.Is(err, domain.ErrIllegalTransition) || payload.Password != "" || payload.Value != "" {
				t.Fatalf("%s %s = %v", purpose, repository, err)
			}
			if err := service.requireDeliveryCredential(
				ctx, domain.BootstrapOwnerID, repository, "", purpose); !errors.Is(err, domain.ErrIllegalTransition) {
				t.Fatalf("require %s %s = %v", purpose, repository, err)
			}
		}
	}
	broken := connection
	broken.Ciphertext = []byte("not-a-sealed-token")
	broken.ResourceVersion++
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.UpdateGitHubConnection(ctx, broken, connection.ResourceVersion)
	}); err != nil {
		t.Fatal(err)
	}
	unreadable, err := openCloneCredential(system, scoped, key, domain.BootstrapOwnerID, "https://github.com/octocat/hello.git", "", nil)
	if err == nil || unreadable != nil {
		t.Fatalf("unreadable connection cloned anonymously: %+v, %v", unreadable, err)
	}
}
