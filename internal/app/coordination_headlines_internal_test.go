package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func headlineLinks(count, summaryBytes int) ([]domain.ThreadLink, func(domain.ThreadLink) coordproto.Report) {
	links := make([]domain.ThreadLink, count)
	for index := range links {
		links[index] = domain.ThreadLink{
			ID: domain.ThreadLinkID(fmt.Sprintf("link-%02d", index)), Name: fmt.Sprintf("sub-%02d", index),
			ReportSeq: 1, FilesChanged: 3, Insertions: 40, Deletions: 2,
		}
	}
	report := func(link domain.ThreadLink) coordproto.Report {
		return coordproto.Report{
			Link: string(link.ID), Name: link.Name, Seq: link.ReportSeq, Status: coordproto.ReportDone,
			Summary: "BEGIN-" + link.Name + " " + strings.Repeat("é", summaryBytes/2),
		}
	}
	return links, report
}

func TestWakeHeadlineCutsEachSummaryAt2KiB(t *testing.T) {
	links, report := headlineLinks(1, 10<<10)
	text, included, ids := wakeHeadlines(links, report)
	if len(ids) != 1 || included[links[0].ID] != 1 {
		t.Fatalf("included = %v", included)
	}
	if !strings.Contains(text, `--- subagent "sub-00" (link link-00), report 1: done ---`) ||
		!strings.Contains(text, "diffstat: 3 files changed, +40 -2") ||
		!strings.Contains(text, "[headline cut at 2 KiB; read the full report with result]") ||
		!strings.Contains(text, "untrusted output") {
		t.Fatalf("headline = %q", text)
	}
	if summary := strings.Count(text, "é") * len("é"); summary > coordinationHeadlineBytes {
		t.Fatalf("headline carries %d summary bytes", summary)
	}
	short, shortReport := headlineLinks(1, 100)
	text, _, _ = wakeHeadlines(short, shortReport)
	if strings.Contains(text, "headline cut") {
		t.Fatalf("short report marked cut: %q", text)
	}
}

func TestWakeHeadlinesAreBoundedInCountAndBytes(t *testing.T) {
	// Short reports: the item cap binds.
	links, report := headlineLinks(20, 64)
	text, included, _ := wakeHeadlines(links, report)
	if len(included) != coordinationWakeItems {
		t.Fatalf("included %d headlines, want %d", len(included), coordinationWakeItems)
	}
	if !strings.Contains(text, "4 more new reports did not fit here") {
		t.Fatalf("overflow line missing: %q", text)
	}
	for _, link := range links[coordinationWakeItems:] {
		if _, ok := included[link.ID]; ok || strings.Contains(text, "BEGIN-"+link.Name) {
			t.Fatalf("headline past the item cap: %s", link.Name)
		}
	}

	// Long reports: the byte cap binds first.
	links, report = headlineLinks(12, 4<<10)
	text, included, _ = wakeHeadlines(links, report)
	if len(text) > coordinationWakeBytes || len(included) == 0 || len(included) >= len(links) {
		t.Fatalf("wake = %d bytes with %d of %d headlines", len(text), len(included), len(links))
	}
	if !strings.Contains(text, fmt.Sprintf("%d more new reports", len(links)-len(included))) {
		t.Fatalf("overflow line missing: %q", text[len(text)-300:])
	}
}

func TestWakeListsHeadlinedButUnreadReportsWithoutRepeatingThem(t *testing.T) {
	links, short := headlineLinks(4, 64)
	_, long := headlineLinks(4, 4<<10)
	report := func(link domain.ThreadLink) coordproto.Report {
		if link.Name == "sub-00" {
			return long(link)
		}
		return short(link)
	}
	links[0].ReportHeadlinedSeq = 1 // shown cut in an earlier wake, not read
	links[1].ReportDeliveredSeq = 1 // read with result
	links[3].ReportHeadlinedSeq = 1 // shown whole in an earlier wake
	text, included, _ := wakeHeadlines(links, report)
	if len(included) != 1 || included[links[2].ID] != 1 {
		t.Fatalf("included = %v", included)
	}
	if strings.Contains(text, "BEGIN-sub-00") || strings.Contains(text, "BEGIN-sub-01") ||
		strings.Contains(text, "BEGIN-sub-03") ||
		!strings.Contains(text, `Still unread from earlier wakes: "sub-00".`) {
		t.Fatalf("wake = %q", text)
	}
	if reportNeedsHeadline(links[0]) || reportNeedsHeadline(links[1]) || !reportNeedsHeadline(links[2]) {
		t.Fatal("reportNeedsHeadline disagrees with the headline and delivery marks")
	}
	links[0].ReportSeq = 2 // a new report on a headlined link is new again
	if !reportNeedsHeadline(links[0]) {
		t.Fatal("a later report on a headlined link needs a headline")
	}
}
