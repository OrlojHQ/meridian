package app_test

import (
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (f *coordFixture) leadGrant(t *testing.T, lead app.ProjectThreadResult) domain.CoordinationGrant {
	t.Helper()
	position, err := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func (f *coordFixture) messageLead(t *testing.T, lead app.ProjectThreadResult, key string) {
	t.Helper()
	thread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SendThreadMessage(f.ctx, thread.ID, thread.ResourceVersion, "keep going", key, ""); err != nil {
		t.Fatal(err)
	}
}

func TestGrantLapsesWithoutTheOperatorAndReopensOnTheirMessage(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{UnattendedLimit: 10 * time.Minute})
	created := f.leadGrant(t, lead)
	link := f.spawnSubagent(t, mailbox, "one")

	// Nothing the Capsules or the daemon do counts as the operator.
	f.advance(5 * time.Minute)
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSend, Key: "more", Link: string(link.ID), Prompt: "more",
	}); !response.OK {
		t.Fatalf("send = %+v", response)
	}
	f.service.CoordinationTick(asSystem(f.ctx))
	thread, _ := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if _, err := f.service.SendThreadMessage(asSystem(f.ctx), thread.ID, thread.ResourceVersion,
		"system prompt", "system-1", ""); err != nil {
		t.Fatal(err)
	}
	if grant := f.leadGrant(t, lead); !grant.LastOperatorAt.Equal(created.LastOperatorAt) {
		t.Fatalf("non-operator activity moved lastOperatorAt: %s -> %s", created.LastOperatorAt, grant.LastOperatorAt)
	}

	f.advance(6 * time.Minute)
	// The refusal holds before the coordination loop applies the lapse.
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "two", Name: "two", Harness: "mock", Prompt: "task",
	}); code(response) != coordproto.CodeCoordinationLapsed || response.Error.Retryable {
		t.Fatalf("spawn while unattended = %+v", response)
	}
	f.service.CoordinationTick(asSystem(f.ctx))
	lapsed := f.leadGrant(t, lead)
	if lapsed.State != domain.GrantActive || lapsed.LapsedAt.IsZero() ||
		lapsed.StateAt(f.clock.Now()) != domain.GrantLapsed || lapsed.RevokeReason != "" {
		t.Fatalf("lapsed grant = %+v", lapsed)
	}
	// cancel_and_pause closes running subagents, as the deadline did.
	if closed, _ := f.service.GetThreadLink(f.ctx, link.ID); closed.State != domain.LinkClosed ||
		closed.CloseReason != domain.LinkRevoked {
		t.Fatalf("subagent after lapse = %+v", closed)
	}
	for _, request := range []coordproto.Request{
		{Type: coordproto.KindSend, Key: "late", Link: string(link.ID), Prompt: "x"},
		{Type: coordproto.KindCancel, Key: "late-cancel", Link: string(link.ID)},
	} {
		if response := tool(t, mailbox, request); code(response) != coordproto.CodeCoordinationLapsed {
			t.Fatalf("%s while lapsed = %+v", request.Type, response)
		}
	}
	if response := tool(t, mailbox, coordproto.Request{Type: coordproto.KindList}); !response.OK {
		t.Fatalf("list while lapsed = %+v", response)
	}
	f.service.CoordinationTick(asSystem(f.ctx))
	if again := f.leadGrant(t, lead); again.ResourceVersion != lapsed.ResourceVersion {
		t.Fatalf("lapse applied twice: %+v", again)
	}

	f.messageLead(t, lead, "back")
	reopened := f.leadGrant(t, lead)
	if !reopened.LapsedAt.IsZero() || !reopened.Usable(f.clock.Now()) ||
		!reopened.LastOperatorAt.After(lapsed.LastOperatorAt) {
		t.Fatalf("reopened grant = %+v", reopened)
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "three", Name: "three", Harness: "mock", Prompt: "task",
	}); !response.OK {
		t.Fatalf("spawn after the operator returned = %+v", response)
	}

	// Revocation stays terminal: a message does not bring it back.
	revoked, err := f.service.RevokeCoordinationGrant(f.ctx, reopened.ID, f.leadGrant(t, lead).ResourceVersion, true, "revoke")
	if err != nil {
		t.Fatal(err)
	}
	f.messageLead(t, lead, "after-revoke")
	if after := f.leadGrant(t, lead); after.State != domain.GrantRevoked ||
		!after.LastOperatorAt.Equal(revoked.LastOperatorAt) {
		t.Fatalf("revoked grant after message = %+v", after)
	}
}

