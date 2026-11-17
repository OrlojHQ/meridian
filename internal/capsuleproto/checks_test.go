package capsuleproto

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newCheckServer(t *testing.T, workspace string) *Client {
	t.Helper()
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	client, err := NewClient(httpServer.URL, testToken, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestCheckRunStripsCredentialsAndKillsOnTimeout(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("MERIDIAN_CAPSULE_TOKEN", "capsule-token-value")
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	t.Setenv("MERIDIAN_GIT_PASSWORD", "git-password-value")
	client := newCheckServer(t, workspace)
	result, err := client.RunChecks(context.Background(), CheckRunRequest{
		Checks: []CheckCommand{{
			Name: "env", TimeoutSeconds: 30,
			Argv: []string{"/bin/sh", "-c", `printf '%s' "$MERIDIAN_CAPSULE_TOKEN|$DOCKER_HOST|$MERIDIAN_GIT_PASSWORD"`},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Results[0].ExitCode != 0 {
		t.Fatalf("env check = %#v", result.Results[0])
	}
	if strings.Contains(result.Results[0].Output, "capsule-token-value") ||
		strings.Contains(result.Results[0].Output, "docker.sock") ||
		strings.Contains(result.Results[0].Output, "git-password-value") {
		t.Fatalf("check environment leaked credentials: %q", result.Results[0].Output)
	}

	timed, err := client.RunChecks(context.Background(), CheckRunRequest{
		Checks: []CheckCommand{{
			Name: "sleep", TimeoutSeconds: 1, Argv: []string{"/bin/sleep", "30"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !timed.Results[0].TimedOut || timed.Results[0].ExitCode == 0 {
		t.Fatalf("hung check was not killed: %#v", timed.Results[0])
	}
	if timed.Results[0].Duration > int64((5 * time.Second).Milliseconds()) {
		t.Fatalf("timeout took %dms", timed.Results[0].Duration)
	}
}

func TestCheckRunKeepsTailAndMarksTreeChangeStale(t *testing.T) {
	workspace := gitWorkspace(t)
	client := newCheckServer(t, workspace)
	result, err := client.RunChecks(context.Background(), CheckRunRequest{
		OutputLimit: 80,
		Checks: []CheckCommand{{
			Name: "volume", TimeoutSeconds: 30,
			Argv: []string{"/bin/sh", "-c", "i=0; while [ \"$i\" -lt 40 ]; do echo \"line-$i secret=ghp_abcdefghijklmnop\"; i=$((i+1)); done"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := result.Results[0]
	if !got.Truncated {
		t.Fatal("expected a truncated tail")
	}
	if !strings.Contains(got.Output, "line-39") || strings.Contains(got.Output, "line-0\n") {
		t.Fatalf("tail = %q", got.Output)
	}
	if strings.Contains(got.Output, "ghp_") {
		t.Fatalf("token survived redaction: %q", got.Output)
	}
	if strings.Contains(got.Output, "\x1b") {
		t.Fatal("control sequence survived")
	}
	if result.Stale {
		t.Fatal("unchanged tree was stale")
	}

	changed, err := client.RunChecks(context.Background(), CheckRunRequest{
		Checks: []CheckCommand{{
			Name: "edit", TimeoutSeconds: 30,
			Argv: []string{"/bin/sh", "-c", "echo more >> README && git add README && git -c user.email=dev@example.com -c user.name=Dev commit -m next"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Stale || changed.TreeBefore == changed.TreeAfter {
		t.Fatalf("tree change was not stale: before %s after %s", changed.TreeBefore, changed.TreeAfter)
	}
}

func TestSuggestionIsNotExecuted(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".meridian"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "checks:\n  - name: test\n    argv: [make, test]\n    timeoutSeconds: 30\n"
	if err := os.WriteFile(filepath.Join(workspace, ".meridian", "checks.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	client := newCheckServer(t, workspace)
	suggestion, err := client.SuggestChecks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if suggestion.Ignored || len(suggestion.Checks) != 1 || suggestion.Checks[0].Name != "test" {
		t.Fatalf("suggestion = %#v", suggestion)
	}
	if _, err := os.Stat(filepath.Join(workspace, "ran")); err == nil {
		t.Fatal("suggestion executed a command")
	}
}

func gitWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_EMAIL=dev@example.com")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(workspace, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-m", "initial")
	return workspace
}
