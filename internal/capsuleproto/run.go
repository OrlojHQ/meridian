package capsuleproto

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/harness"
	"github.com/coder/websocket"
	"github.com/creack/pty"
)

const (
	maxPromptBytes = 256 << 10
	maxFrameBytes  = 64 << 10
	maxJSONLLine   = 64 << 10
	maxAttach      = 4
	maxRunHistory  = 128
)

type RunState string

const (
	RunStarting  RunState = "Starting"
	RunRunning   RunState = "Running"
	RunSucceeded RunState = "Succeeded"
	RunFailed    RunState = "Failed"
	RunCancelled RunState = "Cancelled"
)

type RunStartRequest struct {
	RunID   string `json:"runId"`
	Harness string `json:"harness"`
	Prompt  string `json:"prompt"`
	Columns uint16 `json:"columns,omitempty"`
	Rows    uint16 `json:"rows,omitempty"`
}

type RunStatusResponse struct {
	RunID     string    `json:"runId"`
	State     RunState  `json:"state"`
	ExitCode  *int      `json:"exitCode,omitempty"`
	Failure   string    `json:"failure,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	PTY       bool      `json:"pty"`
	Cursor    uint64    `json:"cursor"`
}

type RunEvent struct {
	Sequence uint64         `json:"sequence"`
	Type     string         `json:"type"`
	Data     string         `json:"data,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type RunEventsResponse struct {
	Events     []RunEvent `json:"events"`
	NextCursor uint64     `json:"nextCursor"`
	Gap        bool       `json:"gap,omitempty"`
}

type GitResponse struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type supervisedRun struct {
	mu          sync.Mutex
	id          string
	state       RunState
	exitCode    *int
	failure     string
	startedAt   time.Time
	endedAt     time.Time
	pty         bool
	profile     harness.Profile
	command     *exec.Cmd
	terminal    *os.File
	cancel      context.CancelFunc
	events      []RunEvent
	next        uint64
	eventLimit  int
	outputLimit int64
	outputBytes int64
	attachments int
}

