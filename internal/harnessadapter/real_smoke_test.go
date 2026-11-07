package harnessadapter

import (
	"context"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

func TestRealPiRPCSmoke(t *testing.T) {
	if os.Getenv("MERIDIAN_PI_SMOKE") != "1" {
		t.Skip("MERIDIAN_PI_SMOKE=1 is not set")
	}
	binary := defaultString(os.Getenv("MERIDIAN_PI_BINARY"), "pi")
	path, err := exec.LookPath(binary)
	if err != nil {
		t.Skip("Pi binary is unavailable")
	}
	prompt := os.Getenv("MERIDIAN_PI_SMOKE_PROMPT")
	if prompt == "" {
		t.Skip("MERIDIAN_PI_SMOKE_PROMPT is unset; provider credentials/model are not confirmed")
	}
	runRealSmoke(t, prompt, func(ctx context.Context, connection net.Conn) error {
		return RunPiRPC(ctx, connection, connection, PiConfig{Command: []string{path}})
	})
}

func TestRealOpenCodeACPSmoke(t *testing.T) {
	if os.Getenv("MERIDIAN_OPENCODE_SMOKE") != "1" {
		t.Skip("MERIDIAN_OPENCODE_SMOKE=1 is not set")
	}
	binary := defaultString(os.Getenv("MERIDIAN_OPENCODE_BINARY"), "opencode")
	path, err := exec.LookPath(binary)
	if err != nil {
		t.Skip("OpenCode binary is unavailable")
	}
	prompt := os.Getenv("MERIDIAN_OPENCODE_SMOKE_PROMPT")
	if prompt == "" {
		t.Skip("MERIDIAN_OPENCODE_SMOKE_PROMPT is unset; provider credentials/model are not confirmed")
	}
	runRealSmoke(t, prompt, func(ctx context.Context, connection net.Conn) error {
		return RunACP(ctx, connection, connection, ACPConfig{
			Command: []string{path, "acp"}, SecretEnv: []string{"OPENCODE_SERVER_PASSWORD"},
		})
	})
}

func TestRealClaudeStreamSmoke(t *testing.T) {
	if os.Getenv("MERIDIAN_CLAUDE_SMOKE") != "1" {
		t.Skip("MERIDIAN_CLAUDE_SMOKE=1 is not set")
	}
	path, err := exec.LookPath(defaultString(os.Getenv("MERIDIAN_CLAUDE_BINARY"), "claude"))
	if err != nil {
		t.Skip("Claude Code binary is unavailable")
	}
	prompt := os.Getenv("MERIDIAN_CLAUDE_SMOKE_PROMPT")
	if prompt == "" {
		t.Skip("MERIDIAN_CLAUDE_SMOKE_PROMPT is unset; provider credentials are not confirmed")
	}
	runRealSmoke(t, prompt, func(ctx context.Context, connection net.Conn) error {
		return RunClaudeStream(ctx, connection, connection, ClaudeConfig{Command: []string{path}})
	})
}

func TestRealCodexAppServerSmoke(t *testing.T) {
	if os.Getenv("MERIDIAN_CODEX_SMOKE") != "1" {
		t.Skip("MERIDIAN_CODEX_SMOKE=1 is not set")
	}
	path, err := exec.LookPath(defaultString(os.Getenv("MERIDIAN_CODEX_BINARY"), "codex"))
	if err != nil {
		t.Skip("Codex binary is unavailable")
	}
	prompt := os.Getenv("MERIDIAN_CODEX_SMOKE_PROMPT")
	if prompt == "" {
		t.Skip("MERIDIAN_CODEX_SMOKE_PROMPT is unset; provider credentials are not confirmed")
	}
	runRealSmoke(t, prompt, func(ctx context.Context, connection net.Conn) error {
		return RunCodexAppServer(ctx, connection, connection, CodexConfig{
			Command: []string{path}, ApprovalPolicy: "never", Sandbox: "read-only",
		})
	})
}

func runRealSmoke(
	t *testing.T,
	prompt string,
	run func(context.Context, net.Conn) error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	controller, adapter := net.Pipe()
	defer controller.Close()
	done := make(chan error, 1)
	go func() { done <- run(ctx, adapter) }()
	decoder, encoder := negotiateTestAdapter(t, controller, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "smoke-message", SessionID: "smoke-session",
		Role: adapterproto.RoleUser, Content: prompt,
	})
	_ = readKind(t, decoder, adapterproto.KindAssistantMessage)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "smoke-cancel",
	})
	_ = readKind(t, decoder, adapterproto.KindEnd)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
