package harnessadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// PiConfig configures the documented Pi --mode rpc subprocess protocol.
type PiConfig struct {
	Command     []string
	SessionName string
	SessionDir  string
}

type piRecord struct {
	Type                  string          `json:"type"`
	ID                    string          `json:"id,omitempty"`
	Command               string          `json:"command,omitempty"`
	Success               *bool           `json:"success,omitempty"`
	Error                 string          `json:"error,omitempty"`
	Data                  json.RawMessage `json:"data,omitempty"`
	Message               json.RawMessage `json:"message,omitempty"`
	AssistantMessageEvent *struct {
		Type       string          `json:"type"`
		Delta      string          `json:"delta,omitempty"`
		ID         string          `json:"id,omitempty"`
		ToolName   string          `json:"toolName,omitempty"`
		ToolCall   json.RawMessage `json:"toolCall,omitempty"`
		ContentIdx int             `json:"contentIndex,omitempty"`
	} `json:"assistantMessageEvent,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	ToolName      string          `json:"toolName,omitempty"`
	Args          json.RawMessage `json:"args,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	PartialResult json.RawMessage `json:"partialResult,omitempty"`
	Method        string          `json:"method,omitempty"`
	Title         string          `json:"title,omitempty"`
	Options       []string        `json:"options,omitempty"`
	Placeholder   string          `json:"placeholder,omitempty"`
	Prefill       string          `json:"prefill,omitempty"`
	Confirmed     bool            `json:"confirmed,omitempty"`
}

type piPendingUI struct {
	method string
}

