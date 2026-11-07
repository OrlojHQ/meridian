package harnessadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

const (
	// Claude Code inlines whole tool results in one stream-json record.
	claudeRecordLimit    = 16 << 20
	claudeStartupTimeout = time.Minute
	claudeMaxPending     = 64
)

var claudeSessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ClaudeConfig configures Claude Code's headless stream-json mode.
type ClaudeConfig struct {
	Command     []string
	Permissions bool
	MCPServers  []MCPServer
}

type claudePrompt struct {
	requestID string
	input     json.RawMessage
	// questions is set for AskUserQuestion, whose answers return as input.
	questions []string
	frames    []string
	answers   map[string]string
}

type claudeSession struct {
	stream    *Stream
	rpc       *JSONLines
	sessionID string
	resuming  bool
	ready     bool
	running   bool
	turn      int
	sequence  int

	// streamMessage and streamTexts name partial text blocks so that deltas
	// share an ID with the authoritative assistant message for that block.
	streamMessage string
	streamTexts   int
	streamBlocks  map[int]string
	messageTexts  map[string]int
	lastAssistant bool

	prompts  map[string]*claudePrompt
	byFrame  map[string]string
	retired  retiredPrompts
	controls map[string]string
}

// RunClaudeStream maps Claude Code's `--print --input-format stream-json
// --output-format stream-json` control protocol to Meridian frames.
func RunClaudeStream(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	config ClaudeConfig,
) error {
	if err := validateClaudeConfig(config); err != nil {
		return err
	}
	stream := NewStream(input, output)
	if err := stream.Handshake(adapterproto.Capabilities{
		Resume: true, ToolEvents: true, Permissions: config.Permissions,
		InputRequests: config.Permissions, Heartbeat: false, OpaqueMetadata: true,
	}); err != nil {
		return err
	}
	initial, err := stream.Read()
	if err != nil {
		return err
	}
	if initial.Type != adapterproto.KindStart && initial.Type != adapterproto.KindResume {
		return errors.New("Claude adapter requires start or resume")
	}
	resumeID := ""
	if initial.Type == adapterproto.KindResume {
		resumeID, err = claudeResumeID(initial.ResumeState)
		if err != nil {
			stream.Fail("claude_resume_invalid")
			return err
		}
	}
	command, err := claudeCommand(config, resumeID)
	if err != nil {
		return err
	}
	child, err := startProcess(processConfig{Command: command})
	if err != nil {
		stream.Fail("claude_start_failed")
		return err
	}
	defer child.Close()

	session := &claudeSession{
		stream: stream, rpc: NewJSONLinesLimit(child.stdout, child.stdin, claudeRecordLimit),
		sessionID: resumeID, resuming: resumeID != "", streamBlocks: map[int]string{}, messageTexts: map[string]int{},
		prompts: map[string]*claudePrompt{}, byFrame: map[string]string{}, retired: retiredPrompts{},
		controls: map[string]string{},
	}
	records, recordErrors := readUpstream(ctx, session.rpc)
	frames, frameErrors := readController(ctx, stream)

	_ = stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusStarting,
	})
	if err := session.control("claude-initialize", map[string]any{"subtype": "initialize", "hooks": nil}); err != nil {
		stream.Fail("claude_write_failed")
		return err
	}
	startup := time.NewTimer(claudeStartupTimeout)
	defer startup.Stop()
	deadline := startup.C

	for {
		select {
		case <-deadline:
			stream.Fail("claude_startup_timeout")
			return errors.New("Claude Code startup timed out")
		case <-ctx.Done():
			return ctx.Err()
		case err := <-frameErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case err := <-recordErrors:
			if errors.Is(err, io.EOF) {
				stream.Fail("claude_process_exited")
				return errors.New("Claude Code exited unexpectedly")
			}
			stream.Fail("claude_protocol_failed")
			return fmt.Errorf("Claude Code stream failed: %w", err)
		case frame, open := <-frames:
			if !open {
				return nil
			}
			done, err := session.controller(frame)
			if err != nil || done {
				return err
			}
		case record, open := <-records:
			if !open {
				stream.Fail("claude_process_exited")
				return errors.New("Claude Code exited unexpectedly")
			}
			ready, err := session.upstream(record)
			if err != nil {
				return err
			}
			if ready && deadline != nil {
				startup.Stop()
				deadline = nil
			}
		}
	}
}

