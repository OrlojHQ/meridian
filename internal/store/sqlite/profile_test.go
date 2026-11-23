package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func openProfileStore(t *testing.T) (*Store, time.Time) {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`INSERT INTO users(id, role, created_at, updated_at)
		VALUES ('usr_other', 'member', ?, ?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	return store, now
}

func putConnection(t *testing.T, store *Store, owner domain.UserID, id string, now time.Time) {
	t.Helper()
	if err := store.Transact(context.Background(), func(tx ports.Transaction) error {
		return tx.PutProviderConnection(context.Background(), domain.ProviderConnection{
			OwnerUserID: owner, ID: id, Provider: "openai", Name: id, CreatedAt: now,
			KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1},
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUserProfileVersionsAndDefaultConnections(t *testing.T) {
	ctx := context.Background()
	store, now := openProfileStore(t)
	owner := domain.BootstrapOwnerID
	putConnection(t, store, owner, "conn-mine", now)
	putConnection(t, store, owner, "conn-mine-2", now)
	putConnection(t, store, "usr_other", "conn-theirs", now)

	var empty domain.UserProfile
	if err := store.View(ctx, func(r ports.Reader) error {
		var err error
		empty, err = r.GetUserProfile(ctx, owner)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if empty.ResourceVersion != 0 || empty.OwnerUserID != owner || len(empty.DefaultConnections) != 0 || !empty.CapsuleGitHubAccess {
		t.Fatalf("empty profile = %+v", empty)
	}

	put := func(profile domain.UserProfile, expected domain.ResourceVersion) error {
		return store.Transact(ctx, func(tx ports.Transaction) error { return tx.PutUserProfile(ctx, profile, expected) })
	}
	first := domain.UserProfile{
		OwnerUserID: owner, GitName: "Ada Lovelace", GitEmail: "ada@example.com",
		DefaultConnections: map[string]string{"codex": "conn-mine", "claude": "conn-mine-2"},
		ResourceVersion:    1, UpdatedAt: now,
	}
	if err := put(first, 0); err != nil {
		t.Fatal(err)
	}
	// A stale writer conflicts; a second create conflicts too.
	stale := first
	stale.ResourceVersion = 2
	if err := put(stale, 5); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("version that skips = %v", err)
	}
	stale.ResourceVersion = 6
	if err := put(stale, 5); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	if err := put(first, 0); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second create = %v", err)
	}

	second := first
	second.GitName, second.GitEmail = "", ""
	second.DefaultConnections = map[string]string{"codex": "conn-mine-2"}
	second.ResourceVersion = 2
	second.UpdatedAt = now.Add(time.Hour)
	if err := put(second, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		got, err := r.GetUserProfile(ctx, owner)
		if err != nil {
			return err
		}
		if got.ResourceVersion != 2 || got.GitName != "" || !got.UpdatedAt.Equal(second.UpdatedAt) ||
			len(got.DefaultConnections) != 1 || got.DefaultConnections["codex"] != "conn-mine-2" {
			t.Fatalf("updated profile = %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Another owner's connection is not there for this profile, and the
	// whole write rolls back.
	third := second
	third.ResourceVersion = 3
	third.DefaultConnections = map[string]string{"codex": "conn-mine", "opencode": "conn-theirs"}
	if err := put(third, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner default connection = %v", err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		got, err := r.GetUserProfile(ctx, owner)
		if err != nil {
			return err
		}
		if got.ResourceVersion != 2 || got.DefaultConnections["codex"] != "conn-mine-2" || len(got.DefaultConnections) != 1 {
			t.Fatalf("profile after refused write = %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Unsafe identities never reach the table.
	for _, identity := range [][2]string{
		{"Ada", ""}, {"Ada\n[core]", "a@x"}, {`Ada"`, "a@x"}, {"Ada", `a\@x`}, {"Ada <x>", "a@x"}, {" Ada", "a@x"},
	} {
		bad := second
		bad.ResourceVersion = 3
		bad.GitName, bad.GitEmail = identity[0], identity[1]
		if err := put(bad, 2); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("identity %q = %v", identity, err)
		}
	}

	// The owner never changes, and a default connection cannot be pointed
	// at another owner's connection in SQL either.
	if _, err := store.db.Exec("UPDATE user_profiles SET owner_user_id = 'usr_other'"); err == nil ||
		!strings.Contains(err.Error(), "owner immutable") {
		t.Fatalf("profile owner change = %v", err)
	}
	if _, err := store.db.Exec("UPDATE user_profile_connections SET connection_id = 'conn-theirs'"); err == nil ||
		!strings.Contains(err.Error(), "owner immutable") {
		t.Fatalf("default connection swap = %v", err)
	}

	if err := store.Transact(ctx, func(tx ports.Transaction) error { return tx.DeleteUserProfile(ctx, owner) }); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		got, err := r.GetUserProfile(ctx, owner)
		if err != nil {
			return err
		}
		if got.ResourceVersion != 0 || len(got.DefaultConnections) != 0 {
			t.Fatalf("deleted profile = %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSetupReportsAndCapsuleProfilePins(t *testing.T) {
	ctx := context.Background()
	store, now := openProfileStore(t)
	owner := domain.BootstrapOwnerID
	setup := domain.HarnessSetup{OwnerUserID: owner, ID: "setup-1", Name: "mine", Harness: "codex", Revision: "rev-1", CreatedAt: now, ResourceVersion: 1}
	revision := domain.HarnessSetupRevision{OwnerUserID: owner, ID: "rev-1", SetupID: "setup-1", Digest: "d", Files: []string{"config.toml"}, CreatedAt: now, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.PutHarnessSetup(ctx, setup); err != nil {
			return err
		}
		if err := tx.InsertHarnessSetupRevision(ctx, revision); err != nil {
			return err
		}
		if err := tx.InsertProject(ctx, domain.Project{OwnerUserID: owner, ID: "p1", Name: "p", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1}); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, domain.Capsule{
			ID: "c1", ProjectID: "p1", Name: "c", LauncherHarness: "codex",
			State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	// Reports.
	if err := store.View(ctx, func(r ports.Reader) error {
		_, err := r.GetHarnessSetupReport(ctx, "rev-1")
		return err
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing report = %v", err)
	}
	report := domain.HarnessSetupReport{RevisionID: "rev-1", KeyID: "k", Nonce: []byte{2}, Ciphertext: []byte{3}}
	if err := store.Transact(ctx, func(tx ports.Transaction) error { return tx.InsertHarnessSetupReport(ctx, report) }); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertHarnessSetupReport(ctx, domain.HarnessSetupReport{RevisionID: "rev-missing", KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}})
	}); err == nil {
		t.Fatal("report for a missing revision was stored")
	}
	resealed := report
	resealed.KeyID, resealed.Nonce, resealed.Ciphertext = "k2", []byte{4}, []byte{5}
	if err := store.Transact(ctx, func(tx ports.Transaction) error { return tx.ReplaceHarnessSetupReportEnvelope(ctx, resealed) }); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		got, err := r.GetHarnessSetupReport(ctx, "rev-1")
		if err != nil {
			return err
		}
		if got.KeyID != "k2" || string(got.Ciphertext) != string([]byte{5}) {
			t.Fatalf("resealed report = %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Pins.
	pin := domain.CapsuleProfilePin{CapsuleID: "c1", GitName: "Ada", GitEmail: "ada@example.com", PinnedAt: now}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.PinHarnessSetup(ctx, "c1", "codex", "rev-1"); err != nil {
			return err
		}
		return tx.PutCapsuleProfilePin(ctx, pin)
	}); err != nil {
		t.Fatal(err)
	}
	pin.GitName, pin.PinnedAt = "Ada L", now.Add(time.Hour)
	if err := store.Transact(ctx, func(tx ports.Transaction) error { return tx.PutCapsuleProfilePin(ctx, pin) }); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		bad := pin
		bad.GitEmail = "ada@example.com\n"
		return tx.PutCapsuleProfilePin(ctx, bad)
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unsafe pinned identity = %v", err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		got, ok, err := r.GetCapsuleProfilePin(ctx, "c1")
		if err != nil {
			return err
		}
		if !ok || got.GitName != "Ada L" || !got.PinnedAt.Equal(pin.PinnedAt) {
			t.Fatalf("pin = %+v, %v", got, ok)
		}
		if _, ok, err := r.GetCapsuleProfilePin(ctx, "c-none"); ok || err != nil {
			t.Fatalf("absent pin = %v, %v", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// An update unpins every setup before pinning the current ones.
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.UnpinHarnessSetups(ctx, "c1"); err != nil {
			return err
		}
		return tx.DeleteCapsuleProfilePin(ctx, "c1")
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		revision, err := r.GetPinnedHarnessSetup(ctx, "c1", "codex")
		if err != nil || revision != "" {
			t.Fatalf("pinned after unpin = %q, %v", revision, err)
		}
		if _, ok, err := r.GetCapsuleProfilePin(ctx, "c1"); ok || err != nil {
			t.Fatalf("identity after delete = %v, %v", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Collecting a deleted setup removes its unpinned revision and report.
	setup.Deleted = true
	setup.ResourceVersion = 2
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.PutHarnessSetup(ctx, setup); err != nil {
			return err
		}
		return tx.CollectDeletedHarnessSetups(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var reports int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM harness_setup_reports").Scan(&reports); err != nil || reports != 0 {
		t.Fatalf("reports after collection = %d, %v", reports, err)
	}
}

func TestTransferClearsDefaultsAndLeavesTheProfile(t *testing.T) {
	ctx := context.Background()
	store, now := openProfileStore(t)
	if _, err := store.db.Exec(`INSERT INTO users(id, role, created_at, updated_at, disabled_at)
		VALUES ('usr_departed', 'member', ?, ?, ?)`, formatTime(now), formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	putConnection(t, store, "usr_departed", "conn-departed", now)
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		for _, setup := range []domain.HarnessSetup{
			{OwnerUserID: "usr_departed", ID: "setup-default", Name: "d", Harness: "codex", Default: true, CreatedAt: now, ResourceVersion: 3},
			{OwnerUserID: "usr_departed", ID: "setup-plain", Name: "p", Harness: "claude", CreatedAt: now, ResourceVersion: 1},
		} {
			if err := tx.PutHarnessSetup(ctx, setup); err != nil {
				return err
			}
		}
		return tx.PutUserProfile(ctx, domain.UserProfile{
			OwnerUserID: "usr_departed", GitName: "Departed", GitEmail: "gone@example.com",
			DefaultConnections: map[string]string{"codex": "conn-departed"}, ResourceVersion: 1, UpdatedAt: now,
		}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.TransferOwnership(ctx, "usr_departed", "usr_other")
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query("SELECT owner_user_id, metadata FROM harness_setups ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	versions := map[string]domain.ResourceVersion{}
	for rows.Next() {
		var owner, raw string
		if err := rows.Scan(&owner, &raw); err != nil {
			t.Fatal(err)
		}
		var setup domain.HarnessSetup
		if err := json.Unmarshal([]byte(raw), &setup); err != nil {
			t.Fatal(err)
		}
		if owner != "usr_other" || setup.Default {
			t.Fatalf("moved setup %s: owner %s default %v", setup.ID, owner, setup.Default)
		}
		versions[setup.ID] = setup.ResourceVersion
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if versions["setup-default"] != 4 || versions["setup-plain"] != 1 {
		t.Fatalf("resource versions after transfer = %v", versions)
	}
	var profileOwner string
	if err := store.db.QueryRow("SELECT owner_user_id FROM user_profiles").Scan(&profileOwner); err != nil || profileOwner != "usr_departed" {
		t.Fatalf("profile owner after transfer = %q, %v", profileOwner, err)
	}
}
