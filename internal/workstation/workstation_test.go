package workstation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDiscoverGitIdentityReadsOnlyTheUserSection(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gitconfig"), `# my config
[core]
	sshCommand = ssh -i ~/.ssh/work
[url "git@github.com:"]
	insteadOf = https://github.com/
[credential]
	helper = osxkeychain
[user "work"]
	name = Not This
[include]
	path = ~/.gitconfig.local
[user]
	name = "Ada Lovelace" ; the countess
	email = ada@example.com # personal
`)
	write(t, filepath.Join(home, ".gitconfig.local"), "[user]\n\temail = included@example.com\n")
	got, err := DiscoverGitIdentity(home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Identity.Name != "Ada Lovelace" || got.Identity.Email != "ada@example.com" || got.Path != "~/.gitconfig" {
		t.Fatalf("identity = %+v", got)
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "include directives were not followed") {
		t.Fatalf("notes = %v", got.Notes)
	}
}

func TestDiscoverGitIdentityFollowsGitPrecedence(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	write(t, filepath.Join(xdg, "git", "config"), "[user]\n\tname = XDG Name\n\temail = xdg@example.com\n")
	write(t, filepath.Join(home, ".gitconfig"), "[User]\n\tEmail = home@example.com\n")
	got, err := DiscoverGitIdentity(home, envOf(map[string]string{"XDG_CONFIG_HOME": xdg}))
	if err != nil {
		t.Fatal(err)
	}
	// ~/.gitconfig is read after the XDG file, so its email wins.
	if got.Identity.Name != "XDG Name" || got.Identity.Email != "home@example.com" || got.Path != "~/xdg/git/config, ~/.gitconfig" {
		t.Fatalf("identity = %+v", got)
	}

	global := filepath.Join(home, "elsewhere.ini")
	write(t, global, "[user]\n\tname = Global\n\temail = global@example.com\n")
	got, err = DiscoverGitIdentity(home, envOf(map[string]string{"XDG_CONFIG_HOME": xdg, "GIT_CONFIG_GLOBAL": global}))
	if err != nil || got.Identity.Name != "Global" {
		t.Fatalf("GIT_CONFIG_GLOBAL identity = %+v, %v", got, err)
	}
}

func TestDiscoverGitIdentityRefusesUnusableValues(t *testing.T) {
	for name, content := range map[string]string{
		"name only":          "[user]\n\tname = Ada\n",
		"angle brackets":     "[user]\n\tname = Ada <x>\n\temail = a@x\n",
		"escaped newline":    "[user]\n\tname = \"Ada\\n[core]\"\n\temail = a@x\n",
		"unterminated quote": "[user]\n\tname = \"Ada\n\temail = a@x\n",
	} {
		home := t.TempDir()
		write(t, filepath.Join(home, ".gitconfig"), content)
		got, err := DiscoverGitIdentity(home, envOf(nil))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got == nil || got.Identity.Name != "" || len(got.Notes) == 0 {
			t.Fatalf("%s: identity = %+v", name, got)
		}
	}
	home := t.TempDir()
	write(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = "+strings.Repeat("a", maxGitConfigBytes)+"\n")
	if got, err := DiscoverGitIdentity(home, envOf(nil)); err != nil || got == nil || got.Identity.Name != "" ||
		len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "larger than 64 KiB") {
		t.Fatalf("oversized config = %+v, %v", got, err)
	}
	if got, err := DiscoverGitIdentity(t.TempDir(), envOf(nil)); err != nil || got != nil {
		t.Fatalf("no config = %+v, %v", got, err)
	}
}

// The scan walks only manifest roots: a secret elsewhere in home is never
// read, and a denied file inside a root is reported, not imported.
func TestScanReadsOnlyManifestRoots(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".codex", "AGENTS.md"), "review carefully")
	write(t, filepath.Join(home, ".codex", "auth.json"), `{"token":"canary"}`)
	write(t, filepath.Join(home, ".ssh", "id_ed25519"), "PRIVATE KEY canary")
	write(t, filepath.Join(home, "notes.txt"), "canary")
	write(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = Ada\n\temail = ada@example.com\n")
	env := map[string]string{}
	for _, harness := range harnesssetup.Harnesses() {
		if manifest, ok := harnesssetup.LookupManifest(harness); ok && manifest.Root.Env != "" {
			env[manifest.Root.Env] = ""
		}
	}
	scan, err := ScanComputer(home, envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Harnesses) != 1 || scan.Harnesses[0].Harness != "codex" || scan.Git == nil || scan.Git.Identity.Name != "Ada" {
		t.Fatalf("scan = %+v", scan)
	}
	for _, file := range scan.Harnesses[0].Bundle.Files {
		if strings.Contains(file.Content, "canary") {
			t.Fatalf("scan carried %s", file.Path)
		}
	}
	excluded := false
	for _, issue := range scan.Harnesses[0].Issues {
		excluded = excluded || strings.HasSuffix(issue.Path, "auth.json")
	}
	if !excluded {
		t.Fatalf("auth.json was not reported: %+v", scan.Harnesses[0].Issues)
	}

	// The digest changes with what a save would send, and only with that.
	before := scan.Digest()
	write(t, filepath.Join(home, "notes.txt"), "changed elsewhere")
	again, _ := ScanComputer(home, envOf(env))
	if again.Digest() != before {
		t.Fatal("a file outside the roots changed the digest")
	}
	write(t, filepath.Join(home, ".codex", "AGENTS.md"), "review even more carefully")
	changed, _ := ScanComputer(home, envOf(env))
	if changed.Digest() == before {
		t.Fatal("a kept file's change did not change the digest")
	}
}
