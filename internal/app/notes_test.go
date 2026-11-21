package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

const notesProject domain.ProjectID = "project-coord"

// notesHome gives a test mailbox a working-notes directory and lets the
// test's temporary directory be removed afterwards: notes are written into
// 0555 directories.
func notesHome(t *testing.T, mailbox *capsuleproto.Mailbox) string {
	t.Helper()
	base := filepath.Join(t.TempDir(), ".meridian")
	mailbox.SetNotesBase(base)
	t.Cleanup(func() {
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	return base
}

func putNote(t *testing.T, f *coordFixture, name, content string, expected int64) app.NoteView {
	t.Helper()
	view, err := f.service.PutProjectNote(f.ctx, notesProject, name, app.NoteWriteInput{
		Content: content, ExpectedRevision: expected, IdempotencyKey: "put-" + name + "-" + strconv.FormatInt(expected, 10),
	})
	if err != nil {
		t.Fatalf("put %s = %v", name, err)
	}
	return view
}

func TestProjectNotesAreOperatorWrittenVersionedAndBounded(t *testing.T) {
	f := newCoordFixture(t)
	created := putNote(t, f, "tests", "run make test\r\n", 0)
	if created.Note.HeadSeq != 1 || created.Note.Size != len("run make test\n") || created.AgentProposed {
		t.Fatalf("created = %+v", created)
	}
	_, err := f.service.PutProjectNote(f.ctx, notesProject, "tests", app.NoteWriteInput{
		Content: "other", ExpectedRevision: 0, IdempotencyKey: "second-create",
	})
	var conflict *app.NoteConflictError
	if !errors.As(err, &conflict) || conflict.Current != 1 || !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second create = %v", err)
	}
	// A replay of the first write changes nothing.
	if replay := putNote(t, f, "tests", "run make test\r\n", 0); replay.Note.HeadSeq != 1 {
		t.Fatalf("replay = %+v", replay)
	}
	putNote(t, f, "tests", "run make test and make lint\n", 1)
	head, err := f.service.GetProjectNote(f.ctx, notesProject, "tests", 0)
	if err != nil || head.Content != "run make test and make lint\n" || head.Revision.Seq != 2 {
		t.Fatalf("head = %+v, %v", head, err)
	}
	first, err := f.service.GetProjectNote(f.ctx, notesProject, "tests", 1)
	if err != nil || first.Content != "run make test\n" {
		t.Fatalf("revision 1 = %+v, %v", first, err)
	}
	for name, input := range map[string]app.NoteWriteInput{
		"a NUL":            {Content: "a\x00b"},
		"an escape":        {Content: "\x1b[31mred"},
		"a bad name":       {Content: "x"},
		"too large a note": {Content: strings.Repeat("a", domain.MaxNoteBytes+1)},
	} {
		input.IdempotencyKey = "bad-" + strings.ReplaceAll(name, " ", "-")
		noteName := "bad"
		if name == "a bad name" {
			noteName = "-bad"
		}
		if _, err := f.service.PutProjectNote(f.ctx, notesProject, noteName, input); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// 128 KiB in total: three full notes fit beside the small one, a fourth
	// does not, and nothing is cut to fit.
	full := strings.Repeat("a", domain.MaxNoteBytes)
	for index := range 3 {
		putNote(t, f, "full-"+strconv.Itoa(index), full, 0)
	}
	if _, err := f.service.PutProjectNote(f.ctx, notesProject, "full-3", app.NoteWriteInput{
		Content: full, IdempotencyKey: "full-3",
	}); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("a note past the total = %v", err)
	}
	for index := range domain.MaxLiveNotes - 4 {
		putNote(t, f, "n"+strconv.Itoa(index), "x", 0)
	}
	if _, err := f.service.PutProjectNote(f.ctx, notesProject, "one-too-many", app.NoteWriteInput{
		Content: "x", IdempotencyKey: "one-too-many",
	}); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("the 33rd note = %v", err)
	}

	// History, delete and restore.
	history, err := f.service.ListProjectNoteRevisions(f.ctx, notesProject, "tests")
	if err != nil || len(history.Revisions) != 2 || history.Revisions[0].Revision.Seq != 2 || !history.Revisions[1].Retained {
		t.Fatalf("history = %+v, %v", history, err)
	}
	restored, err := f.service.RestoreProjectNote(f.ctx, notesProject, "tests", app.NoteRestoreInput{
		Revision: 1, ExpectedRevision: 2, IdempotencyKey: "restore-1",
	})
	if err != nil || restored.Note.HeadSeq != 3 || restored.Note.HeadKind != domain.NoteRevisionRestore {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	deleted, err := f.service.DeleteProjectNote(f.ctx, notesProject, "tests", app.NoteDeleteInput{
		ExpectedRevision: 3, IdempotencyKey: "delete-tests",
	})
	if err != nil || deleted.Note.Live || deleted.Note.HeadSeq != 4 {
		t.Fatalf("delete = %+v, %v", deleted, err)
	}
	if _, err := f.service.GetProjectNote(f.ctx, notesProject, "tests", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a deleted note's head = %v", err)
	}
	if _, err := f.service.RestoreProjectNote(f.ctx, notesProject, "tests", app.NoteRestoreInput{
		Revision: 4, ExpectedRevision: 4, IdempotencyKey: "restore-delete",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("restoring a deletion = %v", err)
	}
	if _, err := f.service.DeleteProjectNote(f.ctx, notesProject, "tests", app.NoteDeleteInput{
		ExpectedRevision: 4, WithHistory: true, IdempotencyKey: "purge-tests",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.GetProjectNote(f.ctx, notesProject, "tests", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a purged revision = %v", err)
	}
	if history, _ := f.service.ListProjectNoteRevisions(f.ctx, notesProject, "tests"); len(history.Revisions) != 4 ||
		history.Revisions[3].Retained {
		t.Fatalf("purged history = %+v", history)
	}
}

func TestOnlyTheOwnerSeesNotes(t *testing.T) {
	f := newCoordFixture(t)
	putNote(t, f, "tests", "run make test\n", 0)
	capsulePrincipal, _ := principal.Capsule(domain.BootstrapOwnerID, "capsule-x")
	other, _ := principal.User("usr_other")
	for name, ctx := range map[string]context.Context{
		"the system":            asSystem(f.ctx),
		"the break-glass admin": principal.With(f.ctx, principal.BreakGlass()),
		"a Capsule":             principal.With(f.ctx, capsulePrincipal),
		"another member":        principal.With(f.ctx, other),
	} {
		if _, err := f.service.ListProjectNotes(ctx, notesProject); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s listed notes: %v", name, err)
		}
		if _, err := f.service.GetProjectNote(ctx, notesProject, "tests", 0); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s read a note: %v", name, err)
		}
		if _, err := f.service.PutProjectNote(ctx, notesProject, "tests", app.NoteWriteInput{
			Content: "x", ExpectedRevision: 1, IdempotencyKey: "intruder",
		}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s wrote a note: %v", name, err)
		}
	}
}

func whoami(t *testing.T, mailbox *capsuleproto.Mailbox) []coordproto.NoteProposalRef {
	t.Helper()
	response := tool(t, mailbox, coordproto.Request{Type: coordproto.KindWhoAmI})
	if !response.OK || response.Self == nil {
		t.Fatalf("whoami = %+v", response)
	}
	return response.Self.NoteProposals
}

func TestAgentProposalsAreReviewedOneExactChangeAtATime(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	proposed := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindProposeNote, Key: "p1", Name: "tests", Content: "token ghp_abcdefghijklmnop is in .env",
	})
	if !proposed.OK || proposed.Proposal == nil || proposed.Proposal.State != "pending" {
		t.Fatalf("propose = %+v", proposed)
	}
	if encoded, _ := json.Marshal(proposed); strings.Contains(string(encoded), "ghp_") {
		t.Fatalf("the proposal answer carried its text: %s", encoded)
	}
	if refs := whoami(t, mailbox); len(refs) != 1 || refs[0].State != "pending" || refs[0].Name != "tests" {
		t.Fatalf("whoami proposals = %+v", refs)
	}
	id := domain.NoteProposalID(proposed.Proposal.ID)
	review, err := f.service.GetNoteProposal(f.ctx, id)
	if err != nil || !review.CredentialShaped || review.Content != "token ghp_abcdefghijklmnop is in .env" ||
		review.Proposal.Source.ThreadID == "" {
		t.Fatalf("review = %+v, %v", review, err)
	}
	if _, err := f.service.ApproveNoteProposal(f.ctx, id, app.ApproveNoteInput{
		Digest: "sha256:other", ExpectedRevision: 0, IdempotencyKey: "approve-wrong",
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("approve with another digest = %v", err)
	}
	// The operator writes the note first: the proposal is stale and cannot
	// be approved as it is.
	putNote(t, f, "tests", "run make test\n", 0)
	if _, err := f.service.ApproveNoteProposal(f.ctx, id, app.ApproveNoteInput{
		Digest: review.Proposal.Digest, ExpectedRevision: 1, IdempotencyKey: "approve-stale",
	}); err == nil {
		t.Fatal("a stale proposal was approved")
	}
	if review, _ := f.service.GetNoteProposal(f.ctx, id); review.State != domain.NoteProposalStale {
		t.Fatalf("state after the note moved = %s", review.State)
	}

	second := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindProposeNote, Key: "p2", Name: "tests", BaseRevision: 1, Content: "run make test -race\n",
	})
	if !second.OK {
		t.Fatalf("second propose = %+v", second)
	}
	secondReview, _ := f.service.GetNoteProposal(f.ctx, domain.NoteProposalID(second.Proposal.ID))
	edited := "run make test -race -count=1\n"
	if _, err := f.service.ApproveNoteProposal(f.ctx, secondReview.Proposal.ID, app.ApproveNoteInput{
		Digest: secondReview.Proposal.Digest, ExpectedRevision: 0, IdempotencyKey: "approve-old-head",
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("approve against an old head = %v", err)
	}
	approved, err := f.service.ApproveNoteProposal(f.ctx, secondReview.Proposal.ID, app.ApproveNoteInput{
		Digest: secondReview.Proposal.Digest, ExpectedRevision: 1, Content: &edited, IdempotencyKey: "approve-2",
	})
	if err != nil || approved.State != domain.NoteProposalApprovedWithEdits || approved.CurrentRevision != 2 {
		t.Fatalf("approve with edits = %+v, %v", approved, err)
	}
	head, _ := f.service.GetProjectNote(f.ctx, notesProject, "tests", 0)
	if head.Content != edited || head.Revision.Kind != domain.NoteRevisionApprovedWithEdits ||
		head.Revision.ProposalID != secondReview.Proposal.ID || !head.Note.AgentProposed ||
		head.Revision.Source.CapsuleID == "" {
		t.Fatalf("approved head = %+v", head)
	}

	third := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindProposeNote, Key: "p3", Name: "branches", Content: "use feature/ names",
	})
	if _, err := f.service.RejectNoteProposal(f.ctx, domain.NoteProposalID(third.Proposal.ID), "reject-3"); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, ref := range whoami(t, mailbox) {
		states[ref.ID] = ref.State
	}
	if states[proposed.Proposal.ID] != "stale" || states[second.Proposal.ID] != "approved_with_edits" ||
		states[third.Proposal.ID] != "rejected" {
		t.Fatalf("whoami states = %v", states)
	}

	// Four pending proposals per Thread.
	for index := range domain.MaxPendingProposalsPerThread {
		if response := tool(t, mailbox, coordproto.Request{
			Type: coordproto.KindProposeNote, Key: "bulk-" + strconv.Itoa(index), Name: "bulk-" + strconv.Itoa(index), Content: "x",
		}); !response.OK {
			t.Fatalf("bulk %d = %+v", index, response)
		}
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindProposeNote, Key: "bulk-over", Name: "bulk-over", Content: "x",
	}); code(response) != coordproto.CodeLimitExceeded {
		t.Fatalf("a fifth pending proposal = %+v", response)
	}
	// Seven days later they expire, and an expired proposal cannot be approved.
	f.advance(domain.NoteProposalExpiry + time.Minute)
	f.service.ExpireNoteProposals(asSystem(f.ctx))
	pending, err := f.service.ListNoteProposals(f.ctx, notesProject, true)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after expiry = %+v, %v", pending, err)
	}
	all, _ := f.service.ListNoteProposals(f.ctx, notesProject, false)
	for _, view := range all {
		if strings.HasPrefix(view.Proposal.Name, "bulk-") && view.State != domain.NoteProposalExpired {
			t.Fatalf("%s = %s", view.Proposal.Name, view.State)
		}
	}
}