func (s *claudeSession) write(frame adapterproto.Frame) error {
	frame.Protocol = adapterproto.Version
	return s.stream.Write(frame)
}

func (s *claudeSession) fail(code string, err error) error {
	s.stream.Fail(code)
	return err
}

func (s *claudeSession) control(id string, request map[string]any) error {
	s.controls[id] = request["subtype"].(string)
	return s.rpc.Write(map[string]any{"type": "control_request", "request_id": id, "request": request})
}

func (s *claudeSession) respond(requestID string, response map[string]any) error {
	return s.rpc.Write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype": "success", "request_id": requestID, "response": response,
		},
	})
}

func (s *claudeSession) idle() error {
	frame := adapterproto.Frame{Type: adapterproto.KindStatus, Status: adapterproto.StatusIdle}
	if s.sessionID != "" {
		frame.ResumeState = boundedJSON(map[string]any{
			"driver": "claude-stream", "sessionId": s.sessionID,
		}, adapterproto.MaxResumeStateBytes)
	}
	return s.write(frame)
}

func (s *claudeSession) setRunning() error {
	if s.running {
		return nil
	}
	s.running = true
	s.turn++
	s.lastAssistant = false
	s.messageTexts = map[string]int{}
	return s.write(adapterproto.Frame{Type: adapterproto.KindStatus, Status: adapterproto.StatusRunning})
}

func (s *claudeSession) controller(frame adapterproto.Frame) (bool, error) {
	switch frame.Type {
	case adapterproto.KindUserMessage:
		// Claude Code queues messages that arrive during a turn.
		if err := s.setRunning(); err != nil {
			return false, err
		}
		if err := s.rpc.Write(map[string]any{
			"type": "user", "parent_tool_use_id": nil, "session_id": s.sessionID,
			"message": map[string]any{"role": "user", "content": frame.Content},
		}); err != nil {
			return false, s.fail("claude_write_failed", err)
		}
	case adapterproto.KindPermissionResponse:
		requestID, exists := s.byFrame[frame.ResponseTo]
		if !exists && s.retired.take(frame.ResponseTo) {
			return false, nil
		}
		prompt := s.prompts[requestID]
		if !exists || prompt == nil || prompt.questions != nil || frame.Permission == nil {
			return false, s.fail("claude_response_invalid", errors.New("unexpected Claude permission response"))
		}
		s.forget(requestID)
		response := map[string]any{"behavior": "deny", "message": "The Meridian operator denied this tool use."}
		if permissionAllowed(frame.Permission.Choice) {
			response = map[string]any{"behavior": "allow", "updatedInput": prompt.input}
		}
		if err := s.respond(requestID, response); err != nil {
			return false, s.fail("claude_write_failed", err)
		}
	case adapterproto.KindInputResponse:
		requestID, exists := s.byFrame[frame.ResponseTo]
		if !exists && s.retired.take(frame.ResponseTo) {
			return false, nil
		}
		prompt := s.prompts[requestID]
		if !exists || prompt == nil || prompt.questions == nil || frame.Input == nil {
			return false, s.fail("claude_response_invalid", errors.New("unexpected Claude input response"))
		}
		for index, id := range prompt.frames {
			if id == frame.ResponseTo {
				prompt.answers[prompt.questions[index]] = frame.Input.Value
			}
		}
		delete(s.byFrame, frame.ResponseTo)
		if len(prompt.answers) < len(prompt.questions) {
			return false, nil
		}
		var input map[string]json.RawMessage
		if json.Unmarshal(prompt.input, &input) != nil {
			return false, s.fail("claude_response_invalid", errors.New("Claude question input is malformed"))
		}
		answers, _ := json.Marshal(prompt.answers)
		input["answers"] = answers
		s.forget(requestID)
		if err := s.respond(requestID, map[string]any{"behavior": "allow", "updatedInput": input}); err != nil {
			return false, s.fail("claude_write_failed", err)
		}
	case adapterproto.KindCancel:
		_ = s.control("claude-interrupt", map[string]any{"subtype": "interrupt"})
		_ = s.write(adapterproto.Frame{Type: adapterproto.KindEnd, Status: adapterproto.StatusCancelled})
		return true, nil
	case adapterproto.KindHeartbeat, adapterproto.KindAck:
	default:
		return false, s.fail("claude_controller_frame_invalid", errors.New("unsupported Claude controller frame"))
	}
	return false, nil
}

