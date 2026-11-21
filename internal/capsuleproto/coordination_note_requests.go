package capsuleproto

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// Note requests (ADR 0060). write-note, sync-notes and propose-note are
// relayed to meridiand from a lead or a subagent. The mailbox resolves
// fromFile from the Capsule's private notes before relaying, and writes the
// working-note files meridiand sends back; a tool never receives note
// content in a response.

// SetNotesBase enables working notes under base ($HOME/.meridian) for a
// mailbox that is not part of a Server. An empty base disables them.
func (m *Mailbox) SetNotesBase(base string) {
	if base == "" {
		m.SetNoteDirectories(nil)
		return
	}
	m.SetNoteDirectories(&noteDirs{base: base})
}

// SetNoteDirectories enables note delivery through dirs; nil disables it.
func (m *Mailbox) SetNoteDirectories(dirs *noteDirs) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.noteDirs = dirs
}

func (m *Mailbox) noteRequest(ctx context.Context, request coordproto.Request) coordproto.Response {
	m.mu.Lock()
	dirs, private := m.noteDirs, m.notes
	m.mu.Unlock()
	if request.FromFile != "" {
		content, err := readPrivateNote(private, request.FromFile)
		if err != nil {
			var refusal *notesError
			if errors.As(err, &refusal) {
				return coordproto.Fail(refusal.code, refusal.message)
			}
			return coordproto.Fail(coordproto.CodeInternal, "could not read fromFile")
		}
		request.Content, request.FromFile = content, ""
		if err := request.Validate(); err != nil {
			return coordproto.Fail(coordproto.CodeInvalidRequest, err.Error())
		}
	}
	response := m.relay(ctx, request)
	files := response.NoteFiles
	response.NoteFiles = nil
	switch request.Type {
	case coordproto.KindSyncNotes:
		if !response.OK {
			return response
		}
		refs, err := dirs.syncWorking(files)
		if err != nil {
			return coordproto.Fail(coordproto.CodeUnavailable, "the working notes could not be written in this Capsule")
		}
		response.Notes = refs
	case coordproto.KindWriteNote:
		if response.OK && response.Note != nil {
			_ = dirs.updateWorking(coordproto.NoteFile{
				Name: request.Name, Revision: response.Note.Revision, Content: request.Content, Deleted: request.Delete,
			})
		} else if response.Error != nil && response.Error.Code == coordproto.CodeConflict && len(files) == 1 &&
			files[0].Name == request.Name {
			// Refresh the note so the agent can read the current text and
			// write again against its revision.
			_ = dirs.updateWorking(files[0])
		}
	}
	return response
}

// readPrivateNote reads one note from the Capsule's private notes for
// fromFile, with the rules contextFiles uses. An empty file is refused:
// a note's content is given, or it is deleted.
func readPrivateNote(dir, name string) (string, error) {
	if dir == "" {
		return "", refuseNotes(coordproto.CodeUnavailable, "fromFile is not available in this Capsule")
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
	content, err := readNote(root, name)
	if err != nil {
		return "", err
	}
	if len(content) > coordproto.MaxNoteBytes {
		return "", refuseNotes(coordproto.CodeLimitExceeded, "fromFile %q is larger than the %d-byte note limit", name, coordproto.MaxNoteBytes)
	}
	if content == "" {
		return "", refuseNotes(coordproto.CodeInvalidRequest, "fromFile %q is empty", name)
	}
	return content, nil
}

// SetClock replaces the clock the mailbox's request rate uses, for tests
// that drive more requests than a real minute allows.
func (m *Mailbox) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}
