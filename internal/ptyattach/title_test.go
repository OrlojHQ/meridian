package ptyattach

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestTerminalTitleSequenceSanitizesControlsAndBoundsLength(t *testing.T) {
	title := " Meridian \x1b]0;spoof\a\n" + strings.Repeat("x", maxTerminalTitleRunes+20)
	sequence := terminalTitleSequence(title)
	if bytes.Contains(sequence, []byte("spoof\a")) || bytes.Contains(sequence, []byte("\n")) {
		t.Fatalf("unsafe title sequence = %q", sequence)
	}
	if !bytes.HasPrefix(sequence, []byte("\x1b]0;Meridian ]0;spoof")) ||
		!bytes.HasSuffix(sequence, []byte("\a")) {
		t.Fatalf("title sequence = %q", sequence)
	}
	if got := len([]rune(sanitizeTerminalTitle(title))); got > maxTerminalTitleRunes || got < maxTerminalTitleRunes-1 {
		t.Fatalf("sanitized title runes = %d", got)
	}
}

func TestRunKeepsTitleWithoutChangingSocketBytes(t *testing.T) {
	harness := newHarness(t)
	harness.options.AttachTitle = "Meridian · review · opencode"
	harness.options.RestoreTitle = "Meridian · project"
	payload := []byte("native\x1b]0;OpenCode\aoutput")
	harness.socket.reads <- socketRead{messageType: websocket.MessageBinary, value: payload}
	harness.socket.reads <- socketRead{err: websocket.CloseError{Code: websocket.StatusNormalClosure}}

	if err := Run(context.Background(), harness.options); err != nil {
		t.Fatal(err)
	}
	want := append(terminalTitleSequence(harness.options.AttachTitle), payload...)
	want = append(want, terminalTitleSequence(harness.options.AttachTitle)...)
	want = append(want, terminalTitleSequence(harness.options.RestoreTitle)...)
	if got := []byte(readTemp(t, harness.output)); !bytes.Equal(got, want) {
		t.Fatalf("local output = %q, want %q", got, want)
	}
	for _, write := range harness.socket.written() {
		if bytes.Contains(write.value, []byte("\x1b]0;")) {
			t.Fatalf("terminal title entered PTY socket: %q", write.value)
		}
	}
}

func TestTerminalTitleCanBeDisabled(t *testing.T) {
	t.Setenv("MERIDIAN_TERMINAL_TITLE", "0")
	var output bytes.Buffer
	if writeTerminalTitle(&output, "Meridian") || output.Len() != 0 {
		t.Fatalf("disabled title output = %q", output.Bytes())
	}
}
