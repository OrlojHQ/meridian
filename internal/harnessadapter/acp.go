package harnessadapter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/buildinfo"
)

const (
	acpProtocolVersion = 1
	// ACP tool calls can inline whole command output and file diffs.
	acpRecordLimit    = 16 << 20
	acpStartupTimeout = time.Minute
	acpCancelGrace    = 2 * time.Second
	acpMaxQueue       = 32
	acpMaxPending     = 64
	acpMaxTools       = 1024
	acpMaxPlanEntries = 64
	acpMaxSessionID   = 512

	acpErrorMethodNotFound = -32601
	acpErrorInvalidParams  = -32602
	acpErrorAuthRequired   = -32000
	acpErrorCancelled      = -32800
)

var acpSecretEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ACPConfig configures an Agent Client Protocol agent launched over stdio.
type ACPConfig struct {
	Command     []string
	Permissions bool
	MCPServers  []MCPServer
	// SecretEnv names environment variables that receive a fresh random
	// value for each launch, for agents that protect a loopback server of
	// their own with a password read from the environment.
	SecretEnv []string
}

type acpTool struct {
	frameID  string
	name     string
	title    string
	kind     string
	input    string
	started  bool
	finished bool
}

type acpToolFields struct {
	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title"`
	Name       *string         `json:"name"`
	Kind       *string         `json:"kind"`
	Status     *string         `json:"status"`
	Content    json.RawMessage `json:"content"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
}

type acpMessage struct {
	id   string
	text strings.Builder
}

type acpPermission struct {
	rpcKey  string
	rpcID   json.RawMessage
	labels  map[string]string
	ids     map[string]bool
	rejects string
}

type acpSession struct {
	stream *Stream
	rpc    *JSONLines
	config ACPConfig
	cwd    string

	resume    string
	sessionID string
	ready     bool
	loading   bool

	request  int
	pending  map[int]string
	promptID int
	queue    []adapterproto.Frame

	turn    int
	segment int
	message *acpMessage

	tools     map[string]*acpTool
	toolOrder []string
	plans     int
	plan      [32]byte

	prompts  map[string]*acpPermission
	byRPC    map[string]string
	retired  retiredPrompts
	sequence int

	cancelled bool
}

// RunACP launches an Agent Client Protocol agent over stdio and maps its
// sessions, streamed updates, tool calls, plans, permission requests, and
// available commands to Meridian frames. The client advertises no file
// system, terminal, authentication, or elicitation capability, so the agent
// can never ask Meridian to act on its behalf.
func RunACP(ctx context.Context, input io.Reader, output io.Writer, config ACPConfig) error {
	if err := validateACPConfig(config); err != nil {
		return err
	}
	stream := NewStream(input, output)
	if err := stream.Handshake(adapterproto.Capabilities{
		Resume: true, ToolEvents: true, Permissions: config.Permissions,
		InputRequests: false, Heartbeat: false, OpaqueMetadata: true, Commands: true,
	}); err != nil {
		return err
	}
	initial, err := stream.Read()
	if err != nil {
		return err
	}
	if initial.Type != adapterproto.KindStart && initial.Type != adapterproto.KindResume {
		return errors.New("ACP adapter requires start or resume")
	}
	session := &acpSession{
		stream: stream, config: config, pending: map[int]string{},
		tools: map[string]*acpTool{}, prompts: map[string]*acpPermission{},
		byRPC: map[string]string{}, retired: retiredPrompts{},
	}
	if initial.Type == adapterproto.KindResume {
		session.resume, err = acpResumeID(initial.ResumeState)
		if err != nil {
			stream.Fail("acp_resume_invalid")
			return err
		}
	}
	session.cwd, err = os.Getwd()
	if err != nil || !filepath.IsAbs(session.cwd) {
		stream.Fail("acp_start_failed")
		return errors.New("resolve ACP working directory")
	}
	env, err := acpEnvironment(config.SecretEnv)
	if err != nil {
		stream.Fail("acp_start_failed")
		return err
	}
	child, err := startProcess(processConfig{Command: config.Command, Env: env})
	if err != nil {
		stream.Fail("acp_start_failed")
		return err
	}
	defer child.Close()
	session.rpc = NewJSONLinesLimit(child.stdout, child.stdin, acpRecordLimit)

	records, recordErrors := readUpstream(ctx, session.rpc)
	frames, frameErrors := readController(ctx, stream)
	if err := session.write(adapterproto.Frame{
		Type: adapterproto.KindStatus, Status: adapterproto.StatusStarting,
	}); err != nil {
		return err
	}
	if err := session.call("initialize", map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]any{
			"name": "meridian", "title": "Meridian", "version": buildinfo.Current().Version,
		},
	}); err != nil {
		return session.fail("acp_write_failed", err)
	}
	startup := time.NewTimer(acpStartupTimeout)
	defer startup.Stop()
	deadline := startup.C
	// Cancel waits briefly for the agent to confirm with a cancelled
	// stop reason before the process group is terminated.
	cancelGrace := time.NewTimer(acpCancelGrace)
	cancelGrace.Stop()
	defer cancelGrace.Stop()
	var grace <-chan time.Time

	for {
		select {
		case <-deadline:
			return session.fail("acp_startup_timeout", errors.New("ACP agent startup timed out"))
		case <-grace:
			return session.endCancelled()
		case <-ctx.Done():
			return ctx.Err()
		case err := <-frameErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case err := <-recordErrors:
			if errors.Is(err, io.EOF) {
				return session.fail("acp_process_exited", errors.New("ACP agent exited unexpectedly"))
			}
			return session.fail("acp_protocol_failed", fmt.Errorf("ACP agent stream failed: %w", err))
		case frame, open := <-frames:
			if !open {
				return nil
			}
			done, err := session.controller(frame)
			if err != nil || done {
				return err
			}
			if session.cancelled && grace == nil {
				cancelGrace.Reset(acpCancelGrace)
				grace = cancelGrace.C
			}
		case record, open := <-records:
			if !open {
				return session.fail("acp_process_exited", errors.New("ACP agent exited unexpectedly"))
			}
			done, err := session.upstream(record)
			if err != nil || done {
				return err
			}
			if session.ready && deadline != nil {
				startup.Stop()
				deadline = nil
			}
		}
	}
}

func (s *acpSession) write(frame adapterproto.Frame) error {
	frame.Protocol = adapterproto.Version
	return s.stream.Write(frame)
}

func (s *acpSession) fail(code string, err error) error {
	s.stream.Fail(code)
	return err
}

func (s *acpSession) call(method string, params any) error {
	s.request++
	s.pending[s.request] = method
	return s.rpc.Write(map[string]any{"jsonrpc": "2.0", "id": s.request, "method": method, "params": params})
}

func (s *acpSession) notify(method string, params any) error {
	return s.rpc.Write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *acpSession) reply(id json.RawMessage, result any) error {
	return s.rpc.Write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *acpSession) refuse(id json.RawMessage, code int, message string) error {
	return s.rpc.Write(map[string]any{
		"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message},
	})
}

func (s *acpSession) idle() error {
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindStatus, Status: adapterproto.StatusIdle,
		ResumeState: boundedJSON(map[string]string{
			"driver": "acp", "sessionId": s.sessionID,
		}, adapterproto.MaxResumeStateBytes),
	})
}

func (s *acpSession) endCancelled() error {
	return s.write(adapterproto.Frame{Type: adapterproto.KindEnd, Status: adapterproto.StatusCancelled})
}

func (s *acpSession) mcpServers() []map[string]any {
	servers := make([]map[string]any, 0, len(s.config.MCPServers))
	for _, server := range s.config.MCPServers {
		servers = append(servers, map[string]any{
			"name": server.Name, "command": server.Command,
			"args": append([]string{}, server.Arguments...), "env": []any{},
		})
	}
	return servers
}

func (s *acpSession) controller(frame adapterproto.Frame) (bool, error) {
	switch frame.Type {
	case adapterproto.KindUserMessage:
		if s.cancelled {
			return false, nil
		}
		if !s.ready || s.promptID != 0 {
			if len(s.queue) >= acpMaxQueue {
				return false, s.write(adapterproto.Frame{
					Type: adapterproto.KindError, Code: "acp_queue_full",
					Summary: "The agent already has the maximum number of queued messages",
				})
			}
			s.queue = append(s.queue, frame)
			return false, nil
		}
		return false, s.startTurn(frame)
	case adapterproto.KindPermissionResponse:
		prompt := s.prompts[frame.ResponseTo]
		if prompt == nil && s.retired.take(frame.ResponseTo) {
			return false, nil
		}
		if prompt == nil || frame.Permission == nil {
			return false, s.fail("acp_response_invalid", errors.New("unexpected ACP permission response"))
		}
		s.forget(frame.ResponseTo)
		return false, s.reply(prompt.rpcID, prompt.outcome(frame.Permission.Choice))
	case adapterproto.KindCancel:
		s.cancelled = true
		s.queue = nil
		for id, prompt := range s.prompts {
			s.forget(id)
			if err := s.reply(prompt.rpcID, acpCancelledOutcome()); err != nil {
				return false, s.fail("acp_write_failed", err)
			}
		}
		if s.sessionID != "" {
			if err := s.notify("session/cancel", map[string]any{"sessionId": s.sessionID}); err != nil {
				return false, s.fail("acp_write_failed", err)
			}
		}
		if s.promptID == 0 {
			return true, s.endCancelled()
		}
		return false, nil
	case adapterproto.KindHeartbeat, adapterproto.KindAck:
		return false, nil
	default:
		return false, s.fail("acp_controller_frame_invalid", errors.New("unsupported ACP controller frame"))
	}
}

func (s *acpSession) startTurn(frame adapterproto.Frame) error {
	s.turn++
	s.segment = 0
	s.message = nil
	if err := s.write(adapterproto.Frame{Type: adapterproto.KindStatus, Status: adapterproto.StatusRunning}); err != nil {
		return err
	}
	if err := s.call("session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []map[string]any{{"type": "text", "text": frame.Content}},
	}); err != nil {
		return s.fail("acp_write_failed", err)
	}
	s.promptID = s.request
	return nil
}

func (s *acpSession) upstream(record map[string]json.RawMessage) (bool, error) {
	method := stringField(record, "method")
	rawID, hasID := record["id"]
	switch {
	case method != "" && hasID:
		return false, s.agentRequest(rawID, method, record["params"])
	case method != "":
		return false, s.notification(method, record["params"])
	case hasID:
		return s.response(rawID, record)
	default:
		return false, s.fail("acp_message_invalid", errors.New("ACP JSON-RPC message is malformed"))
	}
}

func (s *acpSession) response(rawID json.RawMessage, record map[string]json.RawMessage) (bool, error) {
	id, err := strconv.Atoi(string(rawID))
	method, exists := s.pending[id]
	if err != nil || !exists {
		return false, s.fail("acp_response_invalid", errors.New("ACP response correlation failed"))
	}
	delete(s.pending, id)
	var failure struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	failed := record["error"] != nil && string(record["error"]) != "null"
	if failed && field(record, "error", &failure) != nil {
		return false, s.fail("acp_response_invalid", errors.New("ACP error response is malformed"))
	}
	switch method {
	case "initialize":
		return false, s.initialized(record, failed)
	case "session/new", "session/resume", "session/load":
		if failed {
			code := "acp_session_failed"
			switch {
			case failure.Code == acpErrorAuthRequired:
				code = "acp_auth_required"
			case method != "session/new":
				code = "acp_resume_failed"
			}
			return false, s.fail(code, errors.New("ACP agent could not open its session"))
		}
		if method == "session/new" {
			var result struct {
				SessionID string `json:"sessionId"`
			}
			if field(record, "result", &result) != nil || !validACPSessionID(result.SessionID) {
				return false, s.fail("acp_session_failed", errors.New("ACP agent returned an invalid session id"))
			}
			s.sessionID = result.SessionID
		} else {
			s.sessionID = s.resume
		}
		s.ready = true
		s.loading = false
		if err := s.idle(); err != nil {
			return false, err
		}
		return false, s.next()
	case "session/prompt":
		if id != s.promptID {
			return false, s.fail("acp_response_invalid", errors.New("ACP prompt response correlation failed"))
		}
		return s.finishTurn(record, failed, failure.Message)
	}
	return false, nil
}

func (s *acpSession) initialized(record map[string]json.RawMessage, failed bool) error {
	var result struct {
		ProtocolVersion json.RawMessage `json:"protocolVersion"`
	}
	if failed || field(record, "result", &result) != nil {
		return s.fail("acp_initialize_failed", errors.New("ACP agent rejected initialization"))
	}
	if string(result.ProtocolVersion) != strconv.Itoa(acpProtocolVersion) {
		return s.fail("acp_protocol_unsupported", errors.New("ACP agent does not speak protocol version 1"))
	}
	// Capabilities are advisory; a malformed capability object means none.
	var capabilities struct {
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(record["result"], &capabilities)
	agent := capabilities.AgentCapabilities
	if s.resume == "" {
		return s.call("session/new", map[string]any{"cwd": s.cwd, "mcpServers": s.mcpServers()})
	}
	params := map[string]any{"sessionId": s.resume, "cwd": s.cwd, "mcpServers": s.mcpServers()}
	switch {
	case len(agent.SessionCapabilities.Resume) != 0 && string(agent.SessionCapabilities.Resume) != "null":
		return s.call("session/resume", params)
	case agent.LoadSession:
		// session/load replays history Meridian already holds; drop it.
		s.loading = true
		return s.call("session/load", params)
	default:
		return s.fail("acp_resume_unsupported", errors.New("ACP agent cannot resume sessions"))
	}
}

func (s *acpSession) next() error {
	if len(s.queue) == 0 || !s.ready || s.promptID != 0 {
		return nil
	}
	frame := s.queue[0]
	s.queue = s.queue[1:]
	return s.startTurn(frame)
}

func (s *acpSession) finishTurn(record map[string]json.RawMessage, failed bool, message string) (bool, error) {
	s.promptID = 0
	if err := s.flush(); err != nil {
		return false, err
	}
	if s.cancelled {
		return true, s.endCancelled()
	}
	var result struct {
		StopReason string `json:"stopReason"`
	}
	if !failed && field(record, "result", &result) != nil {
		return false, s.fail("acp_response_invalid", errors.New("ACP prompt response is malformed"))
	}
	code, summary := "", ""
	switch {
	case failed:
		code, summary = "acp_turn_failed", defaultString(acpText(message, adapterproto.MaxToolSummaryBytes), "The agent turn failed")
	case result.StopReason == "max_tokens":
		code, summary = "acp_max_tokens", "The agent stopped at its token limit"
	case result.StopReason == "max_turn_requests":
		code, summary = "acp_max_turn_requests", "The agent stopped at its request limit for one turn"
	case result.StopReason == "refusal":
		code, summary = "acp_refusal", "The agent refused to continue"
	}
	if code != "" {
		if err := s.write(adapterproto.Frame{Type: adapterproto.KindError, Code: code, Summary: summary}); err != nil {
			return false, err
		}
	}
	if err := s.idle(); err != nil {
		return false, err
	}
	return false, s.next()
}

func (s *acpSession) notification(method string, raw json.RawMessage) error {
	switch method {
	case "session/update":
		var params struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		var kind struct {
			SessionUpdate string `json:"sessionUpdate"`
		}
		if json.Unmarshal(raw, &params) != nil || json.Unmarshal(params.Update, &kind) != nil ||
			kind.SessionUpdate == "" {
			return s.fail("acp_update_invalid", errors.New("ACP session update is malformed"))
		}
		known := s.sessionID
		if known == "" {
			known = s.resume
		}
		if known != "" && params.SessionID != known {
			return nil
		}
		if kind.SessionUpdate == "available_commands_update" {
			return s.reportCommands(params.Update)
		}
		if !s.ready || s.loading || s.cancelled && s.promptID == 0 {
			return nil
		}
		return s.update(kind.SessionUpdate, params.Update)
	case "$/cancel_request":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return nil
		}
		key := string(compactJSON(params.RequestID))
		frameID, exists := s.byRPC[key]
		if !exists {
			return nil
		}
		prompt := s.prompts[frameID]
		s.forget(frameID)
		s.retired.add([]string{frameID})
		return s.refuse(prompt.rpcID, acpErrorCancelled, "request cancelled")
	}
	// Extension and elicitation notifications have no Meridian equivalent.
	return nil
}

func (s *acpSession) update(kind string, raw json.RawMessage) error {
	switch kind {
	case "agent_message_chunk":
		var chunk struct {
			MessageID *string `json:"messageId"`
			Content   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &chunk) != nil {
			return s.fail("acp_update_invalid", errors.New("ACP message chunk is malformed"))
		}
		if chunk.Content.Type != "text" || chunk.Content.Text == "" {
			return nil
		}
		messageID := ""
		if chunk.MessageID != nil {
			messageID = *chunk.MessageID
		}
		return s.chunk(messageID, chunk.Content.Text)
	case "tool_call", "tool_call_update":
		var fields acpToolFields
		if json.Unmarshal(raw, &fields) != nil || fields.ToolCallID == "" {
			return s.fail("acp_update_invalid", errors.New("ACP tool call is malformed"))
		}
		return s.toolCall(fields, false)
	case "plan":
		var plan struct {
			Entries []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"entries"`
		}
		if json.Unmarshal(raw, &plan) != nil {
			return s.fail("acp_update_invalid", errors.New("ACP plan is malformed"))
		}
		return s.reportPlan(plan.Entries)
	}
	// Thoughts, echoed user chunks, modes, configuration, session info,
	// usage, notices, compaction progress, and future update kinds are
	// presentation data without a lossless Meridian v1 equivalent.
	return nil
}

