package harnessadapter

import (
	"slices"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

func TestCoordinationBriefFollowsRole(t *testing.T) {
	lead := coordinationBrief(adapterproto.CoordinationMetadata(adapterproto.CoordinationLead), coordinationMCP)
	if !strings.Contains(lead, "`meridian_spawn`") || !strings.Contains(lead, "end your turn") {
		t.Fatalf("lead brief = %q", lead)
	}
	cli := coordinationBrief(adapterproto.CoordinationMetadata(adapterproto.CoordinationLead), coordinationCLI)
	if !strings.Contains(cli, "`meridian-coord spawn`") ||
		!strings.Contains(cli, "`meridian-coord propose-delivery`") || strings.Contains(cli, "meridian_") {
		t.Fatalf("CLI lead brief = %q", cli)
	}
	for vocabulary, options := range map[coordinationVocabulary][2]string{
		coordinationMCP: {"`toFile: true`", "`contextFiles`"}, coordinationCLI: {"`--to-file`", "`--context-file`"},
	} {
		brief := coordinationBrief(adapterproto.CoordinationMetadata(adapterproto.CoordinationLead), vocabulary)
		for _, want := range []string{
			options[0], options[1], "instead of retyping", "headlines", vocabulary.call("result"), vocabulary.call("status"),
			"`$HOME/.meridian/coord/`", "`tail`", "reviewer subagent", "`$HOME/.meridian/notes/`",
		} {
			if !strings.Contains(brief, want) {
				t.Fatalf("lead brief (%d) lacks %q", vocabulary, want)
			}
		}
	}
	sub := coordinationBrief(adapterproto.CoordinationMetadata(adapterproto.CoordinationSubagent), coordinationMCP)
	if !strings.Contains(sub, "`meridian_report`") || strings.Contains(sub, "meridian_spawn") ||
		!strings.Contains(sub, "do not repeat the summary") {
		t.Fatalf("subagent brief = %q", sub)
	}
	if !strings.Contains(lead, "not in its final response") {
		t.Fatalf("lead brief does not steer results into the report: %q", lead)
	}
	for _, metadata := range []string{"", `{}`, `{"coordination":"admin"}`, `{"coordination":1}`, `[]`} {
		if brief := coordinationBrief([]byte(metadata), coordinationMCP); brief != "" {
			t.Fatalf("metadata %q gave brief %q", metadata, brief)
		}
	}
}

func TestClaudeCommandAppendsBrief(t *testing.T) {
	config := ClaudeConfig{Command: []string{"/usr/local/bin/claude"}}
	plain, err := claudeCommand(config, "", "")
	if err != nil || slices.Contains(plain, "--append-system-prompt") {
		t.Fatalf("plain command = %q, %v", plain, err)
	}
	briefed, err := claudeCommand(config, "session-1", "BRIEF")
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(briefed, "--append-system-prompt")
	if index < 0 || briefed[index+1] != "BRIEF" || !slices.Contains(briefed, "--resume") {
		t.Fatalf("briefed command = %q", briefed)
	}
}

func TestCodexThreadParamsCarryBrief(t *testing.T) {
	session := &codexSession{config: CodexConfig{ApprovalPolicy: "untrusted"}}
	if _, ok := session.threadParams()["developerInstructions"]; ok {
		t.Fatal("plain Thread sent developer instructions")
	}
	session.brief = "BRIEF"
	if got := session.threadParams()["developerInstructions"]; got != "BRIEF" {
		t.Fatalf("developerInstructions = %v", got)
	}
}

func TestPiCommandAppendsBrief(t *testing.T) {
	config := PiConfig{Command: []string{"/usr/local/bin/pi", "--skill", "x"}, SessionDir: "/sessions"}
	if command := piCommand(config, ""); slices.Contains(command, "--append-system-prompt") {
		t.Fatalf("plain command = %q", command)
	}
	command := piCommand(config, "BRIEF")
	if index := slices.Index(command, "--append-system-prompt"); index < 0 || command[index+1] != "BRIEF" {
		t.Fatalf("briefed command = %q", command)
	}
}

func TestACPBriefTrailsTheFirstPromptOnly(t *testing.T) {
	session := &acpSession{brief: "BRIEF"}
	blocks, err := session.promptBlocks(adapterproto.Frame{Content: "/review now"})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0]["text"] != "/review now" ||
		!strings.Contains(blocks[1]["text"].(string), "BRIEF") {
		t.Fatalf("blocks = %v", blocks)
	}
	// startTurn clears the brief once the prompt is sent.
	session.brief = ""
	blocks, err = session.promptBlocks(adapterproto.Frame{Content: "next"})
	if err != nil || len(blocks) != 1 {
		t.Fatalf("later blocks = %v, %v", blocks, err)
	}
}

func TestLeadBriefIncludesWorkerDefaultsAndAvailableModels(t *testing.T) {
	metadata := adapterproto.WithLeadWorkerBrief(
		adapterproto.CoordinationMetadata(adapterproto.CoordinationLead),
		&adapterproto.LeadWorkerBrief{
			AllowedHarnesses: []string{"claude", "codex"},
			Mode:             "custom",
			DefaultHarness:   "claude",
			DefaultModel:     "sonnet",
			DefaultLabel:     "Sonnet 4.6",
			AvailableModels: []adapterproto.WorkerHarnessModels{
				{Harness: "claude", Models: []string{"opus", "sonnet", "haiku"}},
				{Harness: "codex"},
			},
		},
	)
	mcpBrief := coordinationBrief(metadata, coordinationMCP)
	for _, want := range []string{
		"Allowed harnesses: claude, codex.",
		"Custom (`sonnet` / Sonnet 4.6 on `claude`, model's default effort)",
		"Models for `claude`: opus, sonnet, haiku.",
		"Models for `codex`: not reported in this Project yet",
		"`harness` and `model` are optional",
	} {
		if !strings.Contains(mcpBrief, want) {
			t.Fatalf("MCP lead brief missing %q:\n%s", want, mcpBrief)
		}
	}
	cliBrief := coordinationBrief(metadata, coordinationCLI)
	if !strings.Contains(cliBrief, "`--harness` and `--model` are optional") {
		t.Fatalf("CLI lead brief missing CLI flags:\n%s", cliBrief)
	}
}
