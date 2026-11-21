package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type notesClock struct{ now time.Time }

func (c *notesClock) Now() time.Time { c.now = c.now.Add(time.Millisecond); return c.now }

type notesIDs struct{ next int }

func (i *notesIDs) NewID() string { i.next++; return fmt.Sprintf("notes-id-%d", i.next) }

// createNotesFixture writes one Project note through the service, so the
// envelopes are exactly what meridiand writes.
func createNotesFixture(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := transcripts.DefaultKeyPath(dataDir)
	key, err := transcripts.OpenOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertProject(ctx, domain.Project{OwnerUserID: domain.BootstrapOwnerID, ID: "notes-project",
			Name: "notes", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1})
	}); err != nil {
		t.Fatal(err)
	}
	service := app.NewService(store, &notesClock{now: now}, &notesIDs{}, nil)
	service.ConfigureThreads(nil, key)
	owner, _ := principal.User(principal.BootstrapOwner)
	if _, err := service.PutProjectNote(principal.With(ctx, owner), "notes-project", "prefs", app.NoteWriteInput{
		Content: "short pull requests\n", IdempotencyKey: "note",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir, keyPath
}

func TestRotationRewrapsNotesKeysAndDeepVerificationOpensNotes(t *testing.T) {
	ctx := context.Background()
	dataDir, keyPath := createNotesFixture(t, ctx)
	database := filepath.Join(dataDir, "meridian.db")
	required, err := requiredTranscriptKeys(ctx, database)
	if err != nil || len(required) != 1 {
		t.Fatalf("required keys = %+v, %v", required, err)
	}
	report, err := RotateTranscriptKey(ctx, dataDir, keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.NotesKeysRewrapped != 1 || report.KeyVersion != 2 {
		t.Fatalf("rotation report = %+v", report)
	}
	key, err := transcripts.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	if err := deepVerifyTranscripts(ctx, database, key); err != nil {
		t.Fatalf("deep verification after rotation = %v", err)
	}
	// A revision moved to another note no longer opens.
	db, err := openWritableDatabase(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER note_revisions_immutable;
		UPDATE note_revisions SET seq = 9;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := deepVerifyTranscripts(ctx, database, key); !errors.Is(err, domain.ErrTranscriptCorrupt) {
		t.Fatalf("deep verification of a moved revision = %v", err)
	}
}
