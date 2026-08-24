package capsuleproto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRunStartIsIdempotentAndOutputIsOrdered(t *testing.T) {
	workspace := runWorkspace(t, `
version: v1
harnesses:
  - name: echo
    executable: /bin/echo
    arguments: ["prefix"]
    workingDirectory: .
    promptMode: argument
    pty: false
    outputMode: text
    timeout: 10s
`)
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace, EventLimit: 32})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	input := RunStartRequest{RunID: "run-1", Harness: "echo", Prompt: "task"}
	if _, err := client.StartRun(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartRun(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	status := waitRun(t, client, "run-1")
	if status.State != RunSucceeded || status.ExitCode == nil || *status.ExitCode != 0 {
		t.Fatalf("status = %#v", status)
	}
	events, err := client.RunEvents(context.Background(), "run-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var output string
	for index, event := range events.Events {
		if index > 0 && event.Sequence <= events.Events[index-1].Sequence {
			t.Fatal("events are not ordered")
		}
		if event.Type == "output" {
			value, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				t.Fatal(err)
			}
			output += string(value)
		}
	}
	if !strings.Contains(output, "prefix task") {
		t.Fatalf("output = %q", output)
	}
}

func TestRunCancellationAndGitBounds(t *testing.T) {
	workspace := runWorkspace(t, `
version: v1
harnesses:
  - name: long
    executable: /bin/sleep
    arguments: ["30"]
    workingDirectory: .
    promptMode: stdin
    pty: false
    outputMode: text
    timeout: 1m
`)
	runGitFixture(t, workspace)
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace, DiffLimit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	if _, err := client.StartRun(context.Background(), RunStartRequest{
		RunID: "run-cancel", Harness: "long", Prompt: "not logged",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CancelRun(context.Background(), "run-cancel"); err != nil {
		t.Fatal(err)
	}
	status := waitRun(t, client, "run-cancel")
	if status.State != RunCancelled {
		t.Fatalf("state = %s", status.State)
	}
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("changed and longer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := client.GitDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Truncated || len(diff.Content) > 8 {
		t.Fatalf("diff = %#v", diff)
	}
}

func TestPTYResizeDetachAndReplay(t *testing.T) {
	workspace := runWorkspace(t, `
version: v1
harnesses:
  - name: terminal
    executable: /bin/cat
    workingDirectory: .
    promptMode: interactive
    pty: true
    outputMode: text
    timeout: 1m
`)
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	if _, err := client.StartRun(context.Background(), RunStartRequest{
		RunID: "pty-run", Harness: "terminal", Prompt: "", Columns: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := client.RunStatus(context.Background(), "pty-run")
		if err != nil {
			t.Fatal(err)
		}
		if status.State == RunRunning {
			break
		}
		if status.State == RunFailed || time.Now().After(deadline) {
			t.Fatalf("PTY Run did not start: %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	connection, err := client.Attach(context.Background(), "pty-run", 0)
	if err != nil {
		t.Fatal(err)
	}
	resize, _ := json.Marshal(map[string]any{"type": "resize", "columns": 100, "rows": 40})
	if err := connection.Write(context.Background(), websocket.MessageText, resize); err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(context.Background(), websocket.MessageBinary, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	cursor := readPTYOutput(t, connection, "first")
	_ = connection.Close(websocket.StatusNormalClosure, "")

	reconnected, err := client.Attach(context.Background(), "pty-run", cursor)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconnected.Write(context.Background(), websocket.MessageBinary, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if next := readPTYOutput(t, reconnected, "second"); next <= cursor {
		t.Fatalf("cursor = %d after %d", next, cursor)
	}
	_ = reconnected.Close(websocket.StatusNormalClosure, "")
	if _, err := client.CancelRun(context.Background(), "pty-run"); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsSymlinkedWorkingDirectory(t *testing.T) {
	workspace := runWorkspace(t, `
version: v1
harnesses:
  - name: unsafe
    executable: /bin/echo
    workingDirectory: link
    promptMode: argument
    pty: false
    outputMode: text
    timeout: 1m
`)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	if _, err := client.StartRun(context.Background(), RunStartRequest{
		RunID: "unsafe", Harness: "unsafe", Prompt: "task",
	}); err == nil {
		t.Fatal("expected symlinked working directory rejection")
	}
}

func TestRunEventGapAndMalformedJSONL(t *testing.T) {
	workspace := runWorkspace(t, `
version: v1
harnesses:
  - name: malformed
    executable: /bin/echo
    arguments: ["not-json"]
    workingDirectory: .
    promptMode: stdin
    pty: false
    outputMode: jsonl
    timeout: 1m
`)
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace, EventLimit: 2, OutputLimit: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	if _, err := client.StartRun(context.Background(), RunStartRequest{
		RunID: "malformed", Harness: "malformed", Prompt: "task",
	}); err != nil {
		t.Fatal(err)
	}
	waitRun(t, client, "malformed")
	events, err := client.RunEvents(context.Background(), "malformed", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !events.Gap {
		t.Fatal("expected explicit replay gap")
	}
	found := false
	for _, event := range events.Events {
		found = found || event.Type == "parse_error"
	}
	if !found {
		t.Fatalf("events = %#v", events.Events)
	}
}

func TestConcurrentRunStartsAtomicallyEnforceLimit(t *testing.T) {
	workspace := runWorkspace(t, `
version: v1
harnesses:
  - name: long
    executable: /bin/sleep
    arguments: ["30"]
    workingDirectory: .
    promptMode: stdin
    pty: false
    outputMode: text
    timeout: 1m
`)
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace, RunLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, _ := NewClient(httpServer.URL, testToken, httpServer.Client())
	start := make(chan struct{})
	acceptedIDs := make(chan string, 2)
	var accepted atomic.Int32
	var limited atomic.Int32
	var wait sync.WaitGroup
	for _, id := range []string{"run-one", "run-two"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := client.StartRun(context.Background(), RunStartRequest{
				RunID: id, Harness: "long", Prompt: "task",
			})
			if err == nil {
				accepted.Add(1)
				acceptedIDs <- id
				return
			}
			var protocolError *Error
			if errors.As(err, &protocolError) && protocolError.Status == http.StatusTooManyRequests {
				limited.Add(1)
				return
			}
			t.Errorf("start %s: %v", id, err)
		}()
	}
	close(start)
	wait.Wait()
	close(acceptedIDs)
	for id := range acceptedIDs {
		_, _ = client.CancelRun(context.Background(), id)
	}
	if accepted.Load() != 1 || limited.Load() != 1 {
		t.Fatalf("accepted = %d, limited = %d", accepted.Load(), limited.Load())
	}
}

func readPTYOutput(t *testing.T, connection *websocket.Conn, want string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		messageType, value, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if messageType != websocket.MessageText {
			continue
		}
		var event RunEvent
		if json.Unmarshal(value, &event) != nil || event.Data == "" {
			continue
		}
		output, err := base64.StdEncoding.DecodeString(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(output), want) {
			return event.Sequence
		}
	}
}

func runWorkspace(t *testing.T, config string) string {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".meridian"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, ".meridian", "project.yaml"), []byte(config), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func newAuthenticatedTestServer(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	result := httptest.NewServer(server.Handler())
	t.Cleanup(result.Close)
	return result
}

func runGitFixture(t *testing.T, workspace string) {
	t.Helper()
	run := func(arguments ...string) {
		command := exec.Command("git", arguments...)
		command.Dir = workspace
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	run("init", "--template=", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
}

func waitRun(t *testing.T, client *Client, id string) RunStatusResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, err := client.RunStatus(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == RunSucceeded || status.State == RunFailed || status.State == RunCancelled {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run did not finish: %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
