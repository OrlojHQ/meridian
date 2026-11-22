package app_test

import (
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// A harness ends a turn with a closing reply after it reports. That reply is
// part of the reported turn: it must not wake the lead a second time or
// replace the explicit status. A reply to a later prompt is a new report.
func TestExplicitReportSettlesItsTurn(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 2})
	one := f.spawnSubagent(t, mailbox, "one")
	child, err := f.service.GetCapsule(f.ctx, one.ChildCapsuleID)
	if err != nil {
		t.Fatal(err)
	}
	f.service.CoordinationTick(asSystem(f.ctx))
	childMail := f.runtime.mailbox(child.ProviderResourceID)
	childThread, _ := f.service.GetThread(f.ctx, one.ChildThreadID)
	f.runtime.finishTurn(lead.Intent.RunID, "lead-1", "spawned; waiting")

	if reported := tool(t, childMail, coordproto.Request{
		Type: coordproto.KindReport, Key: "story", Status: coordproto.ReportDone, Summary: "EXPLICIT-STORY",
	}); !reported.OK {
		t.Fatalf("report = %+v", reported)
	}
	f.runtime.finishTurn(childThread.CurrentRunID, "one-close", "CLOSING-REPLY: I sent the story")
	f.settleWakes()

	sent := f.leadMessages(lead.Intent.RunID)
	if len(sent) != 2 {
		t.Fatalf("lead messages = %d, want first prompt plus one wake", len(sent))
	}
	if wake := sent[1].Content; !strings.Contains(wake, "EXPLICIT-STORY") || strings.Contains(wake, "CLOSING-REPLY") {
		t.Fatalf("wake = %q", wake)
	}
	link, _ := f.service.GetThreadLink(f.ctx, one.ID)
	if link.ReportSeq != 1 || link.LastReportStatus != domain.ReportDone {
		t.Fatalf("after closing reply = seq %d, status %s", link.ReportSeq, link.LastReportStatus)
	}

	// The lead finishes summarizing; more scans still record nothing.
	f.runtime.finishTurn(lead.Intent.RunID, "lead-2", "here is the story")
	f.settleWakes()
	if sent := f.leadMessages(lead.Intent.RunID); len(sent) != 2 {
		t.Fatalf("lead messages = %d after more scans, want 2", len(sent))
	}

	// A follow-up reaches the subagent, so its next reply is a new report.
	if response := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSend, Key: "another", Link: string(one.ID), Prompt: "another story",
	}); !response.OK {
		t.Fatalf("send = %+v", response)
	}
	f.runtime.finishTurn(childThread.CurrentRunID, "one-2", "SECOND-STORY")
	f.settleWakes()
	link, _ = f.service.GetThreadLink(f.ctx, one.ID)
	if link.ReportSeq != 2 || link.LastReportStatus != domain.ReportTurnEnded {
		t.Fatalf("after follow-up = seq %d, status %s", link.ReportSeq, link.LastReportStatus)
	}
	sent = f.leadMessages(lead.Intent.RunID)
	if len(sent) != 3 || !strings.Contains(sent[2].Content, "SECOND-STORY") {
		t.Fatalf("lead messages after follow-up = %d", len(sent))
	}
	// The first report's whole headline was shown, so it is not "unread".
	if strings.Contains(sent[2].Content, "Still unread") {
		t.Fatalf("follow-up wake = %q", sent[2].Content)
	}
}