// RunPiRPC maps Pi's strict LF-delimited RPC protocol to Meridian frames.
func RunPiRPC(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	config PiConfig,
) (runErr error) {
	if err := validatePiConfig(config); err != nil {
		return err
	}
	stream := NewStream(input, output)
	if err := stream.Handshake(adapterproto.Capabilities{
		Resume: true, ToolEvents: true, Permissions: true,
		InputRequests: true, Heartbeat: false, OpaqueMetadata: true,
	}); err != nil {
		return err
	}
	initial, err := stream.Read()
	if err != nil {
		return err
	}
	if initial.Type != adapterproto.KindStart && initial.Type != adapterproto.KindResume {
		return errors.New("Pi adapter requires start or resume")
	}

	command := append([]string(nil), config.Command...)
	command = append(command, "--mode", "rpc")
	if config.SessionName != "" {
		command = append(command, "--name", config.SessionName)
	}
	if config.SessionDir != "" {
		command = append(command, "--session-dir", config.SessionDir)
	}
	child, err := startProcess(processConfig{Command: command})
	if err != nil {
		return err
	}
	defer child.Close()
	rpc := NewJSONLines(child.stdout, child.stdin)

	piRecords := make(chan piRecord)
	piErrors := make(chan error, 1)
	go func() {
		defer close(piRecords)
		for {
			var record piRecord
			if readErr := rpc.Read(&record); readErr != nil {
				piErrors <- readErr
				return
			}
			if record.Type == "" {
				piErrors <- errors.New("Pi record type is missing")
				return
			}
			select {
			case piRecords <- record:
			case <-ctx.Done():
				return
			}
		}
	}()
	parentFrames := make(chan adapterproto.Frame)
	parentErrors := make(chan error, 1)
	go func() {
		defer close(parentFrames)
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

	_ = stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusStarting,
	})
	pending := map[string]string{}
	if initial.Type == adapterproto.KindResume {
		sessionPath, decodeErr := piResumePath(initial.ResumeState, config.SessionDir)
		if decodeErr != nil {
			stream.Fail("pi_resume_invalid")
			return decodeErr
		}
		pending["pi-resume"] = "switch_session"
		if err := rpc.Write(map[string]any{
			"id": "pi-resume", "type": "switch_session", "sessionPath": sessionPath,
		}); err != nil {
			stream.Fail("pi_write_failed")
			return err
		}
	}
	pending["pi-state-start"] = "get_state"
	if err := rpc.Write(map[string]any{"id": "pi-state-start", "type": "get_state"}); err != nil {
		stream.Fail("pi_write_failed")
		return err
	}
	startupTimer := time.NewTimer(startupTimeout)
	defer startupTimer.Stop()
	startupDeadline := startupTimer.C

	messageSequence := 0
	currentMessage := ""
	pendingUI := map[string]piPendingUI{}
	for {
		select {
		case <-startupDeadline:
			stream.Fail("pi_startup_timeout")
			return errors.New("Pi RPC startup timed out")
		case <-ctx.Done():
			_ = rpc.Write(map[string]any{"id": "pi-context-abort", "type": "abort"})
			return ctx.Err()
		case err := <-parentErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case err := <-piErrors:
			stream.Fail("pi_protocol_failed")
			return fmt.Errorf("Pi RPC stream failed: %w", err)
		case frame := <-parentFrames:
			if frame.Type == "" {
				return nil
			}
			switch frame.Type {
			case adapterproto.KindUserMessage:
				commandType, decodeErr := piPromptType(frame.Metadata)
				if decodeErr != nil {
					stream.Fail("pi_metadata_invalid")
					return decodeErr
				}
				id := "pi-message-" + frame.ID
				pending[id] = commandType
				command := map[string]any{
					"id": id, "type": commandType, "message": frame.Content,
				}
				if err := rpc.Write(command); err != nil {
					stream.Fail("pi_write_failed")
					return err
				}
			case adapterproto.KindPermissionResponse:
				ui, exists := pendingUI[frame.ResponseTo]
				if !exists || frame.Permission == nil {
					stream.Fail("pi_response_invalid")
					return errors.New("unexpected Pi permission response")
				}
				delete(pendingUI, frame.ResponseTo)
				response := map[string]any{
					"type": "extension_ui_response", "id": frame.ResponseTo,
				}
				if ui.method == "confirm" {
					choice := strings.ToLower(frame.Permission.Choice)
					response["confirmed"] = choice == "allow" || choice == "yes" ||
						choice == "true" || choice == "confirm"
				} else {
					response["value"] = frame.Permission.Choice
				}
				if err := rpc.Write(response); err != nil {
					stream.Fail("pi_write_failed")
					return err
				}
			case adapterproto.KindInputResponse:
				ui, exists := pendingUI[frame.ResponseTo]
				if !exists || (ui.method != "input" && ui.method != "editor") ||
					frame.Input == nil {
					stream.Fail("pi_response_invalid")
					return errors.New("unexpected Pi input response")
				}
				delete(pendingUI, frame.ResponseTo)
				if err := rpc.Write(map[string]any{
					"type": "extension_ui_response", "id": frame.ResponseTo,
					"value": frame.Input.Value,
				}); err != nil {
					stream.Fail("pi_write_failed")
					return err
				}
			case adapterproto.KindCancel:
				pending["pi-abort"] = "abort"
				if err := rpc.Write(map[string]any{"id": "pi-abort", "type": "abort"}); err != nil {
					return err
				}
				_ = stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
					Status: adapterproto.StatusCancelled,
				})
				return nil
			case adapterproto.KindHeartbeat, adapterproto.KindAck:
			default:
				stream.Fail("pi_controller_frame_invalid")
				return errors.New("unsupported Pi controller frame")
			}
		case record := <-piRecords:
			if record.Type == "" {
				stream.Fail("pi_process_exited")
				return errors.New("Pi process exited unexpectedly")
			}
			switch record.Type {
			case "response":
				expected, exists := pending[record.ID]
				if !exists || record.Success == nil || record.Command != expected {
					stream.Fail("pi_response_invalid")
					return errors.New("Pi response correlation failed")
				}
				delete(pending, record.ID)
				if !*record.Success {
					_ = stream.Write(adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindError,
						Code: "pi_command_failed", Summary: bounded(record.Error, adapterproto.MaxToolSummaryBytes),
					})
					continue
				}
				if record.Command == "get_state" {
					resumeState, stateErr := piState(record.Data)
					if stateErr != nil {
						stream.Fail("pi_state_invalid")
						return stateErr
					}
					if err := stream.Write(adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
						Status: adapterproto.StatusIdle, ResumeState: resumeState,
					}); err != nil {
						return err
					}
					if record.ID == "pi-state-start" {
						if !startupTimer.Stop() {
							select {
							case <-startupTimer.C:
							default:
							}
						}
						startupDeadline = nil
					}
				}
			case "agent_start":
				if err := stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
					Status: adapterproto.StatusRunning,
				}); err != nil {
					return err
				}
			case "agent_settled":
				id := "pi-state-settled-" + jsonNumber(messageSequence)
				pending[id] = "get_state"
				if err := rpc.Write(map[string]any{"id": id, "type": "get_state"}); err != nil {
					return err
				}
			case "message_start":
				if piMessageRole(record.Message) == "assistant" {
					messageSequence++
					currentMessage = "pi-message-" + jsonNumber(messageSequence)
				}
			case "message_update":
				event := record.AssistantMessageEvent
				if event == nil {
					stream.Fail("pi_event_invalid")
					return errors.New("Pi message update is missing its delta")
				}
				if event.Type == "text_delta" && event.Delta != "" {
					if currentMessage == "" {
						messageSequence++
						currentMessage = "pi-message-" + jsonNumber(messageSequence)
					}
					if err := stream.Write(adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindAssistantDelta,
						Role: adapterproto.RoleAssistant, MessageID: currentMessage,
						Content: bounded(event.Delta, adapterproto.MaxContentBytes),
					}); err != nil {
						return err
					}
				}
			case "message_end":
				if piMessageRole(record.Message) != "assistant" {
					continue
				}
				content, contentErr := piMessageText(record.Message)
				if contentErr != nil {
					stream.Fail("pi_message_invalid")
					return contentErr
				}
				if currentMessage == "" {
					messageSequence++
					currentMessage = "pi-message-" + jsonNumber(messageSequence)
				}
				if content != "" {
					if err := stream.Write(adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
						Role: adapterproto.RoleAssistant, MessageID: currentMessage,
						Content: bounded(content, adapterproto.MaxContentBytes),
					}); err != nil {
						return err
					}
				}
				currentMessage = ""
			case "tool_execution_start":
				if record.ToolCallID == "" || record.ToolName == "" {
					stream.Fail("pi_tool_invalid")
					return errors.New("Pi tool start is malformed")
				}
				if err := stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindToolStart,
					Role: adapterproto.RoleAssistant, ToolCallID: safeID(record.ToolCallID, "pi-tool"),
					ToolName: bounded(record.ToolName, adapterproto.MaxToolNameBytes),
					Summary:  bounded(string(record.Args), adapterproto.MaxToolSummaryBytes),
				}); err != nil {
					return err
				}
			case "tool_execution_end":
				if record.ToolCallID == "" {
					stream.Fail("pi_tool_invalid")
					return errors.New("Pi tool result is malformed")
				}
				result := bounded(string(record.Result), adapterproto.MaxResultBytes)
				if result == "" || result == "null" {
					result = "{}"
				}
				if err := stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindToolResult,
					Role: adapterproto.RoleTool, ToolCallID: safeID(record.ToolCallID, "pi-tool"),
					Result: result,
				}); err != nil {
					return err
				}
			case "extension_ui_request":
				if err := handlePiUI(stream, record, pendingUI); err != nil {
					stream.Fail("pi_ui_invalid")
					return err
				}
			case "agent_end", "turn_start", "turn_end", "tool_execution_update",
				"queue_update", "compaction_start", "compaction_end",
				"auto_retry_start", "auto_retry_end", "summarization_retry_scheduled",
				"summarization_retry_attempt_start", "summarization_retry_finished",
				"bash_execution_update":
				// These documented events have no lossless Meridian v1 equivalent.
			case "extension_error":
				_ = stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindError,
					Code: "pi_extension_error", Summary: "Pi extension failed",
				})
			default:
				stream.Fail("pi_event_incompatible")
				return fmt.Errorf("unsupported Pi RPC event type %q", record.Type)
			}
		}
	}
}

