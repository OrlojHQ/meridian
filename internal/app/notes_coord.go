package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// Note requests from the coordination mailbox (ADR 0060). The lead and each
// subagent whose link is open are the tree's members: they read and write
// its working notes and may propose Project notes. Everything a Capsule
// sends is hostile; content is bounded and normalized, rate-limited, and
// stored only as an envelope.

// noteMember is the tree member a Capsule's active Thread is.
type noteMember struct {
	grant  domain.CoordinationGrant
	thread domain.Thread
	link   domain.ThreadLink
	lead   bool
}

func (m noteMember) source(capsule domain.Capsule) domain.NoteSource {
	source := domain.NoteSource{
		ThreadID: m.thread.ID, CapsuleID: capsule.ID, LeadThreadID: m.grant.RootThreadID, GrantID: m.grant.ID,
	}
	if !m.lead {
		source.LinkID, source.LinkName = m.link.ID, m.link.Name
	}
	return source
}

// noteMemberAuthority resolves the Capsule's place in a tree. A read
// answers while the grant is lapsed; a write does not. A revoked or ended
// grant, and a detached, cancelled, removed or denied subagent, get no
// answer at all.
func (s *Service) noteMemberAuthority(
	ctx context.Context, capsule domain.Capsule, mutating bool,
) (noteMember, *coordproto.Response) {
	fail := func(code, message string) (noteMember, *coordproto.Response) {
		response := coordproto.Fail(code, message)
		return noteMember{}, &response
	}
	if capsule.LinkID == "" {
		grant, thread, failure := s.coordinationAuthority(ctx, capsule, mutating)
		if failure != nil {
			return noteMember{}, failure
		}
		return noteMember{grant: grant, thread: thread, lead: true}, nil
	}
	var member noteMember
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		member.link, err = reader.GetThreadLink(ctx, capsule.LinkID)
		if err != nil {
			return err
		}
		if member.link.ChildCapsuleID != capsule.ID {
			return domain.ErrNotFound
		}
		if member.grant, err = reader.GetCoordinationGrant(ctx, member.link.GrantID); err != nil {
			return err
		}
		member.thread, err = reader.GetThread(ctx, member.link.ChildThreadID)
		return err
	})
	if err != nil {
		return fail(coordproto.CodeCoordinationDisabled, "this Thread has no coordination grant")
	}
	if member.grant.State != domain.GrantActive {
		return fail(coordproto.CodeGrantInactive, "coordination grant is not active")
	}
	if member.link.State != domain.LinkActive {
		return fail(coordproto.CodeLinkClosed, "this subagent has left its lead's tree")
	}
	if mutating {
		if refusal := grantRefusal(member.grant, s.clock.Now()); refusal != nil {
			return noteMember{}, refusal
		}
	}
	if member.thread.State != domain.ThreadActive || member.thread.CapsuleID != capsule.ID {
		return fail(coordproto.CodeGrantInactive, "subagent Thread is not active")
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" {
		return fail(coordproto.CodeUnavailable, "subagent Capsule is not Ready")
	}
	return member, nil
}

func (s *Service) handleNoteRequest(
	ctx context.Context, capsule domain.Capsule, request coordproto.Request,
) coordproto.Response {
	// fromFile is resolved by the Capsule's mailbox and never relayed.
	if request.FromFile != "" {
		return coordproto.Fail(coordproto.CodeInvalidRequest, "invalid coordination request")
	}
	if _, err := s.notesKey(); err != nil {
		return coordproto.Fail(coordproto.CodeUnavailable, "notes are not available on this Station")
	}
	member, failure := s.noteMemberAuthority(ctx, capsule, request.Mutating())
	if failure != nil {
		s.auditRejection(ctx, member.grant, request.Type, *failure)
		return *failure
	}
	var response coordproto.Response
	switch request.Type {
	case coordproto.KindWriteNote:
		response = s.coordWriteNote(ctx, member, capsule, request)
	case coordproto.KindSyncNotes:
		response = s.coordSyncNotes(ctx, member)
	case coordproto.KindProposeNote:
		response = s.coordProposeNote(ctx, member, capsule, request)
		s.flushNoteMailboxes(ctx)
	default:
		response = coordproto.Fail(coordproto.CodeInvalidRequest, "invalid coordination request")
	}
	s.auditRejection(ctx, member.grant, request.Type, response)
	return response
}

// noteRequestRecord is the idempotency outcome of a write-note.
type noteRequestRecord struct {
	Digest   string `json:"digest"`
	Revision int64  `json:"revision"`
}

