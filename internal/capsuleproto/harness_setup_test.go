package capsuleproto

import (
	"context"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessSetupPrivateHomesAndRehydrate(t *testing.T) {
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-1", Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "personal instructions"}}}}
	a := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	b := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	envA, err := a.prepareHarnessSetup(context.Background(), setup, "codex")
	if err != nil {
		t.Fatal(err)
	}
	envB, err := b.prepareHarnessSetup(context.Background(), setup, "codex")
	if err != nil {
		t.Fatal(err)
	}
	pathA := filepath.Join(envA["HOME"], ".codex", "AGENTS.md")
	if err := os.WriteFile(pathA, []byte("session edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareHarnessSetup(context.Background(), setup, "codex"); err != nil {
		t.Fatal(err)
	}
	own, _ := os.ReadFile(pathA)
	other, _ := os.ReadFile(filepath.Join(envB["HOME"], ".codex", "AGENTS.md"))
	if string(own) != "session edit" || string(other) != "personal instructions" {
		t.Fatal("setups are shared or session edits overwritten")
	}
	if err := os.RemoveAll(envA["HOME"]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareHarnessSetup(context.Background(), setup, "codex"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(pathA)
	if string(restored) != "personal instructions" {
		t.Fatal("rehydration failed")
	}
}
func TestSetupRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, ".meridian-setup-rev")); err != nil {
		t.Fatal(err)
	}
	s := &Server{config: ServerConfig{HarnessHome: base}}
	_, err := s.prepareHarnessSetup(context.Background(), &harnesssetup.RuntimeSetup{Revision: "rev", Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "x"}}}}, "codex")
	if err == nil {
		t.Fatal("accepted external setup home")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside home")
	}
}

func TestGatewayConfigurationDoesNotWriteLeaseToken(t *testing.T) {
	for _, harness := range []string{"codex", "claude", "opencode", "pi"} {
		t.Run(harness, func(t *testing.T) {
			base := t.TempDir()
			s := &Server{config: ServerConfig{HarnessHome: base}}
			provider := "openai"
			if harness == "claude" {
				provider = "anthropic"
			}
			token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: "https://meridian.example/provider-gateway/" + provider, Token: token}, nil, harness, nil)
			if err != nil {
				t.Fatal(err)
			}
			if env["MERIDIAN_PROVIDER_TOKEN"] != token {
				t.Fatal("missing ephemeral credential")
			}
			err = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				raw, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				if strings.Contains(string(raw), token) {
					t.Errorf("lease token persisted in %s", p)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
