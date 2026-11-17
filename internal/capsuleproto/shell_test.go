package capsuleproto

import (
	"context"
	"net/http/httptest"
	"path/filepath"
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
	if err := client.StartShell(ctx, 80, 24); err != nil {
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
	if err := client.StartShell(ctx, 80, 24); err != nil {
		t.Fatal(err)
	}
	_ = conn.Write(ctx, websocket.MessageBinary, []byte("exit\n"))
	exitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := client.StartShell(exitCtx, 40, 12); err != nil {
		t.Fatal(err)
	}
}
