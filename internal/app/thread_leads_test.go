package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// plainThread starts a plain Project Thread whose first prompt is running.
func (f *coordFixture) plainThread(t *testing.T, key string) (domain.Thread, domain.Capsule) {
	t.Helper()
	created, err := f.service.CreateProjectThread(f.ctx, app.CreateProjectThreadInput{
		ProjectID: "project-coord", Harness: "mock", IdempotencyKey: key, Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	capsule := f.ready(t, created.Intent.CapsuleID)
	thread, err := f.service.GetThread(f.ctx, created.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	return thread, capsule
}

// settle ends the Thread's running turn with resume state and ingests it.
func (f *coordFixture) settle(t *testing.T, thread domain.Thread, id string) domain.Thread {
	t.Helper()
	f.runtime.finishTurn(thread.CurrentRunID, id, "answer "+id)
	if err := f.service.SyncThread(f.ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.service.GetThread(f.ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

func (f *coordFixture) startRole(t *testing.T, run domain.RunID) string {
	t.Helper()
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	frame, ok := f.runtime.starts[run]
	if !ok {
		t.Fatalf("run %s never started", run)
	}
	return adapterproto.CoordinationRole(frame.Metadata)
}

func (f *coordFixture) cancelCount(run domain.RunID) int {
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	return f.runtime.cancels[run]
}

func (f *coordFixture) sentTo(run domain.RunID) []adapterproto.Frame {
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	return append([]adapterproto.Frame(nil), f.runtime.sent[run]...)
}

func makeLead(
	ctx context.Context, f *coordFixture, thread domain.Thread, key string, policy domain.CoordinationPolicy,
) (app.MakeThreadLeadResult, error) {
	return f.service.MakeThreadLead(ctx, app.MakeThreadLeadInput{
		ThreadID: thread.ID, Expected: thread.ResourceVersion, IdempotencyKey: key, Policy: policy,
	})
}

// An idle plain Thread becomes a lead at once: the daemon stops its Run and
// resumes the harness session with the lead role and a configured mailbox,
// and sends no prompt of its own.
func TestMakeThreadLeadRestartsAnIdleSessionAsALead(t *testing.T) {
	f := newCoordFixture(t)
	thread, capsule := f.plainThread(t, "plain")
	plainRun := thread.CurrentRunID
	if role := f.startRole(t, plainRun); role != "" {
		t.Fatalf("plain start role = %q", role)
	}
	thread = f.settle(t, thread, "a-1")
	mailbox := f.runtime.mailbox(capsule.ProviderResourceID)
	if disabled := tool(t, mailbox, coordproto.Request{Type: coordproto.KindWhoAmI}); code(disabled) != coordproto.CodeCoordinationDisabled {
		t.Fatalf("plain whoami = %+v", disabled)
	}
	messages := thread.MessageCount

	f.advance(time.Minute)
	result, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{MaxLiveCapsules: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Restarted || result.Grant.State != domain.GrantActive || result.Grant.RootThreadID != thread.ID ||
		result.Grant.MaxLiveCapsules != 2 || !result.Grant.LastOperatorAt.Equal(result.Grant.CreatedAt) ||
		result.Grant.CreatedAt.Before(thread.UpdatedAt) {
		t.Fatalf("result = %+v", result)
	}
	if f.cancelCount(plainRun) != 1 {
		t.Fatalf("plain run cancels = %d", f.cancelCount(plainRun))
	}
	lead := result.Thread
	if lead.CurrentRunID == plainRun || lead.State != domain.ThreadActive {
		t.Fatalf("lead thread = %+v", lead)
	}
	start := func() adapterproto.Frame {
		f.runtime.mu.Lock()
		defer f.runtime.mu.Unlock()
		return f.runtime.starts[lead.CurrentRunID]
	}()
	if start.Type != adapterproto.KindResume || start.ResumeState == "" ||
		adapterproto.CoordinationRole(start.Metadata) != adapterproto.CoordinationLead {
		t.Fatalf("renewed start = %+v", start)
	}
	if sent := f.sentTo(lead.CurrentRunID); len(sent) != 0 || lead.MessageCount != messages {
		t.Fatalf("renewal invented a prompt: %d frames, %d messages", len(sent), lead.MessageCount)
	}
	if whoami := tool(t, mailbox, coordproto.Request{Type: coordproto.KindWhoAmI}); !whoami.OK {
		t.Fatalf("lead whoami = %+v", whoami)
	}
	position, err := f.service.ThreadCoordinationOf(f.ctx, thread.ID)
	if err != nil || position.GrantID != result.Grant.ID || position.ParentThreadID != "" {
		t.Fatalf("coordination = %+v, %v", position, err)
	}

	// The next prompt goes to the renewed session; nothing renews twice.
	sent, err := f.service.SendThreadMessage(f.ctx, lead.ID, lead.ResourceVersion, "split this up", "next", "")
	if err != nil || sent.Run.ID != lead.CurrentRunID {
		t.Fatalf("send = %+v, %v", sent, err)
	}
	if f.cancelCount(lead.CurrentRunID) != 0 {
		t.Fatal("renewed session renewed again")
	}
	// The coordination loop polls the lead's mailbox like any lead's.
	f.service.CoordinationTick(asSystem(f.ctx))
	spawned := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSpawn, Key: "s", Name: "one", Harness: "mock", Prompt: "task"})
	if !spawned.OK {
		t.Fatalf("spawn from an upgraded lead = %+v", spawned)
	}
}

// A Thread mid-turn keeps its turn. The grant is recorded at once and the
// role arrives before the first prompt after the turn ends.
func TestMakeThreadLeadDefersRenewalUntilTheTurnEnds(t *testing.T) {
	f := newCoordFixture(t)
	thread, _ := f.plainThread(t, "plain")
	plainRun := thread.CurrentRunID

	result, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Restarted || f.cancelCount(plainRun) != 0 || result.Thread.CurrentRunID != plainRun {
		t.Fatalf("mid-turn renewal: restarted %v, cancels %d", result.Restarted, f.cancelCount(plainRun))
	}
	// A message sent while the turn runs queues on the same session.
	busy, err := f.service.SendThreadMessage(f.ctx, thread.ID, result.Thread.ResourceVersion, "also this", "busy", "")
	if err != nil || busy.Run.ID != plainRun || f.cancelCount(plainRun) != 0 {
		t.Fatalf("mid-turn send = %+v, %v", busy, err)
	}

	thread = f.settle(t, busy.Thread, "a-1")
	sent, err := f.service.SendThreadMessage(f.ctx, thread.ID, thread.ResourceVersion, "now delegate", "after", "")
	if err != nil {
		t.Fatal(err)
	}
	if sent.Run.ID == plainRun || f.cancelCount(plainRun) != 1 {
		t.Fatalf("deferred renewal: run %s, cancels %d", sent.Run.ID, f.cancelCount(plainRun))
	}
	if role := f.startRole(t, sent.Run.ID); role != adapterproto.CoordinationLead {
		t.Fatalf("renewed role = %q", role)
	}
	frames := f.sentTo(sent.Run.ID)
	if len(frames) != 1 || frames[0].Content != "now delegate" {
		t.Fatalf("renewed session received %+v", frames)
	}
}

// A Thread waiting on the operator is not restarted either.
func TestMakeThreadLeadLeavesASessionWaitingOnTheOperator(t *testing.T) {
	f := newCoordFixture(t)
	thread, _ := f.plainThread(t, "plain")
	f.runtime.mu.Lock()
	next := uint64(len(f.runtime.events[thread.CurrentRunID]) + 1)
	f.runtime.events[thread.CurrentRunID] = append(f.runtime.events[thread.CurrentRunID],
		ports.RuntimeStructuredEvent{Sequence: next, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStatus, Status: adapterproto.StatusIdle,
			ResumeState: `{"turn":"a-0"}`,
		}},
		ports.RuntimeStructuredEvent{Sequence: next + 1, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest, ID: "perm-1",
			Permission: &adapterproto.Permission{Kind: "command", Summary: "run tests"},
		}},
	)
	f.runtime.mu.Unlock()
	if err := f.service.SyncThread(f.ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.GetThread(f.ctx, thread.ID)
	if attention, err := f.service.ThreadAttention(f.ctx, thread); err != nil || attention.Awaiting == nil {
		t.Fatalf("attention = %+v, %v", attention, err)
	}
	result, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Restarted || f.cancelCount(thread.CurrentRunID) != 0 {
		t.Fatalf("restarted a session awaiting the operator: %+v", result)
	}
}

// After a daemon restart the new process does not know which role a session
// was started with. A grant created after the Run started means the session
// predates the role, so the first prompt renews it.
func TestUpgradedLeadRenewsAfterADaemonRestart(t *testing.T) {
	f := newCoordFixture(t)
	thread, _ := f.plainThread(t, "plain")
	plainRun := thread.CurrentRunID
	f.advance(time.Second)
	result, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{})
	if err != nil || result.Restarted {
		t.Fatalf("make lead = %+v, %v", result, err)
	}

	restarted := app.NewService(f.store, f.clock, &testIDs{next: 900}, f.queue, f.runtime)
	restarted.ConfigureThreads(f.runtime, f.key)
	restarted.ConfigureCoordination(f.runtime, 0)
	f.runtime.finishTurn(plainRun, "a-1", "done")
	if err := restarted.SyncThread(f.ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, err = restarted.GetThread(f.ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := restarted.SendThreadMessage(f.ctx, thread.ID, thread.ResourceVersion, "go", "after-restart", "")
	if err != nil {
		t.Fatal(err)
	}
	if sent.Run.ID == plainRun || f.startRole(t, sent.Run.ID) != adapterproto.CoordinationLead {
		t.Fatalf("after restart: run %s", sent.Run.ID)
	}

	// The renewed Run started after the grant, so a further restart leaves it.
	again := app.NewService(f.store, f.clock, &testIDs{next: 950}, f.queue, f.runtime)
	again.ConfigureThreads(f.runtime, f.key)
	again.ConfigureCoordination(f.runtime, 0)
	f.runtime.finishTurn(sent.Run.ID, "a-2", "done")
	if err := again.SyncThread(f.ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	thread, _ = again.GetThread(f.ctx, thread.ID)
	next, err := again.SendThreadMessage(f.ctx, thread.ID, thread.ResourceVersion, "more", "again", "")
	if err != nil || next.Run.ID != sent.Run.ID {
		t.Fatalf("lead renewed without cause: %+v, %v", next.Run, err)
	}
}

func TestMakeThreadLeadIsOperatorOnly(t *testing.T) {
	f := newCoordFixture(t)
	thread, capsule := f.plainThread(t, "plain")
	asCapsule, _ := principal.Capsule(principal.BootstrapOwner, capsule.ID)
	for name, ctx := range map[string]context.Context{
		"break-glass": principal.With(context.Background(), principal.BreakGlass()),
		"system":      asSystem(context.Background()),
		"capsule":     principal.With(context.Background(), asCapsule),
	} {
		if _, err := makeLead(ctx, f, thread, "lead-"+name, domain.CoordinationPolicy{}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := makeLead(context.Background(), f, thread, "lead-anonymous", domain.CoordinationPolicy{}); err == nil {
		t.Error("a request without a principal made a lead")
	}
	if _, err := f.service.ThreadCoordinationOf(f.ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	if position, _ := f.service.ThreadCoordinationOf(f.ctx, thread.ID); position.GrantID != "" {
		t.Fatalf("non-operator created a grant: %+v", position)
	}
}

func TestMakeThreadLeadIsIdempotentAndRefusesConcurrentUpgrades(t *testing.T) {
	f := newCoordFixture(t)
	thread, _ := f.plainThread(t, "plain")
	thread = f.settle(t, thread, "a-1")

	first, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{MaxChildren: 2})
	if err != nil {
		t.Fatal(err)
	}
	renewedRun := first.Thread.CurrentRunID
	replay, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{MaxChildren: 2})
	if err != nil || replay.Grant.ID != first.Grant.ID || replay.Restarted ||
		replay.Thread.CurrentRunID != renewedRun || f.cancelCount(renewedRun) != 0 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := makeLead(f.ctx, f, thread, "lead", domain.CoordinationPolicy{MaxChildren: 3}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reused key with a different policy: %v", err)
	}

	// Two upgrades of one Thread at the same version: exactly one wins.
	other, _ := f.plainThread(t, "plain-2")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = makeLead(f.ctx, f, other, "race-"+string(rune('a'+i)), domain.CoordinationPolicy{})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrIllegalTransition):
			t.Fatalf("racing upgrade: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d racing upgrades succeeded", won)
	}
	stale, _ := f.plainThread(t, "plain-3")
	stale.ResourceVersion--
	if _, err := makeLead(f.ctx, f, stale, "stale", domain.CoordinationPolicy{}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
}

func TestMakeThreadLeadRefusals(t *testing.T) {
	f := newCoordFixture(t)
	refused := func(t *testing.T, thread domain.Thread, want error) {
		t.Helper()
		fresh, err := f.service.GetThread(f.ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := makeLead(f.ctx, f, fresh, "refused-"+string(fresh.ID), domain.CoordinationPolicy{}); !errors.Is(err, want) {
			t.Fatalf("upgrade of %s = %v, want %v", fresh.ID, err, want)
		}
	}

	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 4})
	leadThread, _ := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	t.Run("already a lead", func(t *testing.T) { refused(t, leadThread, domain.ErrIllegalTransition) })

	spawned := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSpawn, Key: "s1", Name: "one", Harness: "mock", Prompt: "task"})
	if !spawned.OK {
		t.Fatalf("spawn = %+v", spawned)
	}
	link, _ := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	child := f.ready(t, link.ChildCapsuleID)
	childThread, _ := f.service.GetThread(f.ctx, link.ChildThreadID)
	t.Run("subagent", func(t *testing.T) { refused(t, childThread, domain.ErrIllegalTransition) })

	if _, err := f.service.PromoteCapsule(f.ctx, child.ID, child.ResourceVersion, false, "promote"); err != nil {
		t.Fatal(err)
	}
	t.Run("promoted and still linked", func(t *testing.T) { refused(t, childThread, domain.ErrIllegalTransition) })

	position, _ := f.service.ThreadCoordinationOf(f.ctx, leadThread.ID)
	grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID)
	if _, err := f.service.RevokeCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion, true, "revoke"); err != nil {
		t.Fatal(err)
	}
	t.Run("revoked grant", func(t *testing.T) { refused(t, leadThread, domain.ErrIllegalTransition) })

	stopped, _ := f.plainThread(t, "stopped")
	if _, err := f.service.CancelThread(f.ctx, stopped.ID, stopped.ResourceVersion, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SyncThread(f.ctx, stopped.ID); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := f.service.GetThread(f.ctx, stopped.ID); fresh.State != domain.ThreadPaused {
		t.Fatalf("stopped Thread state = %s", fresh.State)
	}
	t.Run("not active", func(t *testing.T) { refused(t, stopped, domain.ErrIllegalTransition) })

	other, err := f.service.CreateProjectThread(f.ctx, app.CreateProjectThreadInput{
		ProjectID: "project-coord", Harness: "other", IdempotencyKey: "other", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, other.Intent.CapsuleID)
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		project, err := tx.GetProject(f.ctx, "project-coord")
		if err != nil {
			return err
		}
		previous := project.ResourceVersion
		project.HarnessImages = project.HarnessImages[:1]
		project.ResourceVersion++
		return tx.UpdateProject(f.ctx, project, previous)
	}); err != nil {
		t.Fatal(err)
	}
	otherThread, _ := f.service.GetThread(f.ctx, other.Intent.ThreadID)
	t.Run("harness not allowlisted", func(t *testing.T) { refused(t, otherThread, domain.ErrIllegalTransition) })

	plain, _ := f.plainThread(t, "plain")
	if _, err := makeLead(f.ctx, f, plain, "bad-policy",
		domain.CoordinationPolicy{AllowedHarnesses: []string{"nope"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unlisted subagent harness: %v", err)
	}
	if _, err := makeLead(f.ctx, f, plain, "no-version", domain.CoordinationPolicy{}); err != nil {
		t.Fatalf("valid upgrade after refusals: %v", err)
	}
}
