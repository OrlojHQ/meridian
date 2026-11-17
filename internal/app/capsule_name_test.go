package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestRenameCapsuleSavesTheNameWithoutATranscript(t *testing.T) {
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	project, err := service.CreateProject(ctx, "project", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "thread-ad942540f1e7", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}

	renamed, err := service.RenameCapsule(ctx, capsule.ID, "Fix the header", capsule.ResourceVersion, "rename-1")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Fix the header" || renamed.ResourceVersion != capsule.ResourceVersion+1 {
		t.Fatalf("renamed = %q version %d", renamed.Name, renamed.ResourceVersion)
	}
	replay, err := service.RenameCapsule(ctx, capsule.ID, "Fix the header", capsule.ResourceVersion, "rename-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Name != "Fix the header" {
		t.Fatalf("replay name = %q", replay.Name)
	}
	if _, err := service.RenameCapsule(ctx, capsule.ID, "Other title", capsule.ResourceVersion, "rename-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("idempotency mismatch = %v", err)
	}
	if _, err := service.RenameCapsule(ctx, capsule.ID, "Next", capsule.ResourceVersion, "rename-2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version = %v", err)
	}
	if _, err := service.RenameCapsule(ctx, capsule.ID, "line\nbreak", renamed.ResourceVersion, "rename-3"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("newline = %v", err)
	}
	if _, err := service.RenameCapsule(ctx, capsule.ID, "", renamed.ResourceVersion, "rename-4"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty = %v", err)
	}

	unchanged, err := service.RenameCapsule(ctx, capsule.ID, "Fix the header", renamed.ResourceVersion, "rename-5")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ResourceVersion != renamed.ResourceVersion {
		t.Fatalf("same name bumped version to %d", unchanged.ResourceVersion)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		events, err := reader.ListEvents(ctx, "capsule", string(capsule.ID))
		if err != nil {
			return err
		}
		renames := 0
		for _, event := range events {
			if event.Type == "capsule.rename" {
				renames++
				if len(event.Data) != 0 {
					t.Fatalf("rename event stored text: %q", event.Data)
				}
			}
		}
		if renames != 1 {
			t.Fatalf("rename events = %d", renames)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
