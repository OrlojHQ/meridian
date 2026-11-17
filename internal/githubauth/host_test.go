package githubauth

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCandidatePathsCoverUsualInstalls(t *testing.T) {
	got := candidatePaths("/Users/ada")
	want := []string{
		"/opt/homebrew/bin/gh",
		"/usr/local/bin/gh",
		"/home/linuxbrew/.linuxbrew/bin/gh",
		"/Users/ada/.local/bin/gh",
		"/usr/bin/gh",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("candidates = %v", got)
	}
	if strings.Join(candidatePaths(""), ",") != "/opt/homebrew/bin/gh,/usr/local/bin/gh,/home/linuxbrew/.linuxbrew/bin/gh,/usr/bin/gh" {
		t.Fatalf("candidates without home = %v", candidatePaths(""))
	}
}

func TestParseMarkedPATHIgnoresProfileNoise(t *testing.T) {
	got := parseMarkedPATH("welcome\n" + pathMarker + "/opt/homebrew/bin:/usr/bin" + pathMarker + "\ndone\n")
	if got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("PATH = %q", got)
	}
	if parseMarkedPATH(pathMarker+"has\nnewline"+pathMarker) != "" {
		t.Fatal("newline in PATH was accepted")
	}
	if parseMarkedPATH("no marker") != "" {
		t.Fatal("missing marker was accepted")
	}
}

func TestReadLoginShellPATH(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "shell")
	writeExecutable(t, shell, "#!/bin/sh\nprintf 'noise\\n"+pathMarker+"/opt/custom/bin:/usr/bin"+pathMarker+"\\n'\n")
	got := readLoginShellPATH(context.Background(), shell)
	if got != "/opt/custom/bin:/usr/bin" {
		t.Fatalf("login PATH = %q", got)
	}

	stuck := filepath.Join(dir, "stuck")
	writeExecutable(t, stuck, "#!/bin/sh\nsleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if got := readLoginShellPATH(ctx, stuck); got != "" {
		t.Fatalf("stuck shell PATH = %q", got)
	}
}

