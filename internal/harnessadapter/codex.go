package harnessadapter

import (
	"bytes"
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
	"github.com/OrlojHQ/meridian/internal/buildinfo"
)

const (
	// Codex can inline whole command output and diffs in one record.
	codexRecordLimit    = 16 << 20
	codexStartupTimeout = time.Minute
	codexMaxQueue       = 32
	codexMaxPending     = 64
)

var codexThreadID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// CodexConfig configures the Codex app-server JSON-RPC protocol.
type CodexConfig struct {
	Command []string
	// ApprovalPolicy and Sandbox are passed to thread/start and thread/resume
	// when set; otherwise Codex configuration decides.
	ApprovalPolicy string
	Sandbox        string
	MCPServers     []MCPServer
}

type codexPrompt struct {
	method    string
	id        json.RawMessage
	frames    []string
	questions []string
	answers   map[string]string
	requested json.RawMessage
}

type codexSession struct {
	stream   *Stream
	rpc      *JSONLines
	config   CodexConfig
	threadID string
	resume   string
	request  int
	sequence int

	pending  map[string]string
	starting bool
	turnID   string
	queue    []adapterproto.Frame
	steers   []adapterproto.Frame

	started       map[string]bool
	lastAssistant bool

	prompts map[string]*codexPrompt
	byFrame map[string]string
	retired retiredPrompts

	// skills maps reported skill commands to the SKILL.md path Codex gave.
	skills map[string]string
	// reviewing marks a review/start turn. Codex runs the review as an
	// inner turn inside the turn review/start returns (reviewTurn), and only
	// the outer turn's completion ends the Meridian turn.
	reviewing  bool
	reviewTurn string
}

const (
	codexCompactCommand = "compact"
	codexReviewCommand  = "review"
	codexMaxSkillPath   = 4096
)

// RunCodexAppServer maps `codex app-server` stdio JSON-RPC to Meridian frames.
func RunCodexAppServer(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	config CodexConfig,
) error {
	if err := validateCodexConfig(config); err != nil {
		return err
	}
	stream := NewStream(input, output)
	if err := stream.Handshake(adapterproto.Capabilities{
		Resume: true, ToolEvents: true, Permissions: config.ApprovalPolicy != "never",
		InputRequests: config.ApprovalPolicy != "never", Heartbeat: false, OpaqueMetadata: true,
		Commands: true,
	}); err != nil {
		return err
	}
	initial, err := stream.Read()
	if err != nil {
		return err
	}
	if initial.Type != adapterproto.KindStart && initial.Type != adapterproto.KindResume {
		return errors.New("Codex adapter requires start or resume")
	}
	resumeID := ""
	if initial.Type == adapterproto.KindResume {
		resumeID, err = codexResumeID(initial.ResumeState)
		if err != nil {
			stream.Fail("codex_resume_invalid")
			return err
		}
	}
	child, err := startProcess(processConfig{Command: codexCommand(config)})
	if err != nil {
		stream.Fail("codex_start_failed")
		return err
	}
	defer child.Close()

	session := &codexSession{
		stream: stream, rpc: NewJSONLinesLimit(child.stdout, child.stdin, codexRecordLimit),
		config: config, resume: resumeID, pending: map[string]string{},
		started: map[string]bool{}, prompts: map[string]*codexPrompt{}, byFrame: map[string]string{},
		retired: retiredPrompts{}, skills: map[string]string{},
	}
	records, recordErrors := readUpstream(ctx, session.rpc)
	frames, frameErrors := readController(ctx, stream)

	_ = stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusStarting,
	})
	if err := session.call("initialize", map[string]any{
		"clientInfo": map[string]any{
			"name": "meridian", "title": "Meridian", "version": buildinfo.Current().Version,
		},
		"capabilities": map[string]any{"experimentalApi": false, "requestAttestation": false},
	}); err != nil {
		stream.Fail("codex_write_failed")
		return err
	}
	startup := time.NewTimer(codexStartupTimeout)
	defer startup.Stop()
	deadline := startup.C

	for {
		select {
		case <-deadline:
			stream.Fail("codex_startup_timeout")
			return errors.New("Codex app-server startup timed out")
		case <-ctx.Done():
			return ctx.Err()
		case err := <-frameErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case err := <-recordErrors:
			if errors.Is(err, io.EOF) {
				stream.Fail("codex_process_exited")
				return errors.New("Codex app-server exited unexpectedly")
			}
			stream.Fail("codex_protocol_failed")
			return fmt.Errorf("Codex app-server stream failed: %w", err)
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
				stream.Fail("codex_process_exited")
				return errors.New("Codex app-server exited unexpectedly")
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

func (s *codexSession) write(frame adapterproto.Frame) error {
	frame.Protocol = adapterproto.Version
	return s.stream.Write(frame)
}

func (s *codexSession) fail(code string, err error) error {
	s.stream.Fail(code)
	return err
}

func (s *codexSession) call(method string, params any) error {
	s.request++
	id := "meridian-" + strconv.Itoa(s.request)
	s.pending[id] = method
	return s.rpc.Write(map[string]any{"id": id, "method": method, "params": params})
}

func (s *codexSession) reply(id json.RawMessage, result any) error {
	return s.rpc.Write(map[string]any{"id": id, "result": result})
}

func (s *codexSession) refuse(id json.RawMessage) error {
	return s.rpc.Write(map[string]any{
		"id": id, "error": map[string]any{"code": -32601, "message": "unsupported by the Meridian adapter"},
	})
}

func (s *codexSession) threadParams() map[string]any {
	params := map[string]any{}
	if s.config.ApprovalPolicy != "" {
		params["approvalPolicy"] = s.config.ApprovalPolicy
	}
	if s.config.Sandbox != "" {
		params["sandbox"] = s.config.Sandbox
	}
	return params
}

func (s *codexSession) idle() error {
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindStatus, Status: adapterproto.StatusIdle,
		ResumeState: boundedJSON(map[string]any{
			"driver": "codex-app-server", "threadId": s.threadID,
		}, adapterproto.MaxResumeStateBytes),
	})
}

