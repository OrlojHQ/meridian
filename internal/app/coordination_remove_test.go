package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestOperatorRemovesOneSubagentAndTheLeadKeepsSpawning(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 4, MaxLiveCapsules: 2})
	first := f.finishedSubagent(t, mailbox, "first")
	second := f.finishedSubagent(t, mailbox, "second")
	spawnThird := coordproto.Request{Type: coordproto.KindSpawn, Key: "spawn-third", Name: "third", Harness: "mock", Prompt: "t"}
	if response := tool(t, mailbox, spawnThird); code(response) != coordproto.CodeCapacityExhausted {
		t.Fatalf("spawn over live limit = %+v", response)
	}
	thread, _ := f.service.GetThread(f.ctx, first.ChildThreadID)

	if _, err := f.service.RemoveSubagent(f.ctx, first.ID, first.ResourceVersion+1, "remove"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale remove = %v", err)
	}
	removed, err := f.service.RemoveSubagent(f.ctx, first.ID, first.ResourceVersion, "remove")
	if err != nil || removed.State != domain.LinkClosed || removed.CloseReason != domain.LinkCancelled ||
		removed.LastReportReason != domain.ReportCancelled {
		t.Fatalf("remove = %#v, %v", removed, err)
	}
	if f.cancels(thread.CurrentRunID) != 1 {
		t.Fatalf("session cancels = %d, want 1", f.cancels(thread.CurrentRunID))
	}
	capsule, _ := f.service.GetCapsule(f.ctx, first.ChildCapsuleID)
	if capsule.DesiredState != domain.IntentDeleted || !f.service.ThreadLinkCapsuleRemoved(f.ctx, removed) {
		t.Fatalf("removed subagent Capsule = %#v", capsule)
	}
	if replay, err := f.service.RemoveSubagent(f.ctx, first.ID, first.ResourceVersion, "remove"); err != nil ||
		replay.ResourceVersion != removed.ResourceVersion {
		t.Fatalf("remove replay = %#v, %v", replay, err)
	}
	if _, err := f.service.RemoveSubagent(f.ctx, first.ID, removed.ResourceVersion, "again"); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("remove twice = %v", err)
	}

	// The grant is untouched, the sibling keeps running, and the freed
	// Capsule lets the lead spawn again.
	position, _ := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	if grant.State != domain.GrantActive {
		t.Fatalf("grant after remove = %s", grant.State)
	}
	if sibling, _ := f.service.GetThreadLink(f.ctx, second.ID); sibling.State != domain.LinkActive {
		t.Fatalf("sibling after remove = %#v", sibling)
	}
	f.service.CoordinationTick(f.ctx)
	spawnThird.Key = "spawn-third-again"
	if response := tool(t, mailbox, spawnThird); !response.OK {
		t.Fatalf("spawn after remove = %+v", response)
	}
	if rollup := f.leadRollup(t, lead.Intent.CapsuleID); rollup.Total != 2 || rollup.Done != 0 {
		t.Fatalf("rollup after remove = %#v", rollup)
	}
}

func TestRemoveWaitsForTheSessionToStopBeforeDeleting(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.finishedSubagent(t, mailbox, "slow")
	thread, _ := f.service.GetThread(f.ctx, link.ChildThreadID)
	f.runtime.mu.Lock()
	f.runtime.holdCancel = true
	f.runtime.mu.Unlock()

	if _, err := f.service.RemoveSubagent(f.ctx, link.ID, link.ResourceVersion, "remove"); err != nil {
		t.Fatal(err)
	}
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("Capsule deleted while its session was still stopping")
	}
	f.service.CoordinationTick(f.ctx)
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.DesiredState != domain.IntentReady {
		t.Fatalf("Capsule while stopping = %s, want it left running", capsule.DesiredState)
	}
	// The adapter finishes stopping the session.
	f.runtime.mu.Lock()
	f.runtime.runs[thread.CurrentRunID] = ports.RuntimeRun{State: domain.RunCancelled, Structured: true}
	f.runtime.mu.Unlock()
	f.service.CoordinationTick(f.ctx)
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("Capsule after the session stopped = %s, want deleted", capsule.DesiredState)
	}
}

func TestRemoveFallsBackToPauseWhenTheSessionWillNotStop(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.finishedSubagent(t, mailbox, "stuck")
	f.runtime.mu.Lock()
	f.runtime.holdCancel = true
	f.runtime.mu.Unlock()

	if _, err := f.service.RemoveSubagent(f.ctx, link.ID, link.ResourceVersion, "remove"); err != nil {
		t.Fatal(err)
	}
	f.advance(31 * time.Second)
	f.service.CoordinationTick(f.ctx)
	f.service.CoordinationTick(f.ctx)
	capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID)
	if capsule.DesiredState != domain.IntentPaused {
		t.Fatalf("stuck subagent Capsule = %s, want paused", capsule.DesiredState)
	}
}

func TestRemoveRefusesPromotedSubagents(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.finishedSubagent(t, mailbox, "kept")
	capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID)
	if _, err := f.service.PromoteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion, false, "promote"); err != nil {
		t.Fatal(err)
	}
	link, _ = f.service.GetThreadLink(f.ctx, link.ID)
	if _, err := f.service.RemoveSubagent(f.ctx, link.ID, link.ResourceVersion, "remove"); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("remove promoted = %v", err)
	}
}
