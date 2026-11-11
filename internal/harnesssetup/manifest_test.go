package harnesssetup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestShippedManifestsLoadForEveryPack(t *testing.T) {
	set := loadManifests()
	if len(set.errors) != 0 {
		t.Fatalf("shipped manifests are invalid: %v", set.errors)
	}
	packs, err := filepath.Glob(filepath.Join("..", "..", "images", "capsule-*", "pack.env"))
	if err != nil {
		t.Fatal(err)
	}
	var built []string
	for _, p := range packs {
		built = append(built, strings.TrimPrefix(filepath.Base(filepath.Dir(p)), "capsule-"))
	}
	slices.Sort(built)
	if !slices.Equal(built, Harnesses()) {
		t.Fatalf("packs %v but manifests %v; add images/capsule-<name>/manifest.yaml", built, Harnesses())
	}
}

// The API still enumerates harness names. A manifest without an enum entry
// can never be uploaded, and an enum entry without a manifest is refused.
func TestManifestsMatchAPIHarnessEnum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "enum: [" + strings.Join(Harnesses(), ", ") + "]"
	if strings.Count(string(raw), want) < 2 {
		t.Fatalf("api/openapi.yaml harness setup enums do not match manifests %q", want)
	}
}

func TestManifestRules(t *testing.T) {
	for _, tc := range []struct {
		harness, path string
		allowed       bool
	}{
		{"codex", ".codex/config.toml", true},
		{"codex", ".codex/skills/review/SKILL.md", true},
		{"codex", ".agents/skills/review/SKILL.md", true},
		{"codex", ".codex/auth.json", false},
		{"codex", ".codex/sessions/today.jsonl", false},
		{"codex", ".codex/skills/x/.env", false},
		{"claude", ".claude.json", true},
		{"claude", ".claude/hooks/pre.sh", true},
		{"claude", ".claude/.credentials.json", false},
		{"claude", ".agents/skills/review/SKILL.md", false},
		{"opencode", ".config/opencode/opencode.jsonc", true},
		{"opencode", ".config/opencode/node_modules/x.js", false},
		{"pi", ".pi/agent/models.json", true},
		{"dsh", ".dsh/profiles/work/settings.yaml", true},
		{"dsh", ".dsh/cache/x", false},
		{"mock", ".codex/config.toml", false},
	} {
		if got := AllowedPath(tc.harness, tc.path); got != tc.allowed {
			t.Errorf("AllowedPath(%s, %s) = %v", tc.harness, tc.path, got)
		}
	}
	for p, want := range map[string]bool{
		".codex/config.toml":               true,
		".claude.json":                     true,
		".dsh/profiles/work/settings.yaml": true,
		".codex/AGENTS.md":                 false,
		".pi/agent/settings.json":          true,
	} {
		if IsConfig(p) != want {
			t.Errorf("IsConfig(%s) = %v", p, !want)
		}
	}
	if !ConnectionSupported("codex", "chatgpt", "") || ConnectionSupported("claude", "openai", "") {
		t.Fatal("connection providers do not follow the manifest")
	}
	if ConnectionSupported("pi", "openai-compatible", "") || !ConnectionSupported("pi", "openai-compatible", "m") {
		t.Fatal("requiresModel is not enforced")
	}
	env := CapsuleEnvironment("/h")
	if env["CODEX_HOME"] != "/h/.codex" || env["DSH_HOME"] != "/h/.dsh" || env["PI_CODING_AGENT_DIR"] != "/h/.pi/agent" {
		t.Fatalf("capsule environment: %v", env)
	}
	if _, set := env["OPENCODE_CONFIG_DIR"]; set {
		t.Fatal("an XDG root must rely on XDG_CONFIG_HOME")
	}
}

func TestInvalidManifestsFailClosed(t *testing.T) {
	valid := "version: v1\nname: demo\nroot: {path: .demo, env: DEMO_HOME}\npaths: [config.toml, skills/]\nconfigFiles: [config.toml]\n"
	for name, body := range map[string]string{
		"unknown field":     valid + "extra: true\n",
		"version":           strings.Replace(valid, "v1", "v2", 1),
		"name mismatch":     strings.Replace(valid, "name: demo", "name: other", 1),
		"root escape":       strings.Replace(valid, "path: .demo", "path: ../demo", 1),
		"absolute root":     strings.Replace(valid, "path: .demo", "path: /etc", 1),
		"denied root":       strings.Replace(valid, "path: .demo", "path: .git", 1),
		"path escape":       strings.Replace(valid, "skills/", "../skills/", 1),
		"denied path":       strings.Replace(valid, "skills/", "auth.json", 1),
		"denied key":        strings.Replace(valid, "skills/", "keys/id.pem", 1),
		"reserved env":      strings.Replace(valid, "DEMO_HOME", "LD_PRELOAD", 1),
		"path env":          strings.Replace(valid, "DEMO_HOME", "PATH", 1),
		"unlisted config":   strings.Replace(valid, "configFiles: [config.toml]", "configFiles: [other.toml]", 1),
		"config extension":  strings.Replace(valid, "[config.toml]\n", "[skills/]\n", 1),
		"nested home file":  valid + "homeFiles: [.demo/x.json]\n",
		"shared escape":     valid + "shared: [../skills]\n",
		"shared under root": valid + "shared: [.demo/skills]\n",
		"config instructs":  valid + "instructions: config.toml\n",
		"unknown provider":  valid + "connection: {providers: [{type: bedrock}]}\n",
		"xdg outside":       strings.Replace(valid, "env: DEMO_HOME", "env: DEMO_HOME, xdgConfig: true", 1),
	} {
		t.Run(name, func(t *testing.T) {
			set := parseManifests(fstest.MapFS{"capsule-demo/manifest.yaml": {Data: []byte(body)}})
			if len(set.errors) != 1 || len(set.byName) != 0 {
				t.Fatalf("accepted: %v", set.errors)
			}
		})
	}
	set := parseManifests(fstest.MapFS{"capsule-demo/manifest.yaml": {Data: []byte(valid)}})
	if len(set.errors) != 0 || !set.byName["demo"].allowed(".demo/skills/a.md") || set.byName["demo"].allowed(".demo/other") {
		t.Fatalf("valid manifest: %v", set.errors)
	}
	duplicate := parseManifests(fstest.MapFS{
		"capsule-demo/manifest.yaml":  {Data: []byte(valid)},
		"capsule-other/manifest.yaml": {Data: []byte(strings.Replace(valid, "name: demo", "name: other", 1))},
	})
	if len(duplicate.errors) != 1 || len(duplicate.byName) != 1 {
		t.Fatalf("two manifests claimed one root env: %v", duplicate.errors)
	}
}
