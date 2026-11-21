package capsuleproto

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// Notes delivery (ADR 0060). capsuled writes the Project notes and the
// tree's working notes as files under $HOME/.meridian, outside the
// workspace, at each session start and on sync-notes. Each set is written
// into a temporary directory through an os.Root at $HOME/.meridian and
// renamed into place, so no path in a note name and no symlink the agent
// planted can redirect a write. Files are 0444 in 0555 directories; the
// modes tell the agent the files are not for editing, but they are not a
// control. Nothing ever reads the directories back.

// NotesDelivery is the note sets a session start carries.
type NotesDelivery = coordproto.NotesDelivery

// NotesBaseDir is the directory both sets live under for a mailbox socket
// at $HOME/.meridian/coord.sock: $HOME/.meridian.
func NotesBaseDir(socket string) string {
	if socket == "" {
		return ""
	}
	return filepath.Dir(socket)
}

// noteDirs writes the note sets and remembers which working-note revisions
// the Capsule holds, so sync-notes can say which changed.
type noteDirs struct {
	mu      sync.Mutex
	base    string
	working map[string]int64
}

func (d *noteDirs) dir(tier string) string {
	if d == nil || d.base == "" {
		return ""
	}
	return filepath.Join(d.base, tier)
}

// deliver writes both sets of a session start. It reports which sets are
// in place; a set that could not be written is reported missing and its
// directory is left removed rather than stale.
func (d *noteDirs) deliver(delivery *NotesDelivery) (project, working bool) {
	if d == nil || d.base == "" || delivery == nil {
		return false, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var projectFiles, workingFiles []coordproto.NoteFile
	if delivery.DeliverProject {
		projectFiles = delivery.Project
	}
	if delivery.DeliverWorking {
		workingFiles = delivery.Working
	}
	project = d.replaceLocked(coordproto.ProjectNotesDirName, projectFiles, delivery.DeliverProject) == nil &&
		delivery.DeliverProject
	working = d.replaceLocked(coordproto.WorkingNotesDirName, workingFiles, delivery.DeliverWorking) == nil &&
		delivery.DeliverWorking
	d.working = map[string]int64{}
	if working {
		for _, file := range workingFiles {
			d.working[file.Name] = file.Revision
		}
	}
	return project, working
}

// syncWorking replaces the working-note set and returns its content-free
// view, with Changed set for every note whose revision differs from what
// the Capsule held.
func (d *noteDirs) syncWorking(files []coordproto.NoteFile) ([]coordproto.NoteRef, error) {
	if d == nil || d.base == "" {
		return nil, errors.New("working notes are not available in this Capsule")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.replaceLocked(coordproto.WorkingNotesDirName, files, true); err != nil {
		return nil, err
	}
	previous := d.working
	d.working = make(map[string]int64, len(files))
	refs := make([]coordproto.NoteRef, 0, len(files))
	for _, file := range files {
		d.working[file.Name] = file.Revision
		refs = append(refs, coordproto.NoteRef{
			Name: file.Name, Revision: file.Revision, Size: len(file.Content),
			Changed: previous[file.Name] != file.Revision,
		})
	}
	for name, revision := range previous {
		if _, ok := d.working[name]; !ok {
			refs = append(refs, coordproto.NoteRef{Name: name, Revision: revision, Changed: true, Deleted: true})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

// updateWorking replaces one working note in place after a write or a
// conflict, keeping the rest of the set.
func (d *noteDirs) updateWorking(file coordproto.NoteFile) error {
	if d == nil || d.base == "" {
		return errors.New("working notes are not available in this Capsule")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	files, err := d.readWorkingLocked()
	if err != nil {
		return err
	}
	kept := files[:0]
	for _, existing := range files {
		if existing.Name != file.Name {
			kept = append(kept, existing)
		}
	}
	if !file.Deleted {
		kept = append(kept, file)
	}
	if err := d.replaceLocked(coordproto.WorkingNotesDirName, kept, true); err != nil {
		return err
	}
	if d.working == nil {
		d.working = map[string]int64{}
	}
	if file.Deleted {
		delete(d.working, file.Name)
	} else {
		d.working[file.Name] = file.Revision
	}
	return nil
}

// readWorkingLocked reads the working notes capsuled last wrote, so one
// note can be replaced without asking meridiand for the rest. Only names
// it recorded are read; a file the agent added is ignored, and one it
// changed is replaced by its own content, which is the agent's own copy.
func (d *noteDirs) readWorkingLocked() ([]coordproto.NoteFile, error) {
	root, err := d.openBase()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []coordproto.NoteFile
	for name, revision := range d.working {
		if !coordproto.ValidNoteName(name) {
			continue
		}
		content, err := readNote(root, coordproto.WorkingNotesDirName+"/"+name+".md")
		if err != nil {
			continue
		}
		files = append(files, coordproto.NoteFile{Name: name, Revision: revision, Content: content})
	}
	return files, nil
}

// openBase opens $HOME/.meridian as a root, creating it, after checking
// that it is a real directory and that the root opened is that directory.
func (d *noteDirs) openBase() (*os.Root, error) {
	info, err := os.Lstat(d.base)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(d.base, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, err = os.Lstat(d.base)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("the notes base directory is not a directory")
	}
	root, err := os.OpenRoot(d.base)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("the notes base directory changed while it was opened")
	}
	return root, nil
}

// replaceLocked writes files as <base>/<tier>/<name>.md and renames the
// set into place. With deliver false it only removes the old set.
func (d *noteDirs) replaceLocked(tier string, files []coordproto.NoteFile, deliver bool) error {
	if tier != coordproto.ProjectNotesDirName && tier != coordproto.WorkingNotesDirName {
		return errors.New("unknown notes tier")
	}
	if len(files) > coordproto.MaxNoteFiles {
		return errors.New("too many notes")
	}
	root, err := d.openBase()
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat(tier); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write notes: $HOME/.meridian/%s is a symlink", tier)
	}
	suffix := randomHex(8)
	if !deliver {
		return removeTree(root, tier, suffix)
	}
	temporary := "." + tier + "-" + suffix
	if err := root.Mkdir(temporary, 0o700); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Chmod(temporary, 0o700)
			_ = root.RemoveAll(temporary)
		}
	}()
	seen := map[string]bool{}
	for _, file := range files {
		if !coordproto.ValidNoteName(file.Name) || seen[file.Name] || len(file.Content) > coordproto.MaxNoteBytes {
			return errors.New("invalid note file")
		}
		seen[file.Name] = true
		handle, err := root.OpenFile(temporary+"/"+file.Name+".md", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			return err
		}
		_, writeErr := handle.WriteString(file.Content)
		closeErr := handle.Close()
		if writeErr != nil || closeErr != nil {
			return errors.Join(writeErr, closeErr)
		}
	}
	if err := root.Chmod(temporary, 0o555); err != nil {
		return err
	}
	if err := removeTree(root, tier, suffix); err != nil {
		return err
	}
	if err := root.Rename(temporary, tier); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// removeTree moves an existing set aside and removes it. A set the agent
// made read-only is made writable first.
func removeTree(root *os.Root, tier, suffix string) error {
	info, err := root.Lstat(tier)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace notes: $HOME/.meridian/%s is a symlink", tier)
	}
	old := "." + tier + "-old-" + suffix
	if err := root.Rename(tier, old); err != nil {
		return err
	}
	if info.IsDir() {
		_ = root.Chmod(old, 0o700)
	}
	// What the agent added under the old set may not be removable; it is
	// left hidden rather than blocking the new set.
	_ = root.RemoveAll(old)
	return nil
}
