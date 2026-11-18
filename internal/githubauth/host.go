package githubauth

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	// StateSignedIn means gh was found and `gh auth status` reports a login.
	// The token itself is not read until the person confirms.
	StateSignedIn = "signed_in"
	// StateNotSignedIn means gh was found and is not signed in to github.com.
	StateNotSignedIn = "not_signed_in"
	// StateTokenUnreadable means gh knows an account but could not read the
	// token, usually because the keyring is unreachable. Command output is
	// not kept: it can contain a token.
	StateTokenUnreadable = "token_unreadable"
	// StateNotFound means no executable gh was found.
	StateNotFound = "not_found"
	// StateUnavailable means this request must not look at the host login.
	StateUnavailable = "unavailable"
)

// loginPattern matches the account line from `gh auth status`. The token
// line in that output is masked; this pattern does not capture it.
var loginPattern = regexp.MustCompile(`Logged in to github\.com account ([A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)`)

// failedLoginPattern matches the account line when gh knows the account
// but could not use it. It does not capture a token.
var failedLoginPattern = regexp.MustCompile(`Failed to log in to github\.com account ([A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)`)

// ErrTokenUnreadable is returned when `gh auth token` fails or prints a
// value that cannot be stored. The error text never includes the output.
var ErrTokenUnreadable = errors.New("the GitHub CLI token could not be read")

// Status is the host gh login as far as this process can see. Detail is
// safe to show. It never contains a token or command output.
type Status struct {
	State    string
	Login    string
	Path     string
	Searched []string
	Detail   string
}

// HostCommands reads the local gh login. Probe uses `gh auth status`, which
// masks the token, and does not keep command output. Token uses
// `gh auth token` and must be called only after the person confirms.
type HostCommands struct {
	// Path is an absolute gh binary from --gh-path or MERIDIAN_GH_PATH.
	// When set, other locations are not searched.
	Path string
	// Candidates replaces the usual install locations. Nil uses the defaults.
	// An empty slice searches PATH and the login shell only.
	Candidates []string
	// LoginPATH, when set, is consulted after PATH and Candidates. Tests use
	// it instead of starting a login shell.
	LoginPATH string
	// SkipLoginShell skips the login-shell PATH lookup.
	SkipLoginShell bool
}

// Probe reports whether gh can be reused. It does not return the token.
func (h HostCommands) Probe(ctx context.Context) Status {
	found := h.resolve(ctx)
	if found.path == "" {
		return Status{
			State:    StateNotFound,
			Searched: found.searched,
			Detail:   notFoundDetail(found),
		}
	}
	stdout, stderr, _, runErr := runGH(ctx, found.path, "auth", "status")
	text := string(stdout) + "\n" + string(stderr)
	wipe(stdout)
	wipe(stderr)
	if runErr != nil {
		// gh was found but did not run or answer in time. The state stays
		// not_found so older clients that know only the existing states keep
		// working; the detail says what actually happened.
		return Status{
			State:    StateNotFound,
			Path:     found.path,
			Searched: found.searched,
			Detail:   notRespondingDetail(found.path),
		}
	}
	// A zero exit is not required. gh can print a signed-in account and
	// still exit non-zero for a host that is not github.com, or for another
	// github.com account that is not the active one. Only the active
	// account decides the state, because `gh auth token` returns its token.
	accounts := gitHubAccounts(text)
	if len(accounts) == 0 {
		if keyringFailure(text) {
			return Status{
				State:    StateTokenUnreadable,
				Path:     found.path,
				Searched: found.searched,
				Detail:   tokenUnreadableDetail(found.path, ""),
			}
		}
		return Status{
			State:    StateNotSignedIn,
			Path:     found.path,
			Searched: found.searched,
			Detail:   notSignedInDetail(found.path),
		}
	}
	active := activeAccount(accounts)
	// A keyring failure takes priority: the account is known, but the token
	// is not.
	if active.failed || keyringFailure(active.text) {
		return Status{
			State:    StateTokenUnreadable,
			Login:    active.login,
			Path:     found.path,
			Searched: found.searched,
			Detail:   tokenUnreadableDetail(found.path, active.login),
		}
	}
	return Status{
		State:    StateSignedIn,
		Login:    active.login,
		Path:     found.path,
		Searched: found.searched,
		Detail:   signedInDetail(found.path, active.login),
	}
}

// gitHubAccount is one github.com account in `gh auth status` output. text
// is the indented lines under it; it is matched against fixed phrases only
// and never returned.
type gitHubAccount struct {
	login  string
	failed bool
	active bool
	text   string
}

