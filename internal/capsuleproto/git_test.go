package capsuleproto

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitReportsWorkspaceWithoutRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()

	calls := map[string]func() error{
		"status": func() error { _, err := client.GitStatus(context.Background()); return err },
		"diff":   func() error { _, err := client.GitDiff(context.Background()); return err },
		"patch": func() error {
			_, err := client.GitPatch(context.Background(), GitPatchRequest{})
			return err
		},
	}
	for name, call := range calls {
		var protocolError *Error
		if err := call(); !errors.As(err, &protocolError) ||
			protocolError.Status != http.StatusUnprocessableEntity ||
			protocolError.Code != "git_no_repository" {
			t.Fatalf("%s without a repository = %v", name, err)
		}
	}
}

func TestGitFailureInsideRepositoryStaysGitFailed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	workspace := t.TempDir()
	// A .git that is not a repository makes git fail without the workspace
	// looking repository-less.
	if err := os.WriteFile(filepath.Join(workspace, ".git"), []byte("not a gitdir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()

	var protocolError *Error
	if _, err := client.GitStatus(context.Background()); !errors.As(err, &protocolError) ||
		protocolError.Code != "git_failed" {
		t.Fatalf("broken .git = %v", err)
	}
}
