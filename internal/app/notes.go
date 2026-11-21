package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// Working notes and Project notes (ADR 0060). This file holds what both
// tiers share: the scope's data key, the one write path every revision
// takes, the bounds, and opening content. Content is plaintext only
// between opening an envelope and returning it to the owner's API, a
// member Capsule, or a delivery; it never reaches an event, an audit
// record, a log line or an error message.

// notesKey is the part of the transcript KEK notes use.
type notesKey interface {
	NewDEK() ([]byte, error)
	WrapNotesDEK(transcripts.NoteKeyScope, []byte) ([]byte, error)
	UnwrapNotesDEK(transcripts.NoteKeyScope, string, uint32, []byte) ([]byte, error)
	Metadata() (string, uint32)
}

func (s *Service) notesKey() (notesKey, error) {
	key, ok := s.transcriptKey.(notesKey)
	if !ok || s.transcriptKey == nil {
		return nil, domain.ErrUnsupported
	}
	return key, nil
}

func noteKeyScope(scope domain.NoteScope) transcripts.NoteKeyScope {
	return transcripts.NoteKeyScope{ProjectID: scope.ProjectID, GrantID: scope.GrantID, Owner: scope.OwnerUserID}
}

// requireNoteOperator admits only a user principal. The break-glass admin,
// the system and every Capsule see notes as not found, as for pins; owner
// scoping then narrows a user to their own Projects.
func requireNoteOperator(ctx context.Context) error {
	caller, ok := principal.From(ctx)
	if !ok {
		return principal.ErrMissing
	}
	if caller.Kind() != principal.KindUser {
		return domain.ErrNotFound
	}
	return nil
}

// NoteConflictError is a write against a revision that is no longer the
// note's head. It carries the current revision and who wrote it, never
// content.
type NoteConflictError struct {
	Current  int64
	LinkName string
	Writer   domain.NoteWriterKind
}

func (e *NoteConflictError) Error() string {
	return fmt.Sprintf("conflict: the note is at revision %d", e.Current)
}

func (e *NoteConflictError) Unwrap() error { return domain.ErrConflict }

