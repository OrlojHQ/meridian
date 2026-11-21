package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// Working notes and Project notes (ADR 0060). Responses that carry note or
// proposal text are Cache-Control: no-store; lists are content-free.

func (s *Server) registerNoteRoutes() {
	for _, prefix := range []string{"/projects/{projectId}", "/coordination-grants/{grantId}"} {
		s.mux.HandleFunc("GET "+prefix+"/notes", s.listNotes)
		s.mux.HandleFunc("POST "+prefix+"/notes/shred", s.shredNotes)
		s.mux.HandleFunc("GET "+prefix+"/notes/{noteName}", s.getNote)
		s.mux.HandleFunc("PUT "+prefix+"/notes/{noteName}", s.putNote)
		s.mux.HandleFunc("POST "+prefix+"/notes/{noteName}/delete", s.deleteNote)
		s.mux.HandleFunc("POST "+prefix+"/notes/{noteName}/restore", s.restoreNote)
		s.mux.HandleFunc("GET "+prefix+"/notes/{noteName}/revisions", s.noteRevisions)
	}
	s.mux.HandleFunc("POST /coordination-grants/{grantId}/notes/{noteName}/promote", s.promoteNote)
	s.mux.HandleFunc("PUT /projects/{projectId}/notes-delivery", s.setNotesDelivery)
	s.mux.HandleFunc("GET /projects/{projectId}/note-proposals", s.projectNoteProposals)
	s.mux.HandleFunc("GET /note-proposals", s.pendingNoteProposals)
	s.mux.HandleFunc("GET /note-proposals/{proposalId}", s.getNoteProposal)
	s.mux.HandleFunc("POST /note-proposals/{proposalId}/approve", s.approveNoteProposal)
	s.mux.HandleFunc("POST /note-proposals/{proposalId}/reject", s.rejectNoteProposal)
	s.mux.HandleFunc("GET /threads/{threadId}/notes", s.threadNotes)
}

type noteSourceJSON struct {
	ThreadID            string `json:"threadId,omitempty"`
	CapsuleID           string `json:"capsuleId,omitempty"`
	LinkID              string `json:"linkId,omitempty"`
	LinkName            string `json:"linkName,omitempty"`
	LeadThreadID        string `json:"leadThreadId,omitempty"`
	GrantID             string `json:"grantId,omitempty"`
	WorkingNoteName     string `json:"workingNoteName,omitempty"`
	WorkingNoteRevision int64  `json:"workingNoteRevision,omitempty"`
	WorkingCapsuleID    string `json:"workingCapsuleId,omitempty"`
}

func noteSource(source domain.NoteSource) *noteSourceJSON {
	if source == (domain.NoteSource{}) {
		return nil
	}
	return &noteSourceJSON{
		ThreadID: string(source.ThreadID), CapsuleID: string(source.CapsuleID), LinkID: string(source.LinkID),
		LinkName: source.LinkName, LeadThreadID: string(source.LeadThreadID), GrantID: string(source.GrantID),
		WorkingNoteName: source.WorkingNoteName, WorkingNoteRevision: source.WorkingNoteSeq,
		WorkingCapsuleID: string(source.WorkingCapsuleID),
	}
}

type noteSummaryJSON struct {
	Name          string    `json:"name"`
	Revision      int64     `json:"revision"`
	Live          bool      `json:"live"`
	Size          int       `json:"size"`
	Digest        string    `json:"digest"`
	Kind          string    `json:"kind"`
	Writer        string    `json:"writer"`
	LinkName      string    `json:"linkName,omitempty"`
	AgentProposed bool      `json:"agentProposed"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func noteSummary(view app.NoteView) noteSummaryJSON {
	note := view.Note
	return noteSummaryJSON{
		Name: note.Name, Revision: note.HeadSeq, Live: note.Live, Size: note.Size, Digest: note.Digest,
		Kind: string(note.HeadKind), Writer: string(note.HeadWriter), LinkName: note.HeadLinkName,
		AgentProposed: view.AgentProposed, CreatedAt: note.CreatedAt.UTC(), UpdatedAt: note.UpdatedAt.UTC(),
	}
}

type noteRevisionJSON struct {
	Revision     int64           `json:"revision"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	Writer       string          `json:"writer"`
	WriterUserID string          `json:"writerUserId,omitempty"`
	Source       *noteSourceJSON `json:"source,omitempty"`
	ProposalID   string          `json:"proposalId,omitempty"`
	BaseRevision int64           `json:"baseRevision"`
	RestoredFrom int64           `json:"restoredFrom,omitempty"`
	Digest       string          `json:"digest"`
	Size         int             `json:"size"`
	Retained     bool            `json:"retained"`
	CreatedAt    time.Time       `json:"createdAt"`
}