func (s *claudeSession) forget(requestID string) {
	if prompt := s.prompts[requestID]; prompt != nil {
		for _, id := range prompt.frames {
			delete(s.byFrame, id)
		}
	}
	delete(s.prompts, requestID)
}

func (s *claudeSession) withdraw(requestID string) {
	if prompt := s.prompts[requestID]; prompt != nil {
		var open []string
		for _, id := range prompt.frames {
			if _, pending := s.byFrame[id]; pending {
				open = append(open, id)
			}
		}
		s.retired.add(open)
	}
	s.forget(requestID)
}

// upstream handles one Claude Code record and reports when startup finished.
func (s *claudeSession) upstream(record map[string]json.RawMessage) (bool, error) {
	recordType := stringField(record, "type")
	if recordType == "" {
		return false, s.fail("claude_event_invalid", errors.New("Claude record type is missing"))
	}
	// Subagent internals carry a parent tool use; the parent's own tool
	// events already describe that work.
	var parent *string
	if field(record, "parent_tool_use_id", &parent) != nil {
		return false, s.fail("claude_event_invalid", errors.New("Claude parent tool id is malformed"))
	}
	if parent != nil && *parent != "" && recordType != "control_request" {
		return false, nil
	}
	if id := stringField(record, "session_id"); id != "" && claudeSessionID.MatchString(id) {
		s.sessionID = id
	}
	switch recordType {
	case "control_response":
		return s.controlResponse(record)
	case "control_request":
		return false, s.controlRequest(record)
	case "control_cancel_request":
		s.withdraw(stringField(record, "request_id"))
	case "stream_event":
		return false, s.streamEvent(record)
	case "assistant":
		return false, s.assistant(record)
	case "user":
		return false, s.toolResults(record)
	case "result":
		return false, s.result(record)
	default:
		// system, keep_alive, tool_progress, rate-limit, hook, and later
		// presentation records have no lossless Meridian v1 equivalent.
	}
	return false, nil
}

func (s *claudeSession) controlResponse(record map[string]json.RawMessage) (bool, error) {
	var response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
	}
	if field(record, "response", &response) != nil || response.RequestID == "" {
		return false, s.fail("claude_response_invalid", errors.New("Claude control response is malformed"))
	}
	subtype, exists := s.controls[response.RequestID]
	if !exists {
		return false, s.fail("claude_response_invalid", errors.New("Claude control response correlation failed"))
	}
	delete(s.controls, response.RequestID)
	if subtype != "initialize" {
		return false, nil
	}
	if response.Subtype != "success" {
		return false, s.fail("claude_initialize_failed", errors.New("Claude Code rejected initialization"))
	}
	s.ready = true
	return true, s.idle()
}

func (s *claudeSession) controlRequest(record map[string]json.RawMessage) error {
	requestID := stringField(record, "request_id")
	var request struct {
		Subtype     string          `json:"subtype"`
		ToolName    string          `json:"tool_name"`
		DisplayName string          `json:"display_name"`
		Description string          `json:"description"`
		Input       json.RawMessage `json:"input"`
	}
	if requestID == "" || field(record, "request", &request) != nil {
		return s.fail("claude_request_invalid", errors.New("Claude control request is malformed"))
	}
	if request.Subtype != "can_use_tool" {
		return s.rpc.Write(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype": "error", "request_id": requestID,
				"error": "unsupported by the Meridian adapter",
			},
		})
	}
	if _, exists := s.prompts[requestID]; exists || len(s.prompts) >= claudeMaxPending ||
		request.ToolName == "" || len(request.Input) == 0 {
		return s.fail("claude_request_invalid", errors.New("Claude permission request is invalid"))
	}
	if request.ToolName == "AskUserQuestion" {
		return s.question(requestID, request.Input)
	}
	s.sequence++
	id := "claude-permission-" + strconv.Itoa(s.sequence)
	s.prompts[requestID] = &claudePrompt{requestID: requestID, input: request.Input, frames: []string{id}}
	s.byFrame[id] = requestID
	summary := request.Description
	if summary == "" {
		summary = request.DisplayName
	}
	if summary != "" {
		summary += ": "
	}
	summary += string(request.Input)
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindPermissionRequest, ID: id,
		Permission: &adapterproto.Permission{
			Kind:    bounded(request.ToolName, adapterproto.MaxToolNameBytes),
			Summary: bounded(summary, adapterproto.MaxToolSummaryBytes),
			Options: []string{"allow", "deny"},
		},
	})
}

