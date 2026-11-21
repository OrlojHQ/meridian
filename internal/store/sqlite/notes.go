package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

// Working notes and Project notes (ADR 0060). Rows hold names, sizes,
// digests and provenance in plaintext; content is only ever an envelope.

const noteScopeSelect = `
	SELECT id, project_id, grant_id, owner_user_id, wrapped_dek, kek_id, kek_version, delivery,
		revision_counter, headlined_seq, resource_version, created_at, updated_at
	FROM note_scopes`

const noteSelect = `
	SELECT id, scope_id, project_id, grant_id, owner_user_id, name, head_seq, live, size, digest,
		head_kind, head_writer, head_link_name, resource_version, created_at, updated_at
	FROM notes`

const noteRevisionSelect = `
	SELECT note_id, seq, scope_id, project_id, grant_id, owner_user_id, scope_seq, name, kind, writer,
		writer_user_id, source_thread_id, source_capsule_id, source_link_id, source_link_name,
		source_lead_thread_id, source_grant_id, working_note_id, working_note_seq, working_note_name,
		working_capsule_id, proposal_id, base_seq, restored_from_seq, digest, size, ciphertext, created_at
	FROM note_revisions`

const noteProposalSelect = `
	SELECT id, project_id, owner_user_id, name, is_delete, base_seq, state, reason, origin,
		source_thread_id, source_capsule_id, source_link_id, source_link_name, source_lead_thread_id,
		source_grant_id, working_note_id, working_note_seq, working_note_name, working_capsule_id,
		idempotency_key, request_digest, digest, size, ciphertext, resolved_seq, resolved_by,
		resolved_at, resource_version, created_at, updated_at
	FROM note_proposals`

func (r *repository) GetNoteScope(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID,
) (domain.NoteScope, error) {
	return scanNoteScope(r.q.QueryRowContext(ctx, noteScopeSelect+` WHERE project_id = ? AND grant_id = ?`, project, grant))
}

func (r *repository) GetNoteScopeByID(ctx context.Context, id domain.NoteScopeID) (domain.NoteScope, error) {
	return scanNoteScope(r.q.QueryRowContext(ctx, noteScopeSelect+` WHERE id = ?`, id))
}