func noteRevision(revision domain.NoteRevision, retained bool) noteRevisionJSON {
	return noteRevisionJSON{
		Revision: revision.Seq, Name: revision.Name, Kind: string(revision.Kind), Writer: string(revision.Writer),
		WriterUserID: string(revision.WriterUserID), Source: noteSource(revision.Source),
		ProposalID: string(revision.ProposalID), BaseRevision: revision.BaseSeq,
		RestoredFrom: revision.RestoredFromSeq, Digest: revision.Digest, Size: revision.Size,
		Retained: retained, CreatedAt: revision.CreatedAt.UTC(),
	}
}

type noteProposalJSON struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"projectId"`
	Name             string          `json:"name"`
	Delete           bool            `json:"delete"`
	BaseRevision     int64           `json:"baseRevision"`
	CurrentRevision  int64           `json:"currentRevision"`
	State            string          `json:"state"`
	Reason           string          `json:"reason,omitempty"`
	Origin           string          `json:"origin"`
	Source           *noteSourceJSON `json:"source,omitempty"`
	Digest           string          `json:"digest"`
	Size             int             `json:"size"`
	ResolvedRevision int64           `json:"resolvedRevision,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	ExpiresAt        time.Time       `json:"expiresAt"`
}

func noteProposal(view app.NoteProposalView) noteProposalJSON {
	proposal := view.Proposal
	return noteProposalJSON{
		ID: string(proposal.ID), ProjectID: string(proposal.ProjectID), Name: proposal.Name,
		Delete: proposal.Delete, BaseRevision: proposal.BaseSeq, CurrentRevision: view.CurrentRevision,
		State: string(view.State), Reason: string(proposal.Reason), Origin: string(proposal.Origin),
		Source: noteSource(proposal.Source), Digest: proposal.Digest, Size: proposal.Size,
		ResolvedRevision: proposal.ResolvedSeq, CreatedAt: proposal.CreatedAt.UTC(),
		ExpiresAt: proposal.CreatedAt.Add(domain.NoteProposalExpiry).UTC(),
	}
}

// noteTarget is the Project or grant a note route names.
type noteTarget struct {
	project domain.ProjectID
	grant   domain.CoordinationGrantID
}

func targetOf(request *http.Request) noteTarget {
	return noteTarget{
		project: domain.ProjectID(request.PathValue("projectId")),
		grant:   domain.CoordinationGrantID(request.PathValue("grantId")),
	}
}

func noStore(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
}

func (s *Server) listNotes(writer http.ResponseWriter, request *http.Request) {
	target := targetOf(request)
	var list app.NoteList
	var err error
	if target.grant != "" {
		list, err = s.service.ListWorkingNotes(request.Context(), target.grant)
	} else {
		list, err = s.service.ListProjectNotes(request.Context(), target.project)
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]noteSummaryJSON, 0, len(list.Notes))
	for _, note := range list.Notes {
		items = append(items, noteSummary(note))
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": items, "liveCount": list.LiveCount, "totalBytes": list.TotalBytes,
		"maxNotes": domain.MaxLiveNotes, "maxBytes": domain.MaxScopeNoteBytes,
		"notesDelivery": list.Scope.Delivery, "settingsVersion": list.Scope.ResourceVersion,
		"pendingProposals": list.Pending,
	})
}