func TestWorkingNotesAreSharedInsideOneTree(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{UnattendedLimit: 10 * time.Minute})
	notesHome(t, mailbox)
	link := f.spawnSubagent(t, mailbox, "one")
	child, err := f.service.GetCapsule(f.ctx, link.ChildCapsuleID)
	if err != nil {
		t.Fatal(err)
	}
	subMailbox := f.runtime.mailbox(child.ProviderResourceID)
	subHome := notesHome(t, subMailbox)
	f.service.CoordinationTick(asSystem(f.ctx))

	written := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindWriteNote, Key: "w1", Name: "findings", Content: "the parser is slow\n",
	})
	if !written.OK || written.Note.Revision != 1 {
		t.Fatalf("lead write = %+v", written)
	}
	conflict := tool(t, subMailbox, coordproto.Request{
		Type: coordproto.KindWriteNote, Key: "w2", Name: "findings", Content: "mine\n",
	})
	if code(conflict) != coordproto.CodeConflict || conflict.Note == nil || conflict.Note.Revision != 1 ||
		conflict.Note.WrittenBy != "lead" || len(conflict.NoteFiles) != 0 {
		t.Fatalf("stale write = %+v", conflict)
	}
	refreshed, err := os.ReadFile(filepath.Join(subHome, coordproto.WorkingNotesDirName, "findings.md"))
	if err != nil || string(refreshed) != "the parser is slow\n" {
		t.Fatalf("refreshed file = %q, %v", refreshed, err)
	}
	if response := tool(t, subMailbox, coordproto.Request{
		Type: coordproto.KindWriteNote, Key: "w3", Name: "findings", BaseRevision: 1, Content: "the parser is slow; see bench\n",
	}); !response.OK || response.Note.Revision != 2 {
		t.Fatalf("subagent write = %+v", response)
	}
	synced := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSyncNotes})
	if !synced.OK || len(synced.Notes) != 1 || !synced.Notes[0].Changed || synced.Notes[0].Revision != 2 ||
		len(synced.NoteFiles) != 0 {
		t.Fatalf("sync = %+v", synced)
	}
	grant := f.leadGrant(t, lead)
	list, err := f.service.ListWorkingNotes(f.ctx, grant.ID)
	if err != nil || len(list.Notes) != 1 || list.Notes[0].Note.HeadLinkName != "one" || !list.Notes[0].AgentProposed {
		t.Fatalf("operator list = %+v, %v", list, err)
	}
	// Working notes never reach the Project's notes.
	if projectNotes, _ := f.service.ListProjectNotes(f.ctx, notesProject); len(projectNotes.Notes) != 0 {
		t.Fatalf("a working note leaked into Project notes: %+v", projectNotes)
	}

	// The lead's wake names the subagent's write; the lead's own is left out.
	section, mark := app.NotesWakeSectionForTest(f.service, asSystem(f.ctx), grant)
	if !strings.Contains(section, "findings.md, written by one") || mark != 2 || strings.Contains(section, "lead") {
		t.Fatalf("wake section = %q, %d", section, mark)
	}

	// Writes are rate-limited per Capsule, on top of the mailbox's own rate.
	tick := time.Now()
	mailbox.SetClock(func() time.Time { tick = tick.Add(3 * time.Second); return tick })
	base := int64(2)
	for index := range domain.WorkingNoteWritesPerCapsule - 1 {
		response := tool(t, mailbox, coordproto.Request{
			Type: coordproto.KindWriteNote, Key: "rate-" + strconv.Itoa(index), Name: "findings",
			BaseRevision: base, Content: "v" + strconv.Itoa(index),
		})
		if !response.OK {
			t.Fatalf("write %d = %+v", index, response)
		}
		base = response.Note.Revision
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindWriteNote, Key: "rate-over", Name: "findings", BaseRevision: base, Content: "over",
	}); code(response) != coordproto.CodeRateLimited {
		t.Fatalf("the 31st write in an hour = %+v", response)
	}

	// Lapsed: writes are refused, reads answer.
	f.advance(11 * time.Minute)
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindWriteNote, Key: "lapsed", Name: "other", Content: "x",
	}); code(response) != coordproto.CodeCoordinationLapsed {
		t.Fatalf("write while lapsed = %+v", response)
	}
	if response := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSyncNotes}); !response.OK {
		t.Fatalf("sync while lapsed = %+v", response)
	}

	// Revoked: no Capsule reads or writes; the operator still reads and
	// promotes, but cannot edit.
	grant = f.leadGrant(t, lead)
	if _, err := f.service.RevokeCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion, false, "revoke"); err != nil {
		t.Fatal(err)
	}
	if response := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSyncNotes}); response.OK {
		t.Fatalf("sync after revocation = %+v", response)
	}
	if _, err := f.service.GetWorkingNote(f.ctx, grant.ID, "findings", 0); err != nil {
		t.Fatalf("operator read after revocation = %v", err)
	}
	if _, err := f.service.PutWorkingNote(f.ctx, grant.ID, "findings", app.NoteWriteInput{
		Content: "x", ExpectedRevision: base, IdempotencyKey: "edit-revoked",
	}); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("operator edit after revocation = %v", err)
	}
	promoted, err := f.service.PromoteWorkingNote(f.ctx, grant.ID, "findings", "promote")
	if err != nil || promoted.State != domain.NoteProposalPending || promoted.Proposal.Origin != domain.NoteProposalFromPromotion ||
		promoted.Proposal.Source.WorkingNoteSeq != base || promoted.Proposal.Source.WorkingCapsuleID == "" {
		t.Fatalf("promote = %+v, %v", promoted, err)
	}
	if projectNotes, _ := f.service.ListProjectNotes(f.ctx, notesProject); len(projectNotes.Notes) != 0 {
		t.Fatal("a promotion wrote a Project note before review")
	}
	if err := f.service.ShredWorkingNotes(f.ctx, grant.ID, "shred-tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.GetWorkingNote(f.ctx, grant.ID, "findings", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a shredded working note = %v", err)
	}
	if review, _ := f.service.GetNoteProposal(f.ctx, promoted.Proposal.ID); review.State != domain.NoteProposalRejected {
		t.Fatalf("a promotion of shredded text = %s", review.State)
	}
}

