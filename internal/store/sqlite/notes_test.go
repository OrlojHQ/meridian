package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// seedNotes writes one Project note with a revision, a pending proposal and
// a Thread's delivered set for owner.
func seedNotes(t *testing.T, store *Store, owner domain.UserID) domain.Project {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	project := domain.Project{OwnerUserID: owner, ID: "p-notes", Name: "notes", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		scope := domain.NoteScope{ID: "s1", ProjectID: project.ID, OwnerUserID: owner, WrappedDEK: []byte{1},
			KEKID: "k", KEKVersion: 1, Delivery: domain.NoteDeliveryAllThreads, RevisionCounter: 1,
			ResourceVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.PutNoteScope(ctx, scope, 0); err != nil {
			return err
		}
		note := domain.Note{ID: "n1", ScopeID: "s1", ProjectID: project.ID, OwnerUserID: owner, Name: "tests",
			HeadSeq: 1, Live: true, Size: 1, Digest: "d", HeadKind: domain.NoteRevisionOperatorWrite,
			HeadWriter: domain.NoteWriterOperator, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.InsertNote(ctx, note); err != nil {
			return err
		}
		if err := tx.InsertNoteRevision(ctx, domain.NoteRevision{NoteID: "n1", Seq: 1, ScopeID: "s1",
			ProjectID: project.ID, OwnerUserID: owner, ScopeSeq: 1, Name: "tests",
			Kind: domain.NoteRevisionOperatorWrite, Writer: domain.NoteWriterOperator, Digest: "d", Size: 1,
			Ciphertext: []byte{2}, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.InsertNoteProposal(ctx, domain.NoteProposal{ID: "np1", ProjectID: project.ID,
			OwnerUserID: owner, Name: "tests", BaseSeq: 1, State: domain.NoteProposalPending,
			Origin: domain.NoteProposalFromAgent, Source: domain.NoteSource{CapsuleID: "c1", ThreadID: "t1"},
			IdempotencyKey: "k", Digest: "d", Size: 1, Ciphertext: []byte{3}, ResourceVersion: 1,
			CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return tx.PutThreadNotes(ctx, domain.ThreadNotes{ThreadID: "t1", ProjectID: project.ID, OwnerUserID: owner, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	return project
}

func countNoteRows(t *testing.T, store *Store) int {
	t.Helper()
	total := 0
	for _, table := range []string{"note_scopes", "notes", "note_revisions", "note_proposals", "thread_notes"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		total += count
	}
	return total
}

func TestProjectDeletionRemovesBothTiersOfNotes(t *testing.T) {
	store, now := openProfileStore(t)
	project := seedNotes(t, store, domain.BootstrapOwnerID)
	if countNoteRows(t, store) != 5 {
		t.Fatal("seed failed")
	}
	ctx := context.Background()
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		_, _, err := tx.PurgeProjectContents(ctx, project, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if left := countNoteRows(t, store); left != 0 {
		t.Fatalf("%d note rows survived Project deletion", left)
	}
}

func TestTransferTurnsNotesDeliveryOffAndRejectsPendingProposals(t *testing.T) {
	store, _ := openProfileStore(t)
	seedNotes(t, store, "usr_other")
	ctx := context.Background()
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.TransferOwnership(ctx, "usr_other", domain.BootstrapOwnerID)
	}); err != nil {
		t.Fatal(err)
	}
	var delivery, state, reason, owner string
	if err := store.db.QueryRow(`SELECT delivery, owner_user_id FROM note_scopes`).Scan(&delivery, &owner); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state, reason FROM note_proposals`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if delivery != "off" || owner != string(domain.BootstrapOwnerID) || state != "rejected" || reason != "owner_changed" {
		t.Fatalf("after transfer: delivery=%s owner=%s proposal=%s/%s", delivery, owner, state, reason)
	}
	for _, table := range []string{"notes", "note_revisions", "thread_notes"} {
		var moved string
		if err := store.db.QueryRow("SELECT owner_user_id FROM " + table).Scan(&moved); err != nil || moved != string(domain.BootstrapOwnerID) {
			t.Fatalf("%s owner = %s, %v", table, moved, err)
		}
	}
}

func TestNoteRevisionsAreImmutableExceptForDroppingContent(t *testing.T) {
	store, _ := openProfileStore(t)
	seedNotes(t, store, domain.BootstrapOwnerID)
	for _, statement := range []string{
		`UPDATE note_revisions SET kind = 'agent_write'`,
		`UPDATE note_revisions SET digest = 'other'`,
		`UPDATE note_revisions SET ciphertext = x'09'`,
		`UPDATE note_revisions SET owner_user_id = 'usr_other'`,
	} {
		if _, err := store.db.Exec(statement); err == nil {
			t.Errorf("%s succeeded", statement)
		}
	}
	if _, err := store.db.Exec(`UPDATE note_revisions SET ciphertext = NULL`); err != nil {
		t.Fatalf("dropping content = %v", err)
	}
}
