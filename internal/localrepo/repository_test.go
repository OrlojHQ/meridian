package localrepo

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverCurrentGitHubOrigin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample-project")
	runGit(t, "", "init", "-q", root)
	runGit(t, root, "remote", "add", "origin", "git@github.com:Example/sample-project.git")

	repository, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if repository.Root != resolvedRoot || repository.Name != "sample-project" ||
		repository.OriginURL != "https://github.com/Example/sample-project.git" {
		t.Fatalf("repository = %#v", repository)
	}
}

func TestDiscoverReturnsWorktreeWithoutOrigin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample-project")
	runGit(t, "", "init", "-q", root)

	repository, err := Discover(context.Background(), root)
	if !errors.Is(err, ErrNoOrigin) {
		t.Fatalf("Discover error = %v, want %v", err, ErrNoOrigin)
	}
	if repository.Root == "" || repository.Name != "sample-project" || repository.OriginURL != "" {
		t.Fatalf("repository = %#v", repository)
	}
}

func TestNormalizeRepositoryInput(t *testing.T) {
	tests := map[string]string{
		"Example/project":                          "https://github.com/Example/project",
		"github.com/Example/project":               "https://github.com/Example/project",
		"git@github.com:Example/project.git":       "https://github.com/Example/project.git",
		"ssh://git@github.com/Example/project.git": "https://github.com/Example/project.git",
		"https://gitlab.com/Example/project.git":   "https://gitlab.com/Example/project.git",
	}
	for input, want := range tests {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("Normalize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestRepositoryName(t *testing.T) {
	for input, want := range map[string]string{
		"https://github.com/Example/project.git": "project",
		"git@github.com:Example/project.git":     "project",
		"/tmp/project":                           "project",
	} {
		if got := Name(input); got != want {
			t.Fatalf("Name(%q) = %q, want %q", input, got, want)
		}
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	if directory != "" {
		command.Dir = directory
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
