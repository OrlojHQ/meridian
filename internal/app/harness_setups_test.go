package app_test

import (
	"context"
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
	ctx := context.Background()
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
	rolled, err := service.MutateHarnessSetup(ctx, first.ID, app.SetupMutation{Revision: first.Revision, ExpectedResourceVersion: second.ResourceVersion}, "rollback")
	if err != nil || rolled.Revision != first.Revision {
		t.Fatal("rollback failed", err)
	}
}
