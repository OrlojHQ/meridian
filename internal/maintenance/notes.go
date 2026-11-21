package maintenance

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// Notes keys (ADR 0060) are wrapped with the transcript KEK. Rotation
// re-wraps them in the same transaction as Thread keys, a backup lists the
// KEK they need, and deep verification authenticates every notes envelope.

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func hasTable(ctx context.Context, db queryer, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count)
	return count > 0, err
}

type wrappedNotesKey struct {
	id      string
	scope   transcripts.NoteKeyScope
	wrapped []byte
	keyID   string
	version uint32
}

func listNotesKeys(ctx context.Context, db queryer) ([]wrappedNotesKey, error) {
	exists, err := hasTable(ctx, db, "note_scopes")
	if err != nil || !exists {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, project_id, grant_id, owner_user_id, wrapped_dek, kek_id, kek_version
		FROM note_scopes WHERE wrapped_dek IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []wrappedNotesKey
	for rows.Next() {
		var item wrappedNotesKey
		var project, grant, owner string
		if err := rows.Scan(&item.id, &project, &grant, &owner, &item.wrapped, &item.keyID, &item.version); err != nil {
			return nil, err
		}
		item.scope = transcripts.NoteKeyScope{
			ProjectID: domain.ProjectID(project), GrantID: domain.CoordinationGrantID(grant), Owner: domain.UserID(owner),
		}
		keys = append(keys, item)
	}
	return keys, rows.Err()
}

// notesKeyRequirements adds the KEKs notes keys are wrapped with.
func notesKeyRequirements(ctx context.Context, db queryer, into map[TranscriptKeyRequirement]struct{}) error {
	keys, err := listNotesKeys(ctx, db)
	if err != nil {
		return err
	}
	for _, key := range keys {
		into[TranscriptKeyRequirement{ID: key.keyID, Version: key.version}] = struct{}{}
	}
	return nil
}

// rewrapNotesKeys re-wraps every notes key from oldKey to newKey inside tx.
func rewrapNotesKeys(
	ctx context.Context, tx *sql.Tx, oldKey, newKey *transcripts.InstallationKey, now string,
) (int, error) {
	keys, err := listNotesKeys(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, key := range keys {
		rewrapped, err := transcripts.RewrapNotesDEK(key.scope, key.scope, key.keyID, key.version, key.wrapped, oldKey, newKey)
		if err != nil {
			return 0, fmt.Errorf("authenticate notes key before rotation: %w", err)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE note_scopes SET wrapped_dek = ?, kek_id = ?, kek_version = ?,
				updated_at = ?, resource_version = resource_version + 1
			WHERE id = ? AND wrapped_dek = ? AND kek_id = ? AND kek_version = ?`,
			rewrapped, newKey.ID, newKey.Version, now, key.id, key.wrapped, key.keyID, key.version)
		zeroBytes(rewrapped)
		if err != nil {
			return 0, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return 0, err
			}
			return 0, domain.ErrConflict
		}
	}
	return len(keys), nil
}

// deepVerifyNotes opens every notes key and authenticates every retained
// revision and proposal envelope under it.
func deepVerifyNotes(ctx context.Context, db queryer, key *transcripts.InstallationKey) error {
	keys, err := listNotesKeys(ctx, db)
	if err != nil {
		return err
	}
	for _, scope := range keys {
		dek, err := key.UnwrapNotesDEK(scope.scope, scope.keyID, scope.version, scope.wrapped)
		if err != nil {
			return fmt.Errorf("notes deep verification failed: %w", err)
		}
		err = verifyNoteEnvelopes(ctx, db, scope, dek)
		zeroBytes(dek)
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyNoteEnvelopes(ctx context.Context, db queryer, scope wrappedNotesKey, dek []byte) error {
	rows, err := db.QueryContext(ctx, `
		SELECT note_id, seq, kind, proposal_id, source_capsule_id, ciphertext
		FROM note_revisions WHERE scope_id = ? AND ciphertext IS NOT NULL`, scope.id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var noteID, kind, proposal, capsule string
		var seq int64
		var envelope []byte
		if err := rows.Scan(&noteID, &seq, &kind, &proposal, &capsule, &envelope); err != nil {
			rows.Close()
			return err
		}
		plaintext, err := transcripts.OpenNoteRevision(dek, transcripts.NoteRevisionAAD{
			ProjectID: scope.scope.ProjectID, GrantID: scope.scope.GrantID, NoteID: domain.NoteID(noteID),
			Seq: seq, Kind: domain.NoteRevisionKind(kind), ProposalID: domain.NoteProposalID(proposal),
			CapsuleID: domain.CapsuleID(capsule),
		}, envelope)
		zeroBytes(plaintext)
		if err != nil {
			rows.Close()
			return fmt.Errorf("notes deep verification failed: %w", domain.ErrTranscriptCorrupt)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if scope.scope.GrantID != "" {
		return nil
	}
	rows, err = db.QueryContext(ctx, `
		SELECT id, base_seq, source_thread_id, ciphertext
		FROM note_proposals WHERE project_id = ? AND ciphertext IS NOT NULL`, string(scope.scope.ProjectID))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, thread string
		var base int64
		var envelope []byte
		if err := rows.Scan(&id, &base, &thread, &envelope); err != nil {
			return err
		}
		plaintext, err := transcripts.OpenNoteProposal(dek, transcripts.NoteProposalAAD{
			ProjectID: scope.scope.ProjectID, ProposalID: domain.NoteProposalID(id), BaseSeq: base,
			ThreadID: domain.ThreadID(thread),
		}, envelope)
		zeroBytes(plaintext)
		if err != nil {
			return fmt.Errorf("notes deep verification failed: %w", domain.ErrTranscriptCorrupt)
		}
	}
	return rows.Err()
}
