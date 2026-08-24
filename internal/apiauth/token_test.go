package apiauth

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenOrCreateTokenIsRestrictiveStableAndConcurrent(t *testing.T) {
	path := DefaultTokenPath(t.TempDir())
	const callers = 16
	values := make(chan string, callers)
	errorsChannel := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := OpenOrCreateToken(path); err != nil {
				errorsChannel <- err
				return
			}
			value, err := ReadTokenFile(path)
			if err != nil {
				errorsChannel <- err
				return
			}
			values <- value
		}()
	}
	wait.Wait()
	close(values)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
	var first string
	for value := range values {
		if first == "" {
			first = value
		}
		if value != first {
			t.Fatal("concurrent startup observed different API tokens")
		}
	}
	if len(first) != 43 {
		t.Fatalf("encoded token length = %d", len(first))
	}
	fileInfo, err := os.Lstat(path)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("token file = %#v, %v", fileInfo, err)
	}
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("token directory = %#v, %v", directoryInfo, err)
	}
	token, err := LoadToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if !token.Matches(first) || token.Matches(first+"wrong") {
		t.Fatal("token comparison did not enforce exact value")
	}
}

func TestLoadTokenRejectsUnsafePathsAndModes(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "auth")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "token")
	if err := os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path); err == nil {
		t.Fatal("permissive token file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path); err == nil {
		t.Fatal("symlink token file accepted")
	}
}