func (s *acpSession) chunk(messageID, text string) error {
	id := ""
	if messageID != "" {
		id = acpFrameID(messageID, "acp-message")
	}
	if s.message != nil && id != "" && s.message.id != id {
		if err := s.flush(); err != nil {
			return err
		}
	}
	if s.message == nil {
		if id == "" {
			s.segment++
			id = "acp-message-" + strconv.Itoa(s.turn) + "-" + strconv.Itoa(s.segment)
		}
		s.message = &acpMessage{id: id}
	}
	if s.message.text.Len() < adapterproto.MaxContentBytes {
		s.message.text.WriteString(acpText(text, adapterproto.MaxContentBytes-s.message.text.Len()))
	}
	delta := acpText(text, adapterproto.MaxContentBytes)
	if delta == "" {
		return nil
	}
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindAssistantDelta, Role: adapterproto.RoleAssistant,
		MessageID: s.message.id, Content: delta,
	})
}

// flush finalizes the streamed assistant message, if any.
func (s *acpSession) flush() error {
	message := s.message
	s.message = nil
	if message == nil {
		return nil
	}
	content := acpText(message.text.String(), adapterproto.MaxContentBytes)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindAssistantMessage, Role: adapterproto.RoleAssistant,
		MessageID: message.id, Content: content,
	})
}

