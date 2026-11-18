package app_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
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

func TestGeneratedThreadNameIsNotDerivedFromTheTranscript(t *testing.T) {
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	queue := &recordingQueue{}
	key, err := transcripts.NewInstallationKey("capsule-title-plaintext", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service := app.NewService(store, &testClock{now: now}, &testIDs{}, queue)
	service.ConfigureThreads(&structuredRuntime{}, key)
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertProject(ctx, domain.Project{
			OwnerUserID: domain.BootstrapOwnerID,
			ID:          "project-titles", Name: "project", CreatedAt: now,
			UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	// The transcript is encrypted at rest. No part of it may become the
	// plaintext Capsule name because someone read the Capsule.
	const secret = "PROMPT-TEXT-STAYS-ENCRYPTED"
	prompt := secret + " fix the header layout so it does not wrap."
	created, err := service.CreateProjectThread(ctx, app.CreateProjectThreadInput{
		ProjectID: "project-titles", Harness: "mock", Prompt: prompt, IdempotencyKey: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	generated := created.Intent.CapsuleName
	if !strings.HasPrefix(generated, "thread-") {
		t.Fatalf("generated name = %q", generated)
	}

	got, err := service.GetCapsule(ctx, created.Intent.CapsuleID)
	if err != nil || got.Name != generated {
		t.Fatalf("get = %q, %v; want %q", got.Name, err, generated)
	}
	listed, err := service.ListCapsules(ctx, "project-titles", "", 0, 20)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Name != generated {
		t.Fatalf("list = %#v, %v", listed.Items, err)
	}
	board, err := service.Board(ctx, app.BoardQuery{ProjectID: "project-titles"})
	if err != nil || len(board.Items) != 1 || board.Items[0].Capsule.Name != generated {
		t.Fatalf("board = %#v, %v", board.Items, err)
	}
	again, err := service.GetCapsule(ctx, created.Intent.CapsuleID)
	if err != nil || again.ResourceVersion != got.ResourceVersion {
		t.Fatalf("a read changed the Capsule: version %d -> %d, %v", got.ResourceVersion, again.ResourceVersion, err)
	}

	renamed, err := service.RenameCapsule(ctx, got.ID, "My title", got.ResourceVersion, "rename-explicit")
	if err != nil {
		t.Fatal(err)
	}
	stuck, err := service.GetCapsule(ctx, got.ID)
	if err != nil || stuck.Name != "My title" || stuck.ResourceVersion != renamed.ResourceVersion {
		t.Fatalf("renamed = %#v, %v", stuck, err)
	}

	for _, path := range []string{store.Path(), store.Path() + "-wal"} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("prompt text stored in plaintext in %s", path)
		}
	}
}