// noteScopeTx returns a scope and its open data key. With create it makes
// the scope and, when it has none, a fresh key; otherwise a missing scope
// is domain.ErrNotFound and a shredded one returns a nil key. The caller
// zeroes the key.
func (s *Service) noteScopeTx(
	ctx context.Context, tx ports.Transaction, project domain.ProjectID, grant domain.CoordinationGrantID,
	create bool, now time.Time,
) (domain.NoteScope, []byte, error) {
	key, err := s.notesKey()
	if err != nil {
		return domain.NoteScope{}, nil, err
	}
	scope, err := tx.GetNoteScope(ctx, project, grant)
	switch {
	case errors.Is(err, domain.ErrNotFound) && create:
		owner, err := s.noteOwnerTx(ctx, tx, project)
		if err != nil {
			return domain.NoteScope{}, nil, err
		}
		scope = domain.NoteScope{
			ID: domain.NoteScopeID(s.ids.NewID()), ProjectID: project, GrantID: grant, OwnerUserID: owner,
			Delivery: domain.NoteDeliveryAllThreads, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.PutNoteScope(ctx, scope, 0); err != nil {
			return domain.NoteScope{}, nil, err
		}
	case err != nil:
		return domain.NoteScope{}, nil, err
	}
	if len(scope.WrappedDEK) == 0 {
		if !create {
			return scope, nil, nil
		}
		dek, err := key.NewDEK()
		if err != nil {
			return domain.NoteScope{}, nil, err
		}
		wrapped, err := key.WrapNotesDEK(noteKeyScope(scope), dek)
		if err != nil {
			wipe(dek)
			return domain.NoteScope{}, nil, err
		}
		previous := scope.ResourceVersion
		scope.WrappedDEK = wrapped
		scope.KEKID, scope.KEKVersion = key.Metadata()
		scope.ResourceVersion++
		scope.UpdatedAt = now
		if err := tx.PutNoteScope(ctx, scope, previous); err != nil {
			wipe(dek)
			return domain.NoteScope{}, nil, err
		}
		return scope, dek, nil
	}
	dek, err := key.UnwrapNotesDEK(noteKeyScope(scope), scope.KEKID, scope.KEKVersion, scope.WrappedDEK)
	if err != nil {
		return domain.NoteScope{}, nil, err
	}
	return scope, dek, nil
}

func (s *Service) noteOwnerTx(ctx context.Context, tx ports.Reader, project domain.ProjectID) (domain.UserID, error) {
	item, err := tx.GetProject(ctx, project)
	if err != nil {
		return "", err
	}
	if item.State != domain.ProjectActive && item.State != "" {
		return "", fmt.Errorf("%w: the Project is being deleted", domain.ErrIllegalTransition)
	}
	return item.OwnerUserID, nil
}

// openNoteScope opens a scope's key for reading, outside a write. A scope
// that does not exist or was shredded returns a nil key.
func (s *Service) openNoteScope(
	ctx context.Context, reader ports.Reader, project domain.ProjectID, grant domain.CoordinationGrantID,
) (domain.NoteScope, []byte, error) {
	key, err := s.notesKey()
	if err != nil {
		return domain.NoteScope{}, nil, err
	}
	scope, err := reader.GetNoteScope(ctx, project, grant)
	if err != nil {
		return domain.NoteScope{}, nil, err
	}
	if len(scope.WrappedDEK) == 0 {
		return scope, nil, nil
	}
	dek, err := key.UnwrapNotesDEK(noteKeyScope(scope), scope.KEKID, scope.KEKVersion, scope.WrappedDEK)
	return scope, dek, err
}

func revisionAAD(revision domain.NoteRevision) transcripts.NoteRevisionAAD {
	return transcripts.NoteRevisionAAD{
		ProjectID: revision.ProjectID, GrantID: revision.GrantID, NoteID: revision.NoteID,
		Seq: revision.Seq, Kind: revision.Kind, ProposalID: revision.ProposalID,
		CapsuleID: revision.Source.CapsuleID,
	}
}

// openRevision returns a revision's content. A revision without content
// (deleted, pruned or shredded) is domain.ErrNotFound.
func openRevision(dek []byte, revision domain.NoteRevision) (string, error) {
	if revision.Kind == domain.NoteRevisionDelete {
		return "", nil
	}
	if len(dek) == 0 || len(revision.Ciphertext) == 0 {
		return "", fmt.Errorf("%w: this revision's content is no longer kept", domain.ErrNotFound)
	}
	plaintext, err := transcripts.OpenNoteRevision(dek, revisionAAD(revision), revision.Ciphertext)
	if err != nil {
		return "", err
	}
	content := string(plaintext)
	wipe(plaintext)
	return content, nil
}

// noteWrite is one revision to write.
type noteWrite struct {
	scope domain.NoteScope
	dek   []byte
	name  string
	// rename gives the note a new name in the same revision.
	rename  string
	content string
	delete  bool
	kind    domain.NoteRevisionKind
	writer  domain.NoteWriterKind
	user    domain.UserID
	source  domain.NoteSource
	// proposal names the approved proposal.
	proposal domain.NoteProposalID
	// base is the head the writer last read, 0 to create.
	base         int64
	restoredFrom int64
	now          time.Time
}

// writeNoteTx is the one path every revision takes: it checks the base
// revision and the scope's bounds, seals the content, writes the revision
// and the note's new head, and drops content past the retained window. A
// write that would pass a limit is refused, never cut.
func (s *Service) writeNoteTx(ctx context.Context, tx ports.Transaction, w noteWrite) (domain.Note, domain.NoteRevision, error) {
	if !domain.ValidNoteName(w.name) || (w.rename != "" && !domain.ValidNoteName(w.rename)) {
		return domain.Note{}, domain.NoteRevision{}, fmt.Errorf("%w: a note name must match [a-z0-9][a-z0-9-]{0,47}", domain.ErrInvalid)
	}
	if len(w.dek) == 0 {
		return domain.Note{}, domain.NoteRevision{}, domain.ErrUnsupported
	}
	content := ""
	if !w.delete {
		var err error
		if content, err = domain.NormalizeNoteContent(w.content); err != nil {
			return domain.Note{}, domain.NoteRevision{}, err
		}
	}
	note, err := tx.GetNote(ctx, w.scope.ID, w.name)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.Note{}, domain.NoteRevision{}, err
	}
	head := int64(0)
	if exists {
		head = note.HeadSeq
	}
	if head != w.base {
		conflict := &NoteConflictError{Current: head}
		if exists {
			conflict.LinkName, conflict.Writer = note.HeadLinkName, note.HeadWriter
		}
		return domain.Note{}, domain.NoteRevision{}, conflict
	}
	if w.delete && (!exists || !note.Live) {
		return domain.Note{}, domain.NoteRevision{}, fmt.Errorf("%w: no live note named %s", domain.ErrNotFound, w.name)
	}
	if w.rename != "" && w.rename != w.name {
		if _, err := tx.GetNote(ctx, w.scope.ID, w.rename); err == nil {
			return domain.Note{}, domain.NoteRevision{}, fmt.Errorf("%w: a note named %s already exists", domain.ErrConflict, w.rename)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return domain.Note{}, domain.NoteRevision{}, err
		}
	}
	if !w.delete {
		if err := checkNoteBounds(ctx, tx, w.scope.ID, note, exists, len(content)); err != nil {
			return domain.Note{}, domain.NoteRevision{}, err
		}
	}
	previousScope := w.scope.ResourceVersion
	w.scope.RevisionCounter++
	w.scope.ResourceVersion++
	w.scope.UpdatedAt = w.now
	if err := tx.PutNoteScope(ctx, w.scope, previousScope); err != nil {
		return domain.Note{}, domain.NoteRevision{}, err
	}
	if !exists {
		note = domain.Note{
			ID: domain.NoteID(s.ids.NewID()), ScopeID: w.scope.ID, ProjectID: w.scope.ProjectID,
			GrantID: w.scope.GrantID, OwnerUserID: w.scope.OwnerUserID, Name: w.name,
			CreatedAt: w.now, ResourceVersion: 0,
		}
	}
	name := w.name
	if w.rename != "" {
		name = w.rename
	}
	revision := domain.NoteRevision{
		NoteID: note.ID, Seq: head + 1, ScopeID: w.scope.ID, ProjectID: w.scope.ProjectID,
		GrantID: w.scope.GrantID, OwnerUserID: w.scope.OwnerUserID, ScopeSeq: w.scope.RevisionCounter,
		Name: name, Kind: w.kind, Writer: w.writer, WriterUserID: w.user, Source: w.source,
		ProposalID: w.proposal, BaseSeq: w.base, RestoredFromSeq: w.restoredFrom,
		Digest: domain.NoteDigest(content), Size: len(content), CreatedAt: w.now,
	}
	if w.delete {
		revision.Kind = domain.NoteRevisionDelete
	} else {
		sealed, err := transcripts.SealNoteRevision(w.dek, revisionAAD(revision), []byte(content))
		if err != nil {
			return domain.Note{}, domain.NoteRevision{}, err
		}
		revision.Ciphertext = sealed
	}
	previousNote := note.ResourceVersion
	note.Name = name
	note.HeadSeq = revision.Seq
	note.Live = !w.delete
	note.Size = revision.Size
	note.Digest = revision.Digest
	note.HeadKind = revision.Kind
	note.HeadWriter = revision.Writer
	note.HeadLinkName = w.source.LinkName
	note.ResourceVersion++
	note.UpdatedAt = w.now
	if exists {
		err = tx.UpdateNote(ctx, note, previousNote)
	} else {
		err = tx.InsertNote(ctx, note)
	}
	if err != nil {
		return domain.Note{}, domain.NoteRevision{}, err
	}
	if err := tx.InsertNoteRevision(ctx, revision); err != nil {
		return domain.Note{}, domain.NoteRevision{}, err
	}
	if err := tx.PruneNoteRevisions(ctx, note.ID, domain.NoteRetainedRevisions); err != nil {
		return domain.Note{}, domain.NoteRevision{}, err
	}
	if w.scope.GrantID == "" {
		// A pending proposal against an earlier revision of this note can
		// no longer be approved as it is.
		for _, stale := range []string{w.name, name} {
			if err := s.markProposalsStaleTx(ctx, tx, w.scope.ProjectID, stale, w.proposal, w.now); err != nil {
				return domain.Note{}, domain.NoteRevision{}, err
			}
		}
	}
	return note, revision, nil
}