func (s *claudeSession) question(requestID string, input json.RawMessage) error {
	var value struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(input, &value) != nil || len(value.Questions) == 0 ||
		len(value.Questions) > adapterproto.MaxOptions {
		return s.fail("claude_question_invalid", errors.New("Claude question is malformed"))
	}
	prompt := &claudePrompt{requestID: requestID, input: input, answers: map[string]string{}}
	for _, question := range value.Questions {
		if question.Question == "" {
			return s.fail("claude_question_invalid", errors.New("Claude question is empty"))
		}
		var text strings.Builder
		if question.Header != "" {
			text.WriteString(question.Header + ": ")
		}
		text.WriteString(question.Question)
		for _, option := range question.Options {
			text.WriteString("\n- " + option.Label)
			if option.Description != "" {
				text.WriteString(" (" + option.Description + ")")
			}
		}
		if question.MultiSelect {
			text.WriteString("\nSeveral answers may be separated by commas.")
		}
		s.sequence++
		id := "claude-input-" + strconv.Itoa(s.sequence)
		prompt.questions = append(prompt.questions, question.Question)
		prompt.frames = append(prompt.frames, id)
		s.byFrame[id] = requestID
		if err := s.write(adapterproto.Frame{
			Type: adapterproto.KindInputRequest, ID: id,
			Input: &adapterproto.Input{Prompt: bounded(text.String(), adapterproto.MaxToolSummaryBytes)},
		}); err != nil {
			return err
		}
	}
	s.prompts[requestID] = prompt
	return nil
}

func (s *claudeSession) streamEvent(record map[string]json.RawMessage) error {
	var event struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if field(record, "event", &event) != nil {
		return s.fail("claude_event_invalid", errors.New("Claude stream event is malformed"))
	}
	switch event.Type {
	case "message_start":
		if err := s.setRunning(); err != nil {
			return err
		}
		s.streamMessage = safeID(event.Message.ID, "")
		s.streamTexts = 0
		s.streamBlocks = map[int]string{}
	case "content_block_start":
		if event.ContentBlock.Type == "text" && s.streamMessage != "" {
			s.streamTexts++
			s.streamBlocks[event.Index] = claudeBlockID(s.streamMessage, s.streamTexts)
		}
	case "content_block_delta":
		id := s.streamBlocks[event.Index]
		if event.Delta.Type != "text_delta" || event.Delta.Text == "" || id == "" {
			return nil
		}
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindAssistantDelta, Role: adapterproto.RoleAssistant,
			MessageID: id, Content: bounded(event.Delta.Text, adapterproto.MaxContentBytes),
		})
	}
	return nil
}

func claudeBlockID(message string, ordinal int) string {
	return bounded(message, adapterproto.MaxIDBytes-12) + "-" + strconv.Itoa(ordinal)
}

func (s *claudeSession) assistant(record map[string]json.RawMessage) error {
	var message struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if field(record, "message", &message) != nil {
		return s.fail("claude_message_invalid", errors.New("Claude assistant message is malformed"))
	}
	if err := s.setRunning(); err != nil {
		return err
	}
	base := safeID(message.ID, "claude-message-"+strconv.Itoa(s.turn))
	for _, block := range message.Content {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			s.messageTexts[base]++
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindAssistantMessage, Role: adapterproto.RoleAssistant,
				MessageID: claudeBlockID(base, s.messageTexts[base]),
				Content:   bounded(block.Text, adapterproto.MaxContentBytes),
			}); err != nil {
				return err
			}
			s.lastAssistant = true
		case "tool_use":
			if block.ID == "" || block.Name == "" {
				return s.fail("claude_tool_invalid", errors.New("Claude tool use is malformed"))
			}
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindToolStart, Role: adapterproto.RoleAssistant,
				ToolCallID: safeID(block.ID, "claude-tool"),
				ToolName:   bounded(block.Name, adapterproto.MaxToolNameBytes),
				Summary:    bounded(string(block.Input), adapterproto.MaxToolSummaryBytes),
			}); err != nil {
				return err
			}
			s.lastAssistant = false
		}
	}
	return nil
}

