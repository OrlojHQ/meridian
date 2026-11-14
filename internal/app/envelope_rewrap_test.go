package app

import (
	"bytes"
	"database/sql"
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

func TestBindSetupRevisionOwnersIsAtomicAndIdempotent(t *testing.T) {
	ctx := principal.WithSystem(operatorContext())
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	setup := domain.HarnessSetup{OwnerUserID: domain.BootstrapOwnerID, ID: "setup", Harness: "codex", ResourceVersion: 1}
	var originals []domain.HarnessSetupRevision
	for _, id := range []string{"legacy", "current", "unreadable"} {
		revision := domain.HarnessSetupRevision{OwnerUserID: setup.OwnerUserID, ID: id, SetupID: setup.ID, KeyID: key.ID}
		envelopeID := revision.ID
		if id == "current" {
			envelopeID = setupEnvelopeID(revision)
		}
		revision.Nonce, revision.Ciphertext, err = key.SealSetup(envelopeID, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		if id == "unreadable" {
			revision.Ciphertext[0] ^= 1
		}
		originals = append(originals, revision)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.PutHarnessSetup(ctx, setup); err != nil {
			return err
		}
		for _, revision := range originals {
			if err := tx.InsertHarnessSetupRevision(ctx, revision); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A write failure rolls back all changes rather than leaving a partial migration.
	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER fail_rewrap BEFORE UPDATE OF ciphertext ON harness_setup_revisions BEGIN SELECT RAISE(ABORT, 'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if result, err := BindEnvelopeOwners(ctx, store, key); err == nil || result != (EnvelopeRewrap{}) {
		t.Fatalf("failed migration = %+v, %v", result, err)
	}
	read := func(id string) domain.HarnessSetupRevision {
		t.Helper()
		var revision domain.HarnessSetupRevision
		if err := store.View(ctx, func(r ports.Reader) error {
			var err error
			revision, err = r.GetHarnessSetupRevision(ctx, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	if got := read("legacy"); !bytes.Equal(got.Ciphertext, originals[0].Ciphertext) {
		t.Fatal("failed rewrap changed ciphertext")
	}
	if _, err := raw.Exec("DROP TRIGGER fail_rewrap"); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		result, err := BindEnvelopeOwners(ctx, store, key)
		want := EnvelopeRewrap{Unreadable: 1}
		if pass == 0 {
			want.SetupRevisions = 1
		}
		if err != nil || result != want {
			t.Fatalf("pass %d = %+v, %v", pass, result, err)
		}
	}
	legacy := read("legacy")
	if plain, err := key.OpenSetup(setupEnvelopeID(legacy), legacy.KeyID, legacy.Nonce, legacy.Ciphertext); err != nil || string(plain) != "legacy" {
		t.Fatalf("rewrapped setup = %q, %v", plain, err)
	}
	for _, id := range []string{legacy.ID, "owner:usr_other:" + legacy.ID, "owner:" + string(legacy.OwnerUserID) + ":other"} {
		if _, err := key.OpenSetup(id, legacy.KeyID, legacy.Nonce, legacy.Ciphertext); err == nil {
			t.Fatalf("opened with substituted context %q", id)
		}
	}
	for _, original := range originals[1:] {
		if got := read(original.ID); !bytes.Equal(got.Ciphertext, original.Ciphertext) {
			t.Fatalf("changed %s envelope", original.ID)
		}
	}
	scoped := newScopedStore(store, &fixedClock{now: time.Now()})
	other, _ := principal.User("usr_other")
	otherCtx := principal.With(ctx, other)
	if err := scoped.Transact(otherCtx, func(tx ports.Transaction) error { return tx.ReplaceHarnessSetupRevisionEnvelope(otherCtx, legacy) }); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another owner replaced envelope: %v", err)
	}
	legacy.OwnerUserID = "usr_other"
	if err := scoped.Transact(operatorContext(), func(tx ports.Transaction) error {
		return tx.ReplaceHarnessSetupRevisionEnvelope(operatorContext(), legacy)
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("owner substitution accepted: %v", err)
	}
}
