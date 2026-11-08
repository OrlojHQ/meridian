package harnessadapter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	workspace, err := os.MkdirTemp("", "meridian-adapter-workspace-")
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(workspace); err != nil {
		panic(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	capsuleWorkspace = cwd
	code := m.Run()
	_ = os.RemoveAll(workspace)
	os.Exit(code)
}

func TestContainedWorkingDirectoryAcceptsWorkspaceAndNestedDir(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "src", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := containedWorkingDirectory(workspace, workspace)
	if err != nil || root != workspace {
		t.Fatalf("workspace root = %q, %v", root, err)
	}
	got, err := containedWorkingDirectory(workspace, nested)
	if err != nil || got != nested {
		t.Fatalf("nested = %q, %v", got, err)
	}
}

func TestContainedWorkingDirectoryRejectsHostPathEscapeAndUnclean(t *testing.T) {
	workspace := t.TempDir()
	host := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"host path":     host,
		"escaping path": filepath.Dir(workspace),
		"relative":      "src",
		"unclean":       workspace + string(filepath.Separator) + ".",
	}
	for name, directory := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := containedWorkingDirectory(workspace, directory); err == nil {
				t.Fatalf("accepted %q", directory)
			}
		})
	}
}

func TestContainedWorkingDirectoryRejectsSymlinks(t *testing.T) {
	workspace := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := containedWorkingDirectory(workspace, link); err == nil {
		t.Fatal("accepted a symlinked working directory")
	}
	nested := filepath.Join(link, "src")
	if err := os.MkdirAll(filepath.Join(target, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := containedWorkingDirectory(workspace, nested); err == nil {
		t.Fatal("accepted a path through a symlink")
	}

	parent := t.TempDir()
	workspaceLink := filepath.Join(parent, "workspace")
	if err := os.Symlink(target, workspaceLink); err != nil {
		t.Fatal(err)
	}
	if _, err := containedWorkingDirectory(workspaceLink, workspaceLink); err == nil {
		t.Fatal("accepted a symlinked workspace")
	}
}

func TestSessionWorkingDirectoryRejectsHostPath(t *testing.T) {
	previous := capsuleWorkspace
	t.Cleanup(func() { capsuleWorkspace = previous })
	capsuleWorkspace = filepath.Join(t.TempDir(), "capsule")
	if err := os.Mkdir(capsuleWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionWorkingDirectory(); err == nil {
		t.Fatal("inherited a host working directory")
	}
}

func TestSessionWorkingDirectoryReturnsContainedCwd(t *testing.T) {
	nested := filepath.Join(capsuleWorkspace, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	got, err := sessionWorkingDirectory()
	if err != nil || got != nested {
		t.Fatalf("cwd = %q, %v", got, err)
	}
}

func TestStartProcessPinsContainedWorkingDirectory(t *testing.T) {
	nested := filepath.Join(capsuleWorkspace, "child-cwd")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	child, err := startProcess(processConfig{Command: []string{"/usr/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if child.command.Dir != nested {
		t.Fatalf("child Dir = %q, want %q", child.command.Dir, nested)
	}
}

func TestStartProcessRejectsHostWorkingDirectory(t *testing.T) {
	previous := capsuleWorkspace
	t.Cleanup(func() { capsuleWorkspace = previous })
	capsuleWorkspace = filepath.Join(t.TempDir(), "capsule")
	if err := os.Mkdir(capsuleWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := startProcess(processConfig{Command: []string{"/usr/bin/true"}}); err == nil {
		t.Fatal("started a child with a host working directory")
	}
}
