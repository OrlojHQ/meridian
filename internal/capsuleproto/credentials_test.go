package capsuleproto

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareHashExcludesGitCredentialAndAskpassCleansUp(t *testing.T) {
	base := PrepareRequest{
		RepositoryURL: "https://127.0.0.1:1/private.git",
		Destination:   filepath.Join(t.TempDir(), "workspace"),
		Setup:         []string{"true"},
	}
	first := base
	first.GitCredential = &GitHTTPSCredential{Username: "one", Password: "first-secret"}
	second := base
	second.GitCredential = &GitHTTPSCredential{Username: "two", Password: "second-secret"}
	if requestHash(first) != requestHash(second) {
		t.Fatal("prepare marker hash includes credential material")
	}
	dir, err := askpassDirectory()
	if err != nil {
		t.Fatal(err)
	}
	before, err := filepath.Glob(filepath.Join(dir, ".meridian-askpass-*"))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	script := `#!/bin/sh
username="$("$GIT_ASKPASS" "Username for private HTTPS fixture")" || exit 20
password="$("$GIT_ASKPASS" "Password for private HTTPS fixture")" || exit 21
[ "$username" = "$EXPECTED_GIT_USERNAME" ] || exit 22
[ "$password" = "$EXPECTED_GIT_PASSWORD" ] || exit 23
for destination do :; done
mkdir -p "$destination"
`
	if err := os.WriteFile(git, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EXPECTED_GIT_USERNAME", first.GitCredential.Username)
	t.Setenv("EXPECTED_GIT_PASSWORD", first.GitCredential.Password)
	if err := secureHTTPSClone(
		context.Background(), "", base.RepositoryURL, base.Destination,
		*first.GitCredential, 4096,
	); err != nil {
		t.Fatalf("private askpass fixture failed: %v", err)
	}
	after, err := filepath.Glob(filepath.Join(dir, ".meridian-askpass-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("askpass helper was not removed: before=%v after=%v", before, after)
	}
	if info, err := os.Stat(base.Destination); err != nil || !info.IsDir() {
		t.Fatalf("fixture destination = %#v, %v", info, err)
	}
}

func TestAskpassHelperIsCreatedUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	blocked := t.TempDir()
	t.Setenv("TMPDIR", blocked)
	name, err := createGitAskpass()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(name)
	if !strings.HasPrefix(name, home+string(os.PathSeparator)) {
		t.Fatalf("helper %s is not under HOME", name)
	}
	if strings.HasPrefix(name, blocked+string(os.PathSeparator)) {
		t.Fatalf("helper %s was written to TMPDIR", name)
	}
	command := exec.Command(name, "Username for https://github.com")
	command.Env = []string{"MERIDIAN_GIT_USERNAME=x-access-token", "MERIDIAN_GIT_PASSWORD=not-used"}
	output, err := command.Output()
	if err != nil || string(output) != "x-access-token\n" {
		t.Fatalf("helper output = %q, %v", output, err)
	}
}

func TestResolveProfileSecretsPassesExactlyReferences(t *testing.T) {
	resolved, ok := resolveProfileSecrets(
		[]string{"TOKEN"}, map[string]string{"TOKEN": "value", "UNUSED": "not-delivered"})
	if !ok || len(resolved) != 1 || resolved["TOKEN"] != "value" {
		t.Fatalf("resolved = %#v, %v", resolved, ok)
	}
	if _, ok := resolved["UNUSED"]; ok {
		t.Fatal("unreferenced secret was delivered")
	}
	if _, ok := resolveProfileSecrets([]string{"MISSING"}, map[string]string{}); ok {
		t.Fatal("missing reference did not fail closed")
	}
}
