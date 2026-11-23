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
	cmd := exec.Command(helper, "get")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	out, err := cmd.Output()
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
	t.Setenv("GH_TOKEN", "ghs_shouldnotpass")
	t.Setenv("GITHUB_TOKEN", "ghs_shouldnotpass")
	for _, item := range childEnvironment(nil) {
		if strings.HasPrefix(item, "GH_TOKEN=") || strings.HasPrefix(item, "GITHUB_TOKEN=") {
			t.Fatalf("child environment kept %s", item)
		}
	}
}
