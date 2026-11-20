package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// Project notes (ADR 0060 section 2). Only a user who owns the Project
// writes them; agents propose, and every proposal is reviewed one at a
// time against the note's current revision.

// credentialShaped reuses the setup import's credential-shaped checks
// (ADR 0024) to flag content on review. It is a hint, not a control.
func credentialShaped(content string) bool { return harnesssetup.Sensitive(content) }

func (s *Service) ListProjectNotes(ctx context.Context, project domain.ProjectID) (NoteList, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteList{}, err
	}
	return s.listNotes(ctx, project, "")
}

func (s *Service) GetProjectNote(ctx context.Context, project domain.ProjectID, name string, seq int64) (NoteContent, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteContent{}, err
	}
	return s.readNote(ctx, project, "", name, seq)
}

func (s *Service) ListProjectNoteRevisions(ctx context.Context, project domain.ProjectID, name string) (NoteHistory, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteHistory{}, err
	}
	return s.noteHistory(ctx, project, "", name)
}

func (s *Service) PutProjectNote(
	ctx context.Context, project domain.ProjectID, name string, input NoteWriteInput,
) (NoteView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteView{}, err
	}
	return s.operatorWriteNote(ctx, project, "", name, input, nil)
}

func (s *Service) DeleteProjectNote(
	ctx context.Context, project domain.ProjectID, name string, input NoteDeleteInput,
) (NoteView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteView{}, err
	}
	view, err := s.operatorDeleteNote(ctx, project, "", name, input, nil)
	s.flushNoteMailboxes(ctx)
	return view, err
}

func (s *Service) RestoreProjectNote(
	ctx context.Context, project domain.ProjectID, name string, input NoteRestoreInput,
) (NoteView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteView{}, err
	}
	view, err := s.operatorRestoreNote(ctx, project, "", name, input, nil)
	s.flushNoteMailboxes(ctx)
	return view, err
}

// ShredProjectNotes crypto-shreds the Project's notes (ADR 0060 section 8).
func (s *Service) ShredProjectNotes(ctx context.Context, project domain.ProjectID, key string) error {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return err
	}
	err := s.shredNotes(ctx, project, "", key, nil)
	s.flushNoteMailboxes(ctx)
	return err
}

// SetProjectNotesDelivery sets which Threads receive Project notes.
// expected is the scope's resource version from the note list, zero when
// the Project has none yet.
func (s *Service) SetProjectNotesDelivery(
	ctx context.Context, project domain.ProjectID, delivery domain.NoteDelivery,
	expected domain.ResourceVersion, key string,
) (domain.NoteScope, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return domain.NoteScope{}, err
	}
	if !delivery.Valid() {
		return domain.NoteScope{}, fmt.Errorf("%w: notesDelivery must be all_threads, leads_only or off", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.NoteScope{}, err
	}
	scopeKey := "notes:" + string(project) + ":delivery"
	var result domain.NoteScope
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, key); err != nil {
			return err
		} else if ok {
			result, err = tx.GetNoteScope(ctx, project, "")
			return err
		}
		now := s.clock.Now().UTC()
		scope, err := tx.GetNoteScope(ctx, project, "")
		if errors.Is(err, domain.ErrNotFound) {
			if expected != 0 {
				return domain.ErrConflict
			}
			owner, err := s.noteOwnerTx(ctx, tx, project)
			if err != nil {
				return err
			}
			scope = domain.NoteScope{
				ID: domain.NoteScopeID(s.ids.NewID()), ProjectID: project, OwnerUserID: owner,
				Delivery: delivery, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
			}
			if err := tx.PutNoteScope(ctx, scope, 0); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if scope.ResourceVersion != expected {
				return domain.ErrConflict
			}
			scope.Delivery = delivery
			scope.ResourceVersion++
			scope.UpdatedAt = now
			if err := tx.PutNoteScope(ctx, scope, expected); err != nil {
				return err
			}
		}
		result = scope
		if err := s.audit(ctx, tx, domain.AuditNoteDeliveryUpdate, "project", string(project)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scopeKey, key, noteReplay{}, now)
	})
	result.WrappedDEK = nil
	return result, err
}

