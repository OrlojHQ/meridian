package capsuleproto

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// errRepositoryAccess is a clone Git rejected for access. The error text is
// fixed. Git's own output is classified and then discarded.
var errRepositoryAccess = errors.New("repository access denied")

func cloneDenied(output []byte) bool {
	if len(output) > 4096 {
		output = output[:4096]
	}
	text := strings.ToLower(string(output))
	switch {
	case strings.Contains(text, "could not read username"),
		strings.Contains(text, "authentication failed"),
		strings.Contains(text, "repository not found"),
		strings.Contains(text, "terminal prompts disabled"),
		strings.Contains(text, "invalid username or token"),
		strings.Contains(text, "password authentication was removed"),
		strings.Contains(text, "requested url returned error: 401"),
		strings.Contains(text, "requested url returned error: 403"),
		strings.Contains(text, "requested url returned error: 404"):
		return true
	default:
		return false
	}
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type prefixWriter struct {
	buf []byte
	max int
}

func (w *prefixWriter) Write(value []byte) (int, error) {
	if w.max <= 0 {
		w.max = 4096
	}
	room := w.max - len(w.buf)
	if room > 0 {
		if len(value) > room {
			w.buf = append(w.buf, value[:room]...)
		} else {
			w.buf = append(w.buf, value...)
		}
	}
	return len(value), nil
}

func classifyCloneError(output []byte, err error) error {
	if err == nil {
		return nil
	}
	if cloneDenied(output) {
		return errRepositoryAccess
	}
	return err
}

// runGitClone runs an anonymous git clone. Credential helpers and prompts are
// off, so a private repository fails with a classifiable message instead of
// waiting for a username.
func runGitClone(ctx context.Context, repositoryURL, destination string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git",
		"-c", "credential.helper=",
		"-c", "core.askPass=",
		"clone", "--", repositoryURL, destination)
	command.Dir = filepath.Dir(destination)
	command.WaitDelay = 2 * time.Second
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = append(filterGitEnvironment(os.Environ()),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)
	output := &prefixWriter{max: 4096}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		return output.buf, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return output.buf, err
	case <-ctx.Done():
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return output.buf, ctx.Err()
	}
}

func filterGitEnvironment(host []string) []string {
	result := make([]string, 0, len(host))
	for _, item := range host {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GIT_") || key == "SSH_ASKPASS" ||
			key == "MERIDIAN_GIT_USERNAME" || key == "MERIDIAN_GIT_PASSWORD" {
			continue
		}
		result = append(result, item)
	}
	return result
}
