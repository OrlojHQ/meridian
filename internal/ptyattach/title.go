package ptyattach

import (
	"io"
	"os"
	"strings"
	"unicode"
)

const maxTerminalTitleRunes = 160

type stickyTerminalTitleWriter struct {
	writer   io.Writer
	sequence []byte
}

func (w stickyTerminalTitleWriter) Write(value []byte) (int, error) {
	count, err := w.writer.Write(value)
	if err == nil && count == len(value) && len(w.sequence) > 0 {
		// Best effort: the remote payload has already been written unchanged.
		// Reasserting afterward makes Meridian win when a harness emits its own
		// OSC title, without parsing or filtering arbitrary PTY output.
		_, _ = w.writer.Write(w.sequence)
	}
	return count, err
}

func terminalTitleEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("MERIDIAN_TERMINAL_TITLE")))
	return value != "0" && value != "false" && value != "off"
}

func terminalTitleSequence(title string) []byte {
	title = sanitizeTerminalTitle(title)
	if title == "" {
		return nil
	}
	return []byte("\x1b]0;" + title + "\a")
}

func sanitizeTerminalTitle(value string) string {
	runes := make([]rune, 0, min(len([]rune(value)), maxTerminalTitleRunes))
	for _, current := range value {
		if unicode.IsControl(current) {
			continue
		}
		runes = append(runes, current)
		if len(runes) == maxTerminalTitleRunes {
			break
		}
	}
	return strings.TrimSpace(string(runes))
}

func writeTerminalTitle(writer io.Writer, title string) bool {
	if writer == nil || !terminalTitleEnabled() {
		return false
	}
	sequence := terminalTitleSequence(title)
	if len(sequence) == 0 {
		return false
	}
	_, _ = writer.Write(sequence)
	return true
}

func keepTerminalTitle(writer io.Writer, title string) io.Writer {
	if writer == nil || !terminalTitleEnabled() {
		return writer
	}
	sequence := terminalTitleSequence(title)
	if len(sequence) == 0 {
		return writer
	}
	return stickyTerminalTitleWriter{writer: writer, sequence: sequence}
}