// NoteProposalView is a proposal for the owner's review list. It is
// content-free; State is the effective state at the time of the call.
type NoteProposalView struct {
	Proposal domain.NoteProposal
	State    domain.NoteProposalState
	// CurrentRevision is the Project note's head now, 0 when there is none.
	CurrentRevision int64
}

// NoteProposalReview is one proposal with its text and the note's current
// text, for the review page. Both are hostile until the operator reads them.
type NoteProposalReview struct {
	NoteProposalView
	Content          string
	CurrentContent   string
	CurrentLive      bool
	CredentialShaped bool
}

func (s *Service) ListNoteProposals(
	ctx context.Context, project domain.ProjectID, pendingOnly bool,
) ([]NoteProposalView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return nil, err
	}
	var result []NoteProposalView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetProject(ctx, project); err != nil {
			return err
		}
		proposals, err := reader.ListNoteProposals(ctx, project, pendingOnly, 200)
		if err != nil {
			return err
		}
		heads := map[string]int64{}
		if scope, err := reader.GetNoteScope(ctx, project, ""); err == nil {
			notes, err := reader.ListNotes(ctx, scope.ID)
			if err != nil {
				return err
			}
			for _, note := range notes {
				heads[note.Name] = note.HeadSeq
			}
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		now := s.clock.Now()
		for _, proposal := range proposals {
			state := s.proposalState(proposal, heads[proposal.Name], now)
			if pendingOnly && state != domain.NoteProposalPending {
				continue
			}
			proposal.Ciphertext = nil
			result = append(result, NoteProposalView{Proposal: proposal, State: state, CurrentRevision: heads[proposal.Name]})
		}
		return nil
	})
	return result, err
}

// proposalState is the effective state: expired past the expiry, and stale
// when the note moved past the proposal's base.
func (s *Service) proposalState(proposal domain.NoteProposal, head int64, now time.Time) domain.NoteProposalState {
	state := proposal.EffectiveState(now)
	if state == domain.NoteProposalPending && head != proposal.BaseSeq {
		return domain.NoteProposalStale
	}
	return state
}

