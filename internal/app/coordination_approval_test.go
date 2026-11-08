package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestOperatorSpawnWaitsForApproval(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{SpawnApproval: domain.SpawnOperator})
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "secret task",
	})
	if !spawned.OK || spawned.Link == nil || spawned.Link.State != string(domain.LinkPendingApproval) {
		t.Fatalf("spawn = %+v", spawned)
	}
	link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil || link.State != domain.LinkPendingApproval {
		t.Fatalf("link = %#v, %v", link, err)
	}
	if retry := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "secret task",
	}); !retry.OK || retry.Link.ID != spawned.Link.ID || retry.Link.State != string(domain.LinkPendingApproval) {
		t.Fatalf("spawn retry = %+v", retry)
	}
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSend, Key: "follow", Link: spawned.Link.ID, Prompt: "more",
	}); code(response) != coordproto.CodeUnavailable {
		t.Fatalf("send while pending = %+v", response)
	}
	f.queue.mu.Lock()
	queued := append([]domain.CapsuleID(nil), f.queue.ids...)
	f.queue.mu.Unlock()
	for _, id := range queued {
		if id == link.ChildCapsuleID {
			t.Fatal("pending spawn was enqueued")
		}
	}
	if _, err := f.service.GetProjectThreadIntent(f.ctx, link.ChildIntentID); err != nil {
		t.Fatal(err)
	}
	f.queue.mu.Lock()
	afterLookup := append([]domain.CapsuleID(nil), f.queue.ids...)
	f.queue.mu.Unlock()
	for _, id := range afterLookup {
		if id == link.ChildCapsuleID {
			t.Fatal("intent lookup enqueued a pending spawn")
		}
	}
	board := f.board(t, app.BoardExpandAttention)
	var surfaced app.BoardItem
	for _, item := range board.Items {
		if item.Capsule.ID == link.ChildCapsuleID {
			surfaced = item
			break
		}
	}
	if surfaced.Status != app.BoardNeedsYou || surfaced.NeedsYou != app.BoardAttentionSpawnApproval || !surfaced.Surfaced {
		t.Fatalf("board row = %#v", surfaced)
	}
	if _, err := f.service.PromoteCapsule(f.ctx, link.ChildCapsuleID, 1, false, "promote-pending"); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("promote pending = %v", err)
	}

	requests, err := f.service.ListCoordinationRequests(f.ctx, domain.RequestPendingApproval)
	if err != nil || len(requests) != 1 || requests[0].LinkID != link.ID {
		t.Fatalf("pending requests = %#v, %v", requests, err)
	}
	approved, err := f.service.ApproveCoordinationRequest(f.ctx, requests[0].ID, requests[0].ResourceVersion, "approve")
	if err != nil || approved.State != domain.RequestCompleted {
		t.Fatalf("approve = %#v, %v", approved, err)
	}
	if replay, err := f.service.ApproveCoordinationRequest(f.ctx, requests[0].ID, requests[0].ResourceVersion, "approve"); err != nil || replay.ID != approved.ID {
		t.Fatalf("approve replay = %#v, %v", replay, err)
	}
	link, _ = f.service.GetThreadLink(f.ctx, link.ID)
	if link.State != domain.LinkActive {
		t.Fatalf("approved link = %#v", link)
	}
	f.queue.mu.Lock()
	var found bool
	for _, id := range f.queue.ids {
		if id == link.ChildCapsuleID {
			found = true
		}
	}
	f.queue.mu.Unlock()
	if !found {
		t.Fatal("approved spawn was not enqueued")
	}
	f.ready(t, link.ChildCapsuleID)
	child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil || child.State != domain.ThreadActive {
		t.Fatalf("child thread = %#v, %v", child, err)
	}
	_ = lead
}

func TestOperatorSpawnDenyLeavesDescendantCount(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{SpawnApproval: domain.SpawnOperator, MaxChildren: 2})
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "task",
	})
	if !spawned.OK {
		t.Fatalf("spawn = %+v", spawned)
	}
	requests, err := f.service.ListCoordinationRequests(f.ctx, domain.RequestPendingApproval)
	if err != nil || len(requests) != 1 {
		t.Fatalf("pending = %#v, %v", requests, err)
	}
	denied, err := f.service.DenyCoordinationRequest(f.ctx, requests[0].ID, requests[0].ResourceVersion, "deny")
	if err != nil || denied.State != domain.RequestRejected {
		t.Fatalf("deny = %#v, %v", denied, err)
	}
	link, _ := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if link.State != domain.LinkClosed || link.CloseReason != domain.LinkDenied {
		t.Fatalf("denied link = %#v", link)
	}
	position, _ := f.service.ThreadCoordinationOf(f.ctx, link.ParentThreadID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	if grant.DescendantsCreated != 1 {
		t.Fatalf("descendants after deny = %d", grant.DescendantsCreated)
	}
	board := f.board(t, app.BoardExpandAll)
	for _, item := range board.Items {
		if item.Capsule.ID == link.ChildCapsuleID && item.Status == app.BoardWorking {
			t.Fatalf("denied child still working: %#v", item)
		}
	}
	second := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "t", Name: "two", Harness: "mock", Prompt: "next",
	})
	if !second.OK {
		t.Fatalf("second spawn = %+v", second)
	}
}

