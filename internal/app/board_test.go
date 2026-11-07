package app_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// spawnHelper spawns a named helper through the lead's mailbox and makes it
// Ready.
func (f *coordFixture) spawnHelper(t *testing.T, mailbox *capsuleproto.Mailbox, name string) domain.ThreadLink {
	t.Helper()
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-" + name, Name: name, Harness: "mock", Prompt: "task for " + name,
	})
	if !spawned.OK {
		t.Fatalf("spawn %s = %+v", name, spawned)
	}
	link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, link.ChildCapsuleID)
	return link
}

func (r *coordRuntime) askPermission(run domain.RunID, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := uint64(len(r.events[run]) + 1)
	r.events[run] = append(r.events[run], ports.RuntimeStructuredEvent{Sequence: next, Frame: adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest, ID: id,
		Permission: &adapterproto.Permission{Kind: "mock", Summary: "allow?", Options: []string{"allow", "deny"}},
	}})
}

func (f *coordFixture) board(t *testing.T, expand app.BoardExpand) app.Board {
	t.Helper()
	board, err := f.service.Board(f.ctx, app.BoardQuery{ProjectID: "project-coord", Expand: expand})
	if err != nil {
		t.Fatal(err)
	}
	return board
}

func boardRow(board app.Board, capsule domain.CapsuleID) *app.BoardItem {
	for index := range board.Items {
		if board.Items[index].Capsule.ID == capsule {
			return &board.Items[index]
		}
	}
	return nil
}

func TestBoardFoldsHelpersAndSurfacesOnesThatNeedYou(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 2})
	one := f.spawnHelper(t, mailbox, "one")
	two := f.spawnHelper(t, mailbox, "two")

	board := f.board(t, "")
	if len(board.Items) != 1 || board.Truncated {
		t.Fatalf("default board = %d rows, truncated %v; want the lead only", len(board.Items), board.Truncated)
	}
	row := board.Items[0]
	if row.Capsule.ID != lead.Intent.CapsuleID || row.Status != app.BoardWorking || row.Thread == nil ||
		row.Rollup == nil || *row.Rollup != (app.ChildRollup{Total: 2, Working: 2}) {
		t.Fatalf("lead row = %+v rollup %+v", row, row.Rollup)
	}

	oneThread, err := f.service.GetThread(f.ctx, one.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.askPermission(oneThread.CurrentRunID, "permission-1")
	if _, err := f.service.ThreadBlocks(f.ctx, one.ChildThreadID, 0, 100); err != nil {
		t.Fatal(err)
	}

	board = f.board(t, app.BoardExpandAttention)
	if len(board.Items) != 2 {
		t.Fatalf("attention board = %d rows, want lead plus the surfaced helper", len(board.Items))
	}
	surfaced := board.Items[0]
	if surfaced.Capsule.ID != one.ChildCapsuleID || surfaced.Status != app.BoardNeedsYou ||
		surfaced.NeedsYou != app.BoardAttentionPermission || !surfaced.Surfaced ||
		surfaced.Link == nil || surfaced.Link.Name != "one" {
		t.Fatalf("surfaced row = %+v", surfaced)
	}
	if rollup := board.Items[1].Rollup; rollup == nil || *rollup != (app.ChildRollup{Total: 2, Working: 1, NeedsYou: 1}) {
		t.Fatalf("lead rollup = %+v", rollup)
	}

	if none := f.board(t, app.BoardExpandNone); len(none.Items) != 1 || none.Items[0].Capsule.ID != lead.Intent.CapsuleID {
		t.Fatalf("expand=none rows = %d", len(none.Items))
	}
	all := f.board(t, app.BoardExpandAll)
	if len(all.Items) != 3 {
		t.Fatalf("expand=all rows = %d", len(all.Items))
	}
	if quiet := boardRow(all, two.ChildCapsuleID); quiet == nil || quiet.Surfaced || quiet.Status != app.BoardWorking {
		t.Fatalf("quiet helper row = %+v", quiet)
	}

	if _, err := f.service.Board(f.ctx, app.BoardQuery{Expand: "everything"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad expand = %v", err)
	}
	if _, err := f.service.Board(f.ctx, app.BoardQuery{ProjectID: "missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing project = %v", err)
	}
}

func TestBoardShowsHelpersWhoseLeadIsGone(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	helper := f.spawnHelper(t, mailbox, "orphan")
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(f.ctx, lead.Intent.CapsuleID)
		if err != nil {
			return err
		}
		capsule.State, capsule.DesiredState = domain.CapsuleDeleted, domain.IntentDeleted
		expected := capsule.ResourceVersion
		capsule.UpdatedAt = f.clock.Now()
		capsule.ResourceVersion++
		return tx.UpdateCapsule(f.ctx, capsule, expected)
	}); err != nil {
		t.Fatal(err)
	}
	board := f.board(t, app.BoardExpandNone)
	if len(board.Items) != 1 || board.Items[0].Capsule.ID != helper.ChildCapsuleID || board.Items[0].Surfaced {
		t.Fatalf("rows = %+v", board.Items)
	}
}

