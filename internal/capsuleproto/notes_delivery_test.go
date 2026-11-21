package capsuleproto

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// writableNotes lets the test's temporary directory be removed: notes are
// written into 0555 directories.
func writableNotes(t *testing.T, base string) {
	t.Cleanup(func() {
		_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
}

func TestNotesAreWrittenReadOnlyAndReplacedAsASet(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".meridian")
	writableNotes(t, base)
	dirs := &noteDirs{base: base}
	project, working := dirs.deliver(&NotesDelivery{
		DeliverProject: true, DeliverWorking: true,
		Project: []coordproto.NoteFile{{Name: "tests", Revision: 2, Content: "run make test\n"}},
		Working: []coordproto.NoteFile{{Name: "findings", Revision: 1, Content: "parser is slow\n"}},
	})
	if !project || !working {
		t.Fatalf("delivered = %v %v", project, working)
	}
	file := filepath.Join(base, coordproto.ProjectNotesDirName, "tests.md")
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "run make test\n" {
		t.Fatalf("note = %q, %v", data, err)
	}
	info, _ := os.Stat(file)
	dirInfo, _ := os.Stat(filepath.Dir(file))
	if info.Mode().Perm() != 0o444 || dirInfo.Mode().Perm() != 0o555 {
		t.Fatalf("modes = %v %v", info.Mode(), dirInfo.Mode())
	}
	// A later session without Project notes removes the set; a note that left
	// the working set is gone.
	project, working = dirs.deliver(&NotesDelivery{DeliverWorking: true})
	if project || !working {
		t.Fatalf("second delivery = %v %v", project, working)
	}
	if _, err := os.Stat(filepath.Join(base, coordproto.ProjectNotesDirName)); !os.IsNotExist(err) {
		t.Fatalf("Project notes kept after delivery was turned off: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, coordproto.WorkingNotesDirName, "findings.md")); !os.IsNotExist(err) {
		t.Fatalf("a removed working note was kept: %v", err)
	}
	entries, _ := os.ReadDir(base)
	for _, entry := range entries {
		if entry.Name() != coordproto.WorkingNotesDirName {
			t.Errorf("left behind %s", entry.Name())
		}
	}
}

func TestNotesRefuseASymlinkedTargetAndBase(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".meridian")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(root, "workspace")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(base, coordproto.ProjectNotesDirName)); err != nil {
		t.Fatal(err)
	}
	dirs := &noteDirs{base: base}
	project, _ := dirs.deliver(&NotesDelivery{DeliverProject: true,
		Project: []coordproto.NoteFile{{Name: "tests", Revision: 1, Content: "x"}}})
	if project {
		t.Fatal("notes were written through a symlink")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("a note landed in the symlink target: %v", entries)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(elsewhere, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := (&noteDirs{base: linked}).syncWorking(nil); err == nil {
		t.Fatal("a symlinked base was written")
	}
}

func TestSyncReportsChangesAndAWriteRefreshesOneNote(t *testing.T) {
	dirs := &noteDirs{base: filepath.Join(t.TempDir(), ".meridian")}
	writableNotes(t, dirs.base)
	dirs.deliver(&NotesDelivery{DeliverWorking: true, Working: []coordproto.NoteFile{
		{Name: "a", Revision: 1, Content: "one"}, {Name: "b", Revision: 1, Content: "two"},
	}})
	refs, err := dirs.syncWorking([]coordproto.NoteFile{{Name: "a", Revision: 2, Content: "uno"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].Name != "a" || !refs[0].Changed || refs[1].Name != "b" || !refs[1].Deleted {
		t.Fatalf("refs = %+v", refs)
	}
	if err := dirs.updateWorking(coordproto.NoteFile{Name: "c", Revision: 1, Content: "three"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a": "uno", "c": "three"} {
		data, err := os.ReadFile(filepath.Join(dirs.base, coordproto.WorkingNotesDirName, name+".md"))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v", name, data, err)
		}
	}
}

func TestDeliveredIndexNamesOnlyTheSetsInPlace(t *testing.T) {
	dirs := &noteDirs{base: "/home/capsule/.meridian"}
	index := &adapterproto.NotesIndex{Notes: []adapterproto.NotesEntry{
		{Tier: adapterproto.NotesTierProject, Name: "tests", Size: 3, Revision: 1},
		{Tier: adapterproto.NotesTierWorking, Name: "findings", Size: 3, Revision: 1},
	}}
	got := deliveredIndex(index, dirs, true, false)
	if got == nil || got.ProjectDir != "/home/capsule/.meridian/project-notes" || got.WorkingDir != "" ||
		len(got.Notes) != 1 || got.Notes[0].Name != "tests" {
		encoded, _ := json.Marshal(got)
		t.Fatalf("index = %s", encoded)
	}
	if deliveredIndex(index, dirs, false, false) != nil {
		t.Fatal("an index without delivered notes")
	}
}

func TestMailboxResolvesFromFileAndRefusesUnsafeOnes(t *testing.T) {
	home := t.TempDir()
	notes := filepath.Join(home, "notes")
	if err := os.Mkdir(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "scratch.md"), []byte("found it"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "empty.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if content, err := readPrivateNote(notes, "scratch.md"); err != nil || content != "found it" {
		t.Fatalf("fromFile = %q, %v", content, err)
	}
	if _, err := readPrivateNote(notes, "empty.md"); err == nil {
		t.Fatal("an empty fromFile was accepted")
	}
	if _, err := readPrivateNote(notes, "missing.md"); err == nil {
		t.Fatal("a missing fromFile was accepted")
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(notes, "escape.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateNote(notes, "escape.md"); err == nil {
		t.Fatal("fromFile followed a symlink out of the notes directory")
	}
}