func (s *Service) GetNoteProposal(ctx context.Context, id domain.NoteProposalID) (NoteProposalReview, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteProposalReview{}, err
	}
	var result NoteProposalReview
	err := s.store.View(ctx, func(reader ports.Reader) error {
		proposal, err := reader.GetNoteProposal(ctx, id)
		if err != nil {
			return err
		}
		scope, dek, err := s.openNoteScope(ctx, reader, proposal.ProjectID, "")
		if err != nil {
			return err
		}
		defer wipe(dek)
		head := int64(0)
		note, err := reader.GetNote(ctx, scope.ID, proposal.Name)
		if err == nil {
			head = note.HeadSeq
			result.CurrentLive = note.Live
			if note.Live {
				revision, err := reader.GetNoteRevision(ctx, note.ID, note.HeadSeq)
				if err != nil {
					return err
				}
				if result.CurrentContent, err = openRevision(dek, revision); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if !proposal.Delete {
			content, err := openProposal(dek, proposal)
			if err != nil {
				return err
			}
			result.Content = content
		}
		result.CredentialShaped = credentialShaped(result.Content)
		state := s.proposalState(proposal, head, s.clock.Now())
		proposal.Ciphertext = nil
		result.NoteProposalView = NoteProposalView{Proposal: proposal, State: state, CurrentRevision: head}
		return nil
	})
	return result, err
}

func proposalAAD(proposal domain.NoteProposal) transcripts.NoteProposalAAD {
	return transcripts.NoteProposalAAD{
		ProjectID: proposal.ProjectID, ProposalID: proposal.ID, BaseSeq: proposal.BaseSeq,
		ThreadID: proposal.Source.ThreadID,
	}
}

func openProposal(dek []byte, proposal domain.NoteProposal) (string, error) {
	if proposal.Delete {
		return "", nil
	}
	if len(dek) == 0 || len(proposal.Ciphertext) == 0 {
		return "", fmt.Errorf("%w: this proposal's content is no longer kept", domain.ErrNotFound)
	}
	plaintext, err := transcripts.OpenNoteProposal(dek, proposalAAD(proposal), proposal.Ciphertext)
	if err != nil {
		return "", err
	}
	content := string(plaintext)
	wipe(plaintext)
	return content, nil
}

// insertProposalTx seals a proposal's content under the Project-notes key
// and stores it, after the per-Project pending bound.
func (s *Service) insertProposalTx(
	ctx context.Context, tx ports.Transaction, proposal domain.NoteProposal, content string, now time.Time,
) (domain.NoteProposal, error) {
	pending, err := tx.ListNoteProposals(ctx, proposal.ProjectID, true, 0)
	if err != nil {
		return domain.NoteProposal{}, err
	}
	live, ownThread := 0, 0
	for _, item := range pending {
		if item.EffectiveState(now) != domain.NoteProposalPending {
			continue
		}
		live++
		if proposal.Origin == domain.NoteProposalFromAgent && item.Origin == domain.NoteProposalFromAgent &&
			item.Source.ThreadID == proposal.Source.ThreadID {
			ownThread++
		}
	}
	if live >= domain.MaxPendingProposalsPerProject {
		return domain.NoteProposal{}, fmt.Errorf("%w: the Project already has %d notes to review",
			domain.ErrResourceExhausted, domain.MaxPendingProposalsPerProject)
	}
	if proposal.Origin == domain.NoteProposalFromAgent && ownThread >= domain.MaxPendingProposalsPerThread {
		return domain.NoteProposal{}, fmt.Errorf("%w: this Thread already has %d proposals waiting for review",
			domain.ErrResourceExhausted, domain.MaxPendingProposalsPerThread)
	}
	scope, dek, err := s.noteScopeTx(ctx, tx, proposal.ProjectID, "", true, now)
	if err != nil {
		return domain.NoteProposal{}, err
	}
	defer wipe(dek)
	proposal.OwnerUserID = scope.OwnerUserID
	proposal.State = domain.NoteProposalPending
	proposal.ResourceVersion = 1
	proposal.CreatedAt, proposal.UpdatedAt = now, now
	if proposal.Delete {
		content = ""
	} else {
		normalized, err := domain.NormalizeNoteContent(content)
		if err != nil {
			return domain.NoteProposal{}, err
		}
		content = normalized
	}
	proposal.Digest = domain.NoteDigest(content)
	proposal.Size = len(content)
	if !proposal.Delete {
		if proposal.Ciphertext, err = transcripts.SealNoteProposal(dek, proposalAAD(proposal), []byte(content)); err != nil {
			return domain.NoteProposal{}, err
		}
	}
	if err := tx.InsertNoteProposal(ctx, proposal); err != nil {
		return domain.NoteProposal{}, err
	}
	if err := s.appendEvent(ctx, tx, "note_proposal", string(proposal.ID), "note_proposal.created", 1, nil); err != nil {
		return domain.NoteProposal{}, err
	}
	s.noteProposalChanged(proposal)
	return proposal, nil
}

// ApproveNoteInput binds one exact change: the proposal's content digest
// and the note's current revision. Content, when set and different, is
// the operator's edit of the proposal.
type ApproveNoteInput struct {
	Digest           string
	ExpectedRevision int64
	Content          *string
	IdempotencyKey   string
}

func (s *Service) ApproveNoteProposal(
	ctx context.Context, id domain.NoteProposalID, input ApproveNoteInput,
) (NoteProposalView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteProposalView{}, err
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return NoteProposalView{}, err
	}
	if input.Digest == "" || input.ExpectedRevision < 0 {
		return NoteProposalView{}, fmt.Errorf("%w: approve names the proposal digest and the note's current revision", domain.ErrInvalid)
	}
	user, err := ownerOf(ctx)
	if err != nil {
		return NoteProposalView{}, domain.ErrNotFound
	}
	scopeKey := "note-proposal:" + string(id) + ":approve"
	var result NoteProposalView
	var expired bool
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, input.IdempotencyKey); err != nil {
			return err
		} else if ok {
			proposal, err := tx.GetNoteProposal(ctx, replay.ProposalID)
			if err != nil {
				return err
			}
			proposal.Ciphertext = nil
			result = NoteProposalView{Proposal: proposal, State: proposal.State, CurrentRevision: replay.Seq}
			return nil
		}
		now := s.clock.Now().UTC()
		proposal, err := tx.GetNoteProposal(ctx, id)
		if err != nil {
			return err
		}
		if proposal.EffectiveState(now) == domain.NoteProposalExpired && proposal.State == domain.NoteProposalPending {
			expired = true
			return fmt.Errorf("%w: the proposal expired", domain.ErrIllegalTransition)
		}
		if proposal.State != domain.NoteProposalPending {
			return fmt.Errorf("%w: the proposal is %s", domain.ErrIllegalTransition, proposal.State)
		}
		if proposal.Digest != input.Digest {
			return fmt.Errorf("%w: the proposal is not the one reviewed", domain.ErrConflict)
		}
		scope, dek, err := s.noteScopeTx(ctx, tx, proposal.ProjectID, "", true, now)
		if err != nil {
			return err
		}
		defer wipe(dek)
		head := int64(0)
		if note, err := tx.GetNote(ctx, scope.ID, proposal.Name); err == nil {
			head = note.HeadSeq
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if head != proposal.BaseSeq {
			return fmt.Errorf("%w: the note changed since this proposal was written; open it in the editor and merge it by hand",
				domain.ErrConflict)
		}
		if head != input.ExpectedRevision {
			return &NoteConflictError{Current: head}
		}
		content, err := openProposal(dek, proposal)
		if err != nil {
			return err
		}
		kind := domain.NoteRevisionApprovedProposal
		state := domain.NoteProposalApproved
		if input.Content != nil {
			edited, err := domain.NormalizeNoteContent(*input.Content)
			if err != nil {
				return err
			}
			if proposal.Delete {
				return fmt.Errorf("%w: a proposed deletion cannot be edited", domain.ErrInvalid)
			}
			if edited != content {
				content = edited
				kind = domain.NoteRevisionApprovedWithEdits
				state = domain.NoteProposalApprovedWithEdits
			}
		}
		_, revision, err := s.writeNoteTx(ctx, tx, noteWrite{
			scope: scope, dek: dek, name: proposal.Name, content: content, delete: proposal.Delete,
			kind: kind, writer: domain.NoteWriterOperator, user: user, source: proposal.Source,
			proposal: proposal.ID, base: proposal.BaseSeq, now: now,
		})
		if err != nil {
			return err
		}
		previous := proposal.ResourceVersion
		proposal.State = state
		proposal.ResolvedSeq = revision.Seq
		proposal.ResolvedBy = user
		proposal.ResolvedAt = now
		proposal.ResourceVersion++
		proposal.UpdatedAt = now
		if err := tx.UpdateNoteProposal(ctx, proposal, previous); err != nil {
			return err
		}
		s.noteProposalChanged(proposal)
		if err := s.audit(ctx, tx, domain.AuditNoteApprove, "note_proposal", string(proposal.ID)); err != nil {
			return err
		}
		proposal.Ciphertext = nil
		result = NoteProposalView{Proposal: proposal, State: proposal.State, CurrentRevision: revision.Seq}
		return putReplay(ctx, tx, scopeKey, input.IdempotencyKey,
			noteReplay{ProposalID: proposal.ID, Seq: revision.Seq}, now)
	})
	if expired {
		s.ExpireNoteProposals(principal.WithSystem(ctx))
	}
	s.flushNoteMailboxes(ctx)
	if err != nil {
		s.auditFailure(ctx, domain.AuditNoteApprove, "note_proposal", string(id), err)
		return NoteProposalView{}, err
	}
	return result, nil
}

