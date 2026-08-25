package capsuleproto

import (
	"fmt"
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

func TestOfficialOpenCodeManifestIsNativePTY(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "images", "capsule-opencode", "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config, err := harness.Parse(file)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.Profile("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Interaction != harness.InteractionNative || !profile.PTY ||
		profile.Prompt != harness.PromptInteractive ||
		profile.Executable != "/usr/local/bin/opencode" {
		t.Fatalf("OpenCode profile = %#v", profile)
	}
}

func TestOfficialOpenCodeLauncherUsesExecutablePrivateTemp(t *testing.T) {
	root := filepath.Join("..", "..", "images", "capsule-opencode")
	launcher := filepath.Join(root, "opencode")
	value, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	text := string(value)
	if !strings.Contains(text, `BUN_TMPDIR="${BUN_TMPDIR:-/home/capsule/.cache/opencode/bun-tmp}"`) ||
		!strings.Contains(text, "exec /usr/local/libexec/opencode") {
		t.Fatalf("OpenCode launcher does not redirect Bun's native library temp directory: %s", text)
	}
	if output, err := exec.Command("sh", "-n", launcher).CombinedOutput(); err != nil {
		t.Fatalf("OpenCode launcher syntax: %v: %s", err, output)
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "/usr/local/libexec/opencode") ||
		!strings.Contains(string(dockerfile), "COPY --chmod=0755 images/capsule-opencode/opencode /usr/local/bin/opencode") {
		t.Fatal("OpenCode image does not install the launcher in front of the pinned binary")
	}
}