// checkNoteBounds refuses a write that would pass the scope's live-note
// count or total size.
func checkNoteBounds(
	ctx context.Context, tx ports.Reader, scope domain.NoteScopeID, note domain.Note, exists bool, size int,
) error {
	notes, err := tx.ListNotes(ctx, scope)
	if err != nil {
		return err
	}
	live, total := 0, 0
	for _, item := range notes {
		if item.Live {
			live++
			total += item.Size
		}
	}
	if exists && note.Live {
		total -= note.Size
	} else {
		live++
	}
	if live > domain.MaxLiveNotes {
		return fmt.Errorf("%w: at most %d live notes", domain.ErrResourceExhausted, domain.MaxLiveNotes)
	}
	if total+size > domain.MaxScopeNoteBytes {
		return fmt.Errorf("%w: notes are limited to %d bytes in total", domain.ErrResourceExhausted, domain.MaxScopeNoteBytes)
	}
	return nil
}

func (s *Service) markProposalsStaleTx(
	ctx context.Context, tx ports.Transaction, project domain.ProjectID, name string,
	except domain.NoteProposalID, now time.Time,
) error {
	pending, err := tx.ListNoteProposals(ctx, project, true, 0)
	if err != nil {
		return err
	}
	for _, proposal := range pending {
		if proposal.Name != name || proposal.ID == except {
			continue
		}
		previous := proposal.ResourceVersion
		proposal.State = domain.NoteProposalStale
		proposal.ResourceVersion++
		proposal.UpdatedAt = now
		if err := tx.UpdateNoteProposal(ctx, proposal, previous); err != nil {
			return err
		}
		s.noteProposalChanged(proposal)
	}
	return nil
}