func (s *Server) getNote(writer http.ResponseWriter, request *http.Request) {
	target := targetOf(request)
	name := request.PathValue("noteName")
	var seq int64
	if raw := request.URL.Query().Get("revision"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			writeError(writer, domain.ErrInvalid)
			return
		}
		seq = parsed
	}
	var content app.NoteContent
	var err error
	if target.grant != "" {
		content, err = s.service.GetWorkingNote(request.Context(), target.grant, name, seq)
	} else {
		content, err = s.service.GetProjectNote(request.Context(), target.project, name, seq)
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	noStore(writer)
	writeJSON(writer, http.StatusOK, map[string]any{
		"note": noteSummary(content.Note), "revision": noteRevision(content.Revision, true),
		"content": content.Content, "credentialShaped": content.CredentialShaped,
	})
}

type writeNoteRequest struct {
	Content          string `json:"content"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Rename           string `json:"rename,omitempty"`
	SourceThreadID   string `json:"sourceThreadId,omitempty"`
}

func (s *Server) putNote(writer http.ResponseWriter, request *http.Request) {
	var body writeNoteRequest
	if !decode(writer, request, &body) {
		return
	}
	input := app.NoteWriteInput{
		Content: body.Content, ExpectedRevision: body.ExpectedRevision, Rename: body.Rename,
		SourceThreadID: domain.ThreadID(body.SourceThreadID), IdempotencyKey: request.Header.Get("Idempotency-Key"),
	}
	s.noteMutation(writer, request, func(ctx context.Context, target noteTarget, name string) (app.NoteView, error) {
		if target.grant != "" {
			return s.service.PutWorkingNote(ctx, target.grant, name, input)
		}
		return s.service.PutProjectNote(ctx, target.project, name, input)
	})
}

type deleteNoteRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
	WithHistory      bool  `json:"withHistory,omitempty"`
}

func (s *Server) deleteNote(writer http.ResponseWriter, request *http.Request) {
	var body deleteNoteRequest
	if !decode(writer, request, &body) {
		return
	}
	input := app.NoteDeleteInput{
		ExpectedRevision: body.ExpectedRevision, WithHistory: body.WithHistory,
		IdempotencyKey: request.Header.Get("Idempotency-Key"),
	}
	s.noteMutation(writer, request, func(ctx context.Context, target noteTarget, name string) (app.NoteView, error) {
		if target.grant != "" {
			return s.service.DeleteWorkingNote(ctx, target.grant, name, input)
		}
		return s.service.DeleteProjectNote(ctx, target.project, name, input)
	})
}

type restoreNoteRequest struct {
	Revision         int64 `json:"revision"`
	ExpectedRevision int64 `json:"expectedRevision"`
}

func (s *Server) restoreNote(writer http.ResponseWriter, request *http.Request) {
	var body restoreNoteRequest
	if !decode(writer, request, &body) {
		return
	}
	input := app.NoteRestoreInput{
		Revision: body.Revision, ExpectedRevision: body.ExpectedRevision,
		IdempotencyKey: request.Header.Get("Idempotency-Key"),
	}
	s.noteMutation(writer, request, func(ctx context.Context, target noteTarget, name string) (app.NoteView, error) {
		if target.grant != "" {
			return s.service.RestoreWorkingNote(ctx, target.grant, name, input)
		}
		return s.service.RestoreProjectNote(ctx, target.project, name, input)
	})
}

func (s *Server) noteMutation(
	writer http.ResponseWriter, request *http.Request,
	call func(context.Context, noteTarget, string) (app.NoteView, error),
) {
	view, err := call(request.Context(), targetOf(request), request.PathValue("noteName"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, noteSummary(view))
}

func (s *Server) noteRevisions(writer http.ResponseWriter, request *http.Request) {
	target := targetOf(request)
	name := request.PathValue("noteName")
	var history app.NoteHistory
	var err error
	if target.grant != "" {
		history, err = s.service.ListWorkingNoteRevisions(request.Context(), target.grant, name)
	} else {
		history, err = s.service.ListProjectNoteRevisions(request.Context(), target.project, name)
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]noteRevisionJSON, 0, len(history.Revisions))
	for _, revision := range history.Revisions {
		items = append(items, noteRevision(revision.Revision, revision.Retained))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"note": noteSummary(history.Note), "items": items})
}

func (s *Server) shredNotes(writer http.ResponseWriter, request *http.Request) {
	target := targetOf(request)
	key := request.Header.Get("Idempotency-Key")
	var err error
	if target.grant != "" {
		err = s.service.ShredWorkingNotes(request.Context(), target.grant, key)
	} else {
		err = s.service.ShredProjectNotes(request.Context(), target.project, key)
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) promoteNote(writer http.ResponseWriter, request *http.Request) {
	view, err := s.service.PromoteWorkingNote(request.Context(),
		domain.CoordinationGrantID(request.PathValue("grantId")), request.PathValue("noteName"),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, noteProposal(view))
}

type notesDeliveryRequest struct {
	NotesDelivery   domain.NoteDelivery    `json:"notesDelivery"`
	ExpectedVersion domain.ResourceVersion `json:"expectedVersion"`
}

func (s *Server) setNotesDelivery(writer http.ResponseWriter, request *http.Request) {
	var body notesDeliveryRequest
	if !decode(writer, request, &body) {
		return
	}
	scope, err := s.service.SetProjectNotesDelivery(request.Context(),
		domain.ProjectID(request.PathValue("projectId")), body.NotesDelivery, body.ExpectedVersion,
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"notesDelivery": scope.Delivery, "settingsVersion": scope.ResourceVersion,
	})
}

func writeProposals(writer http.ResponseWriter, views []app.NoteProposalView) {
	items := make([]noteProposalJSON, 0, len(views))
	for _, view := range views {
		items = append(items, noteProposal(view))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) projectNoteProposals(writer http.ResponseWriter, request *http.Request) {
	pending := request.URL.Query().Get("pending") == "true"
	views, err := s.service.ListNoteProposals(request.Context(), domain.ProjectID(request.PathValue("projectId")), pending)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeProposals(writer, views)
}

func (s *Server) pendingNoteProposals(writer http.ResponseWriter, request *http.Request) {
	views, err := s.service.ListPendingNoteProposals(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeProposals(writer, views)
}

func (s *Server) getNoteProposal(writer http.ResponseWriter, request *http.Request) {
	review, err := s.service.GetNoteProposal(request.Context(), domain.NoteProposalID(request.PathValue("proposalId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	noStore(writer)
	writeJSON(writer, http.StatusOK, map[string]any{
		"proposal": noteProposal(review.NoteProposalView), "content": review.Content,
		"currentContent": review.CurrentContent, "currentLive": review.CurrentLive,
		"credentialShaped": review.CredentialShaped,
	})
}

type approveNoteRequest struct {
	Digest           string  `json:"digest"`
	ExpectedRevision int64   `json:"expectedRevision"`
	Content          *string `json:"content,omitempty"`
}

func (s *Server) approveNoteProposal(writer http.ResponseWriter, request *http.Request) {
	var body approveNoteRequest
	if !decode(writer, request, &body) {
		return
	}
	view, err := s.service.ApproveNoteProposal(request.Context(), domain.NoteProposalID(request.PathValue("proposalId")),
		app.ApproveNoteInput{
			Digest: body.Digest, ExpectedRevision: body.ExpectedRevision, Content: body.Content,
			IdempotencyKey: request.Header.Get("Idempotency-Key"),
		})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, noteProposal(view))
}

func (s *Server) rejectNoteProposal(writer http.ResponseWriter, request *http.Request) {
	view, err := s.service.RejectNoteProposal(request.Context(), domain.NoteProposalID(request.PathValue("proposalId")),
		request.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, noteProposal(view))
}

func (s *Server) threadNotes(writer http.ResponseWriter, request *http.Request) {
	view, err := s.service.GetThreadNotes(request.Context(), domain.ThreadID(request.PathValue("threadId")))
	if err != nil {
		writeError(writer, err)
		return
	}
	delivered := view.Delivered
	if delivered == nil {
		delivered = []domain.DeliveredNote{}
	}
	body := map[string]any{"choice": view.Choice, "delivered": delivered, "indexed": view.Indexed}
	if !view.DeliveredAt.IsZero() {
		body["deliveredAt"] = view.DeliveredAt.UTC()
	}
	writeJSON(writer, http.StatusOK, body)
}

// noteChoiceOf maps the new-Thread "Include Project notes" choice.
func noteChoiceOf(include *bool) domain.NoteChoice {
	switch {
	case include == nil:
		return domain.NoteChoiceDefault
	case *include:
		return domain.NoteChoiceInclude
	default:
		return domain.NoteChoiceExclude
	}
}
