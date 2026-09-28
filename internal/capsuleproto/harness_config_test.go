package capsuleproto

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/harness"
)

const nativeHarnessFixture = `version: v1
harnesses:
  - name: %s
    interactionMode: native
    executable: /usr/local/bin/%s
    workingDirectory: .
    promptMode: interactive
    outputMode: text
    pty: true
    timeout: 1h
    secretReferences: []
`

func TestLoadHarnessConfigMergesTrustedAndRepositoryProfiles(t *testing.T) {
	root := t.TempDir()
	trusted := filepath.Join(root, "trusted")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".meridian"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(trusted, "opencode.yaml"),
		[]byte(fmt.Sprintf(nativeHarnessFixture, "opencode", "opencode")),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, ".meridian", "project.yaml"),
		[]byte(fmt.Sprintf(nativeHarnessFixture, "custom", "custom")),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{config: ServerConfig{
		Workspace: workspace, TrustedHarnessDirectory: trusted,
	}}
	config, err := server.loadHarnessConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Harnesses) != 2 ||
		config.Harnesses[0].Name != "opencode" ||
		config.Harnesses[1].Name != "custom" {
		t.Fatalf("merged profiles = %#v", config.Harnesses)
	}
}

func TestLoadHarnessConfigRejectsRepositoryShadowingTrustedProfile(t *testing.T) {
	root := t.TempDir()
	trusted := filepath.Join(root, "trusted")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".meridian"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	value := []byte(fmt.Sprintf(nativeHarnessFixture, "opencode", "opencode"))
	if err := os.WriteFile(filepath.Join(trusted, "opencode.yaml"), value, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".meridian", "project.yaml"), value, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{config: ServerConfig{
		Workspace: workspace, TrustedHarnessDirectory: trusted,
	}}
	_, err := server.loadHarnessConfig()
	if err == nil || !strings.Contains(err.Error(), `duplicate harness profile name "opencode"`) {
		t.Fatalf("duplicate profile error = %v", err)
	}
}

// officialPacks returns the images/capsule-<pack> directories that the shared
// images/harness-pack/Dockerfile builds, keyed by pack name.
func officialPacks(t *testing.T) map[string]string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "..", "images", "capsule-*", "pack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no official pack data files")
	}
	packs := make(map[string]string, len(matches))
	for _, match := range matches {
		root := filepath.Dir(match)
		packs[strings.TrimPrefix(filepath.Base(root), "capsule-")] = root
	}
	return packs
}

// packExecutable returns where images/harness-pack/install puts the pinned
// harness binary for a pack.
func packExecutable(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "pack.env"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for line := range strings.Lines(string(data)) {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && !strings.HasPrefix(key, "#") {
			values[key] = value
		}
	}
	if values["PACK_LAYOUT"] == "tree" {
		return "/usr/local/libexec/" + name + "/" + values["PACK_ENTRY"]
	}
	return "/usr/local/libexec/" + name
}

