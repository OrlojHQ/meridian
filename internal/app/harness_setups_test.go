package app_test

import (
	"errors"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"path/filepath"
	"testing"
	"time"
)

func TestSetupsPinDefaultsAndRollback(t *testing.T) {
	ctx := operatorContext()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service := app.NewService(db, &testClock{now: time.Now()}, &testIDs{}, &recordingQueue{})
	service.ConfigureSecrets(key)
	input := app.SetupImport{Name: "Personal", Default: true, Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "version one"}}}}
	first, err := service.ImportHarnessSetup(ctx, input, "import-1")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := service.HarnessSetupContents(ctx, first.ID)
	if err != nil || contents.ID != first.ID || contents.ExpectedResourceVersion != first.ResourceVersion || contents.Bundle.Digest() != input.Bundle.Digest() {
		t.Fatalf("contents mismatch: %#v %v", contents, err)
	}
	if _, err := service.HarnessSetupContents(ctx, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing setup: %v", err)
	}
	replay, err := service.ImportHarnessSetup(ctx, input, "import-1")
	if err != nil || replay.ID != first.ID {
		t.Fatal("import is not idempotent", err)
	}
	changed := input
	changed.Name = "Different"
	if _, err := service.ImportHarnessSetup(ctx, changed, "import-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("conflicting import accepted", err)
	}
	project, err := service.CreateProject(ctx, "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "one", "capsule-1")
	if err != nil {
		t.Fatal(err)
	}
	input.ID = first.ID
	input.ExpectedResourceVersion = first.ResourceVersion
	input.Bundle.Files = []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "version two"}}
	second, err := service.ImportHarnessSetup(ctx, input, "import-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportHarnessSetup(ctx, contents, "stale-editor-save"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale editor accepted: %v", err)
	}
	newer, err := service.CreateCapsule(ctx, project.ID, "two", "capsule-2")
	if err != nil {
		t.Fatal(err)
	}
	clean, err := service.CreateCapsuleForHarness(ctx, project.ID, "clean", "", "capsule-clean", "clean")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateCapsuleForHarness(ctx, project.ID, "clean", "", "capsule-clean"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("setup selection absent from idempotency", err)
	}
	err = db.View(ctx, func(r ports.Reader) error {
		for id, want := range map[domain.CapsuleID]string{capsule.ID: first.Revision, newer.ID: second.Revision, clean.ID: ""} {
			got, err := r.GetPinnedHarnessSetup(ctx, id, "codex")
			if err != nil {
				return err
			}
			if got != want {
				t.Fatalf("pin %s = %s, want %s", id, got, want)
			}
		}
		rev, err := r.GetHarnessSetupRevision(ctx, first.Revision)
		if err != nil {
			return err
		}
		plain, err := key.OpenSetup(rev.ID, rev.KeyID, rev.Nonce, rev.Ciphertext)
		if err != nil {
			return err
		}
		if string(plain) == "" {
			t.Fatal("empty encrypted revision")
		}
		if _, err := key.OpenSetup(second.Revision, rev.KeyID, rev.Nonce, rev.Ciphertext); err == nil {
			t.Fatal("revision substitution accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pins, err := service.PinnedHarnessSetups(ctx, []domain.CapsuleID{capsule.ID, newer.ID, clean.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(pins[capsule.ID]) != 1 || pins[capsule.ID][0].Revision != first.Revision || pins[capsule.ID][0].Name != "Personal" || pins[capsule.ID][0].Harness != "codex" || pins[capsule.ID][0].SetupID != first.ID || pins[capsule.ID][0].RevisedAt.IsZero() {
		t.Fatalf("first pin = %#v", pins[capsule.ID])
	}
	if len(pins[newer.ID]) != 1 || pins[newer.ID][0].Revision != second.Revision || !pins[newer.ID][0].RevisedAt.After(pins[capsule.ID][0].RevisedAt) {
		t.Fatalf("newer pin = %#v", pins[newer.ID])
	}
	if len(pins[clean.ID]) != 0 {
		t.Fatalf("clean pin = %#v", pins[clean.ID])
	}
	renamed, err := service.MutateHarnessSetup(ctx, first.ID, app.SetupMutation{Name: "Renamed", ExpectedResourceVersion: second.ResourceVersion}, "rename")
	if err != nil {
		t.Fatal(err)
	}
	pins, err = service.PinnedHarnessSetups(ctx, []domain.CapsuleID{capsule.ID, newer.ID})
	if err != nil {
		t.Fatal(err)
	}
	if pins[capsule.ID][0].Name != "Renamed" || pins[capsule.ID][0].Revision != first.Revision || pins[newer.ID][0].Name != "Renamed" || pins[newer.ID][0].Revision != second.Revision {
		t.Fatalf("rename changed the pinned revisions: %#v", pins)
	}
	rolled, err := service.MutateHarnessSetup(ctx, first.ID, app.SetupMutation{Revision: first.Revision, ExpectedResourceVersion: renamed.ResourceVersion}, "rollback")
	if err != nil || rolled.Revision != first.Revision {
		t.Fatal("rollback failed", err)
	}
	pins, err = service.PinnedHarnessSetups(ctx, []domain.CapsuleID{newer.ID})
	if err != nil || pins[newer.ID][0].Revision != second.Revision || pins[newer.ID][0].Name != "Renamed" {
		t.Fatalf("rollback changed a pinned Capsule: %#v %v", pins[newer.ID], err)
	}
}
