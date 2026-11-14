package app

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestBindEnvelopeOwnersRewrapsLegacyConnectionsOnce(t *testing.T) {
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
	other, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "other"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Zero()

	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	legacy := domain.ProviderConnection{
		OwnerUserID: domain.BootstrapOwnerID, ID: "conn-legacy", Provider: "openai",
		Name: "legacy", CreatedAt: now, ResourceVersion: 1,
	}
	current := legacy
	current.ID, current.Name = "conn-current", "current"
	foreign := legacy
	foreign.ID, foreign.Name = "conn-foreign", "foreign"
	seal := func(item *domain.ProviderConnection, with *secrets.InstallationKey, id string, value string) {
		t.Helper()
		item.KeyID = with.ID
		item.Nonce, item.Ciphertext, err = with.SealSetup(id, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
	}
	seal(&legacy, key, legacyConnectionEnvelopeID(legacy), "legacy-key")
	seal(&current, key, connectionEnvelopeID(current), "current-key")
	seal(&foreign, other, legacyConnectionEnvelopeID(foreign), "foreign-key")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		for _, item := range []domain.ProviderConnection{legacy, current, foreign} {
			if err := tx.PutProviderConnection(ctx, item); err != nil {
				return err
			}
		}
		// A legacy secret whose envelope does not open under this key.
		return tx.InsertSecret(ctx, domain.Secret{
			OwnerUserID: domain.BootstrapOwnerID, ID: "secret-stale", Name: "stale",
			Purpose: domain.SecretHarnessEnv, EnvelopeVersion: secrets.LegacyEnvelopeVersion,
			KEKID: key.ID, KEKVersion: key.Version, Nonce: make([]byte, 12), Ciphertext: []byte("not an envelope"),
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	result, err := BindEnvelopeOwners(ctx, store, key)
	if err != nil {
		t.Fatal(err)
	}
	if result != (EnvelopeRewrap{Connections: 1, Unreadable: 2}) {
		t.Fatalf("first re-wrap = %+v", result)
	}
	read := func(id string) domain.ProviderConnection {
		t.Helper()
		var item domain.ProviderConnection
		if err := store.View(ctx, func(r ports.Reader) error {
			var err error
			item, err = r.GetProviderConnection(ctx, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return item
	}
	stored := read(legacy.ID)
	if plain, err := key.OpenSetup(connectionEnvelopeID(stored), stored.KeyID, stored.Nonce, stored.Ciphertext); err != nil || string(plain) != "legacy-key" {
		t.Fatalf("re-wrapped connection = %q, %v", plain, err)
	}
	if _, err := key.OpenSetup(legacyConnectionEnvelopeID(stored), stored.KeyID, stored.Nonce, stored.Ciphertext); err == nil {
		t.Fatal("re-wrapped connection still opens without its owner")
	}
	// The owner is bound: the same envelope under another owner fails.
	moved := stored
	moved.OwnerUserID = "usr_other"
	if _, err := key.OpenSetup(connectionEnvelopeID(moved), moved.KeyID, moved.Nonce, moved.Ciphertext); err == nil {
		t.Fatal("connection envelope opened for another owner")
	}
	if got := read(current.ID); !bytes.Equal(got.Ciphertext, current.Ciphertext) {
		t.Fatal("a current envelope was re-sealed")
	}
	if got := read(foreign.ID); !bytes.Equal(got.Ciphertext, foreign.Ciphertext) || got.KeyID != other.ID {
		t.Fatal("an envelope the key cannot open was changed")
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		stale, err := r.GetSecret(ctx, domain.BootstrapOwnerID, "stale")
		if err == nil && (stale.EnvelopeVersion != secrets.LegacyEnvelopeVersion || string(stale.Ciphertext) != "not an envelope") {
			t.Fatalf("unreadable secret changed: %+v", stale)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	again, err := BindEnvelopeOwners(ctx, store, key)
	if err != nil {
		t.Fatal(err)
	}
	if again != (EnvelopeRewrap{Unreadable: 2}) {
		t.Fatalf("second re-wrap = %+v", again)
	}
}