func (s *Service) coordWriteNote(
	ctx context.Context, member noteMember, capsule domain.Capsule, request coordproto.Request,
) coordproto.Response {
	scopeKey := "coord:" + string(capsule.ID) + ":write-note"
	var response coordproto.Response
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[noteRequestRecord](ctx, tx, scopeKey, request.Key); err != nil {
			return err
		} else if ok {
			if replay.Digest != request.Digest() {
				response = coordproto.Fail(coordproto.CodeConflict, "key was already used for a different request")
				return nil
			}
			response = coordproto.OK()
			response.Note = &coordproto.NoteRef{Name: request.Name, Revision: replay.Revision, Deleted: request.Delete}
			return nil
		}
		now := s.clock.Now().UTC()
		scope, dek, err := s.noteScopeTx(ctx, tx, member.grant.ProjectID, member.grant.ID, true, now)
		if err != nil {
			return err
		}
		defer wipe(dek)
		tree, own, err := tx.CountNoteWrites(ctx, scope.ID, capsule.ID, now.Add(-domain.WorkingNoteWriteWindow))
		if err != nil {
			return err
		}
		if own >= domain.WorkingNoteWritesPerCapsule || tree >= domain.WorkingNoteWritesPerTree {
			response = coordproto.Fail(coordproto.CodeRateLimited, fmt.Sprintf(
				"working-note writes are limited to %d per Capsule and %d per tree in any hour",
				domain.WorkingNoteWritesPerCapsule, domain.WorkingNoteWritesPerTree))
			return nil
		}
		kind := domain.NoteRevisionAgentWrite
		if request.Delete {
			kind = domain.NoteRevisionDelete
		}
		_, revision, err := s.writeNoteTx(ctx, tx, noteWrite{
			scope: scope, dek: dek, name: request.Name, content: request.Content, delete: request.Delete,
			kind: kind, writer: domain.NoteWriterAgent, source: member.source(capsule),
			base: request.BaseRevision, now: now,
		})
		var conflict *NoteConflictError
		if errors.As(err, &conflict) {
			response = coordproto.Fail(coordproto.CodeConflict, fmt.Sprintf(
				"note %s is at revision %d, written by %s; read the refreshed file and write again against that revision",
				request.Name, conflict.Current, noteWriterName(conflict.Writer, conflict.LinkName)))
			response.Note = &coordproto.NoteRef{Name: request.Name, Revision: conflict.Current,
				WrittenBy: noteWriterName(conflict.Writer, conflict.LinkName)}
			if file, ok, err := s.workingNoteFileTx(ctx, tx, scope, dek, request.Name); err != nil {
				return err
			} else if ok {
				response.NoteFiles = []coordproto.NoteFile{file}
			}
			return nil
		}
		if err != nil {
			return err
		}
		response = coordproto.OK()
		response.Note = &coordproto.NoteRef{Name: request.Name, Revision: revision.Seq, Size: revision.Size,
			Deleted: request.Delete}
		return putReplay(ctx, tx, scopeKey, request.Key,
			noteRequestRecord{Digest: request.Digest(), Revision: revision.Seq}, now)
	})
	if err != nil {
		return noteFailure(err)
	}
	return response
}

// workingNoteFileTx reads one working note's current text for the mailbox.
func (s *Service) workingNoteFileTx(
	ctx context.Context, tx ports.Reader, scope domain.NoteScope, dek []byte, name string,
) (coordproto.NoteFile, bool, error) {
	note, err := tx.GetNote(ctx, scope.ID, name)
	if errors.Is(err, domain.ErrNotFound) {
		return coordproto.NoteFile{}, false, nil
	}
	if err != nil {
		return coordproto.NoteFile{}, false, err
	}
	if !note.Live {
		return coordproto.NoteFile{Name: name, Revision: note.HeadSeq, Deleted: true}, true, nil
	}
	revision, err := tx.GetNoteRevision(ctx, note.ID, note.HeadSeq)
	if err != nil {
		return coordproto.NoteFile{}, false, err
	}
	content, err := openRevision(dek, revision)
	if err != nil {
		return coordproto.NoteFile{}, false, err
	}
	return coordproto.NoteFile{Name: name, Revision: note.HeadSeq, Content: content}, true, nil
}

func noteWriterName(writer domain.NoteWriterKind, link string) string {
	switch {
	case writer == domain.NoteWriterOperator:
		return "operator"
	case link != "":
		return link
	default:
		return "lead"
	}
}

// noteFailure maps a store or validation error to a mailbox answer that
// carries no content.
func noteFailure(err error) coordproto.Response {
	switch {
	case errors.Is(err, domain.ErrResourceExhausted):
		return coordproto.Fail(coordproto.CodeLimitExceeded, safeNoteMessage(err))
	case errors.Is(err, domain.ErrInvalid):
		return coordproto.Fail(coordproto.CodeInvalidRequest, safeNoteMessage(err))
	case errors.Is(err, domain.ErrNotFound):
		return coordproto.Fail(coordproto.CodeNotFound, safeNoteMessage(err))
	case errors.Is(err, domain.ErrConflict):
		return coordproto.Fail(coordproto.CodeConflict, safeNoteMessage(err))
	case errors.Is(err, domain.ErrUnsupported):
		return coordproto.Fail(coordproto.CodeUnavailable, "notes are not available on this Station")
	default:
		return coordproto.Fail(coordproto.CodeInternal, "the note request failed")
	}
}