func TestGrantUpdateLoosensAndTightens(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 2, SpawnApproval: domain.SpawnAuto})
	position, _ := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	children := 8
	approval := domain.SpawnOperator
	updated, err := f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion, domain.CoordinationGrantPatch{
		MaxChildren: &children, SpawnApproval: &approval,
	}, "widen")
	if err != nil || updated.MaxChildren != 8 || updated.SpawnApproval != domain.SpawnOperator {
		t.Fatalf("widen = %#v, %v", updated, err)
	}
	if replay, err := f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion, domain.CoordinationGrantPatch{
		MaxChildren: &children, SpawnApproval: &approval,
	}, "widen"); err != nil || replay.ResourceVersion != updated.ResourceVersion {
		t.Fatalf("update replay = %#v, %v", replay, err)
	}
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "task",
	})
	if !spawned.OK || spawned.Link.State != string(domain.LinkPendingApproval) {
		t.Fatalf("spawn after operator mode = %+v", spawned)
	}
	current, err := f.service.GetCoordinationGrant(f.ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	auto := domain.SpawnAuto
	switched, err := f.service.UpdateCoordinationGrant(f.ctx, current.ID, current.ResourceVersion, domain.CoordinationGrantPatch{
		SpawnApproval: &auto,
	}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	link, _ := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if link.State != domain.LinkPendingApproval {
		t.Fatalf("switching to auto started a queued spawn: %#v", link)
	}
	tight := 1
	if _, err := f.service.UpdateCoordinationGrant(f.ctx, switched.ID, switched.ResourceVersion, domain.CoordinationGrantPatch{
		MaxChildren: &tight,
	}, "tighten"); err != nil {
		t.Fatalf("tighten below existing children = %v", err)
	}
}

func TestGrantDeadlinePatchIsCapped(t *testing.T) {
	f := newCoordFixture(t)
	lead, _ := f.spawnLead(t, domain.CoordinationPolicy{})
	position, _ := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	remaining := 72 * time.Hour
	updated, err := f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion, domain.CoordinationGrantPatch{
		Deadline: &remaining,
	}, "deadline")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.DeadlineAt.Equal(grant.CreatedAt.Add(domain.CoordinationMaxDeadline)) {
		t.Fatalf("capped deadline = %s, created = %s", updated.DeadlineAt, grant.CreatedAt)
	}
	if _, err := f.service.UpdateCoordinationGrant(f.ctx, updated.ID, updated.ResourceVersion, domain.CoordinationGrantPatch{}, "empty"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty patch = %v", err)
	}
}

func TestLeadCancelWithdrawsPendingSpawn(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{SpawnApproval: domain.SpawnOperator})
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "task",
	})
	cancelled := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindCancel, Key: "c", Link: spawned.Link.ID,
	})
	if !cancelled.OK || cancelled.Link.State != string(domain.LinkClosed) {
		t.Fatalf("cancel pending = %+v", cancelled)
	}
	requests, err := f.service.ListCoordinationRequests(f.ctx, domain.RequestRejected)
	if err != nil || len(requests) != 1 {
		t.Fatalf("rejected = %#v, %v", requests, err)
	}
}

func TestRecoverSkipsPendingSpawn(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{SpawnApproval: domain.SpawnOperator})
	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "task",
	})
	link, _ := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	reconciler := app.NewReconciler(f.store, nil, f.clock, &testIDs{})
	if err := reconciler.Recover(f.ctx); err != nil {
		t.Fatal(err)
	}
	capsule, err := f.service.GetCapsule(f.ctx, link.ChildCapsuleID)
	if err != nil || capsule.ProviderResourceID != "" || capsule.State != domain.CapsuleCreating {
		t.Fatalf("pending capsule after recover = %#v, %v", capsule, err)
	}
}

func TestCoordinationGrantPatchRequiresChange(t *testing.T) {
	patch := domain.CoordinationGrantPatch{}
	if !patch.Empty() {
		t.Fatal("empty patch")
	}
	grant := domain.CoordinationGrant{
		MaxChildren: 4, MaxDescendants: 8, MaxLiveCapsules: 4, MaxMessagesPerLink: 50,
		SpawnApproval: domain.SpawnAuto, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		DeadlineAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	}
	if _, err := grant.Apply(patch, grant.CreatedAt.Add(time.Hour)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("apply empty = %v", err)
	}
}
