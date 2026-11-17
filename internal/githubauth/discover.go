package githubauth

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const pathMarker = "__MERIDIAN_PATH__"

// loginPATHCommand prints the login shell's PATH between markers so a
// profile that writes other text does not hide it.
const loginPATHCommand = "printf '\\n" + pathMarker + "%s" + pathMarker + "\\n' \"$PATH\""

type resolved struct {
	path         string
	searched     []string
	explicit     bool
	explicitPath string
}

func (h HostCommands) resolve(ctx context.Context) resolved {
	if h.Path != "" {
		found := resolved{explicit: true, explicitPath: h.Path, searched: []string{h.Path}}
		if executableFile(h.Path) {
			found.path = h.Path
		}
		return found
	}
	searched := []string{"PATH"}
	if path, err := exec.LookPath("gh"); err == nil && executableFile(path) {
		return resolved{path: path, searched: searched}
	}
	candidates := h.candidates()
	for _, candidate := range candidates {
		searched = append(searched, candidate)
		if executableFile(candidate) {
			return resolved{path: candidate, searched: searched}
		}
	}
	if h.SkipLoginShell {
		return resolved{searched: searched}
	}
	loginPATH := h.LoginPATH
	if loginPATH == "" {
		loginPATH = cachedLoginPATH(ctx)
	}
	if loginPATH == "" {
		return resolved{searched: searched}
	}
	searched = append(searched, "login shell PATH")
	if path := lookInPath(loginPATH, "gh"); path != "" {
		return resolved{path: path, searched: searched}
	}
	return resolved{searched: searched}
}

func (h HostCommands) candidates() []string {
	if h.Candidates != nil {
		return h.Candidates
	}
	return defaultCandidates()
}

func defaultCandidates() []string {
	home, _ := os.UserHomeDir()
	return candidatePaths(home)
}

// candidatePaths is the install locations checked after PATH. home may be
// empty; ~/.local/bin is then omitted.
func candidatePaths(home string) []string {
	paths := []string{
		"/opt/homebrew/bin/gh",
		"/usr/local/bin/gh",
		"/home/linuxbrew/.linuxbrew/bin/gh",
	}
	if home != "" {
		paths = append(paths, filepath.Join(home, ".local", "bin", "gh"))
	}
	paths = append(paths, "/usr/bin/gh")
	return paths
}

func executableFile(path string) bool {
	if !filepath.IsAbs(path) || len(path) > 4096 || strings.ContainsAny(path, "\r\n\x00") {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func lookInPath(pathEnv, name string) string {
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if executableFile(candidate) {
			return candidate
		}
	}
	return ""
}

var (
	loginPATHOnce  sync.Once
	loginPATHValue string
)

func cachedLoginPATH(context.Context) string {
	loginPATHOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		loginPATHValue = readLoginShellPATH(ctx, loginShell())
	})
	return loginPATHValue
}

func loginShell() string {
	if shell := os.Getenv("SHELL"); executableFile(shell) {
		return shell
	}
	for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if executableFile(candidate) {
			return candidate
		}
	}
	return ""
}

func readLoginShellPATH(ctx context.Context, shell string) string {
	if shell == "" {
		return ""
	}
	command := exec.CommandContext(ctx, shell, "-lc", loginPATHCommand)
	prepareCommand(command)
	command.Env = baseEnv()
	command.Stdin = nil
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	wipe(stderr.Bytes())
	if err != nil {
		wipe(stdout.Bytes())
		return ""
	}
	path := parseMarkedPATH(stdout.String())
	wipe(stdout.Bytes())
	return path
}

func parseMarkedPATH(output string) string {
	start := strings.Index(output, pathMarker)
	if start < 0 {
		return ""
	}
	rest := output[start+len(pathMarker):]
	end := strings.Index(rest, pathMarker)
	if end < 0 {
		return ""
	}
	path := rest[:end]
	if path == "" || strings.ContainsAny(path, "\r\n\x00") {
		return ""
	}
	return path
}

// childEnv is the station's environment plus a PATH that can see gh and the
// usual install locations. HOME is filled from the user record when the
// session left it empty, so a keyring token stored under the user's home
// can be read.
func childEnv(ghPath string) []string {
	env := baseEnv()
	prefix := []string{
		filepath.Dir(ghPath),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	}
	if home := envValue(env, "HOME"); home != "" {
		prefix = append(prefix, filepath.Join(home, ".local", "bin"))
	}
	prefix = append(prefix, "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	env = setEnv(env, "PATH", joinPath(prefix, envValue(env, "PATH")))
	return env
}

func baseEnv() []string {
	env := os.Environ()
	if envValue(env, "HOME") == "" {
		if current, err := user.Current(); err == nil && current.HomeDir != "" {
			env = setEnv(env, "HOME", current.HomeDir)
		}
	}
	if envValue(env, "USER") == "" {
		if current, err := user.Current(); err == nil && current.Username != "" {
			env = setEnv(env, "USER", current.Username)
		}
	}
	return env
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	next := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		next = append(next, entry)
	}
	return append(next, prefix+value)
}

// prepareCommand stops a hung gh or login shell, including helpers it
// started, when the context ends. WaitDelay bounds pipe copies so a child
// that inherited stdout cannot keep the call blocked.
func prepareCommand(command *exec.Cmd) {
	command.WaitDelay = 500 * time.Millisecond
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		return command.Process.Kill()
	}
}

func joinPath(prefix []string, current string) string {
	seen := map[string]bool{}
	parts := make([]string, 0, len(prefix)+8)
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		parts = append(parts, dir)
	}
	for _, dir := range prefix {
		add(dir)
	}
	for _, dir := range filepath.SplitList(current) {
		add(dir)
	}
	return strings.Join(parts, string(os.PathListSeparator))
}
