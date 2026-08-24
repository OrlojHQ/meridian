package capsuleproto

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeliveryStateAndCommitEnforcePreconditionsAndDisableHooks(t *testing.T) {
	workspace := deliveryRepository(t)
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".meridian-prepared"), []byte("private marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	hookMarker := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\nprintf ran >" + shellSingleQuote(hookMarker) + "\nexit 99\n"
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "hooks", "pre-commit"), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectDeliveryState(context.Background(), workspace); err != nil {
		t.Fatalf("inspect delivery fixture: %v", err)
	}
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())

	state, err := client.DeliveryState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Dirty || !validObjectID(state.HEAD) || !validObjectID(state.Tree) ||
		state.Tree == state.HEAD || state.Branch != "main" ||
		state.OriginURL != "https://example.invalid/repository.git" ||
		state.DefaultBranch != "main" {
		t.Fatalf("delivery state = %#v", state)
	}
	bad := DeliveryCommitRequest{
		Message: "reviewed change", AuthorName: "Meridian User",
		AuthorEmail: "user@example.invalid", ExpectedHEAD: state.HEAD,
		ExpectedTree: strings.Repeat("0", len(state.Tree)),
	}
	if _, err := client.DeliveryCommit(context.Background(), bad); errorCode(err) != "tree_precondition_failed" {
		t.Fatalf("bad tree error = %v", err)
	}
	if output := gitOutput(t, workspace, "diff", "--cached", "--name-only"); output != "" {
		t.Fatalf("failed precondition mutated index: %q", output)
	}
	bad.ExpectedTree = state.Tree
	committed, err := client.DeliveryCommit(context.Background(), bad)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Tree != state.Tree || committed.Commit == state.HEAD || !validObjectID(committed.Commit) {
		t.Fatalf("commit response = %#v", committed)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commit hook executed: %v", err)
	}
	if author := gitOutput(t, workspace, "show", "-s", "--format=%an <%ae>", "HEAD"); author != "Meridian User <user@example.invalid>" {
		t.Fatalf("commit author = %q", author)
	}
	if tracked := gitOutput(t, workspace, "ls-files", "--", ".meridian-prepared"); tracked != "" {
		t.Fatalf("private supervisor marker was committed: %q", tracked)
	}

	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, err := client.DeliveryState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	server.runs["active"] = &supervisedRun{
		id: "active", state: RunRunning, startedAt: time.Now(),
	}
	server.mu.Unlock()
	_, err = client.DeliveryCommit(context.Background(), DeliveryCommitRequest{
		Message: "blocked", AuthorName: "Meridian User", AuthorEmail: "user@example.invalid",
		ExpectedHEAD: next.HEAD, ExpectedTree: next.Tree,
	})
	var protocolError *Error
	if !errors.As(err, &protocolError) || protocolError.Status != http.StatusConflict ||
		protocolError.Code != "run_active" {
		t.Fatalf("active-run commit error = %v", err)
	}
	if head := gitOutput(t, workspace, "rev-parse", "HEAD"); head != committed.Commit {
		t.Fatalf("active-run commit mutated HEAD to %q", head)
	}
}