func (s *Service) RejectNoteProposal(ctx context.Context, id domain.NoteProposalID, key string) (NoteProposalView, error) {
	if err := cmp.Or(requirePrincipal(ctx), requireNoteOperator(ctx)); err != nil {
		return NoteProposalView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return NoteProposalView{}, err
	}
	user, err := ownerOf(ctx)
	if err != nil {
		return NoteProposalView{}, domain.ErrNotFound
	}
	scopeKey := "note-proposal:" + string(id) + ":reject"
	var result NoteProposalView
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[noteReplay](ctx, tx, scopeKey, key); err != nil {
			return err
		} else if ok {
			proposal, err := tx.GetNoteProposal(ctx, id)
			proposal.Ciphertext = nil
			result = NoteProposalView{Proposal: proposal, State: proposal.State}
			return err
		}
		now := s.clock.Now().UTC()
		proposal, err := tx.GetNoteProposal(ctx, id)
		if err != nil {
			return err
		}
		// A stale or expired proposal can still be rejected, which closes
		// it for good; an approved or rejected one cannot.
		switch proposal.State {
		case domain.NoteProposalPending, domain.NoteProposalStale, domain.NoteProposalExpired:
		default:
			return fmt.Errorf("%w: the proposal is %s", domain.ErrIllegalTransition, proposal.State)
		}
		previous := proposal.ResourceVersion
		proposal.State = domain.NoteProposalRejected
		proposal.Reason = domain.NoteProposalReasonOperator
		proposal.ResolvedBy = user
		proposal.ResolvedAt = now
		proposal.ResourceVersion++
		proposal.UpdatedAt = now
		if err := tx.UpdateNoteProposal(ctx, proposal, previous); err != nil {
			return err
		}
		s.noteProposalChanged(proposal)
		if err := s.audit(ctx, tx, domain.AuditNoteReject, "note_proposal", string(proposal.ID)); err != nil {
			return err
		}
		proposal.Ciphertext = nil
		result = NoteProposalView{Proposal: proposal, State: proposal.State}
		return putReplay(ctx, tx, scopeKey, key, noteReplay{ProposalID: proposal.ID}, now)
	})
	s.flushNoteMailboxes(ctx)
	if err != nil {
		s.auditFailure(ctx, domain.AuditNoteReject, "note_proposal", string(id), err)
		return NoteProposalView{}, err
	}
	return result, nil
}