func (s *Server) startRun(writer http.ResponseWriter, request *http.Request) {
	var input RunStartRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) {
		return
	}
	if input.RunID == "" || len(input.RunID) > 200 || strings.ContainsAny(input.RunID, "/\\\x00\r\n") ||
		input.Harness == "" || len(input.Harness) > 128 || len(input.Prompt) > maxPromptBytes {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	if existing := s.runs[input.RunID]; existing != nil {
		s.mu.Unlock()
		writeJSON(writer, http.StatusOK, existing.status())
		return
	}
	active := 0
	for _, existing := range s.runs {
		existing.mu.Lock()
		terminal := existing.state == RunSucceeded || existing.state == RunFailed ||
			existing.state == RunCancelled
		existing.mu.Unlock()
		if !terminal {
			active++
		}
	}
	if active >= s.config.RunLimit {
		s.mu.Unlock()
		writeProtocolError(writer, http.StatusTooManyRequests, "run_limit")
		return
	}
	for len(s.runs) >= maxRunHistory {
		var oldest *supervisedRun
		for _, existing := range s.runs {
			existing.mu.Lock()
			terminal := existing.state == RunSucceeded || existing.state == RunFailed ||
				existing.state == RunCancelled
			startedAt := existing.startedAt
			existing.mu.Unlock()
			if terminal && (oldest == nil || startedAt.Before(oldest.startedAt)) {
				oldest = existing
			}
		}
		if oldest == nil {
			break
		}
		delete(s.runs, oldest.id)
	}
	s.mu.Unlock()

	configFile, err := os.Open(filepath.Join(s.config.Workspace, ".meridian", "project.yaml"))
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "harness_configuration_unavailable")
		return
	}
	config, parseErr := harness.Parse(configFile)
	_ = configFile.Close()
	if parseErr != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "harness_configuration_invalid")
		return
	}
	profile, err := config.Profile(input.Harness)
	if err != nil {
		writeProtocolError(writer, http.StatusNotFound, "harness_not_found")
		return
	}
	if profile.Interaction != harness.InteractionNative {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "native_interaction_unavailable")
		return
	}
	if len(profile.SecretRefs) != 0 {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "secrets_unresolved")
		return
	}
	directory, err := workspacePath(s.config.Workspace, profile.Workdir)
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "unsafe_working_directory")
		return
	}
	if err := validateWorkspaceDirectory(s.config.Workspace, directory); err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "working_directory_unavailable")
		return
	}
	run := &supervisedRun{
		id: input.RunID, state: RunStarting, startedAt: time.Now().UTC(),
		pty: profile.PTY, profile: profile, eventLimit: s.config.EventLimit,
		outputLimit: s.config.OutputLimit,
	}
	s.mu.Lock()
	if existing := s.runs[input.RunID]; existing != nil {
		s.mu.Unlock()
		writeJSON(writer, http.StatusOK, existing.status())
		return
	}
	active = 0
	for _, existing := range s.runs {
		existing.mu.Lock()
		terminal := existing.state == RunSucceeded || existing.state == RunFailed ||
			existing.state == RunCancelled
		existing.mu.Unlock()
		if !terminal {
			active++
		}
	}
	if active >= s.config.RunLimit {
		s.mu.Unlock()
		writeProtocolError(writer, http.StatusTooManyRequests, "run_limit")
		return
	}
	for len(s.runs) >= maxRunHistory {
		var oldest *supervisedRun
		for _, existing := range s.runs {
			existing.mu.Lock()
			terminal := existing.state == RunSucceeded || existing.state == RunFailed ||
				existing.state == RunCancelled
			startedAt := existing.startedAt
			existing.mu.Unlock()
			if terminal && (oldest == nil || startedAt.Before(oldest.startedAt)) {
				oldest = existing
			}
		}
		if oldest == nil {
			break
		}
		delete(s.runs, oldest.id)
	}
	s.runs[input.RunID] = run
	s.mu.Unlock()
	run.addEvent("run.starting", nil, nil)
	go run.execute(directory, input.Prompt, input.Columns, input.Rows)
	writeJSON(writer, http.StatusAccepted, run.status())
}

func (s *Server) runStatus(writer http.ResponseWriter, request *http.Request) {
	run := s.lookupRun(request.PathValue("runID"))
	if run == nil {
		writeProtocolError(writer, http.StatusNotFound, "run_not_found")
		return
	}
	writeJSON(writer, http.StatusOK, run.status())
}

func (s *Server) cancelRun(writer http.ResponseWriter, request *http.Request) {
	run := s.lookupRun(request.PathValue("runID"))
	if run == nil {
		writeProtocolError(writer, http.StatusNotFound, "run_not_found")
		return
	}
	run.mu.Lock()
	cancel := run.cancel
	state := run.state
	run.mu.Unlock()
	if cancel != nil && (state == RunStarting || state == RunRunning) {
		cancel()
	}
	writeJSON(writer, http.StatusAccepted, run.status())
}

