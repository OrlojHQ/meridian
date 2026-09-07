package harnesssetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverSanitizesAllHarnesses(t *testing.T) {
	cases := []struct{ name, path, content, want string }{
		{"codex", ".codex/config.toml", "model = 'example'\n[projects.test]\ntrust_level = 'trusted'\n", "example"},
		{"claude", ".claude/settings.json", `{"model":"example","env":{"ANTHROPIC_API_KEY":"sensitive-value"}}`, "example"},
		{"opencode", ".config/opencode/opencode.jsonc", "{// comment\n\"model\":\"openai/example\",\"mcp\":{\"local\":{\"url\":\"http://localhost:3000\"}}}", "openai/example"},
		{"pi", ".pi/agent/settings.json", `{"defaultModel":"example","defaultProvider":"openai"}`, "example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			p := filepath.Join(home, tc.path)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			preview, err := Discover(home, tc.name, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Bundle.Files) != 1 {
				t.Fatalf("preview: %#v", preview)
			}
			content := preview.Bundle.Files[0].Content
			if !strings.Contains(content, tc.want) || strings.Contains(content, "sensitive-value") || strings.Contains(content, "trust_level") {
				t.Fatalf("unsafe or incomplete import: %s", content)
			}
			if err := preview.Bundle.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestImportNeverFollowsSymlinksOrReadsAuth(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".codex")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"access_token":"never-copy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("never-copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills", "escape.md")); err != nil {
		t.Fatal(err)
	}
	preview, err := Discover(home, "codex", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Bundle.Files) != 0 || len(preview.Issues) != 2 {
		t.Fatalf("unexpected preview %#v", preview)
	}
}
func TestBundleRejectsUnsafePathsAndCredentials(t *testing.T) {
	for _, p := range []string{"../escape", "/etc/passwd", ".codex/skills/../../auth.json", ".codex/auth.json", ".codex/skills/.env", ".codex/skills/key.pem"} {
		if err := (Bundle{Harness: "codex", Files: []File{{Path: p, Content: "x"}}}).Validate(); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
	b := Bundle{Harness: "claude", Files: []File{{Path: ".claude/settings.json", Content: `{"env":{"KEY":"arbitrary-secret"}}`}}}
	if b.Validate() == nil {
		t.Fatal("API validation bypasses importer")
	}
}

func TestPortablePathReferencesAreRelocated(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".codex")
	if err := os.MkdirAll(filepath.Join(root, "skills", "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "review", "SKILL.md"), []byte("Review carefully"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "model_instructions_file = '" + filepath.Join(root, "skills", "review", "SKILL.md") + "'\n"
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := Discover(home, "codex", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Files) != 2 {
		t.Fatalf("missing files: %#v", preview)
	}
	content := preview.Bundle.Files[0].Content
	if !strings.Contains(content, HomeReference) {
		t.Fatal("path was not relocated", content)
	}
	expanded, err := ExpandConfigHome(".codex/config.toml", content, "/private/capsule/home")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expanded, "/private/capsule/home/.codex/skills/review/SKILL.md") || strings.Contains(expanded, home) {
		t.Fatal("incorrect destination", expanded)
	}
}
