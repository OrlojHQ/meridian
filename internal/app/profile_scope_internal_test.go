package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

// A workstation profile, its reports, and Capsule pins are reachable only by
// their owner (ADR 0049). The break-glass admin reaches none of them.
func TestProfileRowsStayWithTheirOwner(t *testing.T) {
	ctx := context.Background()
	raw, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	system := principal.WithSystem(ctx)
	other := domain.UserID("usr_other")
	if err := raw.Transact(system, func(tx ports.Transaction) error {
		if err := tx.InsertUser(system, domain.User{ID: other, Issuer: "https://issuer", Subject: "other", Role: domain.UserMember, CreatedAt: now}); err != nil {
			return err
		}
		for _, connection := range []domain.ProviderConnection{
			{OwnerUserID: principal.BootstrapOwner, ID: "conn-mine", Provider: "openai", Name: "mine", CreatedAt: now, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}},
			{OwnerUserID: other, ID: "conn-theirs", Provider: "openai", Name: "theirs", CreatedAt: now, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}},
		} {
			if err := tx.PutProviderConnection(system, connection); err != nil {
				return err
			}
		}
		if err := tx.PutHarnessSetup(system, domain.HarnessSetup{OwnerUserID: other, ID: "setup-theirs", Name: "t", Harness: "codex", Revision: "rev-theirs", CreatedAt: now, ResourceVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertHarnessSetupRevision(system, domain.HarnessSetupRevision{OwnerUserID: other, ID: "rev-theirs", SetupID: "setup-theirs", Digest: "d", Files: []string{}, CreatedAt: now, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}}); err != nil {
			return err
		}
		if err := tx.InsertHarnessSetupReport(system, domain.HarnessSetupReport{RevisionID: "rev-theirs", KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}}); err != nil {
			return err
		}
		if err := tx.InsertProject(system, domain.Project{OwnerUserID: other, ID: "p-theirs", Name: "p", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1}); err != nil {
			return err
		}
		if err := tx.InsertCapsule(system, domain.Capsule{ID: "c-theirs", ProjectID: "p-theirs", Name: "c", LauncherHarness: "codex", State: domain.CapsuleCreating, DesiredState: domain.IntentReady, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1}); err != nil {
			return err
		}
		if err := tx.PutCapsuleProfilePin(system, domain.CapsuleProfilePin{CapsuleID: "c-theirs", GitName: "Other", GitEmail: "o@example.com", PinnedAt: now}); err != nil {
			return err
		}
		return tx.PutUserProfile(system, domain.UserProfile{OwnerUserID: other, GitName: "Other", GitEmail: "o@example.com", DefaultConnections: map[string]string{"codex": "conn-theirs"}, ResourceVersion: 1, UpdatedAt: now}, 0)
	}); err != nil {
		t.Fatal(err)
	}
	store := newScopedStore(raw, &fixedClock{now: now})
	me := operatorContext()

	// Reads of another owner's rows are not found.
	if err := store.View(me, func(r ports.Reader) error {
		if _, err := r.GetUserProfile(me, other); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("other's profile = %v", err)
		}
		if _, err := r.GetHarnessSetupReport(me, "rev-theirs"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("other's report = %v", err)
		}
		if _, _, err := r.GetCapsuleProfilePin(me, "c-theirs"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("other's pin = %v", err)
		}
		mine, err := r.GetUserProfile(me, principal.BootstrapOwner)
		if err != nil || mine.ResourceVersion != 0 {
			t.Fatalf("own empty profile = %+v, %v", mine, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Writes that name another owner's rows are refused.
	for name, write := range map[string]func(ports.Transaction) error{
		"their profile": func(tx ports.Transaction) error {
			return tx.PutUserProfile(me, domain.UserProfile{OwnerUserID: other, ResourceVersion: 2, UpdatedAt: now}, 1)
		},
		"their connection as my default": func(tx ports.Transaction) error {
			return tx.PutUserProfile(me, domain.UserProfile{OwnerUserID: principal.BootstrapOwner, DefaultConnections: map[string]string{"codex": "conn-theirs"}, ResourceVersion: 1, UpdatedAt: now}, 0)
		},
		"delete their profile": func(tx ports.Transaction) error { return tx.DeleteUserProfile(me, other) },
		"reseal their report": func(tx ports.Transaction) error {
			return tx.ReplaceHarnessSetupReportEnvelope(me, domain.HarnessSetupReport{RevisionID: "rev-theirs", KeyID: "k", Nonce: []byte{2}, Ciphertext: []byte{2}})
		},
		"repin their capsule": func(tx ports.Transaction) error {
			return tx.PutCapsuleProfilePin(me, domain.CapsuleProfilePin{CapsuleID: "c-theirs", PinnedAt: now})
		},
		"unpin their capsule":        func(tx ports.Transaction) error { return tx.UnpinHarnessSetups(me, "c-theirs") },
		"delete their capsule's pin": func(tx ports.Transaction) error { return tx.DeleteCapsuleProfilePin(me, "c-theirs") },
	} {
		if err := store.Transact(me, write); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s = %v", name, err)
		}
	}

	// My own profile with my own connection is fine.
	if err := store.Transact(me, func(tx ports.Transaction) error {
		return tx.PutUserProfile(me, domain.UserProfile{OwnerUserID: principal.BootstrapOwner, GitName: "Ada", GitEmail: "ada@example.com", DefaultConnections: map[string]string{"codex": "conn-mine"}, ResourceVersion: 1, UpdatedAt: now}, 0)
	}); err != nil {
		t.Fatal(err)
	}

	// The break-glass admin sees no profile, not even the bootstrap owner's.
	breakGlass := principal.With(ctx, principal.BreakGlass())
	if err := store.View(breakGlass, func(r ports.Reader) error {
		_, err := r.GetUserProfile(breakGlass, principal.BootstrapOwner)
		return err
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("break-glass profile read = %v", err)
	}
}
