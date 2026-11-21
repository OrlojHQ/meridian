package capsuleproto

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// fileMailbox is a lead mailbox that knows one link with a report and keeps
// toFile output under a fresh home.
func fileMailbox(t *testing.T) (*Mailbox, string) {
	t.Helper()
	mailbox := enabledMailbox(t)
	home := t.TempDir()
	dir := CoordinationFilesDir(coordproto.SocketPathForHome(home))
	mailbox.SetFileDirectory(dir)
	mailbox.Notify(coordproto.Notification{
		Links: []coordproto.Link{{
			ID: "link-a", Name: "a", State: "active", ReportSeq: 3,
			Head: "bbbbbbb", BaseCommit: "aaaaaaa", FilesChanged: 2, Insertions: 10, Deletions: 1,
		}},
		Reports: []coordproto.Report{{Link: "link-a", Name: "a", Seq: 3, Status: "done", Summary: "full report text"}},
	})
	return mailbox, dir
}

// answerPatch replies to every polled patch request, and fails the test if
// toFile ever reaches meridiand.
func answerPatch(t *testing.T, mailbox *Mailbox, patch string) {
	t.Helper()
	poll := mailbox.Poll(context.Background(), 0, 0, 2*time.Second)
	var replies []coordproto.Reply
	for _, pending := range poll.Requests {
		if pending.Request.ToFile {
			t.Errorf("toFile was relayed to meridiand: %+v", pending.Request)
		}
		response := coordproto.OK()
		response.Patch, response.Head, response.BaseCommit = patch, "bbbbbbb", "aaaaaaa"
		response.FilesChanged, response.Insertions, response.Deletions = 2, 10, 1
		replies = append(replies, coordproto.Reply{Cursor: pending.Cursor, Response: response})
	}
	mailbox.Reply(replies)
}

func requirePrivateFile(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v", path, info.Mode())
	}
	for dir := filepath.Dir(path); filepath.Base(dir) != ".meridian"; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s = %v, %v", dir, info, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMailboxWritesPatchToAPrivateFile(t *testing.T) {
	mailbox, dir := fileMailbox(t)
	patch := "From 1234 Mon Sep 17 00:00:00 2001\n" + strings.Repeat("+line\n", 1000)
	done := make(chan coordproto.Response, 1)
	go func() {
		done <- mailbox.Handle(context.Background(), coordproto.Request{
			Protocol: coordproto.Version, Type: coordproto.KindPatch, Link: "link-a", ToFile: true,
		})
	}()
	answerPatch(t, mailbox, patch)
	response := <-done
	want := filepath.Join(dir, "link-a", "3.patch")
	if !response.OK || response.Patch != "" || response.File != want || response.FileBytes != len(patch) ||
		response.Head != "bbbbbbb" || response.BaseCommit != "aaaaaaa" || response.FilesChanged != 2 ||
		response.Insertions != 10 || response.Deletions != 1 {
		t.Fatalf("patch toFile = %+v", response)
	}
	if got := requirePrivateFile(t, want); string(got) != patch {
		t.Fatalf("patch file differs: %d bytes", len(got))
	}

	// Without toFile the bytes come back inline, as before.
	go func() {
		done <- mailbox.Handle(context.Background(), coordproto.Request{
			Protocol: coordproto.Version, Type: coordproto.KindPatch, Link: "link-a",
		})
	}()
	answerPatch(t, mailbox, patch)
	if inline := <-done; !inline.OK || inline.Patch != patch || inline.File != "" {
		t.Fatalf("inline patch = %+v", inline.File)
	}
}

