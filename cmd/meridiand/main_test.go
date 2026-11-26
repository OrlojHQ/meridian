package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestAgentSandboxProviderFlagsAreRegistered(t *testing.T) {
	command := newRootCommand()
	for _, name := range []string{
		"provider",
		"api-token-file",
		"allow-non-loopback-listen",
		"capsule-idle-pause",
		"capsule-idle-scan-interval",
		"runtime-refresh-interval",
		"agentsandbox-kubeconfig",
		"agentsandbox-context",
		"agentsandbox-in-cluster",
		"agentsandbox-namespace",
		"agentsandbox-name-prefix",
		"agentsandbox-image",
		"official-pack-tag",
		"agentsandbox-runtime-class",
		"agentsandbox-storage-class",
		"agentsandbox-volume-size",
		"agentsandbox-ttl",
		"agentsandbox-operation-timeout",
		"agentsandbox-setup-timeout",
		"metrics-listen",
		"allow-unsafe-metrics-listen",
		"otel-otlp-endpoint",
		"otel-otlp-insecure",
	} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s is not registered", name)
		}
	}
	provider := command.Flags().Lookup("provider")
	if provider == nil || provider.Usage != "Capsule provider (docker or agentsandbox)" ||
		provider.DefValue != "docker" {
		t.Fatalf("provider flag must default to docker and advertise every provider: %#v", provider)
	}
	image := command.Flags().Lookup("docker-image")
	if image == nil || image.DefValue != domain.LocalCapsuleImage {
		t.Fatalf("development docker-image default = %#v", image)
	}
}

func TestInitDataDirectoryRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "data")
	command := newRootCommand()
	command.SetArgs([]string{"init-data-dir", "--path", path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("initialized directory = %v, %v", info, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, path); err != nil {
		t.Fatal(err)
	}
	command = newRootCommand()
	command.SetArgs([]string{"init-data-dir", "--path", path})
	if err := command.Execute(); err == nil {
		t.Fatal("symlink data directory unexpectedly accepted")
	}
}

func TestInstallationTokenPrintsAnExistingTokenOnly(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	command := newRootCommand()
	command.SetArgs([]string{"installation-token", "--data-dir", dataDir})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err == nil {
		t.Fatal("missing token unexpectedly printed")
	}
	if _, err := os.Stat(apiauth.DefaultTokenPath(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("installation-token created a token: %v", err)
	}
	if _, err := apiauth.OpenOrCreateToken(apiauth.DefaultTokenPath(dataDir)); err != nil {
		t.Fatal(err)
	}
	want, err := apiauth.ReadTokenFile(apiauth.DefaultTokenPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command = newRootCommand()
	command.SetArgs([]string{"installation-token", "--data-dir", dataDir})
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if output.String() != want+"\n" {
		t.Fatalf("printed token does not match the stored token")
	}
}

// The desktop app's import mode is a hidden client mode: help and version
// stay as they were, and --check prints only a digest.
func TestDesktopImportIsHiddenAndCheckPrintsOnlyADigest(t *testing.T) {
	var help bytes.Buffer
	root := newRootCommand()
	root.SetOut(&help)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(help.String(), "desktop-import") || !strings.Contains(help.String(), "healthcheck") {
		t.Fatalf("help:\n%s", help.String())
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	for _, name := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "OPENCODE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "DSH_HOME"} {
		t.Setenv(name, "")
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "AGENTS.md"), []byte("secret-looking canary text"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root = newRootCommand()
	root.SetOut(&out)
	root.SetArgs([]string{"desktop-import", "--check"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	digest, ok := result["digest"].(string)
	if len(result) != 1 || !ok || len(digest) != 64 || strings.Contains(out.String(), "canary") || strings.Contains(out.String(), "AGENTS") {
		t.Fatalf("check output = %s", out.String())
	}
}
