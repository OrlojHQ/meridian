package harnessadapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

const (
	openCodeBodyLimit  = int64(adapterproto.MaxFrameBytes)
	openCodeEventLimit = adapterproto.MaxFrameBytes
)

type OpenCodeConfig struct {
	Command     []string
	SessionName string
	Permissions bool
}

type openCodeClient struct {
	base     *url.URL
	username string
	password string
	dir      string
	client   *http.Client
}

type openCodeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type openCodeState struct {
	stream       *Stream
	client       *openCodeClient
	sessionID    string
	permissions  bool
	toolStarted  map[string]bool
	toolFinished map[string]bool
	finalized    map[string]bool
}

// RunOpenCode starts a private authenticated OpenCode server and maps its
// documented session/message/SSE API to Meridian frames.
func RunOpenCode(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	config OpenCodeConfig,
) error {
	if err := validateOpenCodeConfig(config); err != nil {
		return err
	}
	stream := NewStream(input, output)
	if err := stream.Handshake(adapterproto.Capabilities{
		Resume: true, ToolEvents: true, Permissions: config.Permissions,
		InputRequests: false, Heartbeat: false, OpaqueMetadata: true,
	}); err != nil {
		return err
	}
	initial, err := stream.Read()
	if err != nil {
		return err
	}
	if initial.Type != adapterproto.KindStart && initial.Type != adapterproto.KindResume {
		return errors.New("OpenCode adapter requires start or resume")
	}

	port, err := reserveLoopbackPort()
	if err != nil {
		return errors.New("reserve OpenCode loopback port")
	}
	credential := make([]byte, 32)
	if _, err := rand.Read(credential); err != nil {
		return errors.New("generate OpenCode server credential")
	}
	password := base64.RawURLEncoding.EncodeToString(credential)
	command := append([]string(nil), config.Command...)
	command = append(command, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port), "--mdns=false")
	child, err := startProcess(processConfig{
		Command: command,
		Env: replaceEnv(currentEnv(), map[string]string{
			"OPENCODE_SERVER_USERNAME": "meridian",
			"OPENCODE_SERVER_PASSWORD": password,
		}),
	})
	if err != nil {
		return err
	}
	defer child.Close()

	directory, err := os.Getwd()
	if err != nil {
		return errors.New("resolve OpenCode working directory")
	}
	base, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	api := &openCodeClient{
		base: base, username: "meridian", password: password, dir: directory,
		client: &http.Client{
			Transport: &http.Transport{
				Proxy:       nil,
				DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("OpenCode redirects are disabled")
			},
			Timeout: 5 * time.Second,
		},
	}
	if err := waitOpenCode(ctx, api, child); err != nil {
		stream.Fail("opencode_startup_failed")
		return err
	}

	sessionID := ""
	if initial.Type == adapterproto.KindResume {
		sessionID, err = openCodeResumeID(initial.ResumeState)
		if err == nil {
			var session struct {
				ID string `json:"id"`
			}
			err = api.json(ctx, http.MethodGet, "/session/"+url.PathEscape(sessionID), nil, http.StatusOK, &session)
			if err == nil && session.ID != sessionID {
				err = errors.New("OpenCode resume session mismatch")
			}
		}
	} else {
		body := map[string]any{}
		if config.SessionName != "" {
			body["title"] = config.SessionName
		}
		var session struct {
			ID string `json:"id"`
		}
		err = api.json(ctx, http.MethodPost, "/session", body, http.StatusOK, &session)
		if err == nil {
			sessionID = session.ID
			if !validOpenCodeID(sessionID) {
				err = errors.New("OpenCode returned an invalid session id")
			}
		}
	}
	if err != nil {
		stream.Fail("opencode_session_failed")
		return err
	}

	state := &openCodeState{
		stream: stream, client: api, sessionID: sessionID,
		permissions: config.Permissions, toolStarted: map[string]bool{},
		toolFinished: map[string]bool{}, finalized: map[string]bool{},
	}
	if err := stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusIdle, ResumeState: openCodeResumeState(sessionID),
	}); err != nil {
		return err
	}

	events := make(chan openCodeEvent, 64)
	eventErrors := make(chan error, 1)
	go api.readEvents(ctx, events, eventErrors)

	parentFrames := make(chan adapterproto.Frame)
	parentErrors := make(chan error, 1)
	go func() {
		for {
			frame, readErr := stream.Read()
			if readErr != nil {
				parentErrors <- readErr
				return
			}
			select {
			case parentFrames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = api.json(context.Background(), http.MethodPost,
				"/session/"+url.PathEscape(sessionID)+"/abort", map[string]any{},
				http.StatusOK, nil)
			return ctx.Err()
		case err := <-parentErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case err := <-eventErrors:
			stream.Fail("opencode_event_failed")
			return err
		case frame := <-parentFrames:
			switch frame.Type {
			case adapterproto.KindUserMessage:
				body := map[string]any{
					"parts": []map[string]string{{"type": "text", "text": frame.Content}},
				}
				if err := api.json(ctx, http.MethodPost,
					"/session/"+url.PathEscape(sessionID)+"/prompt_async",
					body, http.StatusNoContent, nil); err != nil {
					stream.Fail("opencode_prompt_failed")
					return err
				}
			case adapterproto.KindPermissionResponse:
				if !config.Permissions || frame.Permission == nil {
					stream.Fail("opencode_permission_invalid")
					return errors.New("unexpected OpenCode permission response")
				}
				response, responseErr := openCodePermission(frame.Permission.Choice)
				if responseErr != nil {
					stream.Fail("opencode_permission_invalid")
					return responseErr
				}
				var accepted bool
				if err := api.json(ctx, http.MethodPost,
					"/session/"+url.PathEscape(sessionID)+"/permissions/"+
						url.PathEscape(frame.ResponseTo),
					map[string]any{"response": response}, http.StatusOK, &accepted); err != nil || !accepted {
					stream.Fail("opencode_permission_failed")
					if err != nil {
						return err
					}
					return errors.New("OpenCode rejected permission response")
				}
			case adapterproto.KindCancel:
				var accepted bool
				err := api.json(ctx, http.MethodPost,
					"/session/"+url.PathEscape(sessionID)+"/abort",
					map[string]any{}, http.StatusOK, &accepted)
				if err != nil {
					stream.Fail("opencode_cancel_failed")
					return err
				}
				_ = stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
					Status: adapterproto.StatusCancelled,
				})
				return nil
			case adapterproto.KindHeartbeat, adapterproto.KindAck:
			default:
				stream.Fail("opencode_controller_frame_invalid")
				return errors.New("unsupported OpenCode controller frame")
			}
		case event := <-events:
			if event.Type == "" {
				continue
			}
			if err := state.handleEvent(ctx, event); err != nil {
				stream.Fail("opencode_event_incompatible")
				return err
			}
		}
	}
}

