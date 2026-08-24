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

func TestRealOpenCodeServerSmoke(t *testing.T) {
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
		return RunOpenCode(ctx, connection, connection, OpenCodeConfig{
			Command: []string{path}, Permissions: false,
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

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