func (s *acpSession) tool(id string) *acpTool {
	if tool := s.tools[id]; tool != nil {
		return tool
	}
	if len(s.toolOrder) >= acpMaxTools {
		delete(s.tools, s.toolOrder[0])
		s.toolOrder = s.toolOrder[1:]
	}
	tool := &acpTool{frameID: acpFrameID(id, "acp-tool")}
	s.tools[id] = tool
	s.toolOrder = append(s.toolOrder, id)
	return tool
}

// toolCall merges a tool call or update. Agents announce calls before their
// input is known, so the tool_start frame waits until the call runs, ends,
// or asks for permission.
func (s *acpSession) toolCall(fields acpToolFields, permission bool) error {
	tool := s.tool(fields.ToolCallID)
	if fields.Name != nil && *fields.Name != "" {
		tool.name = acpLine(*fields.Name, adapterproto.MaxToolNameBytes)
	}
	if fields.Title != nil && *fields.Title != "" {
		tool.title = acpLine(*fields.Title, adapterproto.MaxToolSummaryBytes)
		if tool.name == "" {
			tool.name = acpLine(*fields.Title, adapterproto.MaxToolNameBytes)
		}
	}
	if fields.Kind != nil && *fields.Kind != "" {
		tool.kind = acpLine(*fields.Kind, adapterproto.MaxToolNameBytes)
	}
	if input := acpJSON(fields.RawInput); input != "" {
		tool.input = acpText(input, adapterproto.MaxToolSummaryBytes)
	}
	status := ""
	if fields.Status != nil {
		status = *fields.Status
	}
	if permission || status == "in_progress" || status == "completed" || status == "failed" {
		if err := s.startTool(tool); err != nil {
			return err
		}
	}
	if (status != "completed" && status != "failed") || tool.finished {
		return nil
	}
	tool.finished = true
	result := acpToolResult(fields)
	if result == "" {
		result = status
	}
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindToolResult, Role: adapterproto.RoleTool, ToolCallID: tool.frameID,
		Result:   acpText(result, adapterproto.MaxResultBytes),
		Metadata: metadata(map[string]string{"status": status, "kind": tool.kind}),
	})
}

