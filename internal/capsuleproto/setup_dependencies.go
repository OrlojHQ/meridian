package capsuleproto

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

var errSetupDependencies = errors.New("harness_dependency_install_failed")

func installSetupDependencies(ctx context.Context, home string, packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for _, pkg := range packages {
		// Populate npm's execution cache without running package code. The only
		// command executed after installation is the image's fixed /bin/true.
		args := []string{"exec", "--cache", filepath.Join(home, ".npm"), "--userconfig=/dev/null", "--globalconfig=" + filepath.Join(home, ".meridian-npm-globalrc"), "--registry=https://registry.npmjs.org", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error", "--yes", "--package=" + pkg, "--", "/bin/true"}
		command := exec.CommandContext(ctx, "/opt/meridian-node/bin/npm", args...)
		command.Dir = home
		command.Env = setupEnvironment(childEnvironment(nil), map[string]string{"HOME": home, "PATH": "/opt/meridian-node/bin:" + os.Getenv("PATH")})
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error {
			if command.Process == nil {
				return nil
			}
			err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		command.WaitDelay = 2 * time.Second
		if err := command.Run(); err != nil {
			return errSetupDependencies
		}
	}

	return nil
}
