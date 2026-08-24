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

func TestSecretPersistenceContainsNoPlaintext(t *testing.T) {
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
	item := domain.Secret{
		ID: "secret-1", Name: "TOKEN", Purpose: domain.SecretHarnessEnv,
		EnvelopeVersion: secrets.EnvelopeVersion, KEKID: key.ID, KEKVersion: key.Version,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	item.Nonce, item.Ciphertext, err = key.Seal(item, secrets.Payload{Value: "database-plaintext-canary"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertSecret(ctx, item)
	}); err != nil {
		t.Fatal(err)
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
		if bytes.Contains(value, []byte("database-plaintext-canary")) {
			t.Fatalf("plaintext found in %s", path)
		}
	}
}
