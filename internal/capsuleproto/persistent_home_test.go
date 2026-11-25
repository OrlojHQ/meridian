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

func TestSetupRewritesMeridianOwnedFilesAndPreservesSessionState(t *testing.T) {
	home := t.TempDir()
	sessionDir := t.TempDir()
	workspace := t.TempDir()
	server, err := NewServer(ServerConfig{
		Token:       strings.Repeat("a", 32),
		Workspace:   workspace,
		HarnessHome: home,
		SessionDir:  sessionDir,
	})
	if err != nil {
		t.Fatal(err)
	}

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
	if env["XDG_CACHE_HOME"] != "/tmp/meridian-cache" {
		t.Fatalf("XDG_CACHE_HOME = %q, want /tmp/meridian-cache", env["XDG_CACHE_HOME"])
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale setup dir survived: %v", err)
	}

	// Simulate a harness session file and a prompt-injected tamper of settings.json.
	setupHome := env["HOME"]
	sessionFile := filepath.Join(setupHome, ".claude", "projects", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, []byte(`{"turn":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(setupHome, ".claude", "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"hooks":{"evil":"cmd"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Next session start must heal Meridian-owned settings.json while keeping session.jsonl.
	if _, err := server.prepareHarnessSetup(context.Background(), setup, "claude-structured"); err != nil {
		t.Fatalf("second prepareHarnessSetup: %v", err)
	}
	gotSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotSettings), `"sonnet"`) || strings.Contains(string(gotSettings), "evil") {
		t.Fatalf("settings.json was not restored: %q", string(gotSettings))
	}
	gotSession, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("harness session file lost: %v", err)
	}
	if string(gotSession) != `{"turn":1}` {
		t.Fatalf("harness session content = %q", string(gotSession))
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
