package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (f *coordFixture) leadMessages(run domain.RunID) []adapterproto.Frame {
	f.runtime.mu.Lock()
	defer f.runtime.mu.Unlock()
	return append([]adapterproto.Frame(nil), f.runtime.sent[run]...)
}

func (f *coordFixture) settleWakes() {
	for range 3 {
		f.advance(5 * time.Second)
		f.service.CoordinationTick(asSystem(f.ctx))
	}
}

// awaitDelivered waits for the lead mailbox poller to record a read.
func (f *coordFixture) awaitDelivered(t *testing.T, id domain.ThreadLinkID, seq int) domain.ThreadLink {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		link, err := f.service.GetThreadLink(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if link.ReportDeliveredSeq >= seq || time.Now().After(deadline) {
			return link
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHeadlinedReportDoesNotWakeTheLeadAgainUntilSomethingNewArrives(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 2})
	one := f.spawnSubagent(t, mailbox, "one")
	two := f.spawnSubagent(t, mailbox, "two")
	oneThread, _ := f.service.GetThread(f.ctx, one.ChildThreadID)
	twoThread, _ := f.service.GetThread(f.ctx, two.ChildThreadID)
	long := "FIRST-PART " + strings.Repeat("x", 6<<10) + " TAIL-ONLY-IN-FULL-REPORT"
	f.runtime.finishTurn(lead.Intent.RunID, "lead-1", "spawned; waiting")
	f.runtime.finishTurn(oneThread.CurrentRunID, "one-1", long)
	f.settleWakes()

	sent := f.leadMessages(lead.Intent.RunID)
	if len(sent) != 2 {
		t.Fatalf("lead messages = %d, want first prompt plus one wake", len(sent))
	}
	wake := sent[1].Content
	if !strings.Contains(wake, "FIRST-PART") || strings.Contains(wake, "TAIL-ONLY-IN-FULL-REPORT") ||
		!strings.Contains(wake, "read the full report with result") || len(wake) > 16<<10 {
		t.Fatalf("wake is not a headline (%d bytes): %q", len(wake), wake[:200])
	}

	// The lead ends its turn without reading the report. The headline does
	// not wake it again.
	f.runtime.finishTurn(lead.Intent.RunID, "lead-2", "noted")
	f.settleWakes()
	if sent := f.leadMessages(lead.Intent.RunID); len(sent) != 2 {
		t.Fatalf("lead messages = %d after an unread headline, want 2", len(sent))
	}
	link, _ := f.service.GetThreadLink(f.ctx, one.ID)
	if link.ReportHeadlinedSeq != 1 || link.ReportDeliveredSeq != 0 {
		t.Fatalf("headline/delivery = %d/%d", link.ReportHeadlinedSeq, link.ReportDeliveredSeq)
	}

	// Something new arrives: the wake carries only the new headline and says
	// the earlier one is still unread.
	f.runtime.finishTurn(twoThread.CurrentRunID, "two-1", "SECOND-REPORT")
	f.settleWakes()
	sent = f.leadMessages(lead.Intent.RunID)
	if len(sent) != 3 {
		t.Fatalf("lead messages = %d after a new report, want 3", len(sent))
	}
	wake = sent[2].Content
	if !strings.Contains(wake, "SECOND-REPORT") || strings.Contains(wake, "FIRST-PART") ||
		!strings.Contains(wake, `Still unread from earlier wakes: "one".`) {
		t.Fatalf("second wake = %q", wake)
	}
}

func TestOnlyResultOrWaitMarksAReportDelivered(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 2})
	one := f.spawnSubagent(t, mailbox, "one")
	two := f.spawnSubagent(t, mailbox, "two")
	oneThread, _ := f.service.GetThread(f.ctx, one.ChildThreadID)
	twoThread, _ := f.service.GetThread(f.ctx, two.ChildThreadID)
	f.runtime.finishTurn(lead.Intent.RunID, "lead-1", "spawned; waiting")
	f.runtime.finishTurn(oneThread.CurrentRunID, "one-1", "one finished")
	f.runtime.finishTurn(twoThread.CurrentRunID, "two-1", "two finished")
	f.settleWakes()
	if sent := f.leadMessages(lead.Intent.RunID); len(sent) != 2 {
		t.Fatalf("lead messages = %d, want first prompt plus one wake", len(sent))
	}
	for _, id := range []domain.ThreadLinkID{one.ID, two.ID} {
		link, _ := f.service.GetThreadLink(f.ctx, id)
		if link.ReportHeadlinedSeq != 1 || link.ReportDeliveredSeq != 0 {
			t.Fatalf("%s headline/delivery = %d/%d", link.Name, link.ReportHeadlinedSeq, link.ReportDeliveredSeq)
		}
	}

	read := tool(t, mailbox, coordproto.Request{Type: coordproto.KindResult, Link: string(one.ID)})
	if !read.OK || len(read.Reports) != 1 || read.Reports[0].Summary != "one finished" {
		t.Fatalf("result = %+v", read)
	}
	if link := f.awaitDelivered(t, one.ID, 1); link.ReportDeliveredSeq != 1 {
		t.Fatalf("result did not deliver: %d", link.ReportDeliveredSeq)
	}
	waited := tool(t, mailbox, coordproto.Request{Type: coordproto.KindWait, Links: []string{string(two.ID)}, TimeoutSeconds: 5})
	if !waited.OK || len(waited.Reports) != 1 || waited.Reports[0].Summary != "two finished" {
		t.Fatalf("wait = %+v", waited)
	}
	if link := f.awaitDelivered(t, two.ID, 1); link.ReportDeliveredSeq != 1 {
		t.Fatalf("wait did not deliver: %d", link.ReportDeliveredSeq)
	}
	// Status and list never count as reading a report.
	f.runtime.finishTurn(lead.Intent.RunID, "lead-2", "read both")
	f.runtime.finishTurn(oneThread.CurrentRunID, "one-2", "one again")
	f.service.CoordinationTick(asSystem(f.ctx))
	_ = tool(t, mailbox, coordproto.Request{Type: coordproto.KindStatus, Link: string(one.ID)})
	_ = tool(t, mailbox, coordproto.Request{Type: coordproto.KindList})
	f.settleWakes()
	link, _ := f.service.GetThreadLink(f.ctx, one.ID)
	if link.ReportSeq != 2 || link.ReportHeadlinedSeq != 2 || link.ReportDeliveredSeq != 1 {
		t.Fatalf("after status and list = seq %d, headline %d, delivered %d",
			link.ReportSeq, link.ReportHeadlinedSeq, link.ReportDeliveredSeq)
	}
}