// gitHubAccounts splits `gh auth status` output into github.com accounts.
// An unindented line starts another host, so lines under a different host
// are not attributed to a github.com account.
func gitHubAccounts(text string) []gitHubAccount {
	var accounts []gitHubAccount
	current := -1
	for _, line := range strings.Split(text, "\n") {
		if login := firstMatch(loginPattern, line); login != "" {
			accounts = append(accounts, gitHubAccount{login: login})
			current = len(accounts) - 1
			continue
		}
		if login := firstMatch(failedLoginPattern, line); login != "" {
			accounts = append(accounts, gitHubAccount{login: login, failed: true})
			current = len(accounts) - 1
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			current = -1
			continue
		}
		if current < 0 {
			continue
		}
		accounts[current].text += line + "\n"
		if strings.Contains(strings.ToLower(line), "active account: true") {
			accounts[current].active = true
		}
	}
	return accounts
}

// activeAccount is the account gh marks active. A gh too old to mark one
// lists a single account per host, which is then the active one.
func activeAccount(accounts []gitHubAccount) gitHubAccount {
	for _, account := range accounts {
		if account.active {
			return account
		}
	}
	return accounts[0]
}

// Token returns the gh token. The caller must confirm first. On failure the
// returned error is ErrTokenUnreadable and contains no command output.
func (h HostCommands) Token(ctx context.Context) (string, error) {
	found := h.resolve(ctx)
	if found.path == "" {
		return "", errors.New("gh is not available")
	}
	stdout, stderr, exitErr, runErr := runGH(ctx, found.path, "auth", "token")
	wipe(stderr)
	token := strings.TrimSpace(string(stdout))
	wipe(stdout)
	if runErr != nil || exitErr != nil || !ValidToken(token) {
		wipeString(&token)
		return "", ErrTokenUnreadable
	}
	return token, nil
}

// ValidToken rejects empty, huge, or whitespace-bearing values before they
// are stored or sent.
func ValidToken(token string) bool {
	if len(token) < 8 || len(token) > 2048 {
		return false
	}
	for _, character := range token {
		if character <= ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func runGH(ctx context.Context, path string, args ...string) (stdout, stderr []byte, exitErr, runErr error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	prepareCommand(command)
	command.Env = childEnv(path)
	var out, errBuf bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errBuf
	err := command.Run()
	if err != nil && ctx.Err() != nil {
		// gh was stopped for taking too long. Its exit status says nothing
		// about the login, so it is not treated as an answer.
		wipe(out.Bytes())
		wipe(errBuf.Bytes())
		return nil, nil, nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out.Bytes(), errBuf.Bytes(), err, nil
		}
		wipe(out.Bytes())
		wipe(errBuf.Bytes())
		return nil, nil, nil, err
	}
	return out.Bytes(), errBuf.Bytes(), nil, nil
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func keyringFailure(text string) bool {
	// "keyring" alone is the storage label on a successful status line
	// ("account octocat (keyring)"), so the phrases are the failure text.
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"token in keyring",
		"from keyring",
		"from the keyring",
		"keychain",
		"secure storage",
		"could not read token",
		"failed to get token",
		"token in default is invalid",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func signedInDetail(path, login string) string {
	return "The GitHub CLI at " + path + " is signed in as @" + login + "."
}

func notSignedInDetail(path string) string {
	return "The GitHub CLI at " + path + " is not signed in. Run `gh auth login` on this computer, then connect again."
}

func tokenUnreadableDetail(path, login string) string {
	who := "is signed in"
	if login != "" {
		who = "is signed in as @" + login
	}
	return "The GitHub CLI at " + path + " " + who + ", but its token could not be read. Run `meridian github connect` in a terminal on this computer."
}

func notRespondingDetail(path string) string {
	return "The GitHub CLI at " + path + " did not answer. Run `gh auth status` in a terminal on this computer to check it."
}

func notFoundDetail(found resolved) string {
	if found.explicit {
		return "The GitHub CLI path " + found.explicitPath + " is not an executable file."
	}
	if len(found.searched) == 0 {
		return "The GitHub CLI was not found."
	}
	return "The GitHub CLI was not found. Searched " + strings.Join(found.searched, ", ") + "."
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func wipeString(value *string) {
	if value == nil || *value == "" {
		return
	}
	buf := []byte(*value)
	wipe(buf)
	*value = ""
}
