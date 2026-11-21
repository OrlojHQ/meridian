package capsuleproto

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// CoordinationNotesDir is where a lead keeps the notes a spawn may attach,
// for a mailbox socket at $HOME/.meridian/coord.sock: $HOME/.meridian/notes.
func CoordinationNotesDir(socket string) string {
	if socket == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(socket), coordproto.NotesDirName)
}

// notesError is a refusal to attach notes, already phrased for the lead.
type notesError struct {
	code    string
	message string
}

func (e *notesError) Error() string { return e.message }

func refuseNotes(code, format string, arguments ...any) error {
	return &notesError{code: code, message: fmt.Sprintf(format, arguments...)}
}

// attachNotes returns prompt with each named note appended as a fenced,
// labelled block (ADR 0059). Notes are read through a root at dir, so no
// path or symlink can reach outside it, and only regular UTF-8 files are
// read. The combined prompt must fit coordproto.MaxPromptBytes; nothing is
// cut to fit. Notes are lead-authored text with the same trust as the
// prompt they join.
func attachNotes(dir, prompt string, names []string) (string, error) {
	if dir == "" {
		return "", refuseNotes(coordproto.CodeUnavailable, "contextFiles are not available in this Capsule")
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", refuseNotes(coordproto.CodeNotFound, "the notes directory $HOME/.meridian/%s does not exist", coordproto.NotesDirName)
	}
	if err != nil || !info.IsDir() {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "$HOME/.meridian/%s must be a directory, not a symlink", coordproto.NotesDirName)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "the notes directory cannot be opened")
	}
	defer root.Close()
	var builder strings.Builder
	builder.WriteString(prompt)
	if !strings.HasSuffix(prompt, "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString("\n## Notes attached by the lead\n\n" +
		"The lead attached these files from its notes. Each is fenced; the fence is not part of the file.\n")
	for _, name := range names {
		content, err := readNote(root, name)
		if err != nil {
			return "", err
		}
		writeNoteBlock(&builder, name, content)
		if builder.Len() > coordproto.MaxPromptBytes {
			break
		}
	}
	if builder.Len() > coordproto.MaxPromptBytes {
		return "", refuseNotes(coordproto.CodeContextTooLarge,
			"the prompt with its contextFiles exceeds %d bytes; attach fewer or shorter notes", coordproto.MaxPromptBytes)
	}
	return builder.String(), nil
}

// readNote reads one regular file under root. O_NONBLOCK keeps a FIFO from
// blocking the open; the file is then refused because it is not regular.
func readNote(root *os.Root, name string) (string, error) {
	file, err := root.OpenFile(filepath.FromSlash(name), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return "", refuseNotes(coordproto.CodeNotFound, "context file %q does not exist in $HOME/.meridian/%s", name, coordproto.NotesDirName)
	}
	if err != nil {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "context file %q cannot be read from $HOME/.meridian/%s", name, coordproto.NotesDirName)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "context file %q is not a regular file", name)
	}
	if info.Size() > coordproto.MaxPromptBytes {
		return "", refuseNotes(coordproto.CodeContextTooLarge,
			"context file %q is larger than the %d-byte prompt limit", name, coordproto.MaxPromptBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, coordproto.MaxPromptBytes+1))
	if err != nil {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "context file %q cannot be read", name)
	}
	if len(data) > coordproto.MaxPromptBytes {
		return "", refuseNotes(coordproto.CodeContextTooLarge,
			"context file %q is larger than the %d-byte prompt limit", name, coordproto.MaxPromptBytes)
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "context file %q is not UTF-8 text", name)
	}
	return string(data), nil
}

// writeNoteBlock appends a labelled note in a backtick fence longer than any
// backtick run in its content, so no line of the note can close the fence
// (CommonMark: a closing fence is at least as long as the opening one).
func writeNoteBlock(builder *strings.Builder, name, content string) {
	fence := strings.Repeat("`", max(3, longestRun(content, '`')+1))
	fmt.Fprintf(builder, "\n### %s (%d bytes)\n\n%stext\n", name, len(content), fence)
	builder.WriteString(content)
	if content != "" && !strings.HasSuffix(content, "\n") {
		builder.WriteString("\n")
	}
	builder.WriteString(fence + "\n")
}

func longestRun(value string, char byte) int {
	longest, current := 0, 0
	for index := 0; index < len(value); index++ {
		if value[index] == char {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return longest
}