// ListOwnedNoteScopes lists every scope one owner holds, for an owner
// transfer.
func (r *repository) ListOwnedNoteScopes(ctx context.Context, owner domain.UserID) ([]domain.NoteScope, error) {
	rows, err := r.q.QueryContext(ctx, noteScopeSelect+` WHERE owner_user_id = ? ORDER BY id`, owner)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.NoteScope
	for rows.Next() {
		item, err := scanNoteScope(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) PutNoteScope(ctx context.Context, scope domain.NoteScope, expected domain.ResourceVersion) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if expected == 0 {
		_, err := r.q.ExecContext(ctx, `
			INSERT INTO note_scopes(
				id, project_id, grant_id, owner_user_id, wrapped_dek, kek_id, kek_version, delivery,
				revision_counter, headlined_seq, resource_version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			scope.ID, scope.ProjectID, scope.GrantID, scope.OwnerUserID, nullableBytes(scope.WrappedDEK),
			scope.KEKID, scope.KEKVersion, scope.Delivery, scope.RevisionCounter, scope.HeadlinedSeq,
			scope.ResourceVersion, formatTime(scope.CreatedAt), formatTime(scope.UpdatedAt))
		return mapError(err)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE note_scopes SET owner_user_id = ?, wrapped_dek = ?, kek_id = ?, kek_version = ?,
			delivery = ?, revision_counter = ?, headlined_seq = ?, resource_version = ?, updated_at = ?
		WHERE id = ? AND resource_version = ?`,
		scope.OwnerUserID, nullableBytes(scope.WrappedDEK), scope.KEKID, scope.KEKVersion, scope.Delivery,
		scope.RevisionCounter, scope.HeadlinedSeq, scope.ResourceVersion, formatTime(scope.UpdatedAt),
		scope.ID, expected)
	return expectOneRow(result, err)
}

func (r *repository) GetNote(ctx context.Context, scope domain.NoteScopeID, name string) (domain.Note, error) {
	return scanNote(r.q.QueryRowContext(ctx, noteSelect+` WHERE scope_id = ? AND name = ?`, scope, name))
}

func (r *repository) GetNoteByID(ctx context.Context, id domain.NoteID) (domain.Note, error) {
	return scanNote(r.q.QueryRowContext(ctx, noteSelect+` WHERE id = ?`, id))
}

// ListNotes lists a scope's notes by name, deleted ones included.
func (r *repository) ListNotes(ctx context.Context, scope domain.NoteScopeID) ([]domain.Note, error) {
	rows, err := r.q.QueryContext(ctx, noteSelect+` WHERE scope_id = ? ORDER BY name LIMIT 1000`, scope)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.Note
	for rows.Next() {
		item, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) InsertNote(ctx context.Context, note domain.Note) error {
	if err := note.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO notes(
			id, scope_id, project_id, grant_id, owner_user_id, name, head_seq, live, size, digest,
			head_kind, head_writer, head_link_name, resource_version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		note.ID, note.ScopeID, note.ProjectID, note.GrantID, note.OwnerUserID, note.Name, note.HeadSeq,
		boolInt(note.Live), note.Size, note.Digest, note.HeadKind, note.HeadWriter, note.HeadLinkName,
		note.ResourceVersion, formatTime(note.CreatedAt), formatTime(note.UpdatedAt))
	return mapError(err)
}

func (r *repository) UpdateNote(ctx context.Context, note domain.Note, expected domain.ResourceVersion) error {
	if err := note.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE notes SET name = ?, head_seq = ?, live = ?, size = ?, digest = ?, head_kind = ?,
			head_writer = ?, head_link_name = ?, resource_version = ?, updated_at = ?
		WHERE id = ? AND resource_version = ?`,
		note.Name, note.HeadSeq, boolInt(note.Live), note.Size, note.Digest, note.HeadKind,
		note.HeadWriter, note.HeadLinkName, note.ResourceVersion, formatTime(note.UpdatedAt),
		note.ID, expected)
	return expectOneRow(result, err)
}

func (r *repository) InsertNoteRevision(ctx context.Context, revision domain.NoteRevision) error {
	if err := revision.Validate(); err != nil {
		return err
	}
	source := revision.Source
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO note_revisions(
			note_id, seq, scope_id, project_id, grant_id, owner_user_id, scope_seq, name, kind, writer,
			writer_user_id, source_thread_id, source_capsule_id, source_link_id, source_link_name,
			source_lead_thread_id, source_grant_id, working_note_id, working_note_seq, working_note_name,
			working_capsule_id, proposal_id, base_seq, restored_from_seq, digest, size, ciphertext, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.NoteID, revision.Seq, revision.ScopeID, revision.ProjectID, revision.GrantID,
		revision.OwnerUserID, revision.ScopeSeq, revision.Name, revision.Kind, revision.Writer,
		revision.WriterUserID, source.ThreadID, source.CapsuleID, source.LinkID, source.LinkName,
		source.LeadThreadID, source.GrantID, source.WorkingNoteID, source.WorkingNoteSeq,
		source.WorkingNoteName, source.WorkingCapsuleID, revision.ProposalID, revision.BaseSeq,
		revision.RestoredFromSeq, revision.Digest, revision.Size, nullableBytes(revision.Ciphertext),
		sortableTime(revision.CreatedAt))
	return mapError(err)
}

func (r *repository) GetNoteRevision(ctx context.Context, note domain.NoteID, seq int64) (domain.NoteRevision, error) {
	return scanNoteRevision(r.q.QueryRowContext(ctx, noteRevisionSelect+` WHERE note_id = ? AND seq = ?`, note, seq))
}

// ListNoteRevisions lists a note's revisions, newest first.
func (r *repository) ListNoteRevisions(ctx context.Context, note domain.NoteID, limit int) ([]domain.NoteRevision, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.q.QueryContext(ctx, noteRevisionSelect+` WHERE note_id = ? ORDER BY seq DESC LIMIT ?`, note, limit)
	if err != nil {
		return nil, mapError(err)
	}
	return scanNoteRevisions(rows)
}

// ListScopeRevisionsSince lists a scope's revisions after a scope sequence,
// oldest first.
func (r *repository) ListScopeRevisionsSince(
	ctx context.Context, scope domain.NoteScopeID, after int64, limit int,
) ([]domain.NoteRevision, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := r.q.QueryContext(ctx, noteRevisionSelect+`
		WHERE scope_id = ? AND scope_seq > ? ORDER BY scope_seq LIMIT ?`, scope, after, limit)
	if err != nil {
		return nil, mapError(err)
	}
	return scanNoteRevisions(rows)
}

// CountNoteWrites counts agent revisions in a scope since a time: all of
// them, and those one Capsule wrote.
func (r *repository) CountNoteWrites(
	ctx context.Context, scope domain.NoteScopeID, capsule domain.CapsuleID, since time.Time,
) (int, int, error) {
	var tree, own int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN source_capsule_id = ? THEN 1 ELSE 0 END), 0)
		FROM note_revisions
		WHERE scope_id = ? AND writer = 'agent' AND created_at >= ?`,
		capsule, scope, sortableTime(since)).Scan(&tree, &own)
	return tree, own, mapError(err)
}

// PruneNoteRevisions drops the content of every revision of a note but the
// keep most recent. Their content-free rows stay.
func (r *repository) PruneNoteRevisions(ctx context.Context, note domain.NoteID, keep int) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE note_revisions SET ciphertext = NULL
		WHERE note_id = ? AND ciphertext IS NOT NULL AND seq <= (
			SELECT COALESCE(MAX(seq), 0) FROM note_revisions WHERE note_id = ?
		) - ?`, note, note, keep)
	return mapError(err)
}

// DropNoteContent removes every revision's content of one note, for
// "delete with history". It is a row update, not media erasure.
func (r *repository) DropNoteContent(ctx context.Context, note domain.NoteID) error {
	_, err := r.q.ExecContext(ctx, `UPDATE note_revisions SET ciphertext = NULL WHERE note_id = ?`, note)
	return mapError(err)
}

// ShredNoteScope clears a scope's data key and every envelope under it:
// revisions and, for Project notes, proposals. The live set becomes
// empty; content-free rows stay. Pending proposals close as shredded.
func (r *repository) ShredNoteScope(ctx context.Context, scope domain.NoteScope, at time.Time) error {
	now := formatTime(at)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE note_revisions SET ciphertext = NULL WHERE scope_id = ?`, []any{scope.ID}},
		{`UPDATE notes SET live = 0, size = 0, resource_version = resource_version + 1, updated_at = ?
			WHERE scope_id = ? AND live = 1`, []any{now, scope.ID}},
	} {
		if _, err := r.q.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return mapError(err)
		}
	}
	if scope.GrantID == "" {
		if _, err := r.q.ExecContext(ctx, `
			UPDATE note_proposals SET state = CASE WHEN state = 'pending' THEN 'rejected' ELSE state END,
				reason = CASE WHEN state = 'pending' THEN 'shredded' ELSE reason END,
				ciphertext = NULL, resource_version = resource_version + 1, updated_at = ?
			WHERE project_id = ? AND (ciphertext IS NOT NULL OR state = 'pending')`, now, scope.ProjectID); err != nil {
			return mapError(err)
		}
	} else {
		// A promotion from this tree that is still pending names text
		// that is gone; it closes too.
		if _, err := r.q.ExecContext(ctx, `
			UPDATE note_proposals SET state = 'rejected', reason = 'shredded', ciphertext = NULL,
				resource_version = resource_version + 1, updated_at = ?
			WHERE source_grant_id = ? AND state = 'pending'`, now, scope.GrantID); err != nil {
			return mapError(err)
		}
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE note_scopes SET wrapped_dek = NULL, kek_id = '', kek_version = 0,
			resource_version = resource_version + 1, updated_at = ?
		WHERE id = ?`, now, scope.ID)
	return expectOneRow(result, err)
}

func (r *repository) GetNoteProposal(ctx context.Context, id domain.NoteProposalID) (domain.NoteProposal, error) {
	return scanNoteProposal(r.q.QueryRowContext(ctx, noteProposalSelect+` WHERE id = ?`, id))
}

func (r *repository) GetNoteProposalByKey(
	ctx context.Context, capsule domain.CapsuleID, key string,
) (domain.NoteProposal, error) {
	return scanNoteProposal(r.q.QueryRowContext(ctx, noteProposalSelect+`
		WHERE source_capsule_id = ? AND idempotency_key = ? AND origin = 'agent'`, capsule, key))
}

// ListNoteProposals lists a Project's proposals, newest first. pendingOnly
// limits it to those still pending.
func (r *repository) ListNoteProposals(
	ctx context.Context, project domain.ProjectID, pendingOnly bool, limit int,
) ([]domain.NoteProposal, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	query := noteProposalSelect + ` WHERE project_id = ?`
	if pendingOnly {
		query += ` AND state = 'pending'`
	}
	rows, err := r.q.QueryContext(ctx, query+` ORDER BY created_at DESC, id LIMIT ?`, project, limit)
	if err != nil {
		return nil, mapError(err)
	}
	return scanNoteProposals(rows)
}

// ListThreadNoteProposals lists the proposals one Thread made, newest first.
func (r *repository) ListThreadNoteProposals(
	ctx context.Context, thread domain.ThreadID, limit int,
) ([]domain.NoteProposal, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := r.q.QueryContext(ctx, noteProposalSelect+`
		WHERE source_thread_id = ? AND origin = 'agent' ORDER BY created_at DESC, id LIMIT ?`, thread, limit)
	if err != nil {
		return nil, mapError(err)
	}
	return scanNoteProposals(rows)
}

// ListExpiredNoteProposals lists pending proposals created before a time.
func (r *repository) ListExpiredNoteProposals(ctx context.Context, before time.Time, limit int) ([]domain.NoteProposal, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := r.q.QueryContext(ctx, noteProposalSelect+`
		WHERE state = 'pending' AND created_at < ? ORDER BY created_at LIMIT ?`, sortableTime(before), limit)
	if err != nil {
		return nil, mapError(err)
	}
	return scanNoteProposals(rows)
}

func (r *repository) InsertNoteProposal(ctx context.Context, proposal domain.NoteProposal) error {
	if err := proposal.Validate(); err != nil {
		return err
	}
	source := proposal.Source
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO note_proposals(
			id, project_id, owner_user_id, name, is_delete, base_seq, state, reason, origin,
			source_thread_id, source_capsule_id, source_link_id, source_link_name, source_lead_thread_id,
			source_grant_id, working_note_id, working_note_seq, working_note_name, working_capsule_id,
			idempotency_key, request_digest, digest, size, ciphertext, resolved_seq, resolved_by,
			resolved_at, resource_version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		proposal.ID, proposal.ProjectID, proposal.OwnerUserID, proposal.Name, boolInt(proposal.Delete),
		proposal.BaseSeq, proposal.State, proposal.Reason, proposal.Origin, source.ThreadID,
		source.CapsuleID, source.LinkID, source.LinkName, source.LeadThreadID, source.GrantID,
		source.WorkingNoteID, source.WorkingNoteSeq, source.WorkingNoteName, source.WorkingCapsuleID,
		proposal.IdempotencyKey, proposal.RequestDigest, proposal.Digest, proposal.Size,
		nullableBytes(proposal.Ciphertext), proposal.ResolvedSeq, proposal.ResolvedBy,
		optionalTime(proposal.ResolvedAt), proposal.ResourceVersion, sortableTime(proposal.CreatedAt),
		formatTime(proposal.UpdatedAt))
	return mapError(err)
}

// UpdateNoteProposal changes a proposal's state, resolution and content.
// Everything an approval binds is immutable.
func (r *repository) UpdateNoteProposal(
	ctx context.Context, proposal domain.NoteProposal, expected domain.ResourceVersion,
) error {
	if err := proposal.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE note_proposals SET state = ?, reason = ?, ciphertext = ?, resolved_seq = ?,
			resolved_by = ?, resolved_at = ?, resource_version = ?, updated_at = ?
		WHERE id = ? AND resource_version = ?`,
		proposal.State, proposal.Reason, nullableBytes(proposal.Ciphertext), proposal.ResolvedSeq,
		proposal.ResolvedBy, optionalTime(proposal.ResolvedAt), proposal.ResourceVersion,
		formatTime(proposal.UpdatedAt), proposal.ID, expected)
	return expectOneRow(result, err)
}