func TestLapseWithStopNewWorkLeavesSubagentsRunning(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{
		UnattendedLimit: 5 * time.Minute, OnDeadline: domain.DeadlineStopNewWork,
	})
	link := f.spawnSubagent(t, mailbox, "one")
	f.advance(6 * time.Minute)
	f.service.CoordinationTick(asSystem(f.ctx))
	if grant := f.leadGrant(t, lead); grant.LapsedAt.IsZero() {
		t.Fatalf("grant did not lapse: %+v", grant)
	}
	if current, _ := f.service.GetThreadLink(f.ctx, link.ID); current.State != domain.LinkActive {
		t.Fatalf("stop_new_work closed the subagent: %+v", current)
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "two", Name: "two", Harness: "mock", Prompt: "task",
	}); code(response) != coordproto.CodeCoordinationLapsed {
		t.Fatalf("spawn while lapsed = %+v", response)
	}
}

func TestSpawnLimitsCountARollingWindow(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{
		MaxChildren: 2, MaxDescendants: 2, MaxLiveCapsules: 4, UnattendedLimit: 72 * time.Hour,
	})
	f.spawnSubagent(t, mailbox, "one")
	f.advance(time.Hour)
	f.spawnSubagent(t, mailbox, "two")
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "three", Name: "three", Harness: "mock", Prompt: "task",
	}); code(response) != coordproto.CodeLimitExceeded {
		t.Fatalf("third spawn in the window = %+v", response)
	}
	// The first spawn leaves the window; the second still counts.
	f.advance(23*time.Hour + time.Minute)
	f.spawnSubagent(t, mailbox, "three")
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "four", Name: "four", Harness: "mock", Prompt: "task",
	}); code(response) != coordproto.CodeLimitExceeded {
		t.Fatalf("spawn over the window limit = %+v", response)
	}
	grant := f.leadGrant(t, lead)
	window, _, err := f.service.CoordinationGrantWindow(f.ctx, grant)
	if err != nil || window != 2 || grant.DescendantsCreated != 3 {
		t.Fatalf("window = %d, lifetime = %d, %v", window, grant.DescendantsCreated, err)
	}
}

func TestSpawnApprovalAndGrantUpdateRenewUnattendedTime(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{
		UnattendedLimit: 10 * time.Minute, SpawnApproval: domain.SpawnOperator,
	})
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "task",
	})
	if !spawned.OK {
		t.Fatalf("spawn = %+v", spawned)
	}
	f.advance(8 * time.Minute)
	before := f.clock.Now()
	requests, err := f.service.ListCoordinationRequests(f.ctx, domain.RequestPendingApproval)
	if err != nil || len(requests) != 1 {
		t.Fatalf("pending = %#v, %v", requests, err)
	}
	if _, err := f.service.ApproveCoordinationRequest(f.ctx, requests[0].ID, requests[0].ResourceVersion, "approve"); err != nil {
		t.Fatal(err)
	}
	if grant := f.leadGrant(t, lead); grant.LastOperatorAt.Before(before) {
		t.Fatalf("approval did not renew: %+v", grant)
	}

	// A grant update reopens a lapsed grant.
	f.advance(11 * time.Minute)
	f.service.CoordinationTick(asSystem(f.ctx))
	lapsed := f.leadGrant(t, lead)
	if lapsed.LapsedAt.IsZero() {
		t.Fatalf("grant did not lapse: %+v", lapsed)
	}
	limit := time.Hour
	updated, err := f.service.UpdateCoordinationGrant(f.ctx, lapsed.ID, lapsed.ResourceVersion,
		domain.CoordinationGrantPatch{UnattendedLimit: &limit}, "longer")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.LapsedAt.IsZero() || !updated.Usable(f.clock.Now()) || updated.UnattendedLimit != limit {
		t.Fatalf("updated grant = %+v", updated)
	}
}

func TestIdlePauseDoesNotEndAGrant(t *testing.T) {
	f := newCoordFixture(t)
	lead, _ := f.spawnLead(t, domain.CoordinationPolicy{})
	setProjectThreadCapsuleState(t, f.ctx, f.store, lead.Intent.CapsuleID, domain.CapsulePaused)
	f.advance(time.Hour)
	f.service.CoordinationTick(asSystem(f.ctx))
	if grant := f.leadGrant(t, lead); grant.State != domain.GrantActive || !grant.Usable(f.clock.Now()) {
		t.Fatalf("grant after idle pause = %+v", grant)
	}
}