func (s *acpSession) startTool(tool *acpTool) error {
	if tool.started {
		return nil
	}
	if err := s.flush(); err != nil {
		return err
	}
	tool.started = true
	summary := tool.input
	if tool.title != "" && tool.title != tool.name {
		summary = strings.TrimSpace(tool.title + "\n" + tool.input)
	}
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindToolStart, Role: adapterproto.RoleAssistant, ToolCallID: tool.frameID,
		ToolName: defaultString(defaultString(tool.name, tool.kind), "tool"),
		Summary:  acpText(summary, adapterproto.MaxToolSummaryBytes),
	})
}

// reportPlan shows each changed plan as a completed "plan" tool call whose
// result is a checklist, since Meridian v1 has no plan frame.
func (s *acpSession) reportPlan(entries []struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}) error {
	var checklist strings.Builder
	done := 0
	for index, entry := range entries {
		mark := "[ ]"
		switch entry.Status {
		case "completed":
			mark = "[x]"
			done++
		case "in_progress":
			mark = "[~]"
		}
		if index < acpMaxPlanEntries {
			checklist.WriteString(mark + " " + acpLine(entry.Content, adapterproto.MaxCommandDescriptionBytes) + "\n")
		}
	}
	if len(entries) > acpMaxPlanEntries {
		checklist.WriteString("… " + strconv.Itoa(len(entries)-acpMaxPlanEntries) + " more\n")
	}
	digest := sha256.Sum256([]byte(checklist.String()))
	if len(entries) == 0 || digest == s.plan {
		return nil
	}
	s.plan = digest
	s.plans++
	if err := s.flush(); err != nil {
		return err
	}
	id := "acp-plan-" + strconv.Itoa(s.plans)
	if err := s.write(adapterproto.Frame{
		Type: adapterproto.KindToolStart, Role: adapterproto.RoleAssistant, ToolCallID: id,
		ToolName: "plan", Summary: strconv.Itoa(done) + " of " + strconv.Itoa(len(entries)) + " done",
	}); err != nil {
		return err
	}
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindToolResult, Role: adapterproto.RoleTool, ToolCallID: id,
		Result: acpText(checklist.String(), adapterproto.MaxResultBytes),
	})
}

