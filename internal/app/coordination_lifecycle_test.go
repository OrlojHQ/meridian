package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// finishedSubagent spawns a subagent, starts it and ends its first turn so the
// lead holds a turn_ended report.
func (f *coordFixture) finishedSubagent(t *testing.T, mailbox *capsuleproto.Mailbox, name string) domain.ThreadLink {
	t.Helper()
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-" + name, Name: name, Harness: "mock", Prompt: "task " + name,
	})
	if !spawned.OK {
		t.Fatalf("spawn %s = %+v", name, spawned)
	}
	link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, link.ChildCapsuleID)
	f.service.CoordinationTick(f.ctx)
	thread, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.finishTurn(thread.CurrentRunID, name+"-answer", name+" is done")
	f.service.CoordinationTick(f.ctx)
	link, err = f.service.GetThreadLink(f.ctx, link.ID)
	if err != nil || link.LastReportStatus != domain.ReportTurnEnded {
		t.Fatalf("subagent %s report = %#v, %v", name, link, err)
	}
	return link
}

func (f *coordFixture) leadRollup(t *testing.T, lead domain.CapsuleID) app.ChildRollup {
	t.Helper()
	board, err := f.service.Board(f.ctx, app.BoardQuery{ProjectID: "project-coord"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range board.Items {
		if item.Capsule.ID == lead && item.Rollup != nil {
			return *item.Rollup
		}
	}
	t.Fatalf("lead %s has no rollup on the board", lead)
	return app.ChildRollup{}
}

func (f *coordFixture) cancels(run domain.RunID) int {
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	return f.runtime.cancels[run]
}

func TestFinishedSubagentIsPausedAndArchivedAfterItsPeriod(t *testing.T) {
	f := newCoordFixture(t)
	archiveAfter := time.Hour
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{ArchiveAfter: &archiveAfter})
	position, _ := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	if grant.ArchiveAfter != time.Hour {
		t.Fatalf("grant archiveAfter = %s", grant.ArchiveAfter)
	}
	link := f.finishedSubagent(t, mailbox, "one")
	thread, _ := f.service.GetThread(f.ctx, link.ChildThreadID)
	run := thread.CurrentRunID

	f.service.SweepArchivableSubagents(f.ctx)
	f.advance(59 * time.Minute)
	f.service.SweepArchivableSubagents(f.ctx)
	if current, _ := f.service.GetThreadLink(f.ctx, link.ID); !current.ArchivedAt.IsZero() || f.cancels(run) != 0 {
		t.Fatalf("subagent archived before its period: %#v", current)
	}

	f.advance(2 * time.Minute)
	f.service.SweepArchivableSubagents(f.ctx)
	archived, _ := f.service.GetThreadLink(f.ctx, link.ID)
	if archived.ArchivedAt.IsZero() || archived.State != domain.LinkActive || f.cancels(run) != 1 {
		t.Fatalf("archived link = %#v, cancels %d", archived, f.cancels(run))
	}
	f.service.CoordinationTick(f.ctx)
	f.advance(5 * time.Second)
	f.service.CoordinationTick(f.ctx)
	thread, _ = f.service.GetThread(f.ctx, link.ChildThreadID)
	if thread.State != domain.ThreadPaused {
		t.Fatalf("archived subagent Thread = %s", thread.State)
	}
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady {
		t.Fatalf("archive touched the Capsule: %s/%s", capsule.State, capsule.DesiredState)
	}
	// The cancelled session is not reported to the lead as the subagent ending.
	if current, _ := f.service.GetThreadLink(f.ctx, link.ID); current.ReportSeq != link.ReportSeq {
		t.Fatalf("archive produced report %d (%s)", current.ReportSeq, current.LastReportStatus)
	}
	if rollup := f.leadRollup(t, lead.Intent.CapsuleID); rollup.Archived != 1 || rollup.Done != 1 || rollup.Total != 1 {
		t.Fatalf("rollup = %+v", rollup)
	}
	send := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSend, Key: "wake", Link: string(link.ID), Prompt: "more"})
	if code(send) != coordproto.CodeUnavailable {
		t.Fatalf("send to archived subagent = %+v", send)
	}
	f.service.SweepArchivableSubagents(f.ctx)
	if f.cancels(run) != 1 {
		t.Fatalf("archived subagent was cancelled again")
	}

	resumed, err := f.service.ResumeThread(f.ctx, thread.ID, thread.ResourceVersion, "resume")
	if err != nil || resumed.Thread.State != domain.ThreadActive {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	if current, _ := f.service.GetThreadLink(f.ctx, link.ID); !current.ArchivedAt.IsZero() {
		t.Fatal("resume left the subagent archived")
	}
	if rollup := f.leadRollup(t, lead.Intent.CapsuleID); rollup.Archived != 0 {
		t.Fatalf("rollup after resume = %+v", rollup)
	}
}

