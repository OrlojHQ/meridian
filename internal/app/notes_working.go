package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Working notes (ADR 0060 section 1), as the operator sees them from the
// lead's Agents panel. Members write them through the mailbox; the
// operator reads, edits, deletes and restores them here. An operator edit
// is a revision like any other and does not count as operator presence
// for the grant.

// workingGrant returns the grant and its Project.
func (s *Service) workingGrant(ctx context.Context, id domain.CoordinationGrantID) (domain.CoordinationGrant, error) {
	var grant domain.CoordinationGrant
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrant(ctx, id)
		if err != nil {
			return err
		}
		_, err = reader.GetProject(ctx, grant.ProjectID)
		return err
	})
	return grant, err
}

// requireLiveTree refuses an edit once the grant is revoked or ended with
// its lead: the notes are then read-only, though the operator can still
// delete, shred and promote them.
func requireLiveTree(ctx context.Context, id domain.CoordinationGrantID) func(ports.Transaction) error {
	return func(tx ports.Transaction) error {
		grant, err := tx.GetCoordinationGrant(ctx, id)
		if err != nil {
			return err
		}
		if grant.State != domain.GrantActive {
			return fmt.Errorf("%w: the lead's grant is %s, so its working notes are read-only", domain.ErrIllegalTransition, grant.State)
		}
		return nil
	}
}

func (s *Service) ListWorkingNotes(ctx context.Context, id domain.CoordinationGrantID) (NoteList, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteList{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteList{}, err
	}
	return s.listNotes(ctx, grant.ProjectID, grant.ID)
}

func (s *Service) GetWorkingNote(ctx context.Context, id domain.CoordinationGrantID, name string, seq int64) (NoteContent, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteContent{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteContent{}, err
	}
	return s.readNote(ctx, grant.ProjectID, grant.ID, name, seq)
}

func (s *Service) ListWorkingNoteRevisions(ctx context.Context, id domain.CoordinationGrantID, name string) (NoteHistory, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteHistory{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteHistory{}, err
	}
	return s.noteHistory(ctx, grant.ProjectID, grant.ID, name)
}

func (s *Service) PutWorkingNote(
	ctx context.Context, id domain.CoordinationGrantID, name string, input NoteWriteInput,
) (NoteView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteView{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteView{}, err
	}
	input.SourceThreadID = ""
	return s.operatorWriteNote(ctx, grant.ProjectID, grant.ID, name, input, requireLiveTree(ctx, id))
}

func (s *Service) DeleteWorkingNote(
	ctx context.Context, id domain.CoordinationGrantID, name string, input NoteDeleteInput,
) (NoteView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteView{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteView{}, err
	}
	var check func(ports.Transaction) error
	if !input.WithHistory {
		check = requireLiveTree(ctx, id)
	}
	return s.operatorDeleteNote(ctx, grant.ProjectID, grant.ID, name, input, check)
}

func (s *Service) RestoreWorkingNote(
	ctx context.Context, id domain.CoordinationGrantID, name string, input NoteRestoreInput,
) (NoteView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteView{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteView{}, err
	}
	return s.operatorRestoreNote(ctx, grant.ProjectID, grant.ID, name, input, requireLiveTree(ctx, id))
}

// ShredWorkingNotes crypto-shreds one tree's working notes.
func (s *Service) ShredWorkingNotes(ctx context.Context, id domain.CoordinationGrantID, key string) error {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return err
	}
	err = s.shredNotes(ctx, grant.ProjectID, grant.ID, key, nil)
	s.flushNoteMailboxes(ctx)
	return err
}

// PromoteWorkingNote opens the review of a working note as a change to the
// Project note of the same name, or a new one (ADR 0060 section 2). It
// stores a pending proposal; nothing leaves the tree until the operator
// approves it on the review page. A revoked lead's notes can still be
// promoted.
func (s *Service) PromoteWorkingNote(
	ctx context.Context, id domain.CoordinationGrantID, name string, key string,
) (NoteProposalView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteProposalView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return NoteProposalView{}, err
	}
	grant, err := s.workingGrant(ctx, id)
	if err != nil {
		return NoteProposalView{}, err
	}
	scopeKey := "notes:" + string(grant.ID) + ":" + name + ":promote"
	var result NoteProposalView
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, key); err != nil {
			return err
		} else if ok {
			proposal, err := tx.GetNoteProposal(ctx, replay.ProposalID)
			proposal.Ciphertext = nil
			result = NoteProposalView{Proposal: proposal, State: proposal.State}
			return err
		}
		now := s.clock.Now().UTC()
		content, source, err := s.workingNoteSourceTx(ctx, tx, grant, name)
		if err != nil {
			return err
		}
		base, err := projectNoteHeadTx(ctx, tx, grant.ProjectID, name)
		if err != nil {
			return err
		}
		proposal, err := s.insertProposalTx(ctx, tx, domain.NoteProposal{
			ID: domain.NoteProposalID(s.ids.NewID()), ProjectID: grant.ProjectID, Name: name,
			BaseSeq: base, Origin: domain.NoteProposalFromPromotion, Source: source,
			IdempotencyKey: key,
		}, content, now)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditNotePromote, "note_proposal", string(proposal.ID)); err != nil {
			return err
		}
		proposal.Ciphertext = nil
		result = NoteProposalView{Proposal: proposal, State: proposal.State, CurrentRevision: base}
		return putReplay(ctx, tx, scopeKey, key, noteReplay{ProposalID: proposal.ID}, now)
	})
	return result, err
}

// workingNoteSourceTx reads a live working note's head and the provenance a
// promotion or a fromWorkingNote proposal records.
func (s *Service) workingNoteSourceTx(
	ctx context.Context, tx ports.Transaction, grant domain.CoordinationGrant, name string,
) (string, domain.NoteSource, error) {
	scope, dek, err := s.noteScopeTx(ctx, tx, grant.ProjectID, grant.ID, false, s.clock.Now())
	if err != nil {
		return "", domain.NoteSource{}, err
	}
	defer wipe(dek)
	note, err := tx.GetNote(ctx, scope.ID, name)
	if err != nil {
		return "", domain.NoteSource{}, err
	}
	if !note.Live {
		return "", domain.NoteSource{}, fmt.Errorf("%w: no live working note named %s", domain.ErrNotFound, name)
	}
	revision, err := tx.GetNoteRevision(ctx, note.ID, note.HeadSeq)
	if err != nil {
		return "", domain.NoteSource{}, err
	}
	content, err := openRevision(dek, revision)
	if err != nil {
		return "", domain.NoteSource{}, err
	}
	source := revision.Source
	source.GrantID = grant.ID
	source.LeadThreadID = grant.RootThreadID
	source.WorkingNoteID = note.ID
	source.WorkingNoteSeq = revision.Seq
	source.WorkingNoteName = note.Name
	source.WorkingCapsuleID = revision.Source.CapsuleID
	return content, source, nil
}

// projectNoteHeadTx is the Project note's head revision, 0 when none.
func projectNoteHeadTx(ctx context.Context, tx ports.Reader, project domain.ProjectID, name string) (int64, error) {
	scope, err := tx.GetNoteScope(ctx, project, "")
	if errors.Is(err, domain.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	note, err := tx.GetNote(ctx, scope.ID, name)
	if errors.Is(err, domain.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return note.HeadSeq, nil
}