func (s *codexSession) controller(frame adapterproto.Frame) (bool, error) {
	switch frame.Type {
	case adapterproto.KindUserMessage:
		steer, err := codexSteer(frame.Metadata)
		if err != nil {
			return false, s.fail("codex_metadata_invalid", err)
		}
		if s.threadID == "" || s.starting || s.turnID != "" {
			if steer && s.turnID != "" {
				return false, s.steer(frame)
			}
			if steer && s.starting {
				if len(s.steers) >= codexMaxQueue {
					return false, s.write(adapterproto.Frame{
						Type: adapterproto.KindError, Code: "codex_queue_full",
						Summary: "Codex already has the maximum number of queued messages",
					})
				}
				s.steers = append(s.steers, frame)
				return false, nil
			}
			// Codex runs one turn at a time; later messages become follow-up turns.
			if len(s.queue) >= codexMaxQueue {
				return false, s.write(adapterproto.Frame{
					Type: adapterproto.KindError, Code: "codex_queue_full",
					Summary: "Codex already has the maximum number of queued messages",
				})
			}
			s.queue = append(s.queue, frame)
			return false, nil
		}
		return false, s.startTurn(frame)
	case adapterproto.KindPermissionResponse:
		key, prompt := s.prompt(frame.ResponseTo)
		if prompt == nil && s.retired.take(frame.ResponseTo) {
			return false, nil
		}
		if prompt == nil || prompt.questions != nil || frame.Permission == nil {
			return false, s.fail("codex_response_invalid", errors.New("unexpected Codex permission response"))
		}
		s.forget(key)
		return false, s.reply(prompt.id, codexDecision(prompt, frame.Permission.Choice))
	case adapterproto.KindInputResponse:
		key, prompt := s.prompt(frame.ResponseTo)
		if prompt == nil && s.retired.take(frame.ResponseTo) {
			return false, nil
		}
		if prompt == nil || prompt.questions == nil || frame.Input == nil {
			return false, s.fail("codex_response_invalid", errors.New("unexpected Codex input response"))
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
		answers := map[string]any{}
		for question, value := range prompt.answers {
			answers[question] = map[string]any{"answers": []string{value}}
		}
		s.forget(key)
		return false, s.reply(prompt.id, map[string]any{"answers": answers})
	case adapterproto.KindCancel:
		if s.turnID != "" {
			_ = s.call("turn/interrupt", map[string]any{"threadId": s.threadID, "turnId": s.turnID})
		}
		_ = s.write(adapterproto.Frame{Type: adapterproto.KindEnd, Status: adapterproto.StatusCancelled})
		return true, nil
	case adapterproto.KindHeartbeat, adapterproto.KindAck:
	default:
		return false, s.fail("codex_controller_frame_invalid", errors.New("unsupported Codex controller frame"))
	}
	return false, nil
}

func (s *codexSession) steer(frame adapterproto.Frame) error {
	return s.call("turn/steer", map[string]any{
		"threadId": s.threadID, "expectedTurnId": s.turnID, "input": codexInput(frame.Content),
	})
}

// turnStarted records the active turn and sends steering held while it started.
func (s *codexSession) turnStarted(id string) error {
	s.turnID = id
	s.starting = false
	steers := s.steers
	s.steers = nil
	for _, frame := range steers {
		if err := s.steer(frame); err != nil {
			return s.fail("codex_write_failed", err)
		}
	}
	return nil
}

func (s *codexSession) startTurn(frame adapterproto.Frame) error {
	s.starting = true
	s.lastAssistant = false
	s.started = map[string]bool{}
	if err := s.write(adapterproto.Frame{Type: adapterproto.KindStatus, Status: adapterproto.StatusRunning}); err != nil {
		return err
	}
	method, params := "turn/start", map[string]any{
		"threadId": s.threadID, "input": codexInput(frame.Content),
	}
	if name, arguments, ok := slashCommand(frame.Content); ok {
		switch {
		case name == codexCompactCommand:
			method, params = "thread/compact/start", map[string]any{"threadId": s.threadID}
		case name == codexReviewCommand:
			target := map[string]any{"type": "uncommittedChanges"}
			if arguments != "" {
				target = map[string]any{"type": "custom", "instructions": arguments}
			}
			method, params = "review/start", map[string]any{"threadId": s.threadID, "target": target}
			s.reviewing = true
		case s.skills[name] != "":
			params["input"] = append(codexInput(strings.TrimSpace("$"+name+" "+arguments)),
				map[string]any{"type": "skill", "name": name, "path": s.skills[name]})
		}
	}
	if err := s.call(method, params); err != nil {
		return s.fail("codex_write_failed", err)
	}
	return nil
}

// reportCommands lists Codex's protocol actions and the enabled skills that
// skills/list returned. A failed or malformed listing reports the actions.
func (s *codexSession) reportCommands(record map[string]json.RawMessage) error {
	commands := newCommandSet()
	commands.add(codexCompactCommand, "Summarize the conversation to free context", "",
		adapterproto.CommandAction)
	commands.add(codexReviewCommand, "Review uncommitted changes, or follow custom review instructions",
		"[instructions]", adapterproto.CommandAction)
	var result struct {
		Data []struct {
			Skills []struct {
				Name             string `json:"name"`
				Description      string `json:"description"`
				ShortDescription string `json:"shortDescription"`
				Path             string `json:"path"`
				Enabled          bool   `json:"enabled"`
			} `json:"skills"`
		} `json:"data"`
	}
	if record["error"] == nil && field(record, "result", &result) == nil {
		for _, entry := range result.Data {
			for _, skill := range entry.Skills {
				if !skill.Enabled || commands.has(skill.Name) || !strings.HasPrefix(skill.Path, "/") ||
					len(skill.Path) > codexMaxSkillPath || strings.ContainsRune(skill.Path, 0) {
					continue
				}
				commands.add(skill.Name, defaultString(skill.ShortDescription, skill.Description), "",
					adapterproto.CommandAction)
				if commands.has(skill.Name) {
					s.skills[skill.Name] = skill.Path
				}
			}
		}
	}
	return s.stream.ReportCommands(commands)
}

// finishTurn reports the end of a turn and starts the next queued message.
func (s *codexSession) finishTurn() error {
	s.starting = false
	s.turnID = ""
	s.reviewing = false
	s.reviewTurn = ""
	s.queue = append(s.steers, s.queue...)
	s.steers = nil
	if err := s.idle(); err != nil {
		return err
	}
	if len(s.queue) == 0 {
		return nil
	}
	next := s.queue[0]
	s.queue = s.queue[1:]
	return s.startTurn(next)
}

func (s *codexSession) prompt(frameID string) (string, *codexPrompt) {
	key, exists := s.byFrame[frameID]
	if !exists {
		return "", nil
	}
	return key, s.prompts[key]
}

func (s *codexSession) forget(key string) {
	if prompt := s.prompts[key]; prompt != nil {
		for _, id := range prompt.frames {
			delete(s.byFrame, id)
		}
	}
	delete(s.prompts, key)
}

// withdraw drops a request Codex resolved itself, such as on interrupt.
func (s *codexSession) withdraw(key string) {
	if prompt := s.prompts[key]; prompt != nil {
		var open []string
		for _, id := range prompt.frames {
			if _, pending := s.byFrame[id]; pending {
				open = append(open, id)
			}
		}
		s.retired.add(open)
	}
	s.forget(key)
}

func (s *codexSession) upstream(record map[string]json.RawMessage) (bool, error) {
	method := stringField(record, "method")
	rawID, hasID := record["id"]
	switch {
	case method != "" && hasID:
		return false, s.serverRequest(rawID, method, record["params"])
	case method != "":
		return false, s.notification(method, record["params"])
	case hasID:
		return s.response(rawID, record)
	default:
		return false, s.fail("codex_message_invalid", errors.New("Codex JSON-RPC message is malformed"))
	}
}

func (s *codexSession) response(rawID json.RawMessage, record map[string]json.RawMessage) (bool, error) {
	var id string
	if json.Unmarshal(rawID, &id) != nil {
		return false, s.fail("codex_response_invalid", errors.New("Codex response id is malformed"))
	}
	method, exists := s.pending[id]
	if !exists {
		return false, s.fail("codex_response_invalid", errors.New("Codex response correlation failed"))
	}
	delete(s.pending, id)
	var failure struct {
		Message string `json:"message"`
	}
	failed := record["error"] != nil
	if failed && field(record, "error", &failure) != nil {
		return false, s.fail("codex_response_invalid", errors.New("Codex error response is malformed"))
	}
	switch method {
	case "initialize":
		if failed {
			return false, s.fail("codex_initialize_failed", errors.New("Codex rejected initialization"))
		}
		if err := s.rpc.Write(map[string]any{"method": "initialized"}); err != nil {
			return false, s.fail("codex_write_failed", err)
		}
		params := s.threadParams()
		if s.resume != "" {
			params["threadId"] = s.resume
			params["excludeTurns"] = true
			return false, s.call("thread/resume", params)
		}
		return false, s.call("thread/start", params)
	case "thread/start", "thread/resume":
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if failed || field(record, "result", &result) != nil || !codexThreadID.MatchString(result.Thread.ID) {
			code := "codex_thread_start_failed"
			if method == "thread/resume" {
				code = "codex_resume_failed"
			}
			return false, s.fail(code, errors.New("Codex could not open its thread"))
		}
		s.threadID = result.Thread.ID
		if err := s.idle(); err != nil {
			return false, err
		}
		if s.stream.commands {
			if err := s.call("skills/list", map[string]any{}); err != nil {
				return false, s.fail("codex_write_failed", err)
			}
		}
		if len(s.queue) != 0 {
			next := s.queue[0]
			s.queue = s.queue[1:]
			return true, s.startTurn(next)
		}
		return true, nil
	case "turn/start":
		if failed {
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindError, Code: "codex_turn_rejected",
				Summary: bounded(defaultString(failure.Message, "Codex rejected the turn"), adapterproto.MaxToolSummaryBytes),
			}); err != nil {
				return false, err
			}
			return false, s.finishTurn()
		}
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if field(record, "result", &result) != nil || result.Turn.ID == "" {
			return false, s.fail("codex_response_invalid", errors.New("Codex turn response is malformed"))
		}
		if s.starting {
			return false, s.turnStarted(result.Turn.ID)
		}
	case "turn/steer":
		if failed {
			return false, s.write(adapterproto.Frame{
				Type: adapterproto.KindError, Code: "codex_steer_rejected",
				Summary: bounded(defaultString(failure.Message, "Codex rejected the message"), adapterproto.MaxToolSummaryBytes),
			})
		}
	case "thread/compact/start":
		// Success is followed by turn/started for the compaction turn.
		if failed {
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindError, Code: "codex_compact_rejected",
				Summary: bounded(defaultString(failure.Message, "Codex rejected compaction"), adapterproto.MaxToolSummaryBytes),
			}); err != nil {
				return false, err
			}
			return false, s.finishTurn()
		}
	case "review/start":
		if failed {
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindError, Code: "codex_review_rejected",
				Summary: bounded(defaultString(failure.Message, "Codex rejected the review"), adapterproto.MaxToolSummaryBytes),
			}); err != nil {
				return false, err
			}
			return false, s.finishTurn()
		}
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if field(record, "result", &result) != nil || result.Turn.ID == "" {
			return false, s.fail("codex_response_invalid", errors.New("Codex review response is malformed"))
		}
		s.reviewTurn = result.Turn.ID
		if s.starting {
			return false, s.turnStarted(result.Turn.ID)
		}
		s.turnID = result.Turn.ID
	case "skills/list":
		return false, s.reportCommands(record)
	}
	return false, nil
}

