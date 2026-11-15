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
		{"dsh", ".dsh/settings.yaml", "model: example\napiKey: sensitive-value\n", "example"},
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

func TestDiscoverDeepSeekHarnessHomeAndExclusions(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(filepath.Join(root, "profiles", "acp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skills", "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "settings.yaml"), []byte("displayName: example\napiKey: sensitive-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".credentials.yaml"), []byte("token: never-copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cordis.patch.yml"), []byte("- id: acp\n  config:\n    displayName: example\n    apiKey: sensitive-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles", "acp", "cordis.patch.yml"), []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "review", "SKILL.md"), []byte("Review carefully"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "evil.yml"), []byte("x: !!js '1+1'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := Discover(home, "dsh", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Bundle.Files) != 4 {
		t.Fatalf("preview files=%#v issues=%#v", preview.Files, preview.Issues)
	}
	joined := strings.Join(preview.Files, "\n")
	if !strings.Contains(joined, ".dsh/settings.yaml") || !strings.Contains(joined, ".dsh/cordis.patch.yml") || !strings.Contains(joined, ".dsh/profiles/acp/cordis.patch.yml") || !strings.Contains(joined, ".dsh/skills/review/SKILL.md") {
		t.Fatalf("missing portable files: %s", joined)
	}
	raw := ""
	for _, file := range preview.Bundle.Files {
		raw += file.Content
	}
	if strings.Contains(raw, "sensitive-value") || strings.Contains(raw, "never-copy") || strings.Contains(raw, "!!js") {
		t.Fatalf("unsafe import: %s", raw)
	}
	if err := preview.Bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(override, "settings.yaml"), []byte("model: relocated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	relocated, err := Discover(home, "dsh", func(name string) string {
		if name == "DSH_HOME" {
			return override
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(relocated.Bundle.Files) != 1 || !strings.Contains(relocated.Bundle.Files[0].Content, "relocated") {
		t.Fatalf("DSH_HOME ignored: %#v", relocated)
	}
}

func TestDiscoverRejectsDeepSeekExecutableYAML(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".dsh", "settings.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x: !!js 'process.exit(0)'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := Discover(home, "dsh", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Bundle.Files) != 0 || len(preview.Issues) != 1 {
		t.Fatalf("!!js accepted: %#v", preview)
	}
}

// Codex's bundled skills ship with the harness: an import skips them with
// one line, before they count toward the limit, and personal skills next to
// them are kept. Saved revisions that contain them still validate.
func TestImportSkipsSkillsTheHarnessShips(t *testing.T) {
	home := t.TempDir()
	for path, content := range map[string]string{
		".codex/skills/.system/imagegen/SKILL.md":    strings.Repeat("x", 100<<10),
		".codex/skills/.system/imagegen/ref/more.md": strings.Repeat("y", 100<<10),
		".codex/skills/.system/plan/SKILL.md":        strings.Repeat("z", 100<<10),
		".codex/skills/.system/doc/SKILL.md":         strings.Repeat("w", 100<<10),
		".codex/skills/.system/fmt/SKILL.md":         strings.Repeat("v", 100<<10),
		".codex/skills/release/SKILL.md":             "ship it",
	} {
		full := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := Discover(home, "codex", func(string) string { return "" })
	if err != nil {
		t.Fatalf("bundled skills counted toward the limit: %v", err)
	}
	if len(preview.Files) != 1 || preview.Files[0] != ".codex/skills/release/SKILL.md" {
		t.Fatalf("files = %v", preview.Files)
	}
	skipped := 0
	for _, issue := range preview.Issues {
		if issue.Reason == bundledReason {
			skipped++
			if issue.Path != ".codex/skills/.system" {
				t.Fatalf("skipped path = %q", issue.Path)
			}
		}
	}
	if skipped != 1 {
		t.Fatalf("issues = %+v", preview.Issues)
	}

	upload, err := ReviewFiles("codex", []File{
		{Path: ".codex/skills/.system/imagegen/SKILL.md", Content: strings.Repeat("x", 120<<10)},
		{Path: ".codex/skills/.system/plan/SKILL.md", Content: strings.Repeat("x", 120<<10)},
		{Path: ".codex/skills/.system/doc/SKILL.md", Content: strings.Repeat("x", 120<<10)},
		{Path: ".codex/skills/.system/fmt/SKILL.md", Content: strings.Repeat("x", 120<<10)},
		{Path: ".codex/skills/.system/zip/SKILL.md", Content: strings.Repeat("x", 120<<10)},
		{Path: ".codex/skills/release/SKILL.md", Content: "ship it"},
	})
	if err != nil || len(upload.Files) != 1 || len(upload.Issues) != 1 || upload.Issues[0].Reason != bundledReason {
		t.Fatalf("upload review = %+v, %v", upload, err)
	}

	old := Bundle{Harness: "codex", Files: []File{{Path: ".codex/skills/.system/plan/SKILL.md", Content: "saved before"}}}
	if err := old.Validate(); err != nil {
		t.Fatalf("a revision saved with bundled skills no longer validates: %v", err)
	}
}