func validateOpenCodeConfig(config OpenCodeConfig) error {
	if len(config.Command) == 0 {
		return errors.New("OpenCode executable is required")
	}
	for _, argument := range config.Command[1:] {
		switch {
		case argument == "serve", argument == "--hostname", argument == "--port",
			argument == "--mdns", argument == "--cors",
			strings.HasPrefix(argument, "--hostname="),
			strings.HasPrefix(argument, "--port="),
			strings.HasPrefix(argument, "--mdns="),
			strings.HasPrefix(argument, "--cors="):
			return fmt.Errorf("OpenCode network argument %q is owned by the adapter", argument)
		}
	}
	if strings.ContainsAny(config.SessionName, "\x00\r\n") {
		return errors.New("OpenCode session name is invalid")
	}
	return nil
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func waitOpenCode(ctx context.Context, api *openCodeClient, child *process) error {
	ctx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var health struct {
			Healthy bool   `json:"healthy"`
			Version string `json:"version"`
		}
		requestCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
		err := api.json(requestCtx, http.MethodGet, "/global/health", nil, http.StatusOK, &health)
		stop()
		if err == nil {
			if !health.Healthy || health.Version == "" {
				return errors.New("OpenCode health response is incompatible")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("OpenCode readiness timed out")
		case <-child.done:
			return errors.New("OpenCode server exited during startup")
		case <-ticker.C:
		}
	}
}

func (c *openCodeClient) request(
	ctx context.Context,
	method, path string,
	body any,
) (*http.Response, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("invalid OpenCode API path")
	}
	target := *c.base
	target.Path = path
	target.RawPath = ""
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > adapterproto.MaxFrameBytes {
			return nil, errors.New("invalid OpenCode request body")
		}
		input = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), input)
	if err != nil {
		return nil, errors.New("construct OpenCode request")
	}
	request.SetBasicAuth(c.username, c.password)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-OpenCode-Directory", c.dir)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.client.Do(request)
}

