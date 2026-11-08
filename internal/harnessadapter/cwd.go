package harnessadapter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// capsuleWorkspace is the Capsule mount. Tests may point it at a temporary
// directory that stands in for /workspace. Production never changes it.
var capsuleWorkspace = "/workspace"

func sessionWorkingDirectory() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", errors.New("resolve working directory")
	}
	return containedWorkingDirectory(capsuleWorkspace, cwd)
}

// containedWorkingDirectory returns directory when it is a real directory
// under workspace. It does not follow symlinks and does not accept a host
// path, an unclean path, or a path that escapes the workspace.
func containedWorkingDirectory(workspace, directory string) (string, error) {
	if !safeAbsoluteDirectory(workspace) || !safeAbsoluteDirectory(directory) {
		return "", errors.New("working directory is unsafe")
	}
	relative, err := filepath.Rel(workspace, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("working directory escapes workspace")
	}
	if err := rejectSymlinkedWorkingDirectory(workspace, relative); err != nil {
		return "", err
	}
	return directory, nil
}

func safeAbsoluteDirectory(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, 0)
}

func rejectSymlinkedWorkingDirectory(workspace, relative string) error {
	info, err := os.Lstat(workspace)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("working directory is unavailable")
	}
	if relative == "." {
		return nil
	}
	current := workspace
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return errors.New("working directory is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("working directory contains a symlink")
		}
		if !info.IsDir() {
			return errors.New("working directory is unavailable")
		}
	}
	return nil
}
