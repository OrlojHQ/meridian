package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

func TestReworkMessageFitsOneFrame(t *testing.T) {
	build := func(command string) func(string) string {
		return func(tail string) string {
			return "Required check `test` (`" + command + "`) failed.\n```\n" + tail + "\n```\nFix it. (Attempt 2 of 3)"
		}
	}
	// Every line is multi-byte, so a byte cut lands inside a rune.
	tail := strings.Repeat("ошибка проверки €€€\n", 20000)
	command := clipUTF8(strings.Repeat("ä", 128*4096/2), reworkCommandBytes)
	if len(command) > reworkCommandBytes || !utf8.ValidString(command) || !strings.HasSuffix(command, "…") {
		t.Fatalf("command = %d bytes, valid %v", len(command), utf8.ValidString(command))
	}
	message := fitRework(build(command), tail)
	if len(message) > adapterproto.MaxContentBytes {
		t.Fatalf("message = %d bytes", len(message))
	}
	if !utf8.ValidString(message) {
		t.Fatal("message is not valid UTF-8")
	}
	if !strings.HasSuffix(message, "(Attempt 2 of 3)") {
		t.Fatalf("message lost its instruction: %q", message[len(message)-80:])
	}
	if short := fitRework(build("make test"), "boom"); !strings.Contains(short, "boom") {
		t.Fatalf("short message = %q", short)
	}
}