// NoteView is a note's head, content-free.
type NoteView struct {
	Note domain.Note
	// AgentProposed says the head revision came from agent text: an agent
	// write, or an approved proposal or promotion.
	AgentProposed bool
}

func noteView(note domain.Note) NoteView {
	return NoteView{Note: note, AgentProposed: note.HeadWriter == domain.NoteWriterAgent ||
		note.HeadKind == domain.NoteRevisionApprovedProposal || note.HeadKind == domain.NoteRevisionApprovedWithEdits}
}

// NoteContent is a note revision and its content, for the owner's API.
type NoteContent struct {
	Note     NoteView
	Revision domain.NoteRevision
	Content  string
	// CredentialShaped flags content that the setup import's
	// credential-shaped checks match (ADR 0024).
	CredentialShaped bool
}

// NoteList is one scope's notes.
type NoteList struct {
	Scope      domain.NoteScope
	Notes      []NoteView
	LiveCount  int
	TotalBytes int
	// Pending is the number of pending proposals, for a Project.
	Pending int
}

func (s *Service) listNotes(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID,
) (NoteList, error) {
	var result NoteList
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetProject(ctx, project); err != nil {
			return err
		}
		scope, err := reader.GetNoteScope(ctx, project, grant)
		if errors.Is(err, domain.ErrNotFound) {
			result.Scope = domain.NoteScope{ProjectID: project, GrantID: grant, Delivery: domain.NoteDeliveryAllThreads}
		} else if err != nil {
			return err
		} else {
			result.Scope = scope
			notes, err := reader.ListNotes(ctx, scope.ID)
			if err != nil {
				return err
			}
			for _, note := range notes {
				result.Notes = append(result.Notes, noteView(note))
				if note.Live {
					result.LiveCount++
					result.TotalBytes += note.Size
				}
			}
		}
		if grant == "" {
			pending, err := reader.ListNoteProposals(ctx, project, true, 0)
			if err != nil {
				return err
			}
			now := s.clock.Now()
			for _, proposal := range pending {
				if proposal.EffectiveState(now) == domain.NoteProposalPending {
					result.Pending++
				}
			}
		}
		return nil
	})
	return result, err
}