// reportCommands maps available_commands_update onto prompt commands: ACP
// agents expand "/name input" sent as ordinary prompt text.
func (s *acpSession) reportCommands(raw json.RawMessage) error {
	var update struct {
		AvailableCommands []json.RawMessage `json:"availableCommands"`
	}
	if json.Unmarshal(raw, &update) != nil {
		return s.fail("acp_update_invalid", errors.New("ACP command update is malformed"))
	}
	commands := newCommandSet()
	for _, entry := range update.AvailableCommands {
		var command struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		var input struct {
			Input struct {
				Hint string `json:"hint"`
			} `json:"input"`
		}
		if json.Unmarshal(entry, &command) != nil {
			continue
		}
		_ = json.Unmarshal(entry, &input)
		commands.add(command.Name, command.Description, input.Input.Hint, adapterproto.CommandPrompt)
	}
	return s.stream.ReportCommands(commands)
}

func (s *acpSession) agentRequest(rawID json.RawMessage, method string, raw json.RawMessage) error {
	if method != "session/request_permission" {
		// File system, terminal, elicitation, and extension requests need
		// capabilities this client never advertises.
		return s.refuse(rawID, acpErrorMethodNotFound, "method is not supported by the Meridian client")
	}
	key := string(compactJSON(rawID))
	if _, exists := s.byRPC[key]; exists || len(s.prompts) >= acpMaxPending {
		return s.fail("acp_request_invalid", errors.New("ACP permission request is invalid"))
	}
	var params struct {
		SessionID string        `json:"sessionId"`
		ToolCall  acpToolFields `json:"toolCall"`
		Options   []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return s.refuse(rawID, acpErrorInvalidParams, "permission request is malformed")
	}
	if params.SessionID != s.sessionID || s.cancelled || len(params.Options) == 0 ||
		len(params.Options) > adapterproto.MaxOptions {
		return s.reply(rawID, acpCancelledOutcome())
	}
	prompt := &acpPermission{rpcKey: key, rpcID: rawID, labels: map[string]string{}, ids: map[string]bool{}}
	var labels []string
	for _, option := range params.Options {
		if option.OptionID == "" || len(option.OptionID) > adapterproto.MaxOptionBytes ||
			!utf8.ValidString(option.OptionID) || prompt.ids[option.OptionID] {
			continue
		}
		switch option.Kind {
		case "reject_once":
			prompt.rejects = option.OptionID
		case "reject_always":
			if prompt.rejects == "" {
				prompt.rejects = option.OptionID
			}
		case "allow_once", "allow_always":
		default:
			continue
		}
		label := acpLine(defaultString(option.Name, option.Kind), adapterproto.MaxOptionBytes-8)
		if label == "" || prompt.labels[label] != "" {
			label += " (" + strconv.Itoa(len(labels)+1) + ")"
		}
		prompt.ids[option.OptionID] = true
		prompt.labels[label] = option.OptionID
		labels = append(labels, label)
	}
	if len(labels) == 0 {
		return s.reply(rawID, acpCancelledOutcome())
	}
	if !s.config.Permissions {
		return s.reply(rawID, prompt.outcome(""))
	}
	kind, summary := "tool", ""
	if params.ToolCall.ToolCallID != "" {
		if err := s.toolCall(params.ToolCall, true); err != nil {
			return err
		}
		tool := s.tools[params.ToolCall.ToolCallID]
		kind = defaultString(tool.kind, kind)
		summary = strings.TrimSpace(tool.title + "\n" + tool.input)
	}
	s.sequence++
	frameID := "acp-permission-" + strconv.Itoa(s.sequence)
	s.prompts[frameID] = prompt
	s.byRPC[key] = frameID
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindPermissionRequest, ID: frameID,
		Permission: &adapterproto.Permission{
			Kind:    kind,
			Summary: acpText(defaultString(summary, "The agent asks for permission"), adapterproto.MaxToolSummaryBytes),
			Options: labels,
		},
	})
}