// ExpireNoteProposals stores the expired state of proposals pending for
// longer than the expiry. Readers already see them as expired; the sweep
// makes it durable and tells the proposing Capsules.
func (s *Service) ExpireNoteProposals(ctx context.Context) {
	if requireSystem(ctx) != nil || s.transcriptKey == nil {
		return
	}
	now := s.clock.Now().UTC()
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		expired, err := tx.ListExpiredNoteProposals(ctx, now.Add(-domain.NoteProposalExpiry), 100)
		if err != nil {
			return err
		}
		for _, proposal := range expired {
			previous := proposal.ResourceVersion
			proposal.State = domain.NoteProposalExpired
			proposal.ResourceVersion++
			proposal.UpdatedAt = now
			if err := tx.UpdateNoteProposal(ctx, proposal, previous); err != nil {
				return err
			}
			s.noteProposalChanged(proposal)
		}
		return nil
	})
	s.flushNoteMailboxes(ctx)
}

// sweepNoteProposals runs the expiry sweep at most once a minute.
func (s *Service) sweepNoteProposals(ctx context.Context) {
	now := s.clock.Now()
	s.notesMu.Lock()
	due := now.Sub(s.notesSweptAt) >= time.Minute
	if due {
		s.notesSweptAt = now
	}
	s.notesMu.Unlock()
	if due {
		s.ExpireNoteProposals(ctx)
	}
}