func TestDiscoverExplicitPathWinsAndRejectsRelative(t *testing.T) {
	dir := t.TempDir()
	explicit := writeGH(t, filepath.Join(dir, "explicit"), signedInScript("octocat"))
	other := t.TempDir()
	writeGH(t, filepath.Join(other, "gh"), signedInScript("other"))
	t.Setenv("PATH", other)

	status := (HostCommands{Path: explicit, Candidates: []string{}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateSignedIn || status.Login != "octocat" || status.Path != explicit {
		t.Fatalf("explicit = %+v", status)
	}

	status = (HostCommands{Path: "gh", Candidates: []string{}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateNotFound || !strings.Contains(status.Detail, "gh") || !strings.Contains(status.Detail, "not an executable") {
		t.Fatalf("relative = %+v", status)
	}
}

func TestDiscoverPATHThenCommonLocationThenLoginPATH(t *testing.T) {
	onPath := t.TempDir()
	pathGH := writeGH(t, filepath.Join(onPath, "gh"), signedInScript("from-path"))
	commonDir := t.TempDir()
	commonGH := writeGH(t, filepath.Join(commonDir, "gh"), signedInScript("from-common"))
	loginDir := t.TempDir()
	loginGH := writeGH(t, filepath.Join(loginDir, "gh"), signedInScript("from-login"))
	empty := t.TempDir()

	t.Setenv("PATH", onPath)
	status := (HostCommands{Candidates: []string{commonGH}, LoginPATH: loginDir, SkipLoginShell: false}).Probe(context.Background())
	if status.Path != pathGH || status.Login != "from-path" {
		t.Fatalf("PATH should win, got %+v", status)
	}

	t.Setenv("PATH", empty)
	status = (HostCommands{Candidates: []string{commonGH}, LoginPATH: loginDir}).Probe(context.Background())
	if status.Path != commonGH || status.Login != "from-common" {
		t.Fatalf("common location should win, got %+v", status)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	status = (HostCommands{Candidates: []string{missing}, LoginPATH: loginDir}).Probe(context.Background())
	if status.Path != loginGH || status.Login != "from-login" {
		t.Fatalf("login PATH should win, got %+v", status)
	}
	if !contains(status.Searched, "PATH") || !contains(status.Searched, missing) || !contains(status.Searched, "login shell PATH") {
		t.Fatalf("searched = %v", status.Searched)
	}
}

func TestDiscoverFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := writeGH(t, filepath.Join(dir, "real"), signedInScript("octocat"))
	link := filepath.Join(dir, "gh")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	status := (HostCommands{Candidates: []string{link}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateSignedIn || status.Login != "octocat" || status.Path != link {
		t.Fatalf("symlink = %+v", status)
	}
}

func TestProbeStatesDoNotLeakToken(t *testing.T) {
	const secret = "ghp_supersecrettokenvalue"
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	bin := filepath.Join(dir, "gh")
	writeExecutable(t, bin, "#!/bin/sh\nprintf '%s\\n' '"+secret+"' >&2\nprintf '%s\\n' 'Logged in to github.com account octocat (keyring)'\nexit 0\n")
	status := (HostCommands{Candidates: []string{}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateSignedIn || status.Login != "octocat" {
		t.Fatalf("signed in = %+v", status)
	}
	if strings.Contains(status.Detail, secret) || strings.Contains(status.Login, secret) {
		t.Fatal("probe leaked the token")
	}
	if !strings.Contains(status.Detail, bin) {
		t.Fatalf("detail = %q", status.Detail)
	}

	writeExecutable(t, bin, "#!/bin/sh\nprintf '%s\\n' 'You are not logged into any GitHub hosts. To log in, run: gh auth login'\nprintf '%s\\n' '"+secret+"' >&2\nexit 1\n")
	status = (HostCommands{Candidates: []string{}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateNotSignedIn || status.Path != bin || strings.Contains(status.Detail, secret) {
		t.Fatalf("signed out = %+v", status)
	}
	if !strings.Contains(status.Detail, "not signed in") || !strings.Contains(status.Detail, bin) {
		t.Fatalf("detail = %q", status.Detail)
	}

	writeExecutable(t, bin, "#!/bin/sh\nprintf '%s\\n' 'github.com'\nprintf '%s\\n' '  X Failed to log in to github.com account octocat (keyring)'\nprintf '%s\\n' '  - The token in keyring is invalid.'\nprintf '%s\\n' '"+secret+"'\nexit 1\n")
	status = (HostCommands{Candidates: []string{}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateTokenUnreadable || status.Login != "octocat" || strings.Contains(status.Detail+status.Login, secret) {
		t.Fatalf("unreadable = %+v", status)
	}
	if !strings.Contains(status.Detail, "could not be read") || !strings.Contains(status.Detail, bin) || !strings.Contains(status.Detail, "@octocat") {
		t.Fatalf("detail = %q", status.Detail)
	}

	t.Setenv("PATH", t.TempDir())
	missing := filepath.Join(t.TempDir(), "gh")
	status = (HostCommands{Candidates: []string{missing}, SkipLoginShell: true}).Probe(context.Background())
	if status.State != StateNotFound || status.Path != "" || !strings.Contains(status.Detail, missing) || !strings.Contains(status.Detail, "PATH") {
		t.Fatalf("missing = %+v", status)
	}
}

func TestTokenUsesHomeAndDoesNotReturnSecretOnFailure(t *testing.T) {
	const secret = "ghp_supersecrettokenvalue"
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, "env")
	bin := filepath.Join(dir, "gh")
	writeExecutable(t, bin, "#!/bin/sh\nprintf 'HOME=%s\\nPATH=%s\\n' \"$HOME\" \"$PATH\" > \"$ENV_FILE\"\nif [ \"$1 $2\" = \"auth token\" ]; then\n  if [ \"$TOKEN_OK\" = 1 ]; then printf '%s\\n' \"$TOKEN_VALUE\"; exit 0; fi\n  printf '%s\\n' \"$TOKEN_VALUE\" >&2\n  exit 1\nfi\nexit 1\n")
	t.Setenv("HOME", home)
	t.Setenv("ENV_FILE", envFile)
	t.Setenv("TOKEN_OK", "1")
	t.Setenv("TOKEN_VALUE", secret)
	t.Setenv("PATH", t.TempDir())

	commands := HostCommands{Path: bin, SkipLoginShell: true}
	token, err := commands.Token(context.Background())
	if err != nil || token != secret {
		t.Fatalf("token error = %v", err)
	}
	recorded, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(recorded)
	if !strings.Contains(text, "HOME="+home) {
		t.Fatalf("HOME was not passed: %s", text)
	}
	if !strings.Contains(text, "PATH=") || !strings.Contains(text, dir) {
		t.Fatalf("PATH did not include the GitHub CLI directory: %s", text)
	}
	if strings.Contains(text, secret) {
		t.Fatal("environment record contains the token")
	}

	t.Setenv("TOKEN_OK", "0")
	token, err = commands.Token(context.Background())
	if err != ErrTokenUnreadable || token != "" || strings.Contains(err.Error(), secret) {
		t.Fatalf("failure token = %q err = %v", token, err)
	}

	t.Setenv("HOME", "")
	t.Setenv("TOKEN_OK", "1")
	if _, err := commands.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorded, err = os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	current, userErr := user.Current()
	if userErr != nil || current.HomeDir == "" {
		t.Skip("no user home record")
	}
	if !strings.Contains(string(recorded), "HOME="+current.HomeDir) {
		t.Fatalf("empty HOME was not filled from the user record: %s", recorded)
	}
}

func writeGH(t *testing.T, path, body string) string {
	t.Helper()
	writeExecutable(t, path, body)
	return path
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func signedInScript(login string) string {
	return "#!/bin/sh\nprintf '%s\\n' 'Logged in to github.com account " + login + " (keyring)'\nexit 0\n"
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