func validatePiConfig(config PiConfig) error {
	if len(config.Command) == 0 {
		return errors.New("Pi executable is required")
	}
	for _, argument := range config.Command[1:] {
		switch {
		case argument == "--mode", argument == "--rpc", argument == "--print",
			argument == "-p", argument == "--name", argument == "-n",
			argument == "--session-dir", argument == "--no-session",
			strings.HasPrefix(argument, "--mode="),
			strings.HasPrefix(argument, "--name="),
			strings.HasPrefix(argument, "--session-dir="):
			return fmt.Errorf("Pi argument %q must be configured by the adapter", argument)
		}
	}
	if strings.ContainsAny(config.SessionName, "\x00\r\n") {
		return errors.New("Pi session name is invalid")
	}
	if strings.ContainsAny(config.SessionDir, "\x00\r\n") {
		return errors.New("Pi session directory is invalid")
	}
	return nil
}

func piPromptType(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "prompt", nil
	}
	var value struct {
		Pi *struct {
			Command string `json:"command"`
		} `json:"pi"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("Pi metadata is malformed")
	}
	if value.Pi == nil || value.Pi.Command == "" {
		return "prompt", nil
	}
	switch value.Pi.Command {
	case "prompt", "steer", "follow_up":
		return value.Pi.Command, nil
	default:
		return "", errors.New("Pi metadata command is unsupported")
	}
}

func piMessageRole(raw json.RawMessage) string {
	var value struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Role
}

func piMessageText(raw json.RawMessage) (string, error) {
	var value struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.Role != "assistant" {
		return "", errors.New("Pi assistant message is malformed")
	}
	switch content := value.Content.(type) {
	case string:
		return content, nil
	case []any:
		var result strings.Builder
		for _, item := range content {
			block, ok := item.(map[string]any)
			if !ok || block["type"] != "text" {
				continue
			}
			text, ok := block["text"].(string)
			if ok {
				result.WriteString(text)
			}
		}
		return result.String(), nil
	default:
		return "", errors.New("Pi assistant content is malformed")
	}
}

func handlePiUI(stream *Stream, record piRecord, pending map[string]piPendingUI) error {
	if record.ID == "" || record.Method == "" {
		return errors.New("Pi UI request is malformed")
	}
	id := safeID(record.ID, "")
	if id == "" {
		return errors.New("Pi UI request id is invalid")
	}
	switch record.Method {
	case "select":
		if len(record.Options) == 0 || len(record.Options) > adapterproto.MaxOptions {
			return errors.New("Pi select options are invalid")
		}
		pending[id] = piPendingUI{method: record.Method}
		return stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest, ID: id,
			Permission: &adapterproto.Permission{
				Kind: "select", Summary: bounded(record.Title, adapterproto.MaxToolSummaryBytes),
				Options: record.Options,
			},
		})
	case "confirm":
		pending[id] = piPendingUI{method: record.Method}
		summary := record.Title
		var message string
		if len(record.Message) != 0 && json.Unmarshal(record.Message, &message) == nil && message != "" {
			if summary != "" {
				summary += ": "
			}
			summary += message
		}
		return stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest, ID: id,
			Permission: &adapterproto.Permission{
				Kind: "confirm", Summary: bounded(summary, adapterproto.MaxToolSummaryBytes),
				Options: []string{"allow", "deny"},
			},
		})
	case "input", "editor":
		pending[id] = piPendingUI{method: record.Method}
		prompt := record.Title
		if prompt == "" {
			prompt = record.Placeholder
		}
		return stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindInputRequest, ID: id,
			Input: &adapterproto.Input{
				Prompt: bounded(prompt, adapterproto.MaxToolSummaryBytes),
			},
		})
	case "notify", "setStatus", "setWidget", "setTitle", "set_editor_text":
		return nil
	default:
		return errors.New("Pi UI method is incompatible")
	}
}

func piState(raw json.RawMessage) (string, error) {
	var state struct {
		SessionFile string `json:"sessionFile"`
		SessionID   string `json:"sessionId"`
		SessionName string `json:"sessionName,omitempty"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &state) != nil || state.SessionFile == "" {
		return "", errors.New("Pi state response is incompatible")
	}
	return boundedJSON(map[string]any{
		"driver": "pi-rpc", "sessionPath": state.SessionFile,
		"sessionId": state.SessionID, "sessionName": state.SessionName,
	}, adapterproto.MaxResumeStateBytes), nil
}

func piResumePath(raw, sessionDir string) (string, error) {
	var state struct {
		Driver      string `json:"driver"`
		SessionPath string `json:"sessionPath"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Driver != "pi-rpc" ||
		state.SessionPath == "" || strings.ContainsAny(state.SessionPath, "\x00\r\n") {
		return "", errors.New("Pi resume state is invalid")
	}
	if sessionDir != "" {
		base, err := filepath.Abs(sessionDir)
		if err != nil {
			return "", errors.New("Pi session directory is invalid")
		}
		target, err := filepath.Abs(state.SessionPath)
		if err != nil {
			return "", errors.New("Pi session path is invalid")
		}
		relative, err := filepath.Rel(base, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", errors.New("Pi resume path leaves the configured session directory")
		}
	}
	return state.SessionPath, nil
}