func TestBoardStatusFollowsTheTurnAndTheRun(t *testing.T) {
	f := newCoordFixture(t)
	lead, _ := f.spawnLead(t, domain.CoordinationPolicy{})
	f.runtime.finishTurn(lead.Intent.RunID, "lead-1", "done for now")
	if _, err := f.service.ThreadBlocks(f.ctx, lead.Intent.ThreadID, 0, 100); err != nil {
		t.Fatal(err)
	}
	if row := f.board(t, "").Items[0]; row.Status != app.BoardIdle {
		t.Fatalf("idle lead status = %s", row.Status)
	}
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		run, err := tx.GetRun(f.ctx, lead.Intent.RunID)
		if err != nil {
			return err
		}
		run.State, run.FinishedAt, run.UpdatedAt = domain.RunFailed, f.clock.Now(), f.clock.Now()
		expected := run.ResourceVersion
		run.ResourceVersion++
		return tx.UpdateRun(f.ctx, run, expected)
	}); err != nil {
		t.Fatal(err)
	}
	if row := f.board(t, "").Items[0]; row.Status != app.BoardNeedsYou || row.NeedsYou != app.BoardAttentionFailed {
		t.Fatalf("failed run row = %+v", row)
	}
}

func TestOperatorMessageToHelperIsNoticedOnTheLeadsNextWake(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.spawnHelper(t, mailbox, "one")
	child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	secret := "operator steering text stays out of the lead"
	if _, err := f.service.SendThreadMessage(f.ctx, child.ID, 0, secret, "op-1"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("send without a version = %v", err)
	}
	if _, err := f.service.SendThreadMessage(f.ctx, child.ID, child.ResourceVersion, secret, "op-1"); err != nil {
		t.Fatal(err)
	}
	counted, err := f.service.GetThreadLink(f.ctx, link.ID)
	if err != nil || counted.OperatorMessages != 1 || counted.MessagesForwarded != 0 {
		t.Fatalf("link after operator message = %+v, %v", counted, err)
	}

	f.runtime.finishTurn(lead.Intent.RunID, "lead-1", "waiting")
	f.runtime.finishTurn(child.CurrentRunID, "child-1", "child done")
	for range 3 {
		f.service.CoordinationTick(f.ctx)
		f.advance(5 * time.Second)
	}
	f.runtime.mu.Lock()
	sent := append([]adapterproto.Frame(nil), f.runtime.sent[lead.Intent.RunID]...)
	f.runtime.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("lead messages = %d, want first prompt plus one wake", len(sent))
	}
	wake := sent[1].Content
	if !strings.Contains(wake, `The operator messaged helper "one" directly (1 new)`) || strings.Contains(wake, secret) {
		t.Fatalf("wake prompt = %q", wake)
	}
	noticed, err := f.service.GetThreadLink(f.ctx, link.ID)
	if err != nil || noticed.OperatorNoticed != 1 || noticed.ReportDeliveredSeq != noticed.ReportSeq {
		t.Fatalf("link after wake = %+v, %v", noticed, err)
	}
}

func TestOperatorMessagesAfterTheLinkClosesAreNotCounted(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.spawnHelper(t, mailbox, "one")
	var closed domain.ThreadLink
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThreadLink(f.ctx, link.ID)
		if err != nil {
			return err
		}
		expected := current.ResourceVersion
		current.State, current.CloseReason = domain.LinkClosed, domain.LinkEnded
		current.UpdatedAt = f.clock.Now()
		current.ResourceVersion++
		closed = current
		return tx.UpdateThreadLink(f.ctx, current, expected)
	}); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendThreadMessage(f.ctx, child.ID, child.ResourceVersion, "carry on", "op-1"); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.GetThreadLink(f.ctx, link.ID)
	if err != nil || after.OperatorMessages != 0 || !after.UpdatedAt.Equal(closed.UpdatedAt) {
		t.Fatalf("closed link after operator message = %+v, %v", after, err)
	}
}