func (c *openCodeClient) json(
	ctx context.Context,
	method, path string,
	body any,
	expected int,
	output any,
) error {
	response, err := c.request(ctx, method, path, body)
	if err != nil {
		return errors.New("OpenCode request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, openCodeBodyLimit+1))
		return fmt.Errorf("OpenCode returned status %d", response.StatusCode)
	}
	if output == nil {
		count, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, openCodeBodyLimit+1))
		if count > openCodeBodyLimit {
			return errors.New("OpenCode response exceeds size limit")
		}
		err = copyErr
		return err
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return errors.New("OpenCode response content type is incompatible")
	}
	limited := io.LimitReader(response.Body, openCodeBodyLimit+1)
	value, err := io.ReadAll(limited)
	if err != nil || len(value) > int(openCodeBodyLimit) {
		return errors.New("OpenCode response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	if err := decoder.Decode(output); err != nil {
		return errors.New("OpenCode response is malformed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("OpenCode response has trailing data")
	}
	return nil
}

func (c *openCodeClient) readEvents(
	ctx context.Context,
	output chan<- openCodeEvent,
	errorsOut chan<- error,
) {
	lastID := ""
	seen := make(map[[32]byte]struct{})
	order := make([][32]byte, 0, 256)
	connectFailures := 0
	for ctx.Err() == nil {
		response, err := c.openEventStream(ctx, lastID)
		if err != nil {
			connectFailures++
			if connectFailures >= 10 {
				errorsOut <- errors.New("OpenCode SSE reconnect limit exceeded")
				return
			}
			if !sleepContext(ctx, 100*time.Millisecond) {
				return
			}
			continue
		}
		connectFailures = 0
		dedupPrefix := len(order) != 0
		nextID, readErr := readOpenCodeSSE(ctx, response.Body, func(id string, value []byte) error {
			if id != "" {
				lastID = id
			}
			digest := sha256.Sum256(value)
			if dedupPrefix {
				if _, duplicate := seen[digest]; duplicate {
					return nil
				}
				dedupPrefix = false
			}
			seen[digest] = struct{}{}
			order = append(order, digest)
			if len(order) > 256 {
				delete(seen, order[0])
				order = order[1:]
			}
			var event openCodeEvent
			if err := json.Unmarshal(value, &event); err != nil ||
				event.Type == "" || len(event.Properties) == 0 {
				return errors.New("OpenCode SSE event is malformed")
			}
			select {
			case output <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		_ = response.Body.Close()
		if nextID != "" {
			lastID = nextID
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) &&
			!errors.Is(readErr, context.Canceled) {
			errorsOut <- readErr
			return
		}
		if !sleepContext(ctx, 100*time.Millisecond) {
			return
		}
	}
}

func (c *openCodeClient) openEventStream(ctx context.Context, lastID string) (*http.Response, error) {
	target := *c.base
	target.Path = "/event"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.username, c.password)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("X-OpenCode-Directory", c.dir)
	if lastID != "" {
		request.Header.Set("Last-Event-ID", lastID)
	}
	eventClient := &http.Client{
		Transport: c.client.Transport, CheckRedirect: c.client.CheckRedirect,
	}
	response, err := eventClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK ||
		!strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		_ = response.Body.Close()
		return nil, errors.New("OpenCode SSE response is incompatible")
	}
	return response, nil
}

func readOpenCodeSSE(
	ctx context.Context,
	body io.Reader,
	handle func(string, []byte) error,
) (string, error) {
	reader := bufio.NewReaderSize(body, 4096)
	var data bytes.Buffer
	lastID := ""
	for {
		line, err := readBoundedSSELine(reader)
		if err != nil {
			return lastID, err
		}
		if len(line) == 0 {
			if data.Len() == 0 {
				continue
			}
			value := bytes.TrimSuffix(data.Bytes(), []byte{'\n'})
			if err := handle(lastID, value); err != nil {
				return lastID, err
			}
			data.Reset()
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(string(line), ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			if strings.ContainsRune(value, '\x00') {
				return lastID, errors.New("OpenCode SSE id is invalid")
			}
			lastID = value
		case "data":
			if data.Len()+len(value)+1 > openCodeEventLimit {
				return lastID, errors.New("OpenCode SSE event exceeds size limit")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
		select {
		case <-ctx.Done():
			return lastID, ctx.Err()
		default:
		}
	}
}

func readBoundedSSELine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > openCodeEventLimit {
			return nil, errors.New("OpenCode SSE line exceeds size limit")
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return line, nil
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *openCodeState) handleEvent(ctx context.Context, event openCodeEvent) error {
	switch event.Type {
	case "server.connected", "server.heartbeat":
		return nil
	case "message.part.delta":
		var value struct {
			SessionID string `json:"sessionID"`
			MessageID string `json:"messageID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if json.Unmarshal(event.Properties, &value) != nil {
			return errors.New("OpenCode message delta is malformed")
		}
		if value.SessionID != s.sessionID || value.Field != "text" || value.Delta == "" {
			return nil
		}
		return s.stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindAssistantDelta,
			Role:      adapterproto.RoleAssistant,
			MessageID: safeID(value.MessageID, "opencode-message"),
			Content:   bounded(value.Delta, adapterproto.MaxContentBytes),
		})
	case "message.part.updated":
		var value struct {
			Part struct {
				ID        string `json:"id"`
				CallID    string `json:"callID"`
				SessionID string `json:"sessionID"`
				MessageID string `json:"messageID"`
				Type      string `json:"type"`
				Tool      string `json:"tool"`
				State     struct {
					Status string          `json:"status"`
					Input  json.RawMessage `json:"input"`
					Output string          `json:"output"`
					Error  string          `json:"error"`
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal(event.Properties, &value) != nil {
			return errors.New("OpenCode part event is malformed")
		}
		part := value.Part
		if part.SessionID != s.sessionID || part.Type != "tool" {
			return nil
		}
		if part.CallID == "" {
			return errors.New("OpenCode tool call id is missing")
		}
		id := safeID(part.CallID, "opencode-tool")
		if (part.State.Status == "pending" || part.State.Status == "running") && !s.toolStarted[id] {
			s.toolStarted[id] = true
			return s.stream.Write(adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindToolStart,
				Role: adapterproto.RoleAssistant, ToolCallID: id,
				ToolName: bounded(part.Tool, adapterproto.MaxToolNameBytes),
				Summary:  bounded(string(part.State.Input), adapterproto.MaxToolSummaryBytes),
			})
		}
		if (part.State.Status == "completed" || part.State.Status == "error") && !s.toolFinished[id] {
			s.toolFinished[id] = true
			result := part.State.Output
			if result == "" {
				result = part.State.Error
			}
			if result == "" {
				result = "{}"
			}
			return s.stream.Write(adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindToolResult,
				Role: adapterproto.RoleTool, ToolCallID: id,
				Result: bounded(result, adapterproto.MaxResultBytes),
			})
		}
		return nil
	case "message.updated":
		var value struct {
			Info struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionID"`
				Role      string `json:"role"`
				Finish    string `json:"finish"`
			} `json:"info"`
		}
		if json.Unmarshal(event.Properties, &value) != nil {
			return errors.New("OpenCode message event is malformed")
		}
		if value.Info.SessionID != s.sessionID || value.Info.Role != "assistant" ||
			value.Info.Finish == "" || s.finalized[value.Info.ID] {
			return nil
		}
		return s.emitMessage(ctx, value.Info.ID)
	case "session.status":
		var value struct {
			SessionID string `json:"sessionID"`
			Status    struct {
				Type string `json:"type"`
			} `json:"status"`
		}
		if json.Unmarshal(event.Properties, &value) != nil {
			return errors.New("OpenCode status event is malformed")
		}
		if value.SessionID != s.sessionID {
			return nil
		}
		status := adapterproto.StatusRunning
		switch value.Status.Type {
		case "busy":
			status = adapterproto.StatusRunning
		case "retry":
			status = adapterproto.StatusWaiting
		case "idle":
			status = adapterproto.StatusIdle
		default:
			return errors.New("OpenCode status is incompatible")
		}
		return s.stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
			Status: status, ResumeState: openCodeResumeState(s.sessionID),
		})
	case "session.idle":
		var value struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(event.Properties, &value) != nil {
			return errors.New("OpenCode idle event is malformed")
		}
		if value.SessionID != s.sessionID {
			return nil
		}
		return s.stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
			Status: adapterproto.StatusIdle, ResumeState: openCodeResumeState(s.sessionID),
		})
	case "permission.asked":
		if !s.permissions {
			return errors.New("OpenCode emitted unsupported permission request")
		}
		var value struct {
			ID         string   `json:"id"`
			SessionID  string   `json:"sessionID"`
			Permission string   `json:"permission"`
			Patterns   []string `json:"patterns"`
		}
		if json.Unmarshal(event.Properties, &value) != nil || value.ID == "" ||
			value.Permission == "" {
			return errors.New("OpenCode permission event is malformed")
		}
		if value.SessionID != s.sessionID {
			return nil
		}
		return s.stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest,
			ID: safeID(value.ID, "opencode-permission"),
			Permission: &adapterproto.Permission{
				Kind:    bounded(value.Permission, adapterproto.MaxToolNameBytes),
				Summary: bounded(strings.Join(value.Patterns, ", "), adapterproto.MaxToolSummaryBytes),
				Options: []string{"once", "always", "reject"},
			},
		})
	case "session.error":
		var value struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(event.Properties, &value) != nil {
			return errors.New("OpenCode error event is malformed")
		}
		if value.SessionID != s.sessionID {
			return nil
		}
		return s.stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindError,
			Code: "opencode_session_error", Summary: "OpenCode session failed",
		})
	case "message.removed", "message.part.removed", "session.updated",
		"session.created", "session.deleted", "session.diff", "session.compacted",
		"permission.replied", "todo.updated", "file.edited", "file.watcher.updated",
		"installation.updated", "installation.update-available", "project.updated",
		"plugin.added", "catalog.updated", "reference.updated", "integration.updated",
		"server.instance.disposed", "global.disposed", "lsp.client.diagnostics",
		"lsp.updated", "vcs.branch.updated", "mcp.tools.changed",
		"mcp.browser.open.failed", "command.executed", "workspace.ready",
		"workspace.failed", "pty.created", "pty.updated", "pty.exited",
		"pty.deleted", "worktree.ready", "worktree.failed":
		return nil
	default:
		return fmt.Errorf("unsupported OpenCode event type %q", event.Type)
	}
}