func (s *codexSession) notification(method string, raw json.RawMessage) error {
	switch method {
	case "turn/started":
		var params struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(raw, &params) != nil || params.Turn.ID == "" {
			return s.fail("codex_event_invalid", errors.New("Codex turn start is malformed"))
		}
		if s.reviewing && !s.starting {
			return nil
		}
		if s.starting || s.turnID != params.Turn.ID {
			return s.turnStarted(params.Turn.ID)
		}
	case "turn/completed":
		var params struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return s.fail("codex_event_invalid", errors.New("Codex turn completion is malformed"))
		}
		if s.reviewing && params.Turn.ID != s.reviewTurn {
			return nil
		}
		if params.Turn.Status == "failed" {
			summary := "Codex turn failed"
			if params.Turn.Error != nil && params.Turn.Error.Message != "" {
				summary = params.Turn.Error.Message
			}
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindError, Code: "codex_turn_failed",
				Summary: bounded(summary, adapterproto.MaxToolSummaryBytes),
			}); err != nil {
				return err
			}
		}
		return s.finishTurn()
	case "item/agentMessage/delta":
		var params struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(raw, &params) != nil || params.ItemID == "" {
			return s.fail("codex_event_invalid", errors.New("Codex message delta is malformed"))
		}
		if params.Delta == "" {
			return nil
		}
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindAssistantDelta, Role: adapterproto.RoleAssistant,
			MessageID: safeID(params.ItemID, "codex-message"),
			Content:   bounded(params.Delta, adapterproto.MaxContentBytes),
		})
	case "item/started", "item/completed":
		var params struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(raw, &params) != nil || len(params.Item) == 0 {
			return s.fail("codex_event_invalid", errors.New("Codex item event is malformed"))
		}
		return s.item(params.Item, method == "item/completed")
	case "error":
		var params struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		if json.Unmarshal(raw, &params) != nil {
			return s.fail("codex_event_invalid", errors.New("Codex error is malformed"))
		}
		if params.WillRetry {
			return nil
		}
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindError, Code: "codex_error",
			Summary: bounded(defaultString(params.Error.Message, "Codex reported an error"), adapterproto.MaxToolSummaryBytes),
		})
	case "serverRequest/resolved":
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(raw, &params) == nil {
			s.withdraw(string(compactJSON(params.RequestID)))
		}
	}
	// Thread, token, rate-limit, reasoning, plan, output-delta, and other
	// presentation notifications have no lossless Meridian v1 equivalent.
	return nil
}

