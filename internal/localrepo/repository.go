// Package localrepo discovers safe, portable repository defaults from the
// caller's current Git worktree.
package localrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	discoveryTimeout = 2 * time.Second
	maxGitOutput     = 4096
)

var (
	// ErrNotWorktree means the selected directory is not inside a Git worktree.
	ErrNotWorktree = errors.New("current directory is not a Git worktree")
	// ErrNoOrigin means the worktree exists but does not identify a clone source.
	ErrNoOrigin = errors.New("current Git worktree has no origin remote")
)

// Repository is the current worktree and its portable origin identity.
type Repository struct {
	Root      string
	Name      string
	OriginURL string
}

// Discover reads the current worktree root and origin without running hooks or
// repository-provided commands.
func Discover(parent context.Context, directory string) (Repository, error) {
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return Repository{}, fmt.Errorf("read current directory: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(parent, discoveryTimeout)
	defer cancel()

	root, err := gitOutput(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, ErrNotWorktree
	}
	repository := Repository{Root: root, Name: filepath.Base(root)}
	origin, err := gitOutput(ctx, root, "remote", "get-url", "origin")
	if err != nil {
		return repository, ErrNoOrigin
	}
	origin, err = Normalize(origin)
	if err != nil {
		return repository, fmt.Errorf("normalize origin remote: %w", err)
	}
	repository.OriginURL = origin
	return repository, nil
}

// Normalize expands GitHub shorthand and converts GitHub SSH remotes to
// credential-free HTTPS URLs suitable for Capsule cloning.
func Normalize(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > maxGitOutput || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("repository URL is invalid")
	}
	if strings.HasPrefix(value, "github.com/") {
		value = "https://" + value
	}
	if shorthandGitHub(value) {
		return "https://github.com/" + value, nil
	}
	if repositoryPath, ok := strings.CutPrefix(value, "git@github.com:"); ok {
		if !validGitHubPath(repositoryPath) {
			return "", errors.New("GitHub repository path is invalid")
		}
		return "https://github.com/" + repositoryPath, nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("repository URL is invalid")
	}
	if parsed.Scheme == "ssh" && strings.EqualFold(parsed.Hostname(), "github.com") {
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
			return "", errors.New("GitHub SSH URL is invalid")
		}
		if parsed.User != nil && parsed.User.Username() != "" && parsed.User.Username() != "git" {
			return "", errors.New("GitHub SSH user is unsupported")
		}
		repositoryPath := strings.TrimPrefix(parsed.Path, "/")
		if !validGitHubPath(repositoryPath) {
			return "", errors.New("GitHub repository path is invalid")
		}
		return "https://github.com/" + repositoryPath, nil
	}
	return value, nil
}

// Name returns a useful Project name from a repository URL or path.
func Name(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if _, repositoryPath, ok := strings.Cut(value, ":"); ok &&
		!strings.Contains(value, "://") {
		value = repositoryPath
	} else if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		value = parsed.Path
	}
	name := path.Base(strings.TrimSuffix(strings.TrimRight(value, "/"), ".git"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func shorthandGitHub(value string) bool {
	if strings.Contains(value, "://") || strings.Count(value, "/") != 1 {
		return false
	}
	return validGitHubPath(value)
}

func validGitHubPath(value string) bool {
	value = strings.TrimSuffix(strings.Trim(value, "/"), ".git")
	owner, repository, ok := strings.Cut(value, "/")
	return ok && owner != "" && repository != "" &&
		owner != "." && owner != ".." && repository != "." && repository != ".." &&
		!strings.Contains(repository, "/") &&
		!strings.ContainsAny(owner+repository, " :@?#\\%")
}

func gitOutput(ctx context.Context, directory string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = &limitedBuffer{}
	if err := command.Run(); err != nil {
		return "", err
	}
	value := strings.TrimSpace(output.String())
	if value == "" || output.overflow {
		return "", errors.New("Git output is empty or oversized")
	}
	return value, nil
}

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := maxGitOutput + 1 - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(value)
	return original, nil
}