func startNotes(t *testing.T, f *coordFixture, run domain.RunID) (*coordproto.NotesDelivery, *adapterproto.NotesIndex) {
	t.Helper()
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	return f.runtime.notes[run], adapterproto.ReadNotesIndex(f.runtime.starts[run].Metadata)
}

func plainThread(t *testing.T, f *coordFixture, key string, choice domain.NoteChoice) domain.RunID {
	t.Helper()
	created, err := f.service.CreateProjectThread(f.ctx, app.CreateProjectThreadInput{
		ProjectID: notesProject, Harness: "mock", Prompt: "do work", IdempotencyKey: key, NoteChoice: choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, created.Intent.CapsuleID)
	return created.Intent.RunID
}

func TestNotesDeliveryFollowsTheProjectSetting(t *testing.T) {
	f := newCoordFixture(t)
	putNote(t, f, "tests", "run make test\n", 0)

	run := plainThread(t, f, "plain-default", "")
	delivery, index := startNotes(t, f, run)
	if delivery == nil || !delivery.DeliverProject || delivery.DeliverWorking || len(delivery.Project) != 1 ||
		delivery.Project[0].Content != "run make test\n" {
		t.Fatalf("all_threads delivery = %+v", delivery)
	}
	if index == nil || len(index.Notes) != 1 || index.Notes[0].Name != "tests" || index.Notes[0].Revision != 1 {
		t.Fatalf("index = %+v", index)
	}
	if encoded := string(f.runtime.starts[run].Metadata); strings.Contains(encoded, "make test") {
		t.Fatalf("the index carried content: %s", encoded)
	}

	list, _ := f.service.ListProjectNotes(f.ctx, notesProject)
	scope, err := f.service.SetProjectNotesDelivery(f.ctx, notesProject, domain.NoteDeliveryLeadsOnly,
		list.Scope.ResourceVersion, "leads-only")
	if err != nil {
		t.Fatal(err)
	}
	if delivery, _ := startNotes(t, f, plainThread(t, f, "plain-leads-only", "")); delivery.DeliverProject {
		t.Fatal("leads_only delivered to a plain Thread")
	}
	if delivery, _ := startNotes(t, f, plainThread(t, f, "plain-include", domain.NoteChoiceInclude)); !delivery.DeliverProject {
		t.Fatal("a Thread that chose Project notes did not get them")
	}
	lead, _ := f.spawnLead(t, domain.CoordinationPolicy{})
	if delivery, _ := startNotes(t, f, lead.Intent.RunID); !delivery.DeliverProject || !delivery.DeliverWorking {
		t.Fatalf("leads_only lead delivery = %+v", delivery)
	}

	if _, err := f.service.SetProjectNotesDelivery(f.ctx, notesProject, domain.NoteDeliveryOff,
		scope.ResourceVersion, "off"); err != nil {
		t.Fatal(err)
	}
	if delivery, _ := startNotes(t, f, plainThread(t, f, "plain-off", domain.NoteChoiceInclude)); delivery.DeliverProject {
		t.Fatal("off still delivered to a Thread that chose notes")
	}
	notes, err := f.service.GetThreadNotes(f.ctx, lead.Intent.ThreadID)
	if err != nil || len(notes.Delivered) != 1 || notes.Delivered[0].Name != "tests" || notes.Delivered[0].Tier != domain.NoteTierProject {
		t.Fatalf("recorded set = %+v, %v", notes, err)
	}
}

func TestShreddingProjectNotesLeavesNothingReadable(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	putNote(t, f, "tests", "run make test\n", 0)
	proposed := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindProposeNote, Key: "p", Name: "tests", BaseRevision: 1, Content: "poisoned",
	})
	if err := f.service.ShredProjectNotes(f.ctx, notesProject, "shred"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.GetProjectNote(f.ctx, notesProject, "tests", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a shredded revision = %v", err)
	}
	list, _ := f.service.ListProjectNotes(f.ctx, notesProject)
	if list.LiveCount != 0 || len(list.Notes) != 1 || list.Pending != 0 {
		t.Fatalf("after shred = %+v", list)
	}
	if review, _ := f.service.GetNoteProposal(f.ctx, domain.NoteProposalID(proposed.Proposal.ID)); review.State != domain.NoteProposalRejected ||
		review.Proposal.Reason != domain.NoteProposalReasonShredded {
		t.Fatalf("a pending proposal after shred = %+v", review.NoteProposalView)
	}
	// A new key is made with the next note.
	putNote(t, f, "tests", "fresh\n", 1)
	if head, err := f.service.GetProjectNote(f.ctx, notesProject, "tests", 0); err != nil || head.Content != "fresh\n" {
		t.Fatalf("after shred = %+v, %v", head, err)
	}
}