func (s *codexSession) item(raw json.RawMessage, completed bool) error {
	var item struct {
		Type             string          `json:"type"`
		ID               string          `json:"id"`
		Text             string          `json:"text"`
		Command          string          `json:"command"`
		AggregatedOutput *string         `json:"aggregatedOutput"`
		ExitCode         *int            `json:"exitCode"`
		Status           string          `json:"status"`
		Server           string          `json:"server"`
		Tool             string          `json:"tool"`
		Namespace        string          `json:"namespace"`
		Arguments        json.RawMessage `json:"arguments"`
		Result           json.RawMessage `json:"result"`
		Error            *struct {
			Message string `json:"message"`
		} `json:"error"`
		Query   string `json:"query"`
		Changes []struct {
			Path string `json:"path"`
			Kind struct {
				Type string `json:"type"`
			} `json:"kind"`
		} `json:"changes"`
		ContentItems json.RawMessage `json:"contentItems"`
		Review       string          `json:"review"`
	}
	if json.Unmarshal(raw, &item) != nil || item.Type == "" {
		return s.fail("codex_item_invalid", errors.New("Codex item is malformed"))
	}
	id := safeID(item.ID, "codex-item")
	var name, summary, result string
	switch item.Type {
	case "agentMessage":
		if !completed || item.Text == "" {
			return nil
		}
		s.lastAssistant = true
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindAssistantMessage, Role: adapterproto.RoleAssistant,
			MessageID: id, Content: bounded(item.Text, adapterproto.MaxContentBytes),
		})
	case "commandExecution":
		name, summary = "shell", item.Command
		if item.AggregatedOutput != nil {
			result = *item.AggregatedOutput
		}
		if result == "" && item.ExitCode != nil {
			result = "exit " + strconv.Itoa(*item.ExitCode)
		}
	case "fileChange":
		name = "apply_patch"
		var paths []string
		for _, change := range item.Changes {
			paths = append(paths, change.Kind.Type+" "+change.Path)
		}
		summary = strings.Join(paths, "\n")
		result = item.Status
	case "mcpToolCall":
		name, summary = item.Server+"."+item.Tool, string(item.Arguments)
		result = string(item.Result)
		if item.Error != nil {
			result = item.Error.Message
		}
	case "dynamicToolCall":
		name, summary, result = item.Tool, string(item.Arguments), string(item.ContentItems)
	case "webSearch":
		name, summary, result = "web_search", item.Query, item.Query
	case "exitedReviewMode":
		// The review's findings arrive as this item's text.
		if !completed || item.Review == "" {
			return nil
		}
		s.lastAssistant = true
		return s.write(adapterproto.Frame{
			Type: adapterproto.KindAssistantMessage, Role: adapterproto.RoleAssistant,
			MessageID: id, Content: bounded(item.Review, adapterproto.MaxContentBytes),
		})
	case "contextCompaction":
		name, summary, result = "compact", "Summarize the conversation", "compacted"
	default:
		return nil
	}
	if !s.started[id] {
		s.started[id] = true
		s.lastAssistant = false
		if err := s.write(adapterproto.Frame{
			Type: adapterproto.KindToolStart, Role: adapterproto.RoleAssistant, ToolCallID: id,
			ToolName: bounded(defaultString(name, item.Type), adapterproto.MaxToolNameBytes),
			Summary:  bounded(summary, adapterproto.MaxToolSummaryBytes),
		}); err != nil {
			return err
		}
	}
	if !completed {
		return nil
	}
	result = bounded(result, adapterproto.MaxResultBytes)
	if result == "" || result == "null" {
		result = defaultString(item.Status, "{}")
	}
	frame := adapterproto.Frame{
		Type: adapterproto.KindToolResult, Role: adapterproto.RoleTool, ToolCallID: id, Result: result,
	}
	details := map[string]any{"status": item.Status}
	if item.ExitCode != nil {
		details["exitCode"] = *item.ExitCode
	}
	frame.Metadata = metadata(details)
	return s.write(frame)
}