func TestArchiveSkipsPromotedAndDisabledSubagents(t *testing.T) {
	f := newCoordFixture(t)
	archiveAfter := time.Hour
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{ArchiveAfter: &archiveAfter})
	promoted := f.finishedSubagent(t, mailbox, "kept")
	cancelled := f.finishedSubagent(t, mailbox, "dropped")
	capsule, _ := f.service.GetCapsule(f.ctx, promoted.ChildCapsuleID)
	if _, err := f.service.PromoteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion, false, "promote"); err != nil {
		t.Fatal(err)
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindCancel, Key: "cancel", Link: string(cancelled.ID),
	}); !response.OK {
		t.Fatalf("cancel = %+v", response)
	}
	for range 3 {
		f.service.CoordinationTick(f.ctx)
		f.advance(5 * time.Second)
	}
	// stopClosedChild asked for a pause; the reconciler would finish it.
	if capsule, _ := f.service.GetCapsule(f.ctx, cancelled.ChildCapsuleID); capsule.DesiredState != domain.IntentPaused {
		t.Fatalf("cancelled subagent desired state = %s", capsule.DesiredState)
	}
	setProjectThreadCapsuleState(t, f.ctx, f.store, cancelled.ChildCapsuleID, domain.CapsulePaused)
	f.advance(2 * time.Hour)
	f.service.SweepArchivableSubagents(f.ctx)
	if link, _ := f.service.GetThreadLink(f.ctx, promoted.ID); !link.ArchivedAt.IsZero() {
		t.Fatal("promoted subagent was archived")
	}
	if link, _ := f.service.GetThreadLink(f.ctx, cancelled.ID); link.ArchivedAt.IsZero() || link.CloseReason != domain.LinkCancelled {
		t.Fatalf("cancelled subagent = %#v", link)
	}
	if rollup := f.leadRollup(t, lead.Intent.CapsuleID); rollup.Archived != 1 || rollup.Total != 2 {
		t.Fatalf("rollup = %+v", rollup)
	}

	g := newCoordFixture(t)
	disabled := time.Duration(0)
	_, other := g.spawnLead(t, domain.CoordinationPolicy{ArchiveAfter: &disabled})
	kept := g.finishedSubagent(t, other, "kept")
	g.advance(31 * 24 * time.Hour)
	g.service.SweepArchivableSubagents(g.ctx)
	if link, _ := g.service.GetThreadLink(g.ctx, kept.ID); !link.ArchivedAt.IsZero() {
		t.Fatal("subagent archived with auto-archive disabled")
	}
}