func (s *acpSession) forget(frameID string) {
	if prompt := s.prompts[frameID]; prompt != nil {
		delete(s.byRPC, prompt.rpcKey)
	}
	delete(s.prompts, frameID)
}

// outcome selects only an option the agent offered. A choice that names no
// offered option selects the agent's reject option, or cancels without one.
func (p *acpPermission) outcome(choice string) map[string]any {
	selected := p.labels[choice]
	if selected == "" && p.ids[choice] {
		selected = choice
	}
	if selected == "" {
		selected = p.rejects
	}
	if selected == "" {
		return acpCancelledOutcome()
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": selected}}
}

func acpCancelledOutcome() map[string]any {
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

func acpToolResult(fields acpToolFields) string {
	var items []struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	var parts []string
	if json.Unmarshal(fields.Content, &items) == nil {
		for _, item := range items {
			switch {
			case item.Type == "content" && item.Content.Type == "text" && item.Content.Text != "":
				parts = append(parts, item.Content.Text)
			case item.Type == "diff" && item.Path != "":
				parts = append(parts, "Edited "+acpLine(item.Path, 4096))
			}
		}
	}
	if len(parts) != 0 {
		return strings.Join(parts, "\n")
	}
	var output struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(fields.RawOutput, &output) == nil && output.Output != "" {
		return output.Output
	}
	return acpJSON(fields.RawOutput)
}

// acpJSON compacts a JSON value for display; null and empty objects are
// treated as absent.
func acpJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	compact := string(compactJSON(raw))
	if compact == "null" || compact == "{}" || compact == `""` {
		return ""
	}
	return compact
}

// acpText makes agent text safe for a frame: valid UTF-8 without NUL,
// truncated on a rune boundary.
func acpText(value string, limit int) string {
	value = strings.ReplaceAll(strings.ToValidUTF8(value, "\uFFFD"), "\x00", "")
	if len(value) <= limit {
		return value
	}
	if limit < len("…") {
		return ""
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

// acpLine is acpText folded to one line without control characters.
func acpLine(value string, limit int) string {
	return acpText(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(value, "\uFFFD")), limit)
}

// acpFrameID keeps agent identifiers that are already safe Meridian IDs and
// replaces others with a stable digest.
func acpFrameID(value, prefix string) string {
	if safeID(value, "") != "" && utf8.ValidString(value) {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func validACPSessionID(value string) bool {
	if value == "" || len(value) > acpMaxSessionID || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func acpResumeID(raw string) (string, error) {
	var state struct {
		Driver    string `json:"driver"`
		SessionID string `json:"sessionId"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Driver != "acp" || !validACPSessionID(state.SessionID) {
		return "", errors.New("ACP resume state is invalid")
	}
	return state.SessionID, nil
}

func acpEnvironment(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	values := map[string]string{}
	for _, name := range names {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, errors.New("generate ACP launch secret")
		}
		values[name] = base64.RawURLEncoding.EncodeToString(secret)
	}
	return replaceEnv(currentEnv(), values), nil
}

func validateACPConfig(config ACPConfig) error {
	if len(config.Command) == 0 || !filepath.IsAbs(config.Command[0]) {
		return errors.New("ACP agent executable must be an absolute path")
	}
	for _, argument := range config.Command {
		if strings.ContainsRune(argument, 0) {
			return errors.New("ACP agent arguments must not contain NUL")
		}
	}
	names := map[string]bool{}
	for _, server := range config.MCPServers {
		if names[server.Name] {
			return fmt.Errorf("duplicate MCP server %q", server.Name)
		}
		names[server.Name] = true
	}
	seen := map[string]bool{}
	for _, name := range config.SecretEnv {
		if !acpSecretEnvName.MatchString(name) || seen[name] || strings.HasPrefix(name, "MERIDIAN_") ||
			strings.HasPrefix(name, "LD_") {
			return fmt.Errorf("ACP secret environment name %q is invalid", name)
		}
		switch name {
		case "PATH", "HOME", "SHELL", "USER", "PWD", "TMPDIR", "LANG":
			return fmt.Errorf("ACP secret environment name %q is reserved", name)
		}
		seen[name] = true
	}
	return nil
}
