package harnesssetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewSelectedFilesMatchesCLI(t *testing.T) {
	for _, tc := range []struct{ harness, path, content string }{
		{"opencode", ".config/opencode/opencode.jsonc", "{// comment\n\"model\":\"openai/test\",\"provider\":{\"openai\":{\"apiKey\":\"private-value\"}}}"},
		{"codex", ".codex/config.toml", "model='test'\n[projects.test]\ntrust_level='trusted'"},
		{"claude", ".claude/settings.json", `{"model":"test","env":{"ANTHROPIC_API_KEY":"private-value"}}`},
		{"pi", ".pi/agent/settings.json", `{"defaultModel":"test","defaultProvider":"openai"}`},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			home := t.TempDir()
			file := filepath.Join(home, tc.path)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			local, err := Discover(home, tc.harness, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			selected, err := ReviewFiles(tc.harness, []File{{Path: tc.path, Content: tc.content}})
			if err != nil {
				t.Fatal(err)
			}
			if local.Digest != selected.Digest {
				t.Fatalf("browser and CLI differ: %v vs %v", local.Bundle, selected.Bundle)
			}
			raw, _ := json.Marshal(selected.Bundle)
			if strings.Contains(string(raw), "private-value") {
				t.Fatal("preview leaked excluded credential")
			}
		})
	}
}
func TestReviewRejectsUnsupportedPathsAndWarnsAboutMissingReferences(t *testing.T) {
	result, err := ReviewFiles("opencode", []File{
		{Path: ".config/opencode/auth.json", Content: `{"secret":"never-return"}`},
		{Path: ".config/opencode/../private", Content: "never-return"},
		{Path: ".config/opencode/opencode.json", Content: `{"instructions":["~/.config/opencode/skills/missing/SKILL.md"],"mcp":{"test":{"type":"local","command":["npx","-y","example@1.2.3"]}}}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Bundle.Files) != 1 || len(result.Issues) != 2 || len(result.Warnings) == 0 || len(result.Dependencies) != 1 {
		t.Fatalf("preview=%#v", result)
	}
	raw, _ := json.Marshal(result.Bundle)
	if strings.Contains(string(raw), "never-return") {
		t.Fatal("excluded contents returned")
	}
	if !strings.Contains(result.Bundle.Files[0].Content, "~/.config/opencode/skills/missing/SKILL.md") {
		t.Fatal("missing path was not preserved")
	}
}
func TestReviewBoundedAndInvalidInputs(t *testing.T) {
	if _, err := ReviewFiles("opencode", make([]File, MaxFiles+1)); err == nil {
		t.Fatal("too many files accepted")
	}
	if _, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/AGENTS.md", Content: strings.Repeat("x", MaxBytes+1)}}); err == nil {
		t.Fatal("oversized input accepted")
	}
	result, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.json", Content: "invalid json"}})
	if err != nil || len(result.Bundle.Files) != 0 || len(result.Issues) != 1 {
		t.Fatalf("malformed preview=%#v err=%v", result, err)
	}
	file := File{Path: ".config/opencode/AGENTS.md", Content: "hello"}
	if _, err := ReviewFiles("opencode", []File{file, file}); err == nil {
		t.Fatal("duplicate accepted")
	}
}
