package capsuleproto

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	checkPath           = "/v1/checks/run"
	checkSuggestionPath = "/v1/checks/suggestion"
	checkTailBytes      = 64 << 10
	maxSuggestionBytes  = 64 << 10
	maxChecks           = 16
)

// CheckCommand is one argv. The supervisor does not start a shell.
type CheckCommand struct {
	Name           string   `json:"name" yaml:"name"`
	Argv           []string `json:"argv" yaml:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds" yaml:"timeoutSeconds"`
}

// CheckRunRequest is one suite. OutputLimit is the retained tail per command.
type CheckRunRequest struct {
	Checks      []CheckCommand `json:"checks"`
	OutputLimit int64          `json:"outputLimit,omitempty"`
}

// CheckCommandResult is one command. Output is already stripped and redacted.
type CheckCommandResult struct {
	Name      string `json:"name"`
	ExitCode  int    `json:"exitCode"`
	Duration  int64  `json:"durationMs"`
	TimedOut  bool   `json:"timedOut"`
	Truncated bool   `json:"outputTruncated"`
	Output    string `json:"output,omitempty"`
}

// CheckRunResult binds the suite to the worktree before and after it ran.
type CheckRunResult struct {
	HeadBefore string               `json:"gitHeadBefore"`
	TreeBefore string               `json:"gitTreeBefore"`
	HeadAfter  string               `json:"gitHeadAfter"`
	TreeAfter  string               `json:"gitTreeAfter"`
	Stale      bool                 `json:"stale"`
	Results    []CheckCommandResult `json:"results"`
}

// CheckSuggestion is the repository file, when it parsed. Ignored means a
// file was present and was not usable. Nothing here is executed.
type CheckSuggestion struct {
	Checks  []CheckCommand `json:"checks"`
	Ignored bool           `json:"ignored,omitempty"`
}

func (c *Client) RunChecks(ctx context.Context, input CheckRunRequest) (CheckRunResult, error) {
	var result CheckRunResult
	err := c.callLimit(ctx, http.MethodPost, checkPath, input, &result, true, 2<<20)
	return result, err
}

func (c *Client) SuggestChecks(ctx context.Context) (CheckSuggestion, error) {
	var result CheckSuggestion
	err := c.call(ctx, http.MethodGet, checkSuggestionPath, nil, &result, true)
	return result, err
}

func (s *Server) runChecks(writer http.ResponseWriter, request *http.Request) {
	var input CheckRunRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) {
		return
	}
	if len(input.Checks) == 0 || len(input.Checks) > maxChecks {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	limit := input.OutputLimit
	if limit <= 0 || limit > checkTailBytes {
		limit = checkTailBytes
	}
	for _, check := range input.Checks {
		if check.Name == "" || len(check.Name) > 64 || len(check.Argv) == 0 || len(check.Argv) > 128 {
			writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		if check.TimeoutSeconds <= 0 || check.TimeoutSeconds > 60*60 {
			writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
			return
		}
		for _, argument := range check.Argv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, 0) {
				writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
				return
			}
		}
	}
	if !s.checks.TryLock() {
		writeProtocolError(writer, http.StatusConflict, "checks_active")
		return
	}
	defer s.checks.Unlock()

	headBefore, treeBefore, beforeOK := workspaceIdentity(request.Context(), s.config.Workspace)
	results, code := runCheckedCommands(request.Context(), s.config.Workspace, input.Checks, limit)
	if code != "" {
		status := http.StatusUnprocessableEntity
		if code == "cancelled" {
			status = http.StatusRequestTimeout
		}
		writeProtocolError(writer, status, code)
		return
	}
	headAfter, treeAfter, afterOK := workspaceIdentity(request.Context(), s.config.Workspace)
	stale := beforeOK != afterOK || (beforeOK && (headBefore != headAfter || treeBefore != treeAfter))
	writeJSON(writer, http.StatusOK, CheckRunResult{
		HeadBefore: headBefore, TreeBefore: treeBefore,
		HeadAfter: headAfter, TreeAfter: treeAfter,
		Stale: stale, Results: results,
	})
}

func runCheckedCommands(ctx context.Context, workspace string, checks []CheckCommand, limit int64) ([]CheckCommandResult, string) {
	results := make([]CheckCommandResult, 0, len(checks))
	for _, check := range checks {
		if ctx.Err() != nil {
			return nil, "cancelled"
		}
		timeout := time.Duration(check.TimeoutSeconds) * time.Second
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		started := time.Now()
		output, exitCode, timedOut, truncated, err := runCheckCommand(commandCtx, check.Argv[0], check.Argv[1:], workspace, limit)
		cancel()
		if err != nil && !timedOut {
			return nil, "check_failed_to_start"
		}
		if timedOut {
			exitCode = -1
		}
		results = append(results, CheckCommandResult{
			Name: check.Name, ExitCode: exitCode,
			Duration: time.Since(started).Milliseconds(),
			TimedOut: timedOut, Truncated: truncated,
			Output: redactCheckOutput(output),
		})
	}
	return results, ""
}

