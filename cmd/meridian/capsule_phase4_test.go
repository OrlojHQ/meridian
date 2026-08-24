package main

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type syncTarEntry struct {
	name, link string
	kind       byte
	mode       int64
	content    string
}

func syncTar(t *testing.T, entries ...syncTarEntry) io.Reader {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, entry := range entries {
		size := int64(0)
		if entry.kind == tar.TypeReg {
			size = int64(len(entry.content))
		}
		if err := writer.WriteHeader(&tar.Header{
			Name: entry.name, Linkname: entry.link, Typeflag: entry.kind,
			Mode: entry.mode, Size: size,
		}); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := io.WriteString(writer, entry.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(archive.Bytes())
}

func TestExtractSyncArchivePreservesPortableEntriesAndExcludesPrivatePaths(t *testing.T) {
	stage := t.TempDir()
	err := extractSyncArchive(context.Background(), syncTar(t,
		syncTarEntry{name: ".git", kind: tar.TypeDir, mode: 0o755},
		syncTarEntry{name: ".git/config", kind: tar.TypeReg, mode: 0o600, content: "private"},
		syncTarEntry{name: ".meridian-prepared", kind: tar.TypeReg, mode: 0o600, content: "private"},
		syncTarEntry{name: "bin", kind: tar.TypeDir, mode: 0o755},
		syncTarEntry{name: "bin/tool", kind: tar.TypeReg, mode: 0o755, content: "#!/bin/sh\n"},
		syncTarEntry{name: "tool", link: "bin/tool", kind: tar.TypeSymlink, mode: 0o777},
	), stage)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(stage, "bin", "tool"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable mode was not preserved: %v %v", info, err)
	}
	if value, err := os.Readlink(filepath.Join(stage, "tool")); err != nil || value != "bin/tool" {
		t.Fatalf("safe symlink was not preserved: %q %v", value, err)
	}
	for _, name := range []string{".git", ".meridian-prepared"} {
		if _, err := os.Lstat(filepath.Join(stage, name)); !os.IsNotExist(err) {
			t.Fatalf("private path %q was extracted: %v", name, err)
		}
	}
}

func TestExtractSyncArchiveRejectsMaliciousEntries(t *testing.T) {
	tests := map[string]syncTarEntry{
		"traversal":      {name: "../outside", kind: tar.TypeReg, mode: 0o600},
		"absolute":       {name: "/outside", kind: tar.TypeReg, mode: 0o600},
		"symlink escape": {name: "escape", link: "../outside", kind: tar.TypeSymlink, mode: 0o777},
		"special file":   {name: "pipe", kind: tar.TypeFifo, mode: 0o600},
	}
	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			if err := extractSyncArchive(
				context.Background(), syncTar(t, entry), t.TempDir(),
			); err == nil {
				t.Fatal("expected malicious archive to be rejected")
			}
		})
	}
}

func TestSyncMirrorPreservesGitAndMarker(t *testing.T) {
	target := t.TempDir()
	stage := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".git", "config"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".meridian-prepared"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "deleted"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "new"), []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := mirrorSyncStage(target, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(target, "deleted")); !os.IsNotExist(err) {
		t.Fatalf("force mirror did not delete old content: %v", err)
	}
	for name, expected := range map[string]string{
		".git/config": "preserve", ".meridian-prepared": "preserve", "new": "new",
	} {
		value, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(name)))
		if err != nil || string(value) != expected {
			t.Fatalf("%s = %q, %v", name, value, err)
		}
	}
}

func TestDefaultSyncRefusesReplacementAndPreservesTargetOnlyFiles(t *testing.T) {
	target := t.TempDir()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "conflict"), []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "conflict"), []byte("remote"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeTargetWithoutConflicts(target, stage); err == nil {
		t.Fatal("expected replacement conflict")
	}
	if err := os.Remove(filepath.Join(stage, "conflict")); err != nil {
		t.Fatal(err)
	}
	if err := mergeTargetWithoutConflicts(target, stage); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(stage, "conflict"))
	if err != nil || string(value) != "local" {
		t.Fatalf("target-only file was not preserved: %q %v", value, err)
	}
}

func TestSyncTargetRemoteAndDirtyChecks(t *testing.T) {
	target := t.TempDir()
	command := exec.Command("git", "init", target)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", target, "remote", "add", "origin", "git@github.com:OrlojHQ/meridian.git")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v: %s", err, output)
	}
	if err := validateSyncTarget(
		context.Background(), target, "https://github.com/orlojhq/MERIDIAN", false,
	); err != nil {
		t.Fatalf("equivalent GitHub remotes should match: %v", err)
	}
	if err := validateSyncTarget(
		context.Background(), target, "https://github.com/other/repository", false,
	); err == nil {
		t.Fatal("expected remote mismatch")
	}
	if err := os.WriteFile(filepath.Join(target, "dirty"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateSyncTarget(
		context.Background(), target, "https://github.com/orlojhq/meridian", false,
	); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("expected dirty target refusal, got %v", err)
	}
	if err := validateSyncTarget(
		context.Background(), target, "https://github.com/orlojhq/meridian", true,
	); err != nil {
		t.Fatalf("--force should permit a dirty safe target: %v", err)
	}
}

func TestShipRequiresApprovalBeforeAPIAccess(t *testing.T) {
	command := newCapsuleShipCommand(&cliConfig{tokenFile: filepath.Join(t.TempDir(), "missing")})
	command.SilenceErrors = true
	command.SilenceUsage = true
	command.SetArgs([]string{"capsule-1", "--branch", "feature"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("expected local approval refusal, got %v", err)
	}
}