func (s *claudeSession) toolResults(record map[string]json.RawMessage) error {
	var message struct {
		Content json.RawMessage `json:"content"`
	}
	if field(record, "message", &message) != nil {
		return s.fail("claude_message_invalid", errors.New("Claude user message is malformed"))
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		// Plain text user records echo local commands, not tool results.
		return nil
	}
	for _, block := range blocks {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		result := bounded(textBlocks(block.Content), adapterproto.MaxResultBytes)
		if result == "" {
			result = "{}"
		}
		frame := adapterproto.Frame{
			Type: adapterproto.KindToolResult, Role: adapterproto.RoleTool,
			ToolCallID: safeID(block.ToolUseID, "claude-tool"), Result: result,
		}
		if block.IsError {
			frame.Metadata = metadata(map[string]any{"isError": true})
		}
		if err := s.write(frame); err != nil {
			return err
		}
		s.lastAssistant = false
	}
	return nil
}

func (s *claudeSession) result(record map[string]json.RawMessage) error {
	var isError bool
	if field(record, "is_error", &isError) != nil {
		return s.fail("claude_result_invalid", errors.New("Claude result is malformed"))
	}
	text := stringField(record, "result")
	if !s.ready {
		// A missing --resume session is reported as a result before
		// initialization completes, and Claude Code then exits.
		if s.resuming {
			return s.fail("claude_resume_failed", errors.New("Claude Code could not resume its session"))
		}
		return s.fail("claude_start_failed", errors.New("Claude Code ended before initialization"))
	}
	if isError || stringField(record, "subtype") != "success" {
		summary := text
		if summary == "" {
			summary = "Claude Code turn ended with " + safeID(stringField(record, "subtype"), "an error")
		}
		if err := s.write(adapterproto.Frame{
			Type: adapterproto.KindError, Code: "claude_turn_failed",
			Summary: bounded(summary, adapterproto.MaxToolSummaryBytes),
		}); err != nil {
			return err
		}
	} else if !s.lastAssistant && text != "" {
		// Every successful turn ends with an assistant message, even when
		// the final text block was not streamed as its own record.
		if err := s.write(adapterproto.Frame{
			Type: adapterproto.KindAssistantMessage, Role: adapterproto.RoleAssistant,
			MessageID: "claude-result-" + strconv.Itoa(s.turn),
			Content:   bounded(text, adapterproto.MaxContentBytes),
		}); err != nil {
			return err
		}
	}
	s.running = false
	s.streamMessage = ""
	return s.idle()
}

func claudeCommand(config ClaudeConfig, resumeID string) ([]string, error) {
	command := append([]string(nil), config.Command...)
	command = append(command,
		"--print", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages",
	)
	if config.Permissions {
		command = append(command, "--permission-prompt-tool", "stdio")
	} else {
		command = append(command, "--permission-prompts", "none")
	}
	if resumeID != "" {
		command = append(command, "--resume", resumeID)
	}
	if len(config.MCPServers) != 0 {
		servers := map[string]any{}
		for _, server := range config.MCPServers {
			servers[server.Name] = map[string]any{
				"type": "stdio", "command": server.Command, "args": append([]string{}, server.Arguments...),
			}
		}
		encoded, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return nil, err
		}
		command = append(command, "--mcp-config", string(encoded))
	}
	return command, nil
}

func validateClaudeConfig(config ClaudeConfig) error {
	if len(config.Command) == 0 {
		return errors.New("Claude Code executable is required")
	}
	for _, argument := range config.Command[1:] {
		name, _, _ := strings.Cut(argument, "=")
		switch name {
		case "-p", "--print", "--input-format", "--output-format", "--permission-prompt-tool",
			"--permission-prompts", "-r", "--resume", "-c", "--continue", "--session-id",
			"--fork-session", "--include-partial-messages", "--replay-user-messages",
			"--bg", "--background", "--mcp-config", "--remote-control", "--cloud", "--teleport":
			return fmt.Errorf("Claude Code argument %q must be configured by the adapter", argument)
		}
	}
	names := map[string]bool{}
	for _, server := range config.MCPServers {
		if names[server.Name] {
			return fmt.Errorf("duplicate MCP server %q", server.Name)
		}
		names[server.Name] = true
	}
	return nil
}

func claudeResumeID(raw string) (string, error) {
	var state struct {
		Driver    string `json:"driver"`
		SessionID string `json:"sessionId"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Driver != "claude-stream" ||
		!claudeSessionID.MatchString(state.SessionID) {
		return "", errors.New("Claude resume state is invalid")
	}
	return state.SessionID, nil
}
