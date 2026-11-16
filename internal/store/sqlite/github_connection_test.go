package sqlite

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

func TestGitHubConnectionRoundTripHasNoPlaintext(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, err := Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(dataDir), true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := domain.GitHubConnection{
		ID: "ghc_1", OwnerUserID: domain.BootstrapOwnerID, Login: "octocat",
		Status: domain.GitHubConnectionActive, Source: domain.GitHubSourceDevice,
		EnvelopeVersion: secrets.GitHubEnvelopeVersion(), KEKID: key.ID, KEKVersion: key.Version,
		CreatedAt: now, UpdatedAt: now, CheckedAt: now, ResourceVersion: 1,
	}
	const canary = "gho_databaseplaintextcanary"
	item.Nonce, item.Ciphertext, err = key.SealGitHub(item, canary)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertGitHubConnection(ctx, item)
	}); err != nil {
		t.Fatal(err)
	}
	var loaded domain.GitHubConnection
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		loaded, err = reader.GetGitHubConnection(ctx, domain.BootstrapOwnerID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := key.OpenGitHub(loaded)
	if err != nil || token != canary {
		t.Fatalf("open = %q, %v", token, err)
	}
	if _, err := store.db.Exec("UPDATE github_connections SET owner_user_id = 'usr_other'"); err == nil {
		t.Fatal("owner change was accepted")
	}
	key.Zero()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dataDir, "meridian.db"),
		filepath.Join(dataDir, "meridian.db-wal"),
	} {
		value, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if bytes.Contains(value, []byte(canary)) {
			t.Fatalf("plaintext found in %s", path)
		}
	}
}