func (s *codexSession) serverRequest(rawID json.RawMessage, method string, raw json.RawMessage) error {
	key := string(compactJSON(rawID))
	if _, exists := s.prompts[key]; exists || len(s.prompts) >= codexMaxPending {
		return s.fail("codex_request_invalid", errors.New("Codex approval request is invalid"))
	}
	var params struct {
		Reason      string          `json:"reason"`
		Command     string          `json:"command"`
		Cwd         string          `json:"cwd"`
		GrantRoot   string          `json:"grantRoot"`
		Permissions json.RawMessage `json:"permissions"`
		ServerName  string          `json:"serverName"`
		Mode        string          `json:"mode"`
		Message     string          `json:"message"`
		Meta        *struct {
			ApprovalKind string          `json:"codex_approval_kind"`
			Persist      []string        `json:"persist"`
			ToolParams   json.RawMessage `json:"tool_params"`
		} `json:"_meta"`
		RequestedSchema *struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"requestedSchema"`
		Questions []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			IsSecret bool   `json:"isSecret"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	if len(raw) != 0 && json.Unmarshal(raw, &params) != nil {
		return s.fail("codex_request_invalid", errors.New("Codex request parameters are malformed"))
	}
	prompt := &codexPrompt{method: method, id: rawID}
	var kind, summary string
	options := []string{"accept", "acceptForSession", "decline", "cancel"}
	switch method {
	case "item/commandExecution/requestApproval":
		kind = "command"
		summary = strings.TrimSpace(strings.Join([]string{params.Reason, params.Command, params.Cwd}, "\n"))
	case "item/fileChange/requestApproval":
		kind = "file_change"
		summary = strings.TrimSpace(strings.Join([]string{params.Reason, params.GrantRoot}, "\n"))
	case "item/permissions/requestApproval":
		kind = "permissions"
		summary = strings.TrimSpace(params.Reason + "\n" + string(params.Permissions))
		options = []string{"allowTurn", "allowSession", "deny"}
		prompt.requested = params.Permissions
	case "item/tool/requestUserInput":
		if len(params.Questions) == 0 || len(params.Questions) > adapterproto.MaxOptions {
			return s.fail("codex_request_invalid", errors.New("Codex input request is malformed"))
		}
		prompt.answers = map[string]string{}
		for _, question := range params.Questions {
			if question.ID == "" {
				return s.fail("codex_request_invalid", errors.New("Codex question id is missing"))
			}
			text := question.Question
			if question.Header != "" {
				text = question.Header + ": " + text
			}
			for _, option := range question.Options {
				text += "\n- " + option.Label
				if option.Description != "" {
					text += " (" + option.Description + ")"
				}
			}
			s.sequence++
			id := "codex-input-" + strconv.Itoa(s.sequence)
			prompt.questions = append(prompt.questions, question.ID)
			prompt.frames = append(prompt.frames, id)
			s.byFrame[id] = key
			if err := s.write(adapterproto.Frame{
				Type: adapterproto.KindInputRequest, ID: id,
				Input: &adapterproto.Input{
					Prompt: bounded(defaultString(text, "Codex needs input"), adapterproto.MaxToolSummaryBytes),
					Secret: question.IsSecret,
				},
			}); err != nil {
				return err
			}
		}
		s.prompts[key] = prompt
		return nil
	case "mcpServer/elicitation/request":
		// Only data-free confirmations map onto a permission prompt; forms
		// that collect values and URL flows are declined.
		if params.Mode != "form" || params.RequestedSchema == nil || len(params.RequestedSchema.Properties) != 0 {
			return s.reply(rawID, map[string]any{"action": "decline", "content": nil, "_meta": nil})
		}
		kind = "mcp_elicitation"
		summary = params.ServerName + ": " + params.Message
		options = []string{"accept", "decline", "cancel"}
		if params.Meta != nil {
			if params.Meta.ApprovalKind == "mcp_tool_call" {
				kind = "mcp_tool_call"
			}
			if len(params.Meta.ToolParams) != 0 && string(params.Meta.ToolParams) != "null" {
				summary += "\n" + string(params.Meta.ToolParams)
			}
			for _, persist := range params.Meta.Persist {
				if persist == "session" {
					options = []string{"accept", "acceptForSession", "decline", "cancel"}
				}
			}
		}
	default:
		// Dynamic tools, ChatGPT token refresh, attestation, and the legacy
		// v1 approvals are never enabled by this client.
		return s.refuse(rawID)
	}
	s.sequence++
	id := "codex-permission-" + strconv.Itoa(s.sequence)
	prompt.frames = []string{id}
	s.prompts[key] = prompt
	s.byFrame[id] = key
	return s.write(adapterproto.Frame{
		Type: adapterproto.KindPermissionRequest, ID: id,
		Permission: &adapterproto.Permission{
			Kind: kind, Summary: bounded(defaultString(summary, kind), adapterproto.MaxToolSummaryBytes),
			Options: options,
		},
	})
}

func codexDecision(prompt *codexPrompt, choice string) any {
	if prompt.method == "mcpServer/elicitation/request" {
		switch {
		case choice == "acceptForSession":
			return map[string]any{"action": "accept", "content": map[string]any{}, "_meta": map[string]any{"persist": "session"}}
		case choice == "cancel":
			return map[string]any{"action": "cancel", "content": nil, "_meta": nil}
		case permissionAllowed(choice):
			return map[string]any{"action": "accept", "content": map[string]any{}, "_meta": nil}
		}
		return map[string]any{"action": "decline", "content": nil, "_meta": nil}
	}
	if prompt.method == "item/permissions/requestApproval" {
		switch choice {
		case "allowSession":
			return map[string]any{"permissions": prompt.requested, "scope": "session"}
		case "allowTurn":
			return map[string]any{"permissions": prompt.requested, "scope": "turn"}
		}
		if permissionAllowed(choice) {
			return map[string]any{"permissions": prompt.requested, "scope": "turn"}
		}
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	}
	switch choice {
	case "accept", "acceptForSession", "decline", "cancel":
		return map[string]any{"decision": choice}
	}
	if permissionAllowed(choice) {
		return map[string]any{"decision": "accept"}
	}
	return map[string]any{"decision": "decline"}
}

func codexInput(text string) []map[string]any {
	return []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
}

// codexSteer reads {"codex":{"delivery":"steer"|"queue"}} message metadata.
func codexSteer(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var value struct {
		Codex *struct {
			Delivery string `json:"delivery"`
		} `json:"codex"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return false, errors.New("Codex metadata is malformed")
	}
	if value.Codex == nil {
		return false, nil
	}
	switch value.Codex.Delivery {
	case "", "queue":
		return false, nil
	case "steer":
		return true, nil
	default:
		return false, errors.New("Codex metadata delivery is unsupported")
	}
}

func compactJSON(raw json.RawMessage) []byte {
	var buffer bytes.Buffer
	if json.Compact(&buffer, raw) != nil {
		return raw
	}
	return buffer.Bytes()
}

func codexCommand(config CodexConfig) []string {
	command := []string{config.Command[0], "app-server", "--listen", "stdio://"}
	command = append(command, config.Command[1:]...)
	for _, server := range config.MCPServers {
		command = append(command,
			"-c", "mcp_servers."+server.Name+".command="+tomlString(server.Command),
			"-c", "mcp_servers."+server.Name+".args="+tomlStrings(server.Arguments),
		)
	}
	return command
}

func tomlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func tomlStrings(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, tomlString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func validateCodexConfig(config CodexConfig) error {
	if len(config.Command) == 0 {
		return errors.New("Codex executable is required")
	}
	for _, argument := range config.Command[1:] {
		name, _, _ := strings.Cut(argument, "=")
		switch name {
		case "app-server", "exec", "e", "--listen", "--stdio", "--ws-auth", "--ws-token-file",
			"--ws-token-sha256", "--ws-shared-secret-file", "--remote", "resume", "proxy", "daemon":
			return fmt.Errorf("Codex argument %q must be configured by the adapter", argument)
		}
	}
	switch config.ApprovalPolicy {
	case "", "untrusted", "on-request", "never":
	default:
		return errors.New("Codex approval policy must be untrusted, on-request, or never")
	}
	switch config.Sandbox {
	case "", "read-only", "workspace-write", "danger-full-access":
	default:
		return errors.New("Codex sandbox must be read-only, workspace-write, or danger-full-access")
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

func codexResumeID(raw string) (string, error) {
	var state struct {
		Driver   string `json:"driver"`
		ThreadID string `json:"threadId"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Driver != "codex-app-server" ||
		!codexThreadID.MatchString(state.ThreadID) {
		return "", errors.New("Codex resume state is invalid")
	}
	return state.ThreadID, nil
}