// safeNoteMessage keeps only the fixed text after the error's class; no
// error here ever wraps content.
func safeNoteMessage(err error) string {
	message := err.Error()
	if index := strings.Index(message, ": "); index >= 0 {
		message = message[index+2:]
	}
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

// coordSyncNotes returns the tree's live working notes to the mailbox,
// which rewrites the directory. It is a read and answers while lapsed.
func (s *Service) coordSyncNotes(ctx context.Context, member noteMember) coordproto.Response {
	var files []coordproto.NoteFile
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		files, err = s.workingNoteFiles(ctx, reader, member.grant)
		return err
	})
	if err != nil {
		return noteFailure(err)
	}
	response := coordproto.OK()
	response.NoteFiles = files
	return response
}

// workingNoteFiles reads a tree's live working notes.
func (s *Service) workingNoteFiles(
	ctx context.Context, reader ports.Reader, grant domain.CoordinationGrant,
) ([]coordproto.NoteFile, error) {
	files := []coordproto.NoteFile{}
	scope, dek, err := s.openNoteScope(ctx, reader, grant.ProjectID, grant.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	defer wipe(dek)
	if len(dek) == 0 {
		return files, nil
	}
	return liveNoteFiles(ctx, reader, scope, dek)
}

func liveNoteFiles(ctx context.Context, reader ports.Reader, scope domain.NoteScope, dek []byte) ([]coordproto.NoteFile, error) {
	notes, err := reader.ListNotes(ctx, scope.ID)
	if err != nil {
		return nil, err
	}
	files := []coordproto.NoteFile{}
	for _, note := range notes {
		if !note.Live {
			continue
		}
		revision, err := reader.GetNoteRevision(ctx, note.ID, note.HeadSeq)
		if err != nil {
			return nil, err
		}
		content, err := openRevision(dek, revision)
		if err != nil {
			return nil, err
		}
		files = append(files, coordproto.NoteFile{Name: note.Name, Revision: note.HeadSeq, Content: content})
	}
	return files, nil
}

// coordProposeNote stores a pending Project-note proposal. The answer is
// content-free: the proposal's id and state.
func (s *Service) coordProposeNote(
	ctx context.Context, member noteMember, capsule domain.Capsule, request coordproto.Request,
) coordproto.Response {
	var response coordproto.Response
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		existing, err := tx.GetNoteProposalByKey(ctx, capsule.ID, request.Key)
		if err == nil {
			if existing.RequestDigest != request.Digest() {
				response = coordproto.Fail(coordproto.CodeConflict, "key was already used for a different request")
				return nil
			}
			response = coordproto.OK()
			response.Proposal = &coordproto.NoteProposalRef{ID: string(existing.ID), Name: existing.Name,
				State: string(existing.EffectiveState(s.clock.Now()))}
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		now := s.clock.Now().UTC()
		source := member.source(capsule)
		content := request.Content
		if request.FromWorkingNote != "" {
			text, working, err := s.workingNoteSourceTx(ctx, tx, member.grant, request.FromWorkingNote)
			if err != nil {
				response = noteFailure(err)
				return nil
			}
			content = text
			source.WorkingNoteID, source.WorkingNoteSeq = working.WorkingNoteID, working.WorkingNoteSeq
			source.WorkingNoteName, source.WorkingCapsuleID = working.WorkingNoteName, working.WorkingCapsuleID
		}
		head, err := projectNoteHeadTx(ctx, tx, member.grant.ProjectID, request.Name)
		if err != nil {
			return err
		}
		if head != request.BaseRevision {
			response = coordproto.Fail(coordproto.CodeConflict, fmt.Sprintf(
				"Project note %s is at revision %d; read it again and propose against that revision", request.Name, head))
			return nil
		}
		proposal, err := s.insertProposalTx(ctx, tx, domain.NoteProposal{
			ID: domain.NoteProposalID(s.ids.NewID()), ProjectID: member.grant.ProjectID, Name: request.Name,
			Delete: request.Delete, BaseSeq: request.BaseRevision, Origin: domain.NoteProposalFromAgent,
			Source: source, IdempotencyKey: request.Key, RequestDigest: request.Digest(),
		}, content, now)
		if err != nil {
			return err
		}
		response = coordproto.OK()
		response.Proposal = &coordproto.NoteProposalRef{ID: string(proposal.ID), Name: proposal.Name,
			State: string(proposal.State)}
		return nil
	})
	if err != nil {
		return noteFailure(err)
	}
	return response
}

