package sqlite

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// A pin lives only on a live Thread of a main Capsule (ADR 0059): archive,
// crypto-shred, and Project deletion all leave no pin behind, and the
// schema refuses a pin anywhere else even if a caller forgets to clear it.
func TestThreadPinsAreClearedWithTheThread(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	thread := func(id domain.ThreadID, capsule domain.CapsuleID) domain.Thread {
		return domain.Thread{
			ID: id, CapsuleID: capsule, State: domain.ThreadActive,
			AdapterID: "mock", WrappedDEK: bytes.Repeat([]byte{7}, 64),
			KEKID: "kek", KEKVersion: 1, EnvelopeVersion: 1,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, domain.Project{
			OwnerUserID: domain.BootstrapOwnerID,
			ID:          "project-pins", Name: "pins", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		for _, id := range []domain.CapsuleID{"capsule-a", "capsule-b", "capsule-c"} {
			if err := tx.InsertCapsule(ctx, domain.Capsule{
				ID: id, ProjectID: "project-pins", Name: string(id),
				State: domain.CapsuleReady, DesiredState: domain.IntentReady,
				CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			}); err != nil {
				return err
			}
		}
		pinned := thread("thread-new", "capsule-c")
		pinned.PinnedAt = now
		if err := tx.InsertThread(ctx, pinned); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("insert a pinned Thread = %v", err)
		}
		for _, item := range []domain.Thread{thread("thread-a", "capsule-a"), thread("thread-b", "capsule-b")} {
			if err := tx.InsertThread(ctx, item); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	get := func(id domain.ThreadID) domain.Thread {
		t.Helper()
		var result domain.Thread
		if err := store.View(ctx, func(reader ports.Reader) error {
			var err error
			result, err = reader.GetThread(ctx, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	pin := func(id domain.ThreadID) domain.Thread {
		t.Helper()
		current := get(id)
		current.PinnedAt = now
		current.ResourceVersion++
		if err := store.Transact(ctx, func(tx ports.Transaction) error {
			return tx.UpdateThread(ctx, current, current.ResourceVersion-1)
		}); err != nil {
			t.Fatal(err)
		}
		return get(id)
	}
	if got := pin("thread-a"); !got.PinnedAt.Equal(now) {
		t.Fatalf("pinned Thread = %+v", got)
	}

	// Archive clears the pin through the lifecycle transition.
	archived := get("thread-a")
	previous := archived.ResourceVersion
	if err := archived.Transition(domain.ThreadArchived, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.UpdateThread(ctx, archived, previous)
	}); err != nil {
		t.Fatal(err)
	}
	if got := get("thread-a"); !got.PinnedAt.IsZero() {
		t.Fatalf("archived Thread kept its pin: %+v", got)
	}

	// The schema refuses a pin on an archived Thread or a subagent Capsule.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE threads SET pinned_at = ? WHERE id = 'thread-a'`, formatTime(now)); err == nil {
		t.Fatal("schema pinned an archived Thread")
	}
	pin("thread-b")
	if _, err := store.db.ExecContext(ctx,
		`UPDATE threads SET state = 'archived' WHERE id = 'thread-b'`); err == nil {
		t.Fatal("schema archived a Thread without clearing its pin")
	}

	// Crypto-shred clears it too.
	shredded := get("thread-b")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.CryptoShredThread(ctx, shredded.ID, shredded.ResourceVersion, now.Add(time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	if got := get("thread-b"); got.State != domain.ThreadDeleted || !got.PinnedAt.IsZero() {
		t.Fatalf("crypto-shredded Thread = %+v", got)
	}

	// Project deletion removes the Thread rows and their pins (ADR 0039).
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertThread(ctx, thread("thread-c", "capsule-c"))
	}); err != nil {
		t.Fatal(err)
	}
	pin("thread-c")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		project, err := tx.GetProject(ctx, "project-pins")
		if err != nil {
			return err
		}
		previous := project.ResourceVersion
		project.State, project.UpdatedAt, project.ResourceVersion = domain.ProjectDeleting, now, previous+1
		if err := tx.UpdateProjectState(ctx, project, previous); err != nil {
			return err
		}
		_, _, err = tx.PurgeProjectContents(ctx, project, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var pins int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM threads WHERE pinned_at IS NOT NULL`).Scan(&pins); err != nil || pins != 0 {
		t.Fatalf("pins after Project deletion = %d, %v", pins, err)
	}
}