func TestArchivePeriodIsBoundedAndPartOfTheReplay(t *testing.T) {
	f := newCoordFixture(t)
	for name, archiveAfter := range map[string]time.Duration{
		"short":      30 * time.Second,
		"long":       31 * 24 * time.Hour,
		"fractional": 90*time.Second + time.Millisecond,
		"negative":   -time.Hour,
	} {
		_, err := f.service.CreateProjectThread(f.ctx, app.CreateProjectThreadInput{
			ProjectID: "project-coord", Harness: "mock", Prompt: "lead", IdempotencyKey: "lead-" + name,
			Coordination: &domain.CoordinationPolicy{ArchiveAfter: &archiveAfter},
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	hour := time.Hour
	input := app.CreateProjectThreadInput{
		ProjectID: "project-coord", Harness: "mock", Prompt: "lead", IdempotencyKey: "lead",
		Coordination: &domain.CoordinationPolicy{ArchiveAfter: &hour},
	}
	if _, err := f.service.CreateProjectThread(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	input.Coordination = &domain.CoordinationPolicy{ArchiveAfter: &day}
	if _, err := f.service.CreateProjectThread(f.ctx, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replay with another archive period = %v", err)
	}
}

func TestPromoteKeepsTheLinkAndDetachClosesIt(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 4, MaxLiveCapsules: 2})
	first := f.finishedSubagent(t, mailbox, "first")
	second := f.finishedSubagent(t, mailbox, "second")
	spawnThird := coordproto.Request{Type: coordproto.KindSpawn, Key: "spawn-third", Name: "third", Harness: "mock", Prompt: "t"}
	if response := tool(t, mailbox, spawnThird); code(response) != coordproto.CodeCapacityExhausted {
		t.Fatalf("spawn over live limit = %+v", response)
	}

	capsule, _ := f.service.GetCapsule(f.ctx, first.ChildCapsuleID)
	if _, err := f.service.PromoteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion+1, false, "promote"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale promote = %v", err)
	}
	promoted, err := f.service.PromoteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion, false, "promote")
	if err != nil || promoted.Role != domain.CapsuleMain || promoted.PromotedAt.IsZero() ||
		promoted.LeadThreadID != lead.Intent.ThreadID || promoted.LinkID != first.ID {
		t.Fatalf("promote = %#v, %v", promoted, err)
	}
	if replay, err := f.service.PromoteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion, false, "promote"); err != nil ||
		replay.ResourceVersion != promoted.ResourceVersion {
		t.Fatalf("promote replay = %#v, %v", replay, err)
	}
	if _, err := f.service.PromoteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion, true, "promote"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("replay with detach = %v", err)
	}
	if _, err := f.service.PromoteCapsule(f.ctx, capsule.ID, promoted.ResourceVersion, false, "again"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("promote a main Capsule = %v", err)
	}
	if _, err := f.service.PromoteCapsule(f.ctx, lead.Intent.CapsuleID, 1, true, "lead"); err == nil {
		t.Fatal("lead Capsule was promoted")
	}
	link, _ := f.service.GetThreadLink(f.ctx, first.ID)
	if link.State != domain.LinkActive || link.PromotedAt.IsZero() {
		t.Fatalf("promoted link = %#v", link)
	}
	mains, err := f.service.ListCapsules(f.ctx, "project-coord", domain.CapsuleMain, 0, 100)
	if err != nil || len(mains.Items) != 2 {
		t.Fatalf("main Capsules = %d, %v", len(mains.Items), err)
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSend, Key: "follow-up", Link: string(first.ID), Prompt: "one more thing",
	}); !response.OK {
		t.Fatalf("send to promoted subagent = %+v", response)
	}
	if response := tool(t, mailbox, spawnThird); code(response) != coordproto.CodeCapacityExhausted {
		t.Fatalf("a promoted, linked subagent left the live count: %+v", response)
	}

	detached, err := f.service.PromoteCapsule(f.ctx, capsule.ID, promoted.ResourceVersion, true, "detach")
	if err != nil || detached.Role != domain.CapsuleMain || detached.LeadThreadID != "" || detached.LinkID != "" ||
		!detached.PromotedAt.Equal(promoted.PromotedAt) {
		t.Fatalf("detach = %#v, %v", detached, err)
	}
	link, _ = f.service.GetThreadLink(f.ctx, first.ID)
	if link.State != domain.LinkClosed || link.CloseReason != domain.LinkDetached || link.ReportSeq != first.ReportSeq {
		t.Fatalf("detached link = %#v", link)
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSend, Key: "after-detach", Link: string(first.ID), Prompt: "x",
	}); code(response) != coordproto.CodeLinkClosed {
		t.Fatalf("send after detach = %+v", response)
	}
	// The mailbox caches the earlier refusal under its key, as a retry would see.
	spawnThird.Key = "spawn-third-again"
	if response := tool(t, mailbox, spawnThird); !response.OK {
		t.Fatalf("spawn after detach = %+v", response)
	}

	// A lead-side stop never reaches a promoted subagent.
	other, _ := f.service.GetCapsule(f.ctx, second.ChildCapsuleID)
	if _, err := f.service.PromoteCapsule(f.ctx, other.ID, other.ResourceVersion, false, "promote-second"); err != nil {
		t.Fatal(err)
	}
	secondThread, _ := f.service.GetThread(f.ctx, second.ChildThreadID)
	position, _ := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	if _, err := f.service.RevokeCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion, true, "revoke"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.service.CoordinationTick(f.ctx)
		f.advance(5 * time.Second)
	}
	if f.cancels(secondThread.CurrentRunID) != 0 {
		t.Fatal("revoke cancelled a promoted subagent")
	}
	if current, _ := f.service.GetCapsule(f.ctx, other.ID); current.DesiredState != domain.IntentReady {
		t.Fatalf("revoke paused a promoted subagent: %s", current.DesiredState)
	}
}

func TestResumedSubagentIsArchivedAgainOnceIdle(t *testing.T) {
	f := newCoordFixture(t)
	archiveAfter := time.Minute
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{ArchiveAfter: &archiveAfter})
	link := f.finishedSubagent(t, mailbox, "one")
	f.advance(2 * time.Minute)
	f.service.SweepArchivableSubagents(f.ctx)
	for range 2 {
		f.service.CoordinationTick(f.ctx)
		f.advance(5 * time.Second)
	}
	thread, _ := f.service.GetThread(f.ctx, link.ChildThreadID)
	resumed, err := f.service.ResumeThread(f.ctx, thread.ID, thread.ResourceVersion, "resume")
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, link.ChildCapsuleID)
	f.runtime.finishTurn(resumed.Thread.CurrentRunID, "resumed", "back")
	f.service.CoordinationTick(f.ctx)
	f.advance(2 * time.Minute)
	f.service.SweepArchivableSubagents(f.ctx)
	if current, _ := f.service.GetThreadLink(f.ctx, link.ID); current.ArchivedAt.IsZero() {
		t.Fatalf("resumed subagent was not archived again: %#v", current)
	}
}