// readNote returns one revision of a note with its content; seq 0 is the
// head. A deleted head is not found unless a revision is named.
func (s *Service) readNote(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID, name string, seq int64,
) (NoteContent, error) {
	var result NoteContent
	err := s.store.View(ctx, func(reader ports.Reader) error {
		scope, dek, err := s.openNoteScope(ctx, reader, project, grant)
		if err != nil {
			return err
		}
		defer wipe(dek)
		note, err := reader.GetNote(ctx, scope.ID, name)
		if err != nil {
			return err
		}
		if seq == 0 {
			if !note.Live {
				return fmt.Errorf("%w: the note was deleted; open a revision from its history", domain.ErrNotFound)
			}
			seq = note.HeadSeq
		}
		revision, err := reader.GetNoteRevision(ctx, note.ID, seq)
		if err != nil {
			return err
		}
		content, err := openRevision(dek, revision)
		if err != nil {
			return err
		}
		result = NoteContent{Note: noteView(note), Revision: revision, Content: content,
			CredentialShaped: credentialShaped(content)}
		return nil
	})
	return result, err
}

// NoteHistory is a note's revisions, newest first, content-free.
type NoteHistory struct {
	Note      NoteView
	Revisions []NoteRevisionView
}

type NoteRevisionView struct {
	Revision domain.NoteRevision
	// Retained says the revision still has content to show or restore.
	Retained bool
}

func (s *Service) noteHistory(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID, name string,
) (NoteHistory, error) {
	var result NoteHistory
	err := s.store.View(ctx, func(reader ports.Reader) error {
		scope, err := reader.GetNoteScope(ctx, project, grant)
		if err != nil {
			return err
		}
		note, err := reader.GetNote(ctx, scope.ID, name)
		if err != nil {
			return err
		}
		revisions, err := reader.ListNoteRevisions(ctx, note.ID, 0)
		if err != nil {
			return err
		}
		result.Note = noteView(note)
		for _, revision := range revisions {
			retained := len(revision.Ciphertext) > 0 && len(scope.WrappedDEK) > 0
			revision.Ciphertext = nil
			result.Revisions = append(result.Revisions, NoteRevisionView{Revision: revision, Retained: retained})
		}
		return nil
	})
	return result, err
}

// noteReplay is the idempotency outcome of a note mutation.
type noteReplay struct {
	NoteID     domain.NoteID         `json:"noteId,omitempty"`
	Seq        int64                 `json:"seq,omitempty"`
	ProposalID domain.NoteProposalID `json:"proposalId,omitempty"`
}

func (s *Service) noteReplayView(ctx context.Context, reader ports.Reader, replay noteReplay) (NoteView, error) {
	note, err := reader.GetNoteByID(ctx, replay.NoteID)
	if err != nil {
		return NoteView{}, err
	}
	return noteView(note), nil
}

// NoteWriteInput is an operator write of a whole note.
type NoteWriteInput struct {
	Content string
	// ExpectedRevision is the head the editor opened, 0 to create.
	ExpectedRevision int64
	// Rename gives the note a new name in the same revision.
	Rename string
	// SourceThreadID records the Thread a transcript selection came from.
	SourceThreadID domain.ThreadID
	IdempotencyKey string
}