func (s *openCodeState) emitMessage(ctx context.Context, messageID string) error {
	var message struct {
		Info struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Role      string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := s.client.json(ctx, http.MethodGet,
		"/session/"+url.PathEscape(s.sessionID)+"/message/"+url.PathEscape(messageID),
		nil, http.StatusOK, &message); err != nil {
		return err
	}
	if message.Info.ID != messageID || message.Info.SessionID != s.sessionID ||
		message.Info.Role != "assistant" {
		return errors.New("OpenCode final message is incompatible")
	}
	var content strings.Builder
	for _, part := range message.Parts {
		if part.Type == "text" {
			content.WriteString(part.Text)
		}
	}
	if content.Len() == 0 {
		s.finalized[messageID] = true
		return nil
	}
	s.finalized[messageID] = true
	return s.stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
		Role: adapterproto.RoleAssistant, MessageID: safeID(messageID, "opencode-message"),
		Content: bounded(content.String(), adapterproto.MaxContentBytes),
	})
}

func validOpenCodeID(value string) bool {
	return value != "" && len(value) <= adapterproto.MaxIDBytes &&
		!strings.ContainsAny(value, " \t\r\n/\\\x00")
}

func openCodeResumeState(sessionID string) string {
	return boundedJSON(map[string]string{
		"driver": "opencode-server", "sessionId": sessionID,
	}, adapterproto.MaxResumeStateBytes)
}

func openCodeResumeID(raw string) (string, error) {
	var state struct {
		Driver    string `json:"driver"`
		SessionID string `json:"sessionId"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Driver != "opencode-server" ||
		!validOpenCodeID(state.SessionID) {
		return "", errors.New("OpenCode resume state is invalid")
	}
	return state.SessionID, nil
}

func openCodePermission(choice string) (string, error) {
	switch strings.ToLower(choice) {
	case "allow", "once":
		return "once", nil
	case "always":
		return "always", nil
	case "deny", "reject":
		return "reject", nil
	default:
		return "", errors.New("OpenCode permission choice is invalid")
	}
}