func (s *Server) runEvents(writer http.ResponseWriter, request *http.Request) {
	run := s.lookupRun(request.PathValue("runID"))
	if run == nil {
		writeProtocolError(writer, http.StatusNotFound, "run_not_found")
		return
	}
	after, err := strconv.ParseUint(defaultString(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_cursor")
		return
	}
	writeJSON(writer, http.StatusOK, run.eventsAfter(after))
}

func (s *Server) lookupRun(id string) *supervisedRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (r *supervisedRun) execute(directory, prompt string, columns, rows uint16) {
	ctx, cancel := context.WithTimeout(context.Background(), r.profile.Timeout)
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
	defer cancel()

	arguments := append([]string(nil), r.profile.Arguments...)
	if r.profile.Prompt == harness.PromptArgument {
		arguments = append(arguments, prompt)
	}
	command := exec.Command(r.profile.Executable, arguments...)
	command.Dir = directory
	r.mu.Lock()
	r.command = command
	r.mu.Unlock()

	var wait func() error
	if r.profile.PTY {
		if columns == 0 {
			columns = 80
		}
		if rows == 0 {
			rows = 24
		}
		terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: columns, Rows: rows})
		if err != nil {
			r.finish(RunFailed, nil, "run process failed to start")
			return
		}
		r.mu.Lock()
		r.terminal = terminal
		r.state = RunRunning
		r.mu.Unlock()
		r.addEvent("run.running", nil, nil)
		if r.profile.Prompt == harness.PromptStdin || r.profile.Prompt == harness.PromptInteractive {
			_, _ = io.WriteString(terminal, prompt)
			if r.profile.Prompt == harness.PromptStdin {
				_, _ = io.WriteString(terminal, "\n")
			}
		}
		go r.captureText(terminal)
		wait = command.Wait
	} else {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := command.StdoutPipe()
		if err != nil {
			r.finish(RunFailed, nil, "run output setup failed")
			return
		}
		stderr, err := command.StderrPipe()
		if err != nil {
			r.finish(RunFailed, nil, "run output setup failed")
			return
		}
		var stdin io.WriteCloser
		if r.profile.Prompt == harness.PromptStdin {
			stdin, err = command.StdinPipe()
			if err != nil {
				r.finish(RunFailed, nil, "run input setup failed")
				return
			}
		}
		if err := command.Start(); err != nil {
			r.finish(RunFailed, nil, "run process failed to start")
			return
		}
		r.mu.Lock()
		r.state = RunRunning
		r.mu.Unlock()
		r.addEvent("run.running", nil, nil)
		if stdin != nil {
			_, _ = io.WriteString(stdin, prompt)
			_ = stdin.Close()
		}
		if r.profile.Output == harness.OutputJSONL {
			go r.captureJSONL(stdout, "stdout")
			go r.captureJSONL(stderr, "stderr")
		} else {
			go r.captureText(stdout)
			go r.captureText(stderr)
		}
		wait = command.Wait
	}

	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case err := <-done:
		r.completeFromWait(err, false)
	case <-ctx.Done():
		r.mu.Lock()
		pid := 0
		if r.command != nil && r.command.Process != nil {
			pid = r.command.Process.Pid
		}
		r.mu.Unlock()
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			select {
			case err := <-done:
				r.completeFromWait(err, errors.Is(ctx.Err(), context.Canceled))
				return
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
		err := <-done
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			r.finish(RunFailed, exitCode(err), "run timed out")
		} else {
			r.finish(RunCancelled, exitCode(err), "")
		}
	}
}

func (r *supervisedRun) completeFromWait(err error, cancelled bool) {
	if cancelled {
		r.finish(RunCancelled, exitCode(err), "")
		return
	}
	if err == nil {
		code := 0
		r.finish(RunSucceeded, &code, "")
		return
	}
	r.finish(RunFailed, exitCode(err), "run process exited unsuccessfully")
}

func exitCode(err error) *int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		return &code
	}
	return nil
}

func (r *supervisedRun) finish(state RunState, code *int, failure string) {
	r.mu.Lock()
	r.state, r.exitCode, r.failure = state, code, failure
	r.endedAt = time.Now().UTC()
	if r.terminal != nil {
		_ = r.terminal.Close()
	}
	r.mu.Unlock()
	r.addEvent("run."+strings.ToLower(string(state)), nil, nil)
}

func (r *supervisedRun) captureText(reader io.Reader) {
	buffer := make([]byte, 16<<10)
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			r.addEvent("output", append([]byte(nil), buffer[:count]...), nil)
		}
		if err != nil {
			return
		}
	}
}

