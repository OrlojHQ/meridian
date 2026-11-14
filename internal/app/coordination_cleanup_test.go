package app_test

import (
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// setLinkFilesChanged records the diffstat the subagent's last report carried.
func (f *coordFixture) setLinkFilesChanged(t *testing.T, id domain.ThreadLinkID, files int) {
	t.Helper()
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		link, err := tx.GetThreadLink(f.ctx, id)
		if err != nil {
			return err
		}
		previous := link.ResourceVersion
		link.FilesChanged = files
		link.ResourceVersion++
		return tx.UpdateThreadLink(f.ctx, link, previous)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchivedSubagentWithNothingUnsavedIsDeleted(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	position, _ := f.service.ThreadCoordinationOf(f.ctx, lead.Intent.ThreadID)
	if grant, _ := f.service.GetCoordinationGrant(f.ctx, position.GrantID); grant.ArchiveAfter != time.Hour {
		t.Fatalf("default archiveAfter = %s", grant.ArchiveAfter)
	}
	clean := f.finishedSubagent(t, mailbox, "clean")
	dirty := f.finishedSubagent(t, mailbox, "dirty")
	f.setLinkFilesChanged(t, clean.ID, 0)
	f.setLinkFilesChanged(t, dirty.ID, 2)
	reportSeq := map[domain.ThreadLinkID]int{}
	for _, link := range []domain.ThreadLink{clean, dirty} {
		current, _ := f.service.GetThreadLink(f.ctx, link.ID)
		reportSeq[link.ID] = current.ReportSeq
	}

	f.advance(59 * time.Minute)
	f.service.SweepArchivableSubagents(asSystem(f.ctx))
	if capsule, _ := f.service.GetCapsule(f.ctx, clean.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("subagent deleted before its archive period")
	}

	f.advance(2 * time.Minute)
	f.service.SweepArchivableSubagents(asSystem(f.ctx))
	if capsule, _ := f.service.GetCapsule(f.ctx, clean.ChildCapsuleID); capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("clean subagent Capsule = %s/%s", capsule.State, capsule.DesiredState)
	}
	if capsule, _ := f.service.GetCapsule(f.ctx, dirty.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("subagent with unsaved changes was deleted")
	}
	for _, link := range []domain.ThreadLink{clean, dirty} {
		current, _ := f.service.GetThreadLink(f.ctx, link.ID)
		if current.ArchivedAt.IsZero() || current.ReportSeq != reportSeq[link.ID] {
			t.Fatalf("%s link = archived %v, report %d", link.Name, current.ArchivedAt, current.ReportSeq)
		}
	}
	if !f.service.ThreadLinkCapsuleRemoved(f.ctx, mustLink(t, f, clean.ID)) {
		t.Fatal("clean subagent not shown as removed")
	}
	// The transcript stays.
	if _, err := f.service.GetThread(f.ctx, clean.ChildThreadID); err != nil {
		t.Fatalf("clean subagent Thread = %v", err)
	}
	send := tool(t, mailbox, coordproto.Request{Type: coordproto.KindSend, Key: "wake", Link: string(clean.ID), Prompt: "more"})
	if send.OK {
		t.Fatalf("send to deleted subagent = %+v", send)
	}
	if rollup := f.leadRollup(t, lead.Intent.CapsuleID); rollup.Total != 1 || rollup.Archived != 1 {
		t.Fatalf("rollup = %+v", rollup)
	}

	// Later sweeps leave the kept subagent alone.
	f.advance(24 * time.Hour)
	f.service.SweepArchivableSubagents(asSystem(f.ctx))
	if capsule, _ := f.service.GetCapsule(f.ctx, dirty.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("subagent with unsaved changes was deleted later")
	}
}

func TestCleanupWaitsForTheArchivedSessionToEnd(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{})
	link := f.finishedSubagent(t, mailbox, "slow")
	f.setLinkFilesChanged(t, link.ID, 0)
	thread, _ := f.service.GetThread(f.ctx, link.ChildThreadID)
	f.runtime.mu.Lock()
	f.runtime.holdCancel = true
	f.runtime.mu.Unlock()

	f.advance(2 * time.Hour)
	f.service.SweepArchivableSubagents(asSystem(f.ctx))
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("Capsule deleted while its session was still stopping")
	}
	f.runtime.mu.Lock()
	f.runtime.runs[thread.CurrentRunID] = ports.RuntimeRun{State: domain.RunCancelled, Structured: true}
	f.runtime.mu.Unlock()
	for range 3 {
		f.service.CoordinationTick(asSystem(f.ctx))
		f.advance(5 * time.Second)
	}
	f.advance(archiveSweep)
	f.service.SweepArchivableSubagents(asSystem(f.ctx))
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("Capsule after the session ended = %s/%s", capsule.State, capsule.DesiredState)
	}
}

func TestCleanupIsOffWhenAutoArchiveIsDisabled(t *testing.T) {
	f := newCoordFixture(t)
	disabled := time.Duration(0)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{ArchiveAfter: &disabled})
	link := f.finishedSubagent(t, mailbox, "kept")
	f.setLinkFilesChanged(t, link.ID, 0)
	f.advance(48 * time.Hour)
	f.service.SweepArchivableSubagents(asSystem(f.ctx))
	if capsule, _ := f.service.GetCapsule(f.ctx, link.ChildCapsuleID); capsule.DesiredState == domain.IntentDeleted {
		t.Fatal("subagent deleted with auto-archive disabled")
	}
}

const archiveSweep = 30 * time.Second

func mustLink(t *testing.T, f *coordFixture, id domain.ThreadLinkID) domain.ThreadLink {
	t.Helper()
	link, err := f.service.GetThreadLink(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return link
}