// NoteDeleteInput deletes a note, or with WithHistory also every
// revision's content and, for a Project note, its pending proposals.
type NoteDeleteInput struct {
	ExpectedRevision int64
	WithHistory      bool
	IdempotencyKey   string
}

// NoteRestoreInput makes an older retained revision the head again.
type NoteRestoreInput struct {
	Revision         int64
	ExpectedRevision int64
	IdempotencyKey   string
}

// operatorWriteNote is an operator write to either tier.
func (s *Service) operatorWriteNote(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID, name string,
	input NoteWriteInput, check func(ports.Transaction) error,
) (NoteView, error) {
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return NoteView{}, err
	}
	if input.ExpectedRevision < 0 {
		return NoteView{}, fmt.Errorf("%w: expected revision must not be negative", domain.ErrInvalid)
	}
	user, err := ownerOf(ctx)
	if err != nil {
		return NoteView{}, domain.ErrNotFound
	}
	scopeKey := "notes:" + string(project) + ":" + string(grant) + ":" + name + ":write"
	var result NoteView
	var revision domain.NoteRevision
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if ok {
			result, err = s.noteReplayView(ctx, tx, replay)
			return err
		}
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		source := domain.NoteSource{}
		if input.SourceThreadID != "" {
			thread, err := tx.GetThread(ctx, input.SourceThreadID)
			if err != nil {
				return err
			}
			capsule, err := tx.GetCapsule(ctx, thread.CapsuleID)
			if err != nil {
				return err
			}
			if capsule.ProjectID != project {
				return domain.ErrNotFound
			}
			source.ThreadID = thread.ID
		}
		now := s.clock.Now().UTC()
		scope, dek, err := s.noteScopeTx(ctx, tx, project, grant, true, now)
		if err != nil {
			return err
		}
		defer wipe(dek)
		note, written, err := s.writeNoteTx(ctx, tx, noteWrite{
			scope: scope, dek: dek, name: name, rename: input.Rename, content: input.Content,
			kind: domain.NoteRevisionOperatorWrite, writer: domain.NoteWriterOperator, user: user,
			source: source, base: input.ExpectedRevision, now: now,
		})
		if err != nil {
			return err
		}
		revision = written
		result = noteView(note)
		if err := s.audit(ctx, tx, domain.AuditNoteWrite, noteObjectType(grant), string(note.ID)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scopeKey, input.IdempotencyKey, noteReplay{NoteID: note.ID, Seq: written.Seq}, now)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditNoteWrite, noteObjectType(grant), name, err)
		return NoteView{}, err
	}
	_ = revision
	return result, nil
}

func (s *Service) operatorDeleteNote(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID, name string,
	input NoteDeleteInput, check func(ports.Transaction) error,
) (NoteView, error) {
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return NoteView{}, err
	}
	user, err := ownerOf(ctx)
	if err != nil {
		return NoteView{}, domain.ErrNotFound
	}
	scopeKey := "notes:" + string(project) + ":" + string(grant) + ":" + name + ":delete"
	var result NoteView
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if ok {
			result, err = s.noteReplayView(ctx, tx, replay)
			return err
		}
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		now := s.clock.Now().UTC()
		scope, err := tx.GetNoteScope(ctx, project, grant)
		if err != nil {
			return err
		}
		note, err := tx.GetNote(ctx, scope.ID, name)
		if err != nil {
			return err
		}
		if note.HeadSeq != input.ExpectedRevision {
			return &NoteConflictError{Current: note.HeadSeq, LinkName: note.HeadLinkName, Writer: note.HeadWriter}
		}
		if note.Live {
			scope, dek, err := s.noteScopeTx(ctx, tx, project, grant, true, now)
			if err != nil {
				return err
			}
			note, _, err = s.writeNoteTx(ctx, tx, noteWrite{
				scope: scope, dek: dek, name: name, delete: true, kind: domain.NoteRevisionDelete,
				writer: domain.NoteWriterOperator, user: user, base: input.ExpectedRevision, now: now,
			})
			wipe(dek)
			if err != nil {
				return err
			}
		} else if !input.WithHistory {
			return fmt.Errorf("%w: the note is already deleted", domain.ErrIllegalTransition)
		}
		if input.WithHistory {
			if err := tx.DropNoteContent(ctx, note.ID); err != nil {
				return err
			}
			if grant == "" {
				if err := s.purgeNoteProposalsTx(ctx, tx, project, name, now); err != nil {
					return err
				}
			}
		}
		result = noteView(note)
		action := domain.AuditNoteDelete
		if input.WithHistory {
			action = domain.AuditNotePurge
		}
		if err := s.audit(ctx, tx, action, noteObjectType(grant), string(note.ID)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scopeKey, input.IdempotencyKey, noteReplay{NoteID: note.ID, Seq: note.HeadSeq}, now)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditNoteDelete, noteObjectType(grant), name, err)
		return NoteView{}, err
	}
	return result, nil
}

