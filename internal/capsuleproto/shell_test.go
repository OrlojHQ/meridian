package capsuleproto

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWorkspaceShellStartsAnswersAndOmitsSecrets(t *testing.T) {
	t.Setenv("MERIDIAN_PROVIDER_TOKEN", "super-secret-token")
	t.Setenv("EXAMPLE_API_KEY", "super-secret-token")
	t.Setenv("MERIDIAN_SHELL_MARKER", "marker-ok")
	workspace := filepath.Join(t.TempDir(), "workspace")
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, err := NewClient(httpServer.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.StartShell(ctx, 80, 24, nil); err != nil {
		t.Fatal(err)
	}
	conn, err := client.AttachShell(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	command := "printf 'marker=%s token=%s key=%s\\n' \"$MERIDIAN_SHELL_MARKER\" \"$MERIDIAN_PROVIDER_TOKEN\" \"$EXAMPLE_API_KEY\"\n"
	if err := conn.Write(ctx, websocket.MessageBinary, []byte(command)); err != nil {
		t.Fatal(err)
	}
	var output string
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(output, "marker=marker-ok") && time.Now().Before(deadline) {
		readCtx, stop := context.WithTimeout(ctx, time.Second)
		kind, payload, err := conn.Read(readCtx)
		stop()
		if err != nil {
			continue
		}
		if kind == websocket.MessageBinary {
			output += string(payload)
		}
	}
	if !strings.Contains(output, "marker=marker-ok") {
		t.Fatalf("shell output = %q", output)
	}
	if strings.Contains(output, "super-secret-token") {
		t.Fatal("shell printed a secret")
	}
	if err := client.StartShell(ctx, 80, 24, nil); err != nil {
		t.Fatal(err)
	}
	_ = conn.Write(ctx, websocket.MessageBinary, []byte("exit\n"))
	exitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := client.StartShell(exitCtx, 40, 12, nil); err != nil {
		t.Fatal(err)
	}
}

// terminalControl matches ANSI CSI and OSC sequences.
var terminalControl = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

func TestWorkspaceShellKeepsBurstOutputForASlowViewer(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client, err := NewClient(httpServer.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := client.AttachShell(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(maxFrameBytes)
	const lines = 20000
	// The markers are computed by the shell so the echoed command line does
	// not contain them; only real output does.
	command := "stty -echo; echo BEGIN-$((0+1)); seq 1 " + strconv.Itoa(lines) + "; echo BURST-$((1+1))\n"
	if err := conn.Write(ctx, websocket.MessageBinary, []byte(command)); err != nil {
		t.Fatal(err)
	}
	// Let the whole burst arrive before reading, as a slow browser would.
	time.Sleep(time.Second)
	var output strings.Builder
	for !strings.Contains(output.String(), "BURST-2") {
		kind, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read after %d bytes: %v", output.Len(), err)
		}
		if kind == websocket.MessageBinary {
			output.Write(payload)
		}
	}
	// Interactive bash on Linux writes terminal control sequences (such as
	// bracketed-paste mode) around command output, so only the text between
	// the markers is counted, with escape sequences removed.
	text := output.String()
	start := strings.Index(text, "BEGIN-1")
	end := strings.Index(text, "BURST-2")
	if start < 0 || end < start {
		t.Fatalf("markers missing from %d bytes of output", len(text))
	}
	seen := map[int]bool{}
	for _, line := range strings.Split(terminalControl.ReplaceAllString(text[start+len("BEGIN-1"):end], ""), "\n") {
		if value, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			seen[value] = true
		}
	}
	for value := 1; value <= lines; value++ {
		if !seen[value] {
			t.Fatalf("line %d was dropped (%d of %d lines arrived)", value, len(seen), lines)
		}
	}
}