func TestMailboxWritesFullReportToAPrivateFileAndMarksItRead(t *testing.T) {
	mailbox, dir := fileMailbox(t)
	response := mailbox.Handle(context.Background(), coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindResult, Link: "link-a", ToFile: true,
	})
	want := filepath.Join(dir, "link-a", "3.md")
	if !response.OK || response.File != want || len(response.Reports) != 1 || response.Reports[0].Summary != "" ||
		response.Reports[0].Seq != 3 || response.Link == nil || response.Link.FilesChanged != 2 {
		t.Fatalf("result toFile = %+v", response)
	}
	data := requirePrivateFile(t, want)
	if response.FileBytes != len(data) || !strings.Contains(string(data), "full report text") ||
		!strings.Contains(string(data), "Untrusted subagent output") {
		t.Fatalf("report file = %q", data)
	}
	if poll := mailbox.Poll(context.Background(), 0, 0, 10*time.Millisecond); poll.Consumed["link-a"] != 3 {
		t.Fatalf("report file did not mark the report read: %+v", poll.Consumed)
	}
}

func TestMailboxToFileNeedsAFileDirectory(t *testing.T) {
	mailbox, _ := fileMailbox(t)
	mailbox.SetFileDirectory("")
	response := mailbox.Handle(context.Background(), coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindResult, Link: "link-a", ToFile: true,
	})
	if response.OK || response.Error.Code != coordproto.CodeUnavailable {
		t.Fatalf("result toFile without a directory = %+v", response)
	}
	if poll := mailbox.Poll(context.Background(), 0, 0, 10*time.Millisecond); poll.Consumed["link-a"] != 0 {
		t.Fatalf("failed toFile marked the report read: %+v", poll.Consumed)
	}
}

func TestCoordinationFilesEvictOldestPastTheQuota(t *testing.T) {
	files := &coordFiles{dir: filepath.Join(t.TempDir(), "coord"), quota: 10}
	at := time.Unix(1_800_000_000, 0)
	write := func(link string, seq int, size int) string {
		t.Helper()
		path, err := files.write(link, seq, "patch", []byte(strings.Repeat("x", size)))
		if err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Minute)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := write("link-a", 1, 4)
	second := write("link-b", 1, 4)
	// Replacing a file does not count its old size.
	second = write("link-b", 1, 5)
	third := write("link-a", 2, 5)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("oldest file kept: %v", err)
	}
	for _, path := range []string{second, third} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("newer file removed: %v", err)
		}
	}
	if _, err := files.write("link-a", 3, "patch", []byte(strings.Repeat("x", 11))); err == nil {
		t.Fatal("a file over the quota was written")
	}
}

func TestCoordinationFilesStayInTheirDirectory(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(home, "workspace")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	files := &coordFiles{dir: filepath.Join(home, "coord"), quota: 1 << 20}
	for _, link := range []string{"", "..", "../workspace", "a/b", ".hidden", "link a"} {
		if _, err := files.write(link, 1, "patch", []byte("x")); err == nil {
			t.Fatalf("link %q was accepted", link)
		}
	}
	if _, err := files.write("link-a", 1, "sh", []byte("x")); err == nil {
		t.Fatal("unknown extension was accepted")
	}
	// A symlink planted for a link directory cannot redirect the write.
	if err := os.MkdirAll(files.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(files.dir, "link-evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := files.write("link-evil", 1, "patch", []byte("x")); err == nil {
		t.Fatal("write followed a symlinked link directory")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("write escaped into %s: %v", outside, entries)
	}
}

func TestServerRefusesCoordinationFilesInsideTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	_, err := NewServer(ServerConfig{
		Token: strings.Repeat("t", 32), Workspace: workspace,
		CoordinationFiles: filepath.Join(workspace, ".meridian", "coord"),
	})
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("NewServer = %v", err)
	}
}

func TestOutsideWorkspace(t *testing.T) {
	for dir, want := range map[string]bool{
		"/home/capsule/.meridian/coord": true,
		"/workspace-other/coord":        true,
		"/workspace":                    false,
		"/workspace/.meridian/coord":    false,
	} {
		if got := OutsideWorkspace("/workspace", dir); got != want {
			t.Errorf("OutsideWorkspace(%q) = %v", dir, got)
		}
	}
}
