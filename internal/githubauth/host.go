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

// loginPattern matches the account line from `gh auth status`. The token
// line in that output is masked; this pattern does not capture it.
var loginPattern = regexp.MustCompile(`Logged in to github\.com account ([A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?)`)

// HostCommands reads the local gh login. Login uses `gh auth status`, which
// does not print the token. Token uses `gh auth token` and must be called
// only after the person confirms.
type HostCommands struct{}

func (HostCommands) Login(ctx context.Context) (string, error) {
	output, err := runGH(ctx, "auth", "status")
	if err != nil {
		return "", err
	}
	match := loginPattern.FindSubmatch(output)
	if len(match) != 2 {
		return "", errors.New("gh is not signed in")
	}
	return string(match[1]), nil
}

func (HostCommands) Token(ctx context.Context) (string, error) {
	output, err := runGH(ctx, "auth", "token")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(output))
	wipe(output)
	if !ValidToken(token) {
		return "", errors.New("gh has no token")
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

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	wipe(stderr.Bytes())
	if err != nil {
		wipe(stdout.Bytes())
		return nil, errors.New("gh is not available")
	}
	return stdout.Bytes(), nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