func TestOfficialPacksInstallNativePTYProfiles(t *testing.T) {
	for name, root := range officialPacks(t) {
		if output, err := exec.Command("sh", filepath.Join("..", "..", "images", "harness-pack", "install"),
			"--check", root).CombinedOutput(); err != nil {
			t.Fatalf("%s pack.env: %v: %s", name, err, output)
		}
		file, err := os.Open(filepath.Join(root, "project.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		config, err := harness.Parse(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("%s profile: %v", name, err)
		}
		profile, err := config.Profile(name)
		if err != nil {
			t.Fatal(err)
		}
		if profile.Interaction != harness.InteractionNative || !profile.PTY ||
			profile.Prompt != harness.PromptInteractive ||
			profile.Executable != "/usr/local/bin/"+name {
			t.Fatalf("%s profile = %#v", name, profile)
		}
	}
}

func TestOfficialPackLaunchersInvokePinnedBinary(t *testing.T) {
	for name, root := range officialPacks(t) {
		launcher := filepath.Join(root, name)
		value, err := os.ReadFile(launcher)
		if os.IsNotExist(err) {
			// The install script symlinks /usr/local/bin/<name> to the binary.
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if want := "exec " + packExecutable(t, root, name) + ` "$@"`; !strings.Contains(string(value), want) {
			t.Fatalf("%s launcher does not %q: %s", name, want, value)
		}
		if output, err := exec.Command("sh", "-n", launcher).CombinedOutput(); err != nil {
			t.Fatalf("%s launcher syntax: %v: %s", name, err, output)
		}
	}
}

func TestOfficialBunLaunchersUseExecutablePrivateTemp(t *testing.T) {
	// Bun extracts native libraries before loading them, and /tmp is noexec.
	for _, name := range []string{"opencode", "pi", "claude"} {
		value, err := os.ReadFile(filepath.Join("..", "..", "images", "capsule-"+name, name))
		if err != nil {
			t.Fatal(err)
		}
		if want := `BUN_TMPDIR="${BUN_TMPDIR:-/home/capsule/.cache/` + name + `/bun-tmp}"`; !strings.Contains(string(value), want) {
			t.Fatalf("%s launcher does not redirect Bun's native library temp directory: %s", name, value)
		}
	}
}

func TestOfficialClaudeLauncherDisablesSelfUpdate(t *testing.T) {
	value, err := os.ReadFile(filepath.Join("..", "..", "images", "capsule-claude", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(value), `DISABLE_UPDATES="${DISABLE_UPDATES:-1}"`) {
		t.Fatalf("Claude launcher does not pin updates: %s", value)
	}
}

func TestOfficialPackDockerfileUsesConfigurableBase(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "images", "harness-pack", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	if !strings.Contains(text, "ARG CAPSULE_BASE=meridian-capsule:dev") ||
		!strings.Contains(text, "FROM ${CAPSULE_BASE}") {
		t.Fatal("pack Dockerfile does not accept a published Capsule base")
	}
}

func TestPackInstallCheckRejectsUnsafeData(t *testing.T) {
	valid := map[string]string{
		"PACK_NAME":         "demo",
		"PACK_VERSION":      "1.2.3",
		"PACK_URL":          "https://example.test/v{version}/demo-{arch}.tar.gz",
		"PACK_ARCH_AMD64":   "x64",
		"PACK_ARCH_ARM64":   "arm64",
		"PACK_SHA256_AMD64": strings.Repeat("a", 64),
		"PACK_SHA256_ARM64": strings.Repeat("b", 64),
		"PACK_LAYOUT":       "file",
		"PACK_ENTRY":        "demo",
	}
	check := func(values map[string]string, extra string) ([]byte, error) {
		root := filepath.Join(t.TempDir(), "capsule-demo")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		var data strings.Builder
		for key, value := range values {
			fmt.Fprintf(&data, "%s=%s\n", key, value)
		}
		data.WriteString(extra)
		if err := os.WriteFile(filepath.Join(root, "pack.env"), []byte(data.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "project.yaml"), []byte(fmt.Sprintf(nativeHarnessFixture, "demo", "demo")), 0o600); err != nil {
			t.Fatal(err)
		}
		return exec.Command("sh", filepath.Join("..", "..", "images", "harness-pack", "install"), "--check", root).CombinedOutput()
	}
	if output, err := check(valid, ""); err != nil {
		t.Fatalf("valid pack rejected: %v: %s", err, output)
	}
	for name, change := range map[string][2]string{
		"unknown key":      {"PACK_EXTRA", "1"},
		"name mismatch":    {"PACK_NAME", "other"},
		"http url":         {"PACK_URL", "http://example.test/demo-{arch}.tar.gz"},
		"no arch":          {"PACK_URL", "https://example.test/demo.tar.gz"},
		"shell in url":     {"PACK_URL", "https://example.test/$(id)-{arch}.tar.gz"},
		"short checksum":   {"PACK_SHA256_AMD64", "abc"},
		"unknown layout":   {"PACK_LAYOUT", "zip"},
		"file entry path":  {"PACK_ENTRY", "bin/demo"},
		"tree dot segment": {"PACK_ENTRY", "../demo"},
		"version space":    {"PACK_VERSION", "1 2"},
	} {
		values := maps.Clone(valid)
		values[change[0]] = change[1]
		if name == "tree dot segment" {
			values["PACK_LAYOUT"] = "tree"
		}
		if _, err := check(values, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := check(valid, "not a key value line\n"); err == nil {
		t.Error("malformed line: accepted")
	}
}