// purgeNoteProposalsTx closes a note's pending proposals and drops every
// proposal's content for that name.
func (s *Service) purgeNoteProposalsTx(
	ctx context.Context, tx ports.Transaction, project domain.ProjectID, name string, now time.Time,
) error {
	proposals, err := tx.ListNoteProposals(ctx, project, false, 0)
	if err != nil {
		return err
	}
	for _, proposal := range proposals {
		if proposal.Name != name || (proposal.State != domain.NoteProposalPending && len(proposal.Ciphertext) == 0) {
			continue
		}
		previous := proposal.ResourceVersion
		if proposal.State == domain.NoteProposalPending {
			proposal.State = domain.NoteProposalRejected
			proposal.Reason = domain.NoteProposalReasonPurged
			proposal.ResolvedAt = now
		}
		proposal.Ciphertext = nil
		proposal.ResourceVersion++
		proposal.UpdatedAt = now
		if err := tx.UpdateNoteProposal(ctx, proposal, previous); err != nil {
			return err
		}
		s.noteProposalChanged(proposal)
	}
	return nil
}

func (s *Service) operatorRestoreNote(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID, name string,
	input NoteRestoreInput, check func(ports.Transaction) error,
) (NoteView, error) {
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return NoteView{}, err
	}
	if input.Revision <= 0 {
		return NoteView{}, fmt.Errorf("%w: name the revision to restore", domain.ErrInvalid)
	}
	user, err := ownerOf(ctx)
	if err != nil {
		return NoteView{}, domain.ErrNotFound
	}
	scopeKey := "notes:" + string(project) + ":" + string(grant) + ":" + name + ":restore"
	var result NoteView
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, input.IdempotencyKey)
		if err != nil {
			return err
		}
		if ok {
			result, err = s.noteReplayView(ctx, tx, replay)
			return err
		}
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		now := s.clock.Now().UTC()
		scope, dek, err := s.noteScopeTx(ctx, tx, project, grant, false, now)
		if err != nil {
			return err
		}
		defer wipe(dek)
		note, err := tx.GetNote(ctx, scope.ID, name)
		if err != nil {
			return err
		}
		revision, err := tx.GetNoteRevision(ctx, note.ID, input.Revision)
		if err != nil {
			return err
		}
		if revision.Kind == domain.NoteRevisionDelete {
			return fmt.Errorf("%w: a deletion cannot be restored; restore the revision before it", domain.ErrInvalid)
		}
		content, err := openRevision(dek, revision)
		if err != nil {
			return err
		}
		written, rev, err := s.writeNoteTx(ctx, tx, noteWrite{
			scope: scope, dek: dek, name: name, content: content, kind: domain.NoteRevisionRestore,
			writer: domain.NoteWriterOperator, user: user, base: input.ExpectedRevision,
			restoredFrom: revision.Seq, now: now,
		})
		if err != nil {
			return err
		}
		result = noteView(written)
		if err := s.audit(ctx, tx, domain.AuditNoteRestore, noteObjectType(grant), string(note.ID)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scopeKey, input.IdempotencyKey, noteReplay{NoteID: note.ID, Seq: rev.Seq}, now)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditNoteRestore, noteObjectType(grant), name, err)
		return NoteView{}, err
	}
	return result, nil
}

