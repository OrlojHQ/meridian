package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTokenFilePermissions(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(token), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			got, err := loadToken(path)
			if err != nil || got != token {
				t.Fatalf("loadToken() = %q, %v", got, err)
			}
		})
	}
}

func TestLoadTokenRejectsExposedAndNonRegularFiles(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o660, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte("token"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := loadToken(path); err == nil {
				t.Fatal("exposed token file was accepted")
			}
		})
	}

	directory := filepath.Join(t.TempDir(), "token")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(directory); err == nil {
		t.Fatal("directory token path was accepted")
	}
}

func TestLoadTokenAcceptsContainedProjectionSymlinkOnly(t *testing.T) {
	root := t.TempDir()
	projection := filepath.Join(root, "..2026_08_24")
	if err := os.Mkdir(projection, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(projection, "token")
	if err := os.WriteFile(target, []byte("projected-token"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o440); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "token")
	if err := os.Symlink(filepath.Join("..2026_08_24", "token"), path); err != nil {
		t.Fatal(err)
	}
	if value, err := loadToken(path); err != nil || value != "projected-token" {
		t.Fatalf("contained projection = %q, %v", value, err)
	}

	outside := filepath.Join(t.TempDir(), "outside-token")
	if err := os.WriteFile(outside, []byte("outside"), 0o400); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.Symlink(outside, external); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken(external); err == nil {
		t.Fatal("external token symlink was accepted")
	}
}