// noteProposalChanged marks the proposing Capsule's mailbox for a whoami
// refresh once the change commits. A rolled-back change only re-pushes the
// current states.
func (s *Service) noteProposalChanged(proposal domain.NoteProposal) {
	if proposal.Origin != domain.NoteProposalFromAgent || proposal.Source.CapsuleID == "" {
		return
	}
	s.notesMu.Lock()
	if s.notesDirty == nil {
		s.notesDirty = map[domain.CapsuleID]struct{}{}
	}
	if len(s.notesDirty) < 256 {
		s.notesDirty[proposal.Source.CapsuleID] = struct{}{}
	}
	s.notesMu.Unlock()
}

// flushNoteMailboxes re-pushes the configuration of every marked Capsule,
// best effort, as the system.
func (s *Service) flushNoteMailboxes(ctx context.Context) {
	s.notesMu.Lock()
	dirty := s.notesDirty
	s.notesDirty = nil
	s.notesMu.Unlock()
	if len(dirty) == 0 || s.coordination == nil {
		return
	}
	system := principal.WithSystem(context.WithoutCancel(ctx))
	for id := range dirty {
		var thread domain.Thread
		if err := s.store.View(system, func(reader ports.Reader) error {
			var err error
			thread, err = reader.GetActiveThread(system, id)
			return err
		}); err != nil {
			continue
		}
		s.configureCoordinationMailbox(system, id, thread.ID)
	}
}

// noteProposalRefs is the content-free state of a Thread's most recent
// proposals, for whoami.
func (s *Service) noteProposalRefs(ctx context.Context, thread domain.ThreadID) []coordproto.NoteProposalRef {
	var refs []coordproto.NoteProposalRef
	_ = s.store.View(ctx, func(reader ports.Reader) error {
		proposals, err := reader.ListThreadNoteProposals(ctx, thread, domain.ProposalStatesShown)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		for _, proposal := range proposals {
			refs = append(refs, coordproto.NoteProposalRef{
				ID: string(proposal.ID), Name: proposal.Name, State: string(proposal.EffectiveState(now)),
			})
		}
		return nil
	})
	return refs
}

// notesWakeSection lists, by name, the working notes that changed since the
// lead's last wake and who wrote them. The lead's own writes are left out.
// It returns the scope sequence to mark once the wake is sent, or 0.
func (s *Service) notesWakeSection(ctx context.Context, grant domain.CoordinationGrant) (string, int64) {
	var revisions []domain.NoteRevision
	var scope domain.NoteScope
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		scope, err = reader.GetNoteScope(ctx, grant.ProjectID, grant.ID)
		if err != nil {
			return err
		}
		revisions, err = reader.ListScopeRevisionsSince(ctx, scope.ID, scope.HeadlinedSeq, 200)
		return err
	}); err != nil || len(revisions) == 0 {
		return "", 0
	}
	writers := map[string]map[string]bool{}
	mark := scope.HeadlinedSeq
	for _, revision := range revisions {
		mark = max(mark, revision.ScopeSeq)
		if revision.Writer == domain.NoteWriterAgent && revision.Source.LinkID == "" {
			continue
		}
		if writers[revision.Name] == nil {
			writers[revision.Name] = map[string]bool{}
		}
		writers[revision.Name][noteWriterName(revision.Writer, revision.Source.LinkName)] = true
	}
	if len(writers) == 0 {
		return "", mark
	}
	names := make([]string, 0, len(writers))
	for name := range writers {
		names = append(names, name)
	}
	sort.Strings(names)
	var text strings.Builder
	text.WriteString("Working notes changed since the last wake (run sync-notes before relying on them):\n")
	for index, name := range names {
		if index == 16 {
			fmt.Fprintf(&text, "- and %d more\n", len(names)-16)
			break
		}
		who := make([]string, 0, len(writers[name]))
		for writer := range writers[name] {
			who = append(who, writer)
		}
		sort.Strings(who)
		fmt.Fprintf(&text, "- %s.md, written by %s\n", name, strings.Join(who, ", "))
	}
	return text.String() + "\n", mark
}

func (s *Service) markNotesHeadlinedTx(
	ctx context.Context, tx ports.Transaction, grant domain.CoordinationGrant, mark int64, now time.Time,
) error {
	if mark == 0 {
		return nil
	}
	scope, err := tx.GetNoteScope(ctx, grant.ProjectID, grant.ID)
	if err != nil {
		return err
	}
	if scope.HeadlinedSeq >= mark || mark > scope.RevisionCounter {
		return nil
	}
	previous := scope.ResourceVersion
	scope.HeadlinedSeq = mark
	scope.ResourceVersion++
	scope.UpdatedAt = now
	return tx.PutNoteScope(ctx, scope, previous)
}