// shredNotes clears a scope's data key, so every revision and proposal
// under it is unreadable and the live set is empty. A new key is made with
// the next note.
func (s *Service) shredNotes(
	ctx context.Context, project domain.ProjectID, grant domain.CoordinationGrantID, key string,
	check func(ports.Transaction) error,
) error {
	if err := requireIdempotency(key); err != nil {
		return err
	}
	scopeKey := "notes:" + string(project) + ":" + string(grant) + ":shred"
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, key); err != nil || ok {
			return err
		}
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		now := s.clock.Now().UTC()
		if err := s.shredNoteScopeTx(ctx, tx, project, grant, now); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditNoteShred, noteObjectType(grant), noteScopeObject(project, grant)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scopeKey, key, noteReplay{}, now)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditNoteShred, noteObjectType(grant), noteScopeObject(project, grant), err)
	}
	return err
}

func (s *Service) shredNoteScopeTx(
	ctx context.Context, tx ports.Transaction, project domain.ProjectID, grant domain.CoordinationGrantID, now time.Time,
) error {
	scope, err := tx.GetNoteScope(ctx, project, grant)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.ShredNoteScope(ctx, scope, now)
}

func noteObjectType(grant domain.CoordinationGrantID) string {
	if grant == "" {
		return "project_note"
	}
	return "working_note"
}

func noteScopeObject(project domain.ProjectID, grant domain.CoordinationGrantID) string {
	if grant == "" {
		return string(project)
	}
	return string(grant)
}

// movingNoteKey is a notes key opened for an owner transfer.
type movingNoteKey struct {
	scope domain.NoteScopeID
	dek   []byte
}

// openOwnedNoteKeys opens every notes key one owner holds. A key that does
// not open under the installation key was already unreadable; it moves
// unchanged and stays unreadable, as a secret does.
func (s *Service) openOwnedNoteKeys(ctx context.Context, tx ports.Transaction, owner domain.UserID) ([]movingNoteKey, error) {
	scopes, err := tx.ListOwnedNoteScopes(ctx, owner)
	if err != nil || len(scopes) == 0 {
		return nil, err
	}
	key, err := s.notesKey()
	if err != nil {
		return nil, nil
	}
	var keys []movingNoteKey
	for _, scope := range scopes {
		if len(scope.WrappedDEK) == 0 {
			continue
		}
		dek, err := key.UnwrapNotesDEK(noteKeyScope(scope), scope.KEKID, scope.KEKVersion, scope.WrappedDEK)
		if err != nil {
			continue
		}
		keys = append(keys, movingNoteKey{scope: scope.ID, dek: dek})
	}
	return keys, nil
}

// rewrapNoteKeysTx wraps each opened key for its scope's new owner.
func (s *Service) rewrapNoteKeysTx(ctx context.Context, tx ports.Transaction, keys []movingNoteKey) error {
	if len(keys) == 0 {
		return nil
	}
	key, err := s.notesKey()
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	for _, moving := range keys {
		scope, err := tx.GetNoteScopeByID(ctx, moving.scope)
		if err != nil {
			return err
		}
		wrapped, err := key.WrapNotesDEK(noteKeyScope(scope), moving.dek)
		if err != nil {
			return err
		}
		previous := scope.ResourceVersion
		scope.WrappedDEK = wrapped
		scope.KEKID, scope.KEKVersion = key.Metadata()
		scope.ResourceVersion++
		scope.UpdatedAt = now
		if err := tx.PutNoteScope(ctx, scope, previous); err != nil {
			return err
		}
	}
	return nil
}
