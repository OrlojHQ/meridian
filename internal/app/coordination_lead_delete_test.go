package app_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// deleteLead deletes the lead's Capsule as the web UI does: force stop its
// sessions, then delete.
func (f *coordFixture) deleteLead(t *testing.T, lead app.ProjectThreadResult) {
	t.Helper()
	capsule, err := f.service.GetCapsule(f.ctx, lead.Intent.CapsuleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ForceDeleteCapsule(f.ctx, capsule.ID, capsule.ResourceVersion, "delete-lead"); err != nil {
		t.Fatalf("delete lead: %v", err)
	}
}

func (f *coordFixture) capsule(t *testing.T, id domain.CapsuleID) domain.Capsule {
	t.Helper()
	capsule, err := f.service.GetCapsule(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return capsule
}

func TestDeletingALeadDeletesEveryOneOfItsSubagents(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 8, MaxLiveCapsules: 8})
	idle := f.finishedSubagent(t, mailbox, "idle")
	unsaved := f.finishedSubagent(t, mailbox, "unsaved")
	f.setLinkFilesChanged(t, unsaved.ID, 3)
	paused := f.finishedSubagent(t, mailbox, "paused")
	setProjectThreadCapsuleState(t, f.ctx, f.store, paused.ChildCapsuleID, domain.CapsulePaused)
	failed := f.finishedSubagent(t, mailbox, "failed")
	setProjectThreadCapsuleState(t, f.ctx, f.store, failed.ChildCapsuleID, domain.CapsuleFailed)
	promoted := f.finishedSubagent(t, mailbox, "promoted")
	promotedCapsule := f.capsule(t, promoted.ChildCapsuleID)
	if _, err := f.service.PromoteCapsule(f.ctx, promotedCapsule.ID, promotedCapsule.ResourceVersion, false, "promote"); err != nil {
		t.Fatal(err)
	}
	working := f.spawnSubagent(t, mailbox, "working")
	f.service.CoordinationTick(asSystem(f.ctx))
	workingThread, _ := f.service.GetThread(f.ctx, working.ChildThreadID)
	if run, err := f.service.GetRunStored(f.ctx, workingThread.CurrentRunID); err != nil || run.State.Terminal() {
		t.Fatalf("working subagent run = %#v, %v", run, err)
	}

	f.deleteLead(t, lead)
	f.service.CoordinationTick(asSystem(f.ctx))

	for _, link := range []domain.ThreadLink{idle, unsaved, paused, failed, working} {
		capsule := f.capsule(t, link.ChildCapsuleID)
		if capsule.DesiredState != domain.IntentDeleted {
			t.Fatalf("%s subagent Capsule = %s/%s, want it deleted", link.Name, capsule.State, capsule.DesiredState)
		}
		current := mustLink(t, f, link.ID)
		if !f.service.ThreadLinkCapsuleRemoved(f.ctx, current) {
			t.Fatalf("%s subagent not shown as removed", link.Name)
		}
		// The encrypted transcript stays, as with any Capsule delete.
		if _, err := f.service.GetThread(f.ctx, link.ChildThreadID); err != nil {
			t.Fatalf("%s subagent Thread = %v", link.Name, err)
		}
	}
	if run, err := f.service.GetRunStored(f.ctx, workingThread.CurrentRunID); err != nil || !run.State.Terminal() {
		t.Fatalf("working subagent run after delete = %#v, %v", run, err)
	}
	// A promoted subagent is the operator's main Capsule and stays.
	if capsule := f.capsule(t, promoted.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("promoted subagent was deleted with its lead")
	}

	// Later ticks change nothing.
	f.service.CoordinationTick(asSystem(f.ctx))
	if capsule := f.capsule(t, promoted.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("promoted subagent was deleted later")
	}
}

func TestSubagentsOfALiveLeadAreNotDeleted(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.finishedSubagent(t, mailbox, "kept")
	f.advance(time.Minute)
	f.service.CoordinationTick(asSystem(f.ctx))
	if capsule := f.capsule(t, link.ChildCapsuleID); capsule.DesiredState != domain.IntentReady {
		t.Fatalf("subagent of a live lead = %s/%s", capsule.State, capsule.DesiredState)
	}
}

func TestLeadDeleteRetriesASubagentItCouldNotDeleteYet(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.finishedSubagent(t, mailbox, "busy")
	f.setMaintenance(t, link.ChildCapsuleID, "capture")

	f.deleteLead(t, lead)
	f.service.CoordinationTick(asSystem(f.ctx))
	if capsule := f.capsule(t, link.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("subagent deleted during maintenance")
	}

	f.setMaintenance(t, link.ChildCapsuleID, "")
	f.advance(time.Hour)
	f.service.CoordinationTick(asSystem(f.ctx))
	if capsule := f.capsule(t, link.ChildCapsuleID); capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("subagent after maintenance = %s/%s, want it deleted", capsule.State, capsule.DesiredState)
	}
}

// Grants allow depth 1 today, but the link schema allows deeper links; the
// cascade must reach them, because a deleted subagent is itself a removed
// parent.
func TestLeadDeleteCascadesThroughNestedSubagents(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 4, MaxLiveCapsules: 4})
	child := f.finishedSubagent(t, mailbox, "child")
	grandchild := f.finishedSubagent(t, mailbox, "grandchild")
	f.reparent(t, grandchild.ID, child)

	f.deleteLead(t, lead)
	f.service.CoordinationTick(asSystem(f.ctx))
	if capsule := f.capsule(t, child.ChildCapsuleID); capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("child Capsule = %s/%s", capsule.State, capsule.DesiredState)
	}
	f.service.CoordinationTick(asSystem(f.ctx))
	if capsule := f.capsule(t, grandchild.ChildCapsuleID); capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("grandchild Capsule = %s/%s", capsule.State, capsule.DesiredState)
	}
}

func (f *coordFixture) setMaintenance(t *testing.T, id domain.CapsuleID, maintenance string) {
	t.Helper()
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(f.ctx, id)
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.Maintenance = maintenance
		capsule.ResourceVersion++
		return tx.UpdateCapsule(f.ctx, capsule, previous)
	}); err != nil {
		t.Fatal(err)
	}
}

// reparent makes the link a child of parent's subagent at depth 2. Link
// identity is immutable in the store, so the test lifts that trigger.
func (f *coordFixture) reparent(t *testing.T, id domain.ThreadLinkID, parent domain.ThreadLink) {
	t.Helper()
	raw, err := sql.Open("sqlite", f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, statement := range []string{
		"DROP TRIGGER thread_links_identity_immutable",
		"UPDATE thread_links SET parent_thread_id = '" + string(parent.ChildThreadID) +
			"', parent_capsule_id = '" + string(parent.ChildCapsuleID) + "', depth = 2 WHERE id = '" + string(id) + "'",
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