func (r *supervisedRun) captureJSONL(reader io.Reader, stream string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxJSONLLine)
	for scanner.Scan() {
		var metadata map[string]any
		line := scanner.Bytes()
		if json.Unmarshal(line, &metadata) != nil || !safeMetadata(metadata) {
			r.addEvent("parse_error", nil, map[string]any{"stream": stream, "reason": "invalid_jsonl"})
			continue
		}
		metadata["stream"] = stream
		r.addEvent("record", nil, metadata)
	}
	if scanner.Err() != nil {
		r.addEvent("parse_error", nil, map[string]any{"stream": stream, "reason": "line_too_large"})
	}
}

func safeMetadata(value map[string]any) bool {
	if len(value) > 32 {
		return false
	}
	encoded, err := json.Marshal(value)
	return err == nil && len(encoded) <= maxJSONLLine
}

func (r *supervisedRun) addEvent(kind string, data []byte, metadata map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	event := RunEvent{Sequence: r.next, Type: kind, Metadata: metadata}
	if len(data) > maxFrameBytes {
		data = data[:maxFrameBytes]
	}
	if len(data) > 0 {
		event.Data = base64.StdEncoding.EncodeToString(data)
	}
	r.events = append(r.events, event)
	r.outputBytes += int64(len(event.Data))
	for len(r.events) > r.eventLimit || r.outputBytes > r.outputLimit {
		r.outputBytes -= int64(len(r.events[0].Data))
		r.events = r.events[1:]
	}
}

func (r *supervisedRun) status() RunStatusResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RunStatusResponse{
		RunID: r.id, State: r.state, ExitCode: r.exitCode, Failure: r.failure,
		StartedAt: r.startedAt, EndedAt: r.endedAt, PTY: r.pty, Cursor: r.next,
	}
}

func (r *supervisedRun) eventsAfter(after uint64) RunEventsResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	response := RunEventsResponse{NextCursor: r.next}
	if len(r.events) == 0 {
		return response
	}
	if after+1 < r.events[0].Sequence {
		response.Gap = true
		after = r.events[0].Sequence - 1
	}
	for _, event := range r.events {
		if event.Sequence > after {
			response.Events = append(response.Events, event)
		}
	}
	return response
}

func (s *Server) attachRun(writer http.ResponseWriter, request *http.Request) {
	run := s.lookupRun(request.PathValue("runID"))
	if run == nil || !run.pty {
		writeProtocolError(writer, http.StatusNotFound, "attach_unavailable")
		return
	}
	after, err := strconv.ParseUint(defaultString(request.URL.Query().Get("after"), "0"), 10, 64)
	if err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_cursor")
		return
	}
	run.mu.Lock()
	if run.attachments >= maxAttach {
		run.mu.Unlock()
		writeProtocolError(writer, http.StatusTooManyRequests, "attachment_limit")
		return
	}
	run.attachments++
	run.mu.Unlock()
	defer func() {
		run.mu.Lock()
		run.attachments--
		run.mu.Unlock()
	}()
	connection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(maxFrameBytes)
	initial := run.eventsAfter(after)
	if initial.Gap {
		gap := RunEvent{Type: "gap", Metadata: map[string]any{"requestedAfter": after}}
		if len(initial.Events) > 0 {
			gap.Sequence = initial.Events[0].Sequence - 1
		}
		if err := connection.Write(request.Context(), websocket.MessageText, mustJSON(gap)); err != nil {
			return
		}
	}
	for _, event := range initial.Events {
		if err := connection.Write(request.Context(), websocket.MessageText, mustJSON(event)); err != nil {
			return
		}
		after = event.Sequence
	}
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	errorsChannel := make(chan error, 2)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		cursor := after
		for {
			select {
			case <-ctx.Done():
				errorsChannel <- ctx.Err()
				return
			case <-ticker.C:
				replay := run.eventsAfter(cursor)
				if replay.Gap {
					gap := RunEvent{Type: "gap", Metadata: map[string]any{"requestedAfter": cursor}}
					if len(replay.Events) > 0 {
						gap.Sequence = replay.Events[0].Sequence - 1
					}
					if err := connection.Write(ctx, websocket.MessageText, mustJSON(gap)); err != nil {
						errorsChannel <- err
						return
					}
				}
				for _, event := range replay.Events {
					if err := connection.Write(ctx, websocket.MessageText, mustJSON(event)); err != nil {
						errorsChannel <- err
						return
					}
					cursor = event.Sequence
				}
			}
		}
	}()
	go func() {
		for {
			messageType, value, err := connection.Read(ctx)
			if err != nil {
				errorsChannel <- err
				return
			}
			if messageType == websocket.MessageBinary {
				run.mu.Lock()
				terminal := run.terminal
				run.mu.Unlock()
				if terminal != nil {
					_, _ = terminal.Write(value)
				}
				continue
			}
			var control struct {
				Type    string `json:"type"`
				Columns uint16 `json:"columns"`
				Rows    uint16 `json:"rows"`
			}
			if json.Unmarshal(value, &control) != nil || control.Type != "resize" ||
				control.Columns == 0 || control.Rows == 0 {
				continue
			}
			run.mu.Lock()
			terminal := run.terminal
			run.mu.Unlock()
			if terminal != nil {
				_ = pty.Setsize(terminal, &pty.Winsize{Cols: control.Columns, Rows: control.Rows})
			}
		}
	}()
	<-errorsChannel
}

