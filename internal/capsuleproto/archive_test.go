package capsuleproto

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func makeFIFO(name string) error {
	return syscall.Mkfifo(name, 0o600)
}

func TestArchiveDeterministicRestoreRoundTrip(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "目录", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "目录", "run.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "zero"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte("0123456789abcdef"), 64<<10)
	if err := os.WriteFile(filepath.Join(source, "large.bin"), large, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("目录/run.sh", filepath.Join(source, "run-link")); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := WriteWorkspaceArchive(context.Background(), source, &first, ArchiveLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkspaceArchive(context.Background(), source, &second, ArchiveLimits{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("identical workspaces produced different archives")
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "old"), []byte("preserve only on failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreWorkspaceArchive(context.Background(), target, bytes.NewReader(first.Bytes()), ArchiveLimits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old workspace content remains: %v", err)
	}
	value, err := os.ReadFile(filepath.Join(target, "large.bin"))
	if err != nil || !bytes.Equal(value, large) {
		t.Fatal("large file did not round trip")
	}
	info, err := os.Stat(filepath.Join(target, "目录", "run.sh"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("executable bit did not round trip")
	}
	link, err := os.Readlink(filepath.Join(target, "run-link"))
	if err != nil || filepath.ToSlash(link) != "目录/run.sh" {
		t.Fatalf("symlink = %q, %v", link, err)
	}
}

func TestArchiveRejectsUnsafeAndSpecialEntriesWithoutMutation(t *testing.T) {
	cases := map[string][]tarSpec{
		"traversal": {{name: "../escape", kind: tar.TypeReg, body: "x"}},
		"absolute":  {{name: "/escape", kind: tar.TypeReg, body: "x"}},
		"duplicate": {
			{name: "same", kind: tar.TypeReg, body: "a"},
			{name: "same", kind: tar.TypeReg, body: "b"},
		},
		"symlink pivot": {
			{name: "pivot", kind: tar.TypeSymlink, link: "../outside"},
			{name: "pivot/file", kind: tar.TypeReg, body: "x"},
		},
		"fifo":   {{name: "pipe", kind: tar.TypeFifo}},
		"device": {{name: "device", kind: tar.TypeChar}},
		"socket": {{name: "socket", kind: 0x7f}},
	}
	for name, specs := range cases {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "original"), []byte("safe"), 0o600); err != nil {
				t.Fatal(err)
			}
			archive := makeTar(t, specs)
			if err := RestoreWorkspaceArchive(context.Background(), workspace, bytes.NewReader(archive), ArchiveLimits{}); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			value, err := os.ReadFile(filepath.Join(workspace, "original"))
			if err != nil || string(value) != "safe" {
				t.Fatalf("original workspace changed: %q %v", value, err)
			}
		})
	}
}

func TestArchiveBoundsCorruptionAndCancellationRollback(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "original"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	oversized := makeTar(t, []tarSpec{{name: "large", kind: tar.TypeReg, body: "12345"}})
	limits := ArchiveLimits{MaxFiles: 10, MaxPathBytes: 128, MaxFileBytes: 4, MaxTotalBytes: 4, MaxArchiveBytes: 1 << 20}
	if err := RestoreWorkspaceArchive(context.Background(), workspace, bytes.NewReader(oversized), limits); err == nil {
		t.Fatal("oversized archive accepted")
	}
	if err := RestoreWorkspaceArchive(context.Background(), workspace, bytes.NewReader([]byte("not a tar")), ArchiveLimits{}); err == nil {
		t.Fatal("corrupt archive accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RestoreWorkspaceArchive(ctx, workspace, bytes.NewReader(makeTar(t, []tarSpec{{name: "x", kind: tar.TypeReg, body: "x"}})), ArchiveLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	value, err := os.ReadFile(filepath.Join(workspace, "original"))
	if err != nil || string(value) != "safe" {
		t.Fatalf("original workspace changed: %q %v", value, err)
	}
}

func TestRestoreRollsBackImmediatelyWhenFinalSyncFails(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "original"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := makeTar(t, []tarSpec{{name: "replacement", kind: tar.TypeReg, body: "new"}})
	syncFailure := errors.New("injected workspace sync failure")
	err := restoreWorkspaceArchive(
		context.Background(), workspace, bytes.NewReader(archive), ArchiveLimits{},
		func(string) error { return syncFailure },
	)
	if !errors.Is(err, syncFailure) {
		t.Fatalf("restore error = %v", err)
	}
	value, readErr := os.ReadFile(filepath.Join(workspace, "original"))
	if readErr != nil || string(value) != "safe" {
		t.Fatalf("original workspace was not restored: %q, %v", value, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, "replacement")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement remained after rollback: %v", statErr)
	}
}

func TestRestorePreservesOutOfOrderDirectoryMode(t *testing.T) {
	archive := makeTar(t, []tarSpec{
		{name: "directory/child", kind: tar.TypeReg, body: "child", mode: 0o600},
		{name: "directory", kind: tar.TypeDir, mode: 0o701},
	})
	workspace := t.TempDir()
	if err := RestoreWorkspaceArchive(
		context.Background(), workspace, bytes.NewReader(archive), ArchiveLimits{},
	); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(workspace, "directory"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o701 {
		t.Fatalf("directory mode = %o, want 701", info.Mode().Perm())
	}
}

func TestCaptureRejectsSpecialFilesAndCancellation(t *testing.T) {
	workspace := t.TempDir()
	fifo := filepath.Join(workspace, "fifo")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := makeFIFO(fifo); err == nil {
		if err := WriteWorkspaceArchive(context.Background(), workspace, io.Discard, ArchiveLimits{}); err == nil {
			t.Fatal("FIFO capture accepted")
		}
	}
	_ = os.Remove(fifo)
	if err := os.WriteFile(filepath.Join(workspace, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteWorkspaceArchive(ctx, workspace, io.Discard, ArchiveLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation error = %v", err)
	}
}

type tarSpec struct {
	name string
	kind byte
	body string
	link string
	mode int64
}

func makeTar(t *testing.T, specs []tarSpec) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, spec := range specs {
		mode := spec.mode
		if mode == 0 {
			mode = 0o600
		}
		header := &tar.Header{
			Name: spec.name, Typeflag: spec.kind, Mode: mode,
			Size: int64(len(spec.body)), Linkname: spec.link,
		}
		if spec.kind != tar.TypeReg {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if spec.kind == tar.TypeReg {
			if _, err := writer.Write([]byte(spec.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
