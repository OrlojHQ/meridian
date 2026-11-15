package harnesssetup

import (
	"strings"
	"testing"
)

func TestReportIsBoundedDisplayText(t *testing.T) {
	ok := Report{Issues: []Issue{{Path: ".codex/auth.json", Reason: "excluded"}}, Warnings: []Issue{{Path: "config.toml:mcp_servers:docs", Reason: "kept"}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	many := Report{}
	for range maxReportEntries + 1 {
		many.Issues = append(many.Issues, Issue{Path: "p", Reason: "r"})
	}
	for name, report := range map[string]Report{
		"too many":       many,
		"control path":   {Issues: []Issue{{Path: "a\x1b[2J", Reason: "r"}}},
		"newline reason": {Warnings: []Issue{{Path: "a", Reason: "r\nmore"}}},
		"empty reason":   {Issues: []Issue{{Path: "a"}}},
		"long path":      {Issues: []Issue{{Path: strings.Repeat("a", maxReportPath+1), Reason: "r"}}},
		"invalid utf8":   {Issues: []Issue{{Path: "\xff", Reason: "r"}}},
	} {
		if report.Validate() == nil {
			t.Fatalf("%s report was accepted", name)
		}
	}
	preview := Preview{Issues: []Issue{{Path: "x", Reason: "y"}}, Warnings: []Issue{}}
	if got := preview.Report(); len(got.Issues) != 1 || got.Warnings == nil {
		t.Fatalf("preview report = %+v", got)
	}
}

func TestReportRedactsHomePaths(t *testing.T) {
	report := Report{
		Issues: []Issue{
			{Path: ".codex/config.toml:projects:/Users/ada/Documents/acme-secret:trust_level", Reason: "Machine trust or approval override excluded"},
			{Path: ".codex/auth.json", Reason: "Authentication, runtime state, or unsupported file excluded"},
		},
		Warnings: []Issue{
			{Path: ".claude/settings.json:hooks", Reason: "Absolute path /home/ada/bin/hook kept"},
			{Path: `.pi/agent/settings.json:C:\Users\Ada\repo:trust`, Reason: "kept"},
		},
	}
	got := report.Redacted()
	for _, entry := range append(got.Issues, got.Warnings...) {
		for _, leak := range []string{"ada", "Ada", "acme-secret", "/Users/", "/home/"} {
			if strings.Contains(entry.Path+entry.Reason, leak) && !strings.HasPrefix(entry.Path, ".codex/auth.json") {
				t.Fatalf("redacted entry %+v still contains %q", entry, leak)
			}
		}
	}
	if got.Issues[0].Path != ".codex/config.toml:projects:~/…:trust_level" || got.Issues[1].Path != ".codex/auth.json" {
		t.Fatalf("redacted issues = %+v", got.Issues)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}
