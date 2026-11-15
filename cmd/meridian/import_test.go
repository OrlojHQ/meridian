package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/spf13/cobra"
)

type importClock struct{}

func (importClock) Now() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }

type importIDs struct{ next int }

func (ids *importIDs) NewID() string {
	ids.next++
	return fmt.Sprintf("id%06d", ids.next)
}

type importQueue struct{}

func (importQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

// importHome is a fake laptop home with nothing from the real one: every
// manifest root override and git config variable is cleared.
func importHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	for _, harness := range harnesssetup.Harnesses() {
		if manifest, ok := harnesssetup.LookupManifest(harness); ok && manifest.Root.Env != "" {
			t.Setenv(manifest.Root.Env, "")
		}
	}
	return home
}

func writeHomeFile(t *testing.T, home, path, content string) {
	t.Helper()
	full := filepath.Join(home, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func importServer(t *testing.T) (*app.Service, string, string) {
	t.Helper()
	ctx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service := app.NewService(store, importClock{}, &importIDs{}, importQueue{})
	service.ConfigureSecrets(key)
	tokenFile := testAPITokenFile(t)
	raw, err := apiauth.ReadTokenFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	token, err := apiauth.ParseToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(service, token))
	t.Cleanup(server.Close)
	return service, server.URL, tokenFile
}

func importRoot(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&config.json, "json", false, "")
	root.AddCommand(newImportCommand(config))
	return root
}

func TestImportPreviewsComparesAndSavesTheWholeComputer(t *testing.T) {
	home := importHome(t)
	writeHomeFile(t, home, ".codex/AGENTS.md", "review carefully")
	writeHomeFile(t, home, ".codex/auth.json", `{"tokens":"auth-canary"}`)
	writeHomeFile(t, home, ".claude/CLAUDE.md", "be brief")
	writeHomeFile(t, home, ".gitconfig", "[credential]\n\thelper = store\n[user]\n\tname = Ada Lovelace\n\temail = ada@example.com\n")
	service, url, tokenFile := importServer(t)

	// --preview never contacts the server.
	var output bytes.Buffer
	config := &cliConfig{server: "https://unreachable.invalid", tokenFile: tokenFile, stdout: &output, stdin: strings.NewReader("")}
	command := importRoot(config)
	command.SetArgs([]string{"import", "--preview"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"codex: 1 portable files", "claude: 1 portable files", `Exclude ".codex/auth.json"`, `"Ada Lovelace" <ada@example.com> from ~/.gitconfig`, "code that runs inside the Capsule"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "auth-canary") || strings.Contains(text, "helper") {
		t.Fatalf("preview leaked:\n%s", text)
	}

	// An unattended save must accept the exclusion explicitly.
	output.Reset()
	config = &cliConfig{server: url, tokenFile: tokenFile, stdout: &output, stdin: strings.NewReader("")}
	command = importRoot(config)
	command.SetArgs([]string{"import", "--yes"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--accept-exclusions") {
		t.Fatalf("save without accepting exclusions = %v", err)
	}

	// Interactive save after one confirmation.
	output.Reset()
	config = &cliConfig{server: url, tokenFile: tokenFile, stdout: &output, stdin: strings.NewReader("y\n")}
	command = importRoot(config)
	command.SetArgs([]string{"import"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "codex: new") || !strings.Contains(text, "+ .codex/AGENTS.md") ||
		!strings.Contains(text, "Saved ") || strings.Contains(text, "auth-canary") {
		t.Fatalf("save output:\n%s", text)
	}
	ctx := principal.With(context.Background(), principal.Owner())
	profile, err := service.GetProfile(ctx)
	if err != nil || len(profile.Setups) != 2 || profile.GitName != "Ada Lovelace" || profile.ResourceVersion != 1 {
		t.Fatalf("saved profile = %+v, %v", profile, err)
	}

	// Running it again finds nothing to save.
	output.Reset()
	config = &cliConfig{server: url, tokenFile: tokenFile, stdout: &output, stdin: strings.NewReader("")}
	command = importRoot(config)
	command.SetArgs([]string{"import", "--yes", "--accept-exclusions"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "codex: unchanged") || !strings.Contains(text, "already current") {
		t.Fatalf("second run output:\n%s", text)
	}

	// A local change shows as a diff, and --yes reports what it accepted.
	writeHomeFile(t, home, ".codex/AGENTS.md", "review even more carefully")
	output.Reset()
	config = &cliConfig{server: url, tokenFile: tokenFile, stdout: &output, stdin: strings.NewReader("")}
	command = importRoot(config)
	command.SetArgs([]string{"import", "--yes", "--accept-exclusions", "--no-git"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "codex: changed") || !strings.Contains(text, "~ .codex/AGENTS.md") ||
		!strings.Contains(text, "Accepted 1 exclusions") || !strings.Contains(text, "Saved codex. Unchanged: claude.") {
		t.Fatalf("changed run output:\n%s", text)
	}
}

func TestImportRefusesPlainHTTPToARemoteServer(t *testing.T) {
	home := importHome(t)
	writeHomeFile(t, home, ".codex/AGENTS.md", "review")
	var output bytes.Buffer
	config := &cliConfig{server: "http://meridian.example.com", tokenFile: testAPITokenFile(t), stdout: &output, stdin: strings.NewReader("y\n")}
	command := importRoot(config)
	command.SetArgs([]string{"import"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("plain HTTP import = %v", err)
	}
}