func TestTransferRewrapsNotesForTheNewOwnerAndTurnsDeliveryOff(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	key, err := transcripts.NewInstallationKey("transfer-notes", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	s.ConfigureThreads(&structuredRuntime{}, key)
	project, err := s.CreateProject(f.bob, "bob", "bob-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutProjectNote(f.bob, project.ID, "prefs", app.NoteWriteInput{
		Content: "short pull requests\n", IdempotencyKey: "bob-note",
	}); err != nil {
		t.Fatal(err)
	}
	breakGlass := principal.With(context.Background(), principal.BreakGlass())
	// The admin who transfers cannot read notes.
	if _, err := s.GetProjectNote(breakGlass, project.ID, "prefs", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("break-glass read a note: %v", err)
	}
	disableMember(t, f, "usr_bob")
	if err := s.TransferDepartedMember(breakGlass, "usr_bob", principal.BootstrapOwner); err != nil {
		t.Fatal(err)
	}
	head, err := s.GetProjectNote(f.alice, project.ID, "prefs", 0)
	if err != nil || head.Content != "short pull requests\n" {
		t.Fatalf("the new owner's read = %+v, %v", head, err)
	}
	list, err := s.ListProjectNotes(f.alice, project.ID)
	if err != nil || list.Scope.Delivery != domain.NoteDeliveryOff {
		t.Fatalf("delivery after transfer = %+v, %v", list.Scope, err)
	}
	if _, err := s.GetProjectNote(f.bob, project.ID, "prefs", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the departed member still reads the note: %v", err)
	}
}

func TestShreddingALeadShredsItsWorkingNotes(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	notesHome(t, mailbox)
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindWriteNote, Key: "w", Name: "findings", Content: "found it\n",
	}); !response.OK {
		t.Fatalf("write = %+v", response)
	}
	grant := f.leadGrant(t, lead)
	f.runtime.endSession(lead.Intent.RunID)
	if err := f.service.SyncThread(asSystem(f.ctx), lead.Intent.ThreadID); err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.DeleteThread(f.ctx, thread.ID, thread.ResourceVersion, "shred-lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.GetWorkingNote(f.ctx, grant.ID, "findings", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a shredded lead's working note = %v", err)
	}
}