func (s *Server) suggestChecks(writer http.ResponseWriter, request *http.Request) {
	root, err := os.OpenRoot(s.config.Workspace)
	if err != nil {
		writeProtocolError(writer, http.StatusInternalServerError, "workspace_unavailable")
		return
	}
	defer root.Close()
	file, err := root.Open(".meridian/checks.yaml")
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(writer, http.StatusOK, CheckSuggestion{})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, CheckSuggestion{Ignored: true})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxSuggestionBytes || !info.Mode().IsRegular() {
		writeJSON(writer, http.StatusOK, CheckSuggestion{Ignored: true})
		return
	}
	body, err := io.ReadAll(io.LimitReader(file, maxSuggestionBytes+1))
	if err != nil || len(body) > maxSuggestionBytes {
		writeJSON(writer, http.StatusOK, CheckSuggestion{Ignored: true})
		return
	}
	suggestion, ok := parseCheckSuggestion(body)
	if !ok {
		writeJSON(writer, http.StatusOK, CheckSuggestion{Ignored: true})
		return
	}
	writeJSON(writer, http.StatusOK, suggestion)
}

type suggestionFile struct {
	Checks []CheckCommand `yaml:"checks"`
}

func parseCheckSuggestion(body []byte) (CheckSuggestion, bool) {
	var file suggestionFile
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil || len(file.Checks) == 0 || len(file.Checks) > maxChecks {
		return CheckSuggestion{}, false
	}
	for _, check := range file.Checks {
		if check.Name == "" || len(check.Argv) == 0 || check.TimeoutSeconds < 0 || check.TimeoutSeconds > 60*60 {
			return CheckSuggestion{}, false
		}
		for _, argument := range check.Argv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, 0) {
				return CheckSuggestion{}, false
			}
		}
	}
	return CheckSuggestion{Checks: file.Checks}, true
}

func workspaceIdentity(ctx context.Context, workspace string) (head, tree string, ok bool) {
	head, err := deliveryGitString(ctx, workspace, nil, "rev-parse", "--verify", "HEAD")
	if err != nil || head == "" {
		return "", "", false
	}
	tree, err = deliveryWorktreeTree(ctx, workspace)
	if err != nil || tree == "" {
		return "", "", false
	}
	return head, tree, true
}

func runCheckCommand(ctx context.Context, name string, arguments []string, directory string, outputLimit int64) ([]byte, int, bool, bool, error) {
	command := exec.Command(name, arguments...)
	command.Env = checkEnvironment()
	command.Dir = directory
	command.WaitDelay = 2 * time.Second
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	tail := &tailBuffer{limit: int(outputLimit)}
	command.Stdout = tail
	command.Stderr = tail
	if err := command.Start(); err != nil {
		return nil, -1, false, tail.truncated, err
	}
	defer syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return tail.bytes(), 0, false, tail.truncated, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return tail.bytes(), exitErr.ExitCode(), false, tail.truncated, nil
		}
		return tail.bytes(), -1, false, tail.truncated, err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return tail.bytes(), -1, true, tail.truncated, ctx.Err()
	}
}

func checkEnvironment() []string {
	result := make([]string, 0, 32)
	for _, item := range childEnvironment(nil) {
		key, _, _ := strings.Cut(item, "=")
		switch {
		case key == "DOCKER_HOST", key == "DOCKER_CONTEXT", key == "DOCKER_CERT_PATH":
			continue
		case strings.HasPrefix(key, "MERIDIAN_"):
			continue
		default:
			result = append(result, item)
		}
	}
	return result
}

type tailBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

func (t *tailBuffer) Write(value []byte) (int, error) {
	t.buf = append(t.buf, value...)
	if t.limit > 0 && len(t.buf) > t.limit {
		t.truncated = true
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.limit:]...)
	}
	return len(value), nil
}

func (t *tailBuffer) bytes() []byte {
	return append([]byte(nil), t.buf...)
}

func redactCheckOutput(raw []byte) string {
	if !utf8.Valid(raw) {
		raw = bytes.ToValidUTF8(raw, nil)
	}
	return redactSecrets(stripControls(string(raw)))
}

func stripControls(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); {
		if value[index] == 0x1b {
			index = skipEscape(value, index)
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		index += size
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func skipEscape(value string, index int) int {
	index++
	if index >= len(value) {
		return index
	}
	if value[index] == '[' {
		index++
		for index < len(value) {
			b := value[index]
			index++
			if b >= 0x40 && b <= 0x7e {
				break
			}
		}
		return index
	}
	return index + 1
}

var (
	secretTokenPattern  = regexp.MustCompile(`(?i)\b(?:ghp_|github_pat_|sk-|xox[baprs]-|AKIA)[A-Za-z0-9_\-]{6,}`)
	secretAssignPattern = regexp.MustCompile(`(?i)((?:authorization\s*:\s*bearer|token|password|secret|api_key)\s*[=:]\s*)\S+`)
)

func redactSecrets(value string) string {
	value = secretTokenPattern.ReplaceAllString(value, "[redacted]")
	return secretAssignPattern.ReplaceAllString(value, "${1}[redacted]")
}