func (r *repository) GetThreadNotes(ctx context.Context, thread domain.ThreadID) (domain.ThreadNotes, error) {
	var item domain.ThreadNotes
	var delivered, deliveredAt, updated string
	var indexed int
	err := r.q.QueryRowContext(ctx, `
		SELECT thread_id, project_id, owner_user_id, choice, delivered, delivered_at, indexed, updated_at
		FROM thread_notes WHERE thread_id = ?`, thread).Scan(
		&item.ThreadID, &item.ProjectID, &item.OwnerUserID, &item.Choice, &delivered, &deliveredAt,
		&indexed, &updated)
	if err != nil {
		return domain.ThreadNotes{}, mapError(err)
	}
	item.Indexed = indexed == 1
	if err := json.Unmarshal([]byte(delivered), &item.Delivered); err != nil {
		return domain.ThreadNotes{}, domain.ErrCorrupt
	}
	if item.DeliveredAt, err = parseOptionalTime(deliveredAt); err != nil {
		return domain.ThreadNotes{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.ThreadNotes{}, err
	}
	return item, nil
}

func (r *repository) PutThreadNotes(ctx context.Context, item domain.ThreadNotes) error {
	if item.ThreadID == "" || item.ProjectID == "" || !item.Choice.Valid() {
		return domain.ErrInvalid
	}
	delivered := item.Delivered
	if delivered == nil {
		delivered = []domain.DeliveredNote{}
	}
	encoded, err := json.Marshal(delivered)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO thread_notes(thread_id, project_id, owner_user_id, choice, delivered, delivered_at, indexed, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET choice = excluded.choice, delivered = excluded.delivered,
			delivered_at = excluded.delivered_at, indexed = excluded.indexed, updated_at = excluded.updated_at
		WHERE thread_notes.project_id = excluded.project_id AND thread_notes.owner_user_id = excluded.owner_user_id`,
		item.ThreadID, item.ProjectID, item.OwnerUserID, item.Choice, string(encoded),
		optionalTime(item.DeliveredAt), boolInt(item.Indexed), formatTime(item.UpdatedAt))
	return mapError(err)
}

func scanNoteScope(row scanner) (domain.NoteScope, error) {
	var item domain.NoteScope
	var created, updated string
	err := row.Scan(&item.ID, &item.ProjectID, &item.GrantID, &item.OwnerUserID, &item.WrappedDEK,
		&item.KEKID, &item.KEKVersion, &item.Delivery, &item.RevisionCounter, &item.HeadlinedSeq,
		&item.ResourceVersion, &created, &updated)
	if err != nil {
		return domain.NoteScope{}, mapError(err)
	}
	if item.CreatedAt, err = parseTime(created); err != nil {
		return domain.NoteScope{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.NoteScope{}, err
	}
	return item, nil
}

func scanNote(row scanner) (domain.Note, error) {
	var item domain.Note
	var live int
	var created, updated string
	err := row.Scan(&item.ID, &item.ScopeID, &item.ProjectID, &item.GrantID, &item.OwnerUserID, &item.Name,
		&item.HeadSeq, &live, &item.Size, &item.Digest, &item.HeadKind, &item.HeadWriter, &item.HeadLinkName,
		&item.ResourceVersion, &created, &updated)
	if err != nil {
		return domain.Note{}, mapError(err)
	}
	item.Live = live == 1
	if item.CreatedAt, err = parseTime(created); err != nil {
		return domain.Note{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Note{}, err
	}
	return item, nil
}

func scanNoteRevisions(rows *sql.Rows) ([]domain.NoteRevision, error) {
	defer rows.Close()
	var items []domain.NoteRevision
	for rows.Next() {
		item, err := scanNoteRevision(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func scanNoteRevision(row scanner) (domain.NoteRevision, error) {
	var item domain.NoteRevision
	var created string
	source := &item.Source
	err := row.Scan(&item.NoteID, &item.Seq, &item.ScopeID, &item.ProjectID, &item.GrantID, &item.OwnerUserID,
		&item.ScopeSeq, &item.Name, &item.Kind, &item.Writer, &item.WriterUserID, &source.ThreadID,
		&source.CapsuleID, &source.LinkID, &source.LinkName, &source.LeadThreadID, &source.GrantID,
		&source.WorkingNoteID, &source.WorkingNoteSeq, &source.WorkingNoteName, &source.WorkingCapsuleID,
		&item.ProposalID, &item.BaseSeq, &item.RestoredFromSeq, &item.Digest, &item.Size, &item.Ciphertext,
		&created)
	if err != nil {
		return domain.NoteRevision{}, mapError(err)
	}
	if item.CreatedAt, err = parseTime(created); err != nil {
		return domain.NoteRevision{}, err
	}
	return item, nil
}

func scanNoteProposals(rows *sql.Rows) ([]domain.NoteProposal, error) {
	defer rows.Close()
	var items []domain.NoteProposal
	for rows.Next() {
		item, err := scanNoteProposal(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func scanNoteProposal(row scanner) (domain.NoteProposal, error) {
	var item domain.NoteProposal
	var isDelete int
	var resolved, created, updated string
	source := &item.Source
	err := row.Scan(&item.ID, &item.ProjectID, &item.OwnerUserID, &item.Name, &isDelete, &item.BaseSeq,
		&item.State, &item.Reason, &item.Origin, &source.ThreadID, &source.CapsuleID, &source.LinkID,
		&source.LinkName, &source.LeadThreadID, &source.GrantID, &source.WorkingNoteID,
		&source.WorkingNoteSeq, &source.WorkingNoteName, &source.WorkingCapsuleID, &item.IdempotencyKey,
		&item.RequestDigest, &item.Digest, &item.Size, &item.Ciphertext, &item.ResolvedSeq, &item.ResolvedBy,
		&resolved, &item.ResourceVersion, &created, &updated)
	if err != nil {
		return domain.NoteProposal{}, mapError(err)
	}
	item.Delete = isDelete == 1
	if item.ResolvedAt, err = parseOptionalTime(resolved); err != nil {
		return domain.NoteProposal{}, err
	}
	if item.CreatedAt, err = parseTime(created); err != nil {
		return domain.NoteProposal{}, err
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.NoteProposal{}, err
	}
	return item, nil
}
