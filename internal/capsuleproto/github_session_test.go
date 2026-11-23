package capsuleproto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

func TestSessionGitHubTokenIsNotInTheHelper(t *testing.T) {
	base := t.TempDir()
	server := &Server{config: ServerConfig{HarnessHome: base}, githubTokens: &tokenVault{}}
	const token = "ghs_installationtokenvalue"
	env, err := server.prepareSessionGit(
		&harnesssetup.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"},
		&harnesssetup.GitHubAccess{Token: token, Login: "meridian"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if env["GH_TOKEN"] != token || env["GITHUB_TOKEN"] != token {
		t.Fatalf("env token missing")
	}
	config, err := os.ReadFile(env["GIT_CONFIG_GLOBAL"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), token) || !strings.Contains(string(config), "[user]") {
		t.Fatalf("gitconfig =\n%s", config)
	}
	helper := filepath.Join(base, ".meridian-identity", "git-credential-github")
	script, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(script), token) {
		t.Fatal("helper contains the token")
	}
	file, err := os.ReadFile(filepath.Join(base, ".meridian-identity", "github-token"))
	if err != nil || strings.TrimSpace(string(file)) != token {
		t.Fatalf("token file = %q %v", file, err)
	}
	plain := exec.Command(helper, "get")
	plain.Stdin = strings.NewReader("protocol=http\nhost=github.com\n\n")
	out, err := plain.Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("non-https = %q %v", out, err)
	}
	cmd := exec.Command(helper, "get")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	out, err = cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "username=x-access-token\npassword="+token+"\n" {
		t.Fatalf("helper output = %q", out)
	}
	other := exec.Command(helper, "get")
	other.Stdin = strings.NewReader("protocol=https\nhost=example.com\n\n")
	out, err = other.Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("other host = %q %v", out, err)
	}
	if err := server.refreshGitHubToken("ghs_refreshedtokenvalue"); err != nil {
		t.Fatal(err)
	}
	refreshed, err := os.ReadFile(filepath.Join(base, ".meridian-identity", "github-token"))
	if err != nil || strings.TrimSpace(string(refreshed)) != "ghs_refreshedtokenvalue" {
		t.Fatalf("refreshed = %q %v", refreshed, err)
	}
	cmd = exec.Command(helper, "get")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	out, err = cmd.Output()
	if err != nil || !strings.Contains(string(out), "ghs_refreshedtokenvalue") || strings.Contains(string(out), token) {
		t.Fatalf("refreshed helper = %q %v", out, err)
	}
	redactor := newTokenRedactor(server.githubTokens)
	part := redactor.push([]byte("see ghs_refreshed"))
	rest := append(redactor.push([]byte("tokenvalue")), redactor.flush()...)
	if got := string(part) + string(rest); got != "see [redacted]" {
		t.Fatalf("redacted = %q", got)
	}
	identityPath := env["GIT_CONFIG_GLOBAL"]
	before, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	shellEnv, err := server.prepareShellGit(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(identityPath)
	if err != nil || string(after) != string(before) || !strings.Contains(string(after), "[user]") {
		t.Fatalf("harness identity changed:\n%s", after)
	}
	tokenAfter, err := os.ReadFile(filepath.Join(base, ".meridian-identity", "github-token"))
	if err != nil || strings.TrimSpace(string(tokenAfter)) != "ghs_refreshedtokenvalue" {
		t.Fatalf("token file = %q %v", tokenAfter, err)
	}
	if shellEnv["GIT_CONFIG_GLOBAL"] == identityPath {
		t.Fatal("shell reused the harness gitconfig")
	}
	shellConfig, err := os.ReadFile(shellEnv["GIT_CONFIG_GLOBAL"])
	if err != nil || !strings.Contains(string(shellConfig), "[user]") || strings.Contains(string(shellConfig), "ghs_") {
		t.Fatalf("shell config =\n%s", shellConfig)
	}

	t.Setenv("GH_TOKEN", "ghs_shouldnotpass")
	t.Setenv("GITHUB_TOKEN", "ghs_shouldnotpass")
	redactor = newTokenRedactor(&tokenVault{})
	redactor.vault.add("ghs_exampletokenvalue")
	if got := redactor.push([]byte("g")); string(got) != "" || !redactor.holding() {
		t.Fatalf("held prefix = %q holding=%v", got, redactor.holding())
	}
	if got := string(redactor.flush()); got != "g" {
		t.Fatalf("idle flush = %q", got)
	}
	part = redactor.push([]byte("see ghs_example"))
	rest = append(redactor.push([]byte("tokenvalue")), redactor.flush()...)
	if got := string(part) + string(rest); got != "see [redacted]" {
		t.Fatalf("eof flush = %q", got)
	}

	sh := &shellSession{subscribers: map[int]*shellSubscriber{}}
	sub := &shellSubscriber{ready: make(chan struct{}, 1)}
	sh.subscribers[1] = sub
	sh.emit(nil)
	select {
	case <-sub.ready:
		t.Fatal("empty chunk signaled a subscriber")
	default:
	}

	for _, item := range childEnvironment(nil) {
		if strings.HasPrefix(item, "GH_TOKEN=") || strings.HasPrefix(item, "GITHUB_TOKEN=") {
			t.Fatalf("child environment kept %s", item)
		}
	}
}