func (s *Server) gitStatus(writer http.ResponseWriter, request *http.Request) {
	s.git(writer, request, []string{"-C", s.config.Workspace, "status", "--porcelain=v1", "--untracked-files=all"})
}

func (s *Server) gitDiff(writer http.ResponseWriter, request *http.Request) {
	s.git(writer, request, []string{"-C", s.config.Workspace, "diff", "--no-ext-diff", "--no-color", "--"})
}

func (s *Server) git(writer http.ResponseWriter, request *http.Request, arguments []string) {
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", arguments...)
	var output boundedBuffer
	output.limit = s.config.DiffLimit
	command.Stdout, command.Stderr = &output, io.Discard
	err := command.Run()
	if ctx.Err() != nil {
		writeProtocolError(writer, http.StatusGatewayTimeout, "git_timeout")
		return
	}
	if err != nil && !errors.Is(err, errOutputTruncated) {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "git_failed")
		return
	}
	writeJSON(writer, http.StatusOK, GitResponse{Content: output.String(), Truncated: output.truncated})
}

var errOutputTruncated = errors.New("output truncated")

type boundedBuffer struct {
	value     []byte
	limit     int64
	truncated bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - int64(len(b.value))
	if remaining <= 0 {
		b.truncated = true
		return len(value), errOutputTruncated
	}
	if int64(len(value)) > remaining {
		b.value = append(b.value, value[:remaining]...)
		b.truncated = true
		return len(value), errOutputTruncated
	}
	b.value = append(b.value, value...)
	return len(value), nil
}

func (b *boundedBuffer) String() string { return string(b.value) }

func workspacePath(workspace, relative string) (string, error) {
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return "", errors.New("unsafe relative path")
	}
	result := filepath.Join(workspace, relative)
	rel, err := filepath.Rel(workspace, result)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return result, nil
}

func validateWorkspaceDirectory(workspace, directory string) error {
	relative, err := filepath.Rel(workspace, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("working directory escapes workspace")
	}
	current := workspace
	parts := strings.Split(relative, string(filepath.Separator))
	if relative == "." {
		parts = nil
	}
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("working directory contains a symlink")
		}
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("working directory is unavailable")
	}
	return nil
}

func decodeRequest(writer http.ResponseWriter, request *http.Request, limit int64, output any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (r RunStatusResponse) String() string {
	return fmt.Sprintf("%s:%s", r.RunID, r.State)
}