func TestDeliveryRejectsUnsafeRepositoryAndCommitBounds(t *testing.T) {
	workspace := deliveryRepository(t)
	if err := os.WriteFile(filepath.Join(workspace, ".gitmodules"), []byte("[submodule \"unsafe\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectDeliveryState(context.Background(), workspace); err == nil {
		t.Fatal("repository with submodule configuration was accepted")
	}
	if validateCommitRequest(DeliveryCommitRequest{
		Message: strings.Repeat("x", maxDeliveryMessageBytes+1), AuthorName: "name",
		AuthorEmail: "email@example.invalid", ExpectedHEAD: strings.Repeat("0", 40),
		ExpectedTree: strings.Repeat("1", 40),
	}) == nil {
		t.Fatal("oversized commit message was accepted")
	}
	if validateCommitRequest(DeliveryCommitRequest{
		Message: "message", AuthorName: "", AuthorEmail: "",
		ExpectedHEAD: strings.Repeat("0", 40), ExpectedTree: strings.Repeat("1", 40),
	}) == nil {
		t.Fatal("missing author was accepted")
	}
}

func TestDeliveryRejectsURLRewritesAndTrackedSupervisorMarker(t *testing.T) {
	for name, configure := range map[string]func(*testing.T, string){
		"url rewrite": func(t *testing.T, workspace string) {
			gitRun(t, workspace, "config", "--local", "url.https://attacker.invalid/.insteadOf", "https://example.invalid/")
		},
		"tracked marker": func(t *testing.T, workspace string) {
			if err := os.WriteFile(filepath.Join(workspace, ".meridian-prepared"), []byte("marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitRun(t, workspace, "add", "--", ".meridian-prepared")
		},
	} {
		t.Run(name, func(t *testing.T) {
			workspace := deliveryRepository(t)
			configure(t, workspace)
			if err := validateDeliveryRepository(context.Background(), workspace); err == nil {
				t.Fatal("unsafe repository configuration was accepted")
			}
		})
	}
}

func TestDeliveryPushUsesExactRefAndCleansCredentialHelper(t *testing.T) {
	workspace := deliveryRepository(t)
	head := gitOutput(t, workspace, "rev-parse", "HEAD")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "push-arguments")
	script := `#!/bin/sh
command_name=
for argument do
  case "$argument" in
    ls-remote|push) command_name="$argument";;
  esac
done
if [ "$command_name" = ls-remote ] || [ "$command_name" = push ]; then
  username="$("$GIT_ASKPASS" "Username for HTTPS")" || exit 31
  password="$("$GIT_ASKPASS" "Password for HTTPS")" || exit 32
  [ "$username" = "$EXPECTED_GIT_USERNAME" ] || exit 33
  [ "$password" = "$EXPECTED_GIT_PASSWORD" ] || exit 34
fi
if [ "$command_name" = ls-remote ]; then
  for last do :; done
  printf '%s\t%s\n' "$EXPECTED_OLD_REF" "$last"
  exit 0
fi
if [ "$command_name" = push ]; then
  printf '%s\n' "$@" >"$PUSH_ARGUMENT_RECORD"
  exit 0
fi
exec "$REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	credential := &GitHTTPSCredential{Username: "delivery-user", Password: "credential-secret-value"}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REAL_GIT", realGit)
	t.Setenv("EXPECTED_GIT_USERNAME", credential.Username)
	t.Setenv("EXPECTED_GIT_PASSWORD", credential.Password)
	t.Setenv("EXPECTED_OLD_REF", head)
	t.Setenv("PUSH_ARGUMENT_RECORD", record)
	before, err := filepath.Glob(filepath.Join(os.TempDir(), ".meridian-askpass-*"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())

	invalidRefs := []string{
		"main", "refs/tags/release", "refs/heads/-bad", "refs/heads/main:other",
		"refs/heads/topic/*", "refs/heads/topic..bad", "refs/heads/topic.lock",
	}
	for _, destination := range invalidRefs {
		_, err := client.DeliveryPush(context.Background(), DeliveryPushRequest{
			SourceCommit: head, DestinationRef: destination, GitCredential: credential,
		})
		var protocolError *Error
		if !errors.As(err, &protocolError) || protocolError.Status != http.StatusBadRequest {
			t.Fatalf("invalid ref %q error = %v", destination, err)
		}
	}
	wrong := strings.Repeat("f", len(head))
	_, err = client.DeliveryPush(context.Background(), DeliveryPushRequest{
		SourceCommit: head, DestinationRef: "refs/heads/review/topic",
		ExpectedOldRef: &wrong, GitCredential: credential,
	})
	if errorCode(err) != "remote_precondition_failed" {
		t.Fatalf("wrong old ref error = %v, code %q", err, errorCode(err))
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("push ran after failed precondition: %v", err)
	}
	server.mu.Lock()
	server.runs["active-push"] = &supervisedRun{
		id: "active-push", state: RunRunning, startedAt: time.Now(),
	}
	server.mu.Unlock()
	_, err = client.DeliveryPush(context.Background(), DeliveryPushRequest{
		SourceCommit: head, DestinationRef: "refs/heads/review/topic",
		ExpectedOldRef: &head, GitCredential: credential,
	})
	if errorCode(err) != "run_active" {
		t.Fatalf("active-run push error = %v", err)
	}
	server.mu.Lock()
	delete(server.runs, "active-push")
	server.mu.Unlock()
	response, err := client.DeliveryPush(context.Background(), DeliveryPushRequest{
		SourceCommit: head, DestinationRef: "refs/heads/review/topic",
		ExpectedOldRef: &head, GitCredential: credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Commit != head || response.DestinationRef != "refs/heads/review/topic" {
		t.Fatalf("push response = %#v", response)
	}
	arguments, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	value := string(arguments)
	if strings.Contains(value, credential.Password) || strings.Contains(value, credential.Username) ||
		strings.Contains(value, "--force") || strings.Contains(value, "+") ||
		!strings.Contains(value, head+":refs/heads/review/topic") ||
		!strings.Contains(value, "https://example.invalid/repository.git") {
		t.Fatalf("unsafe push arguments = %q", value)
	}
	after, err := filepath.Glob(filepath.Join(os.TempDir(), ".meridian-askpass-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("askpass helper leaked: before=%v after=%v", before, after)
	}
}

func deliveryRepository(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	gitRun(t, workspace, "init", "--template=", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workspace, "add", "--", "tracked.txt")
	gitRun(t, workspace,
		"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"commit", "--no-gpg-sign", "-m", "initial")
	gitRun(t, workspace, "remote", "add", "origin", "https://example.invalid/repository.git")
	gitRun(t, workspace, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, workspace, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return workspace
}

func gitRun(t *testing.T, workspace string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = workspace
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func gitOutput(t *testing.T, workspace string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = workspace
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}

func errorCode(err error) string {
	var protocolError *Error
	if errors.As(err, &protocolError) {
		return protocolError.Code
	}
	return ""
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
