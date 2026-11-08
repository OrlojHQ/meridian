package harnessadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

const claudeFixtureSession = "0be09774-1259-4747-b6b0-81cc54dda2e4"

func runClaudeFixture(
	t *testing.T,
	config ClaudeConfig,
	initial adapterproto.Kind,
	resume string,
) (*adapterproto.Decoder, *adapterproto.Encoder, string, chan error) {
	t.Helper()
	return runClaudeFixtureWith(t, config, initial, resume, false)
}

func runClaudeFixtureWith(
	t *testing.T,
	config ClaudeConfig,
	initial adapterproto.Kind,
	resume string,
	models bool,
) (*adapterproto.Decoder, *adapterproto.Encoder, string, chan error) {
	t.Helper()
	arguments := filepath.Join(t.TempDir(), "arguments.json")
	t.Setenv("MERIDIAN_CLAUDE_HELPER", "1")
	t.Setenv("MERIDIAN_CLAUDE_HELPER_ARGUMENTS", arguments)
	config.Command = append([]string{os.Args[0], "-test.run=^TestClaudeStreamHelper$", "--"}, config.Command...)
	controller, adapter := net.Pipe()
	t.Cleanup(func() { _ = controller.Close() })
	done := make(chan error, 1)
	go func() {
		done <- RunClaudeStream(context.Background(), adapter, adapter, config)
	}()
	decoder, encoder := negotiateTestAdapterWith(t, controller, initial, resume, models)
	return decoder, encoder, arguments, done
}

func TestClaudeStreamReportsAndSwitchesModels(t *testing.T) {
	decoder, encoder, _, _ := runClaudeFixtureWith(t, ClaudeConfig{Permissions: true}, adapterproto.KindStart, "", true)
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	models := readKind(t, decoder, adapterproto.KindModels).Models
	if models.Current != "default" || len(models.Options) != 3 ||
		models.Options[0].Label != "Default (recommended)" || models.Options[1].ID != "sonnet" ||
		models.Options[2].Description != "Locked for this account" {
		t.Fatalf("models = %#v", models)
	}
	sendSetModel(t, encoder, "switch-1", "gpt-6")
	if failure := readKind(t, decoder, adapterproto.KindError); failure.Code != "model_unavailable" {
		t.Fatalf("unknown model = %#v", failure)
	}
	sendSetModel(t, encoder, "switch-2", "sonnet")
	if switched := readKind(t, decoder, adapterproto.KindModels).Models; switched.Current != "sonnet" {
		t.Fatalf("switched = %#v", switched)
	}
	sendSetModel(t, encoder, "switch-3", "opus-locked")
	failure := readKind(t, decoder, adapterproto.KindError)
	if failure.Code != "model_unavailable" || !strings.Contains(failure.Summary, "not available") {
		t.Fatalf("refused switch = %#v", failure)
	}
}

func sendTestMessage(t *testing.T, encoder *adapterproto.Encoder, id, content string) {
	t.Helper()
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: id, SessionID: "session-1", Role: adapterproto.RoleUser, Content: content,
	})
}

func TestClaudeStreamContract(t *testing.T) {
	decoder, encoder, argumentsPath, done := runClaudeFixture(t, ClaudeConfig{
		Command:     []string{"--model", "sonnet"},
		Permissions: true,
		MCPServers: []MCPServer{{
			Name: "meridian-coord", Command: "/usr/local/bin/meridian-coord", Arguments: []string{"mcp"},
		}},
	}, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-1", "hello")
	frames := readUntil(t, decoder, adapterproto.KindAssistantMessage)
	assertKinds(t, frames, adapterproto.KindStatus, adapterproto.KindAssistantDelta)
	final := frames[len(frames)-1]
	if final.Content != "hello" || final.MessageID != "msg_fixture_1-1" {
		t.Fatalf("final message = %#v", final)
	}
	for _, frame := range frames {
		if frame.Type == adapterproto.KindAssistantDelta && frame.MessageID != final.MessageID {
			t.Fatalf("delta and final message ids differ: %#v", frame)
		}
	}
	idle := readStatus(t, decoder, adapterproto.StatusIdle)
	wantResume := `{"driver":"claude-stream","sessionId":"` + claudeFixtureSession + `"}`
	if idle.ResumeState != wantResume {
		t.Fatalf("resume state = %q", idle.ResumeState)
	}

	sendTestMessage(t, encoder, "message-2", "tool")
	frames = readUntil(t, decoder, adapterproto.KindPermissionRequest)
	assertKinds(t, frames, adapterproto.KindToolStart)
	request := frames[len(frames)-1]
	if request.Permission.Kind != "Bash" || !strings.Contains(request.Permission.Summary, "echo meridian") {
		t.Fatalf("permission request = %#v", request)
	}
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "permission-response-1", ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: "Bash", Choice: "allow"},
	})
	result := readKind(t, decoder, adapterproto.KindToolResult)
	if result.ToolCallID != "toolu_fixture" || result.Result != "allowed: echo meridian" {
		t.Fatalf("tool result = %#v", result)
	}
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != "tool finished" {
		t.Fatalf("tool turn final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-3", "ask")
	first := readKind(t, decoder, adapterproto.KindInputRequest)
	second := readKind(t, decoder, adapterproto.KindInputRequest)
	if !strings.Contains(first.Input.Prompt, "Which color?") || !strings.Contains(first.Input.Prompt, "- red (warm)") {
		t.Fatalf("first question = %#v", first)
	}
	for _, answer := range []struct{ request, value string }{{second.ID, "fast"}, {first.ID, "red"}} {
		writeTestFrame(t, encoder, adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindInputResponse,
			ID: "input-response-" + answer.value, ResponseTo: answer.request,
			Input: &adapterproto.Input{Value: answer.value},
		})
	}
	// Claude reported the answer only in its result record, so the adapter
	// must synthesize the turn's final assistant message.
	final = readKind(t, decoder, adapterproto.KindAssistantMessage)
	if final.Content != `answered {"Speed?":"fast","Which color?":"red"}` {
		t.Fatalf("synthesized final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-4", "subagent")
	frames = readUntil(t, decoder, adapterproto.KindAssistantMessage)
	for _, frame := range frames {
		if strings.Contains(frame.Content, "subagent internals") {
			t.Fatalf("subagent record leaked: %#v", frame)
		}
	}
	if frames[len(frames)-1].Content != "subagent done" {
		t.Fatalf("subagent final = %#v", frames[len(frames)-1])
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-5", "withdraw")
	withdrawn := readKind(t, decoder, adapterproto.KindPermissionRequest)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "late-response", ResponseTo: withdrawn.ID,
		Permission: &adapterproto.Permission{Kind: "Bash", Choice: "allow"},
	})

	sendTestMessage(t, encoder, "message-6", "fail")
	failure := readKind(t, decoder, adapterproto.KindError)
	if failure.Code != "claude_turn_failed" || failure.Summary != "Not logged in · Please run /login" {
		t.Fatalf("failure = %#v", failure)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	if end := readKind(t, decoder, adapterproto.KindEnd); end.Status != adapterproto.StatusCancelled {
		t.Fatalf("end = %#v", end)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	arguments := readHelperArguments(t, argumentsPath)
	for _, want := range []string{
		"--model sonnet", "--print", "--input-format stream-json", "--output-format stream-json",
		"--verbose", "--include-partial-messages", "--permission-prompt-tool stdio",
	} {
		if !strings.Contains(strings.Join(arguments, " "), want) {
			t.Fatalf("arguments %q do not contain %q", arguments, want)
		}
	}
	config := argumentAfter(t, arguments, "--mcp-config")
	var mcp struct {
		Servers map[string]struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(config), &mcp); err != nil {
		t.Fatal(err)
	}
	server := mcp.Servers["meridian-coord"]
	if server.Type != "stdio" || server.Command != "/usr/local/bin/meridian-coord" ||
		len(server.Args) != 1 || server.Args[0] != "mcp" {
		t.Fatalf("mcp config = %s", config)
	}
}

func TestClaudeStreamResumeAndDisabledPermissions(t *testing.T) {
	resume := `{"driver":"claude-stream","sessionId":"` + claudeFixtureSession + `"}`
	decoder, encoder, argumentsPath, done := runClaudeFixture(
		t, ClaudeConfig{}, adapterproto.KindResume, resume,
	)
	if idle := readStatus(t, decoder, adapterproto.StatusIdle); idle.ResumeState != resume {
		t.Fatalf("resumed idle = %#v", idle)
	}
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	_ = readKind(t, decoder, adapterproto.KindEnd)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	arguments := readHelperArguments(t, argumentsPath)
	if argumentAfter(t, arguments, "--resume") != claudeFixtureSession ||
		argumentAfter(t, arguments, "--permission-prompts") != "none" {
		t.Fatalf("arguments = %q", arguments)
	}
}

func TestClaudeStreamMissingResumeSessionFailsClosed(t *testing.T) {
	resume := `{"driver":"claude-stream","sessionId":"11111111-2222-4333-8444-555555555555"}`
	decoder, _, _, done := runClaudeFixture(t, ClaudeConfig{}, adapterproto.KindResume, resume)
	failure := readKind(t, decoder, adapterproto.KindError)
	if failure.Code != "claude_resume_failed" {
		t.Fatalf("failure = %#v", failure)
	}
	if end := readKind(t, decoder, adapterproto.KindEnd); end.Status != adapterproto.StatusFailed {
		t.Fatalf("end = %#v", end)
	}
	if err := <-done; err == nil {
		t.Fatal("missing resume session was accepted")
	}
}

func TestClaudeStreamRejectsInvalidResumeAndOwnedArguments(t *testing.T) {
	for _, state := range []string{
		`{"driver":"claude-stream","sessionId":"../../etc"}`,
		`{"driver":"pi-rpc","sessionId":"` + claudeFixtureSession + `"}`,
		`{"driver":"claude-stream","sessionId":"` + claudeFixtureSession + `","extra":true}`,
	} {
		if _, err := claudeResumeID(state); err == nil {
			t.Fatalf("resume state %s was accepted", state)
		}
	}
	for _, argument := range []string{"--resume=x", "-p", "--output-format", "--mcp-config", "--permission-prompt-tool"} {
		if err := validateClaudeConfig(ClaudeConfig{Command: []string{"claude", argument}}); err == nil {
			t.Fatalf("argument %q was accepted", argument)
		}
	}
	duplicate := []MCPServer{{Name: "a", Command: "/bin/a"}, {Name: "a", Command: "/bin/b"}}
	if err := validateClaudeConfig(ClaudeConfig{Command: []string{"claude"}, MCPServers: duplicate}); err == nil {
		t.Fatal("duplicate MCP servers were accepted")
	}
}

func TestParseMCPServer(t *testing.T) {
	server, err := ParseMCPServer("meridian-coord=/usr/local/bin/meridian-coord,mcp,--stdio")
	if err != nil || server.Name != "meridian-coord" || server.Command != "/usr/local/bin/meridian-coord" ||
		strings.Join(server.Arguments, " ") != "mcp --stdio" {
		t.Fatalf("server = %#v, %v", server, err)
	}
	for _, value := range []string{
		"", "coord", "coord=relative", "bad.name=/bin/x", "coord=/bin/x,a\"b", "coord=/bin/x,a\nb",
		strings.Repeat("a", 65) + "=/bin/x",
	} {
		if _, err := ParseMCPServer(value); err == nil {
			t.Fatalf("%q was accepted", value)
		}
	}
}

func readHelperArguments(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var arguments []string
	if err := json.Unmarshal(raw, &arguments); err != nil {
		t.Fatal(err)
	}
	return arguments
}

func argumentAfter(t *testing.T, arguments []string, name string) string {
	t.Helper()
	for index, argument := range arguments {
		if argument == name && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	t.Fatalf("arguments %q do not contain %s", arguments, name)
	return ""
}

func helperArguments() []string {
	for index, argument := range os.Args {
		if argument == "--" {
			return os.Args[index+1:]
		}
	}
	return nil
}

// claudeFixtureCommands is a subset of the initialize response of Claude Code
// 2.1.283 for a workspace with .claude/commands/ship.md.
var claudeFixtureCommands = []any{
	map[string]any{"name": "ship", "description": "Ship the current branch (project)", "argumentHint": "target"},
	map[string]any{"name": "compact", "description": "Free up context by summarizing the conversation so far",
		"argumentHint": "<optional custom summarization instructions>", "builtin": true},
	map[string]any{"name": "usage", "description": "Show session cost, plan usage, and what's contributing to your limits",
		"argumentHint": "", "aliases": []any{"cost", "stats"}, "builtin": true},
	map[string]any{"name": "doctor", "description": "Health-check the user's Claude Code setup", "argumentHint": "", "builtin": true},
	map[string]any{"name": "claude-api", "description": "Reference for the Claude API.\nTRIGGER when: " +
		strings.Repeat("code imports anthropic; ", 40), "argumentHint": ""},
	map[string]any{"name": "__remote-workflow", "description": "server-launched sessions only", "argumentHint": "", "builtin": true},
}

func TestClaudeStreamCommands(t *testing.T) {
	decoder, _, _, _ := runClaudeFixture(t, ClaudeConfig{Permissions: true}, adapterproto.KindStart, "")
	initial := readKind(t, decoder, adapterproto.KindCommands)
	names := func(commands []adapterproto.Command) string {
		var result []string
		for _, command := range commands {
			result = append(result, command.Name)
		}
		return strings.Join(result, " ")
	}
	if got := names(initial.Commands); got != "ship compact usage doctor claude-api" {
		t.Fatalf("initialize commands = %q", got)
	}
	for _, command := range initial.Commands {
		if command.Kind != adapterproto.CommandPrompt {
			t.Fatalf("command kind = %#v", command)
		}
	}
	if usage := initial.Commands[2]; strings.Join(usage.Aliases, ",") != "cost,stats" {
		t.Fatalf("usage aliases = %#v", usage)
	}
	api := initial.Commands[4].Description
	if len(api) > adapterproto.MaxCommandDescriptionBytes || strings.ContainsAny(api, "\n\t") ||
		!strings.HasPrefix(api, "Reference for the Claude API. TRIGGER when:") || !strings.HasSuffix(api, "…") {
		t.Fatalf("folded description = %q", api)
	}
	frames := readUntil(t, decoder, adapterproto.KindCommands)
	assertKinds(t, frames, adapterproto.KindStatus, adapterproto.KindCommands)
	if got := names(frames[1].Commands); got != "ship compact usage claude-api" {
		t.Fatalf("headless commands = %q", got)
	}
}

// TestClaudeStreamHelper replays record shapes captured from Claude Code
// 2.1.283 in --input-format/--output-format stream-json mode.
func TestClaudeStreamHelper(t *testing.T) {
	if os.Getenv("MERIDIAN_CLAUDE_HELPER") != "1" {
		return
	}
	encoded, _ := json.Marshal(helperArguments())
	if os.WriteFile(os.Getenv("MERIDIAN_CLAUDE_HELPER_ARGUMENTS"), encoded, 0o600) != nil {
		os.Exit(2)
	}
	missing := strings.Contains(string(encoded), "11111111-2222-4333-8444-555555555555")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	writer := json.NewEncoder(os.Stdout)
	emit := func(record map[string]any) {
		if _, exists := record["session_id"]; !exists {
			record["session_id"] = claudeFixtureSession
		}
		if _, exists := record["parent_tool_use_id"]; !exists && record["type"] != "control_request" &&
			record["type"] != "control_response" && record["type"] != "control_cancel_request" {
			record["parent_tool_use_id"] = nil
		}
		_ = writer.Encode(record)
	}
	result := func(text string, failed bool) {
		emit(map[string]any{
			"type": "result", "subtype": "success", "is_error": failed, "result": text,
			"duration_ms": 10, "num_turns": 1, "total_cost_usd": 0,
		})
	}
	assistant := func(id string, content ...any) {
		emit(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"id": id, "type": "message", "role": "assistant", "model": "claude-fixture", "content": content,
			},
		})
	}
	text := func(value string) map[string]any { return map[string]any{"type": "text", "text": value} }
	var pendingTool, pendingAsk string
	var askInput map[string]any
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			os.Exit(3)
		}
		switch record["type"] {
		case "control_request":
			request, _ := record["request"].(map[string]any)
			if missing {
				emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true})
				os.Exit(1)
			}
			body := map[string]any{}
			if request["subtype"] == "initialize" {
				body = map[string]any{"commands": claudeFixtureCommands, "models": []any{
					map[string]any{"value": "default", "displayName": "Default (recommended)", "description": "Opus"},
					map[string]any{"value": "sonnet", "displayName": "Sonnet"},
					map[string]any{"value": "opus-locked", "displayName": "Opus", "description": "Locked for\nthis account"},
				}}
			}
			if request["subtype"] == "set_model" && request["model"] == "opus-locked" {
				emit(map[string]any{"type": "control_response", "response": map[string]any{
					"subtype": "error", "request_id": record["request_id"], "error": "Model not available",
				}})
				continue
			}
			emit(map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype": "success", "request_id": record["request_id"], "response": body,
				},
			})
			if request["subtype"] == "initialize" {
				emit(map[string]any{
					"type": "system", "subtype": "init", "tools": []any{"Bash"},
					"slash_commands":          []any{"ship", "compact", "usage", "doctor", "claude-api", "__remote-workflow"},
					"terminal_slash_commands": []any{"doctor", "color"},
				})
			}
			if request["subtype"] == "interrupt" {
				os.Exit(0)
			}
		case "control_response":
			response, _ := record["response"].(map[string]any)
			body, _ := response["response"].(map[string]any)
			switch response["request_id"] {
			case pendingTool:
				pendingTool = ""
				input, _ := body["updatedInput"].(map[string]any)
				output := "denied"
				if body["behavior"] == "allow" {
					output = "allowed: " + input["command"].(string)
				}
				emit(map[string]any{
					"type": "user",
					"message": map[string]any{"role": "user", "content": []any{map[string]any{
						"type": "tool_result", "tool_use_id": "toolu_fixture", "content": output,
					}}},
				})
				assistant("msg_fixture_3", text("tool finished"))
				result("tool finished", false)
			case pendingAsk:
				pendingAsk = ""
				input, _ := body["updatedInput"].(map[string]any)
				if input["questions"] == nil || body["behavior"] != "allow" {
					os.Exit(5)
				}
				answers, _ := json.Marshal(input["answers"])
				result("answered "+string(answers), false)
			}
		case "user":
			message, _ := record["message"].(map[string]any)
			prompt, _ := message["content"].(string)
			emit(map[string]any{"type": "system", "subtype": "status", "status": "requesting"})
			switch prompt {
			case "hello":
				emit(map[string]any{"type": "stream_event", "event": map[string]any{
					"type": "message_start", "message": map[string]any{"id": "msg_fixture_1"},
				}})
				emit(map[string]any{"type": "stream_event", "event": map[string]any{
					"type": "content_block_start", "index": 0, "content_block": text(""),
				}})
				for _, part := range []string{"hel", "lo"} {
					emit(map[string]any{"type": "stream_event", "event": map[string]any{
						"type": "content_block_delta", "index": 0,
						"delta": map[string]any{"type": "text_delta", "text": part},
					}})
				}
				assistant("msg_fixture_1", text("hello"))
				emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "message_stop"}})
				result("hello", false)
			case "tool":
				input := map[string]any{"command": "echo meridian", "description": "print"}
				assistant("msg_fixture_2", map[string]any{
					"type": "tool_use", "id": "toolu_fixture", "name": "Bash", "input": input,
				})
				pendingTool = "tool-request"
				emit(map[string]any{
					"type": "control_request", "request_id": pendingTool,
					"request": map[string]any{
						"subtype": "can_use_tool", "tool_name": "Bash", "display_name": "Bash",
						"input": input, "description": "print", "tool_use_id": "toolu_fixture",
					},
				})
			case "ask":
				askInput = map[string]any{"questions": []any{
					map[string]any{
						"question": "Which color?", "header": "Color", "multiSelect": false,
						"options": []any{
							map[string]any{"label": "red", "description": "warm"},
							map[string]any{"label": "blue", "description": "cool"},
						},
					},
					map[string]any{"question": "Speed?", "header": "Speed", "options": []any{}},
				}}
				pendingAsk = "ask-request"
				emit(map[string]any{
					"type": "control_request", "request_id": pendingAsk,
					"request": map[string]any{
						"subtype": "can_use_tool", "tool_name": "AskUserQuestion",
						"input": askInput, "tool_use_id": "toolu_ask",
					},
				})
			case "subagent":
				emit(map[string]any{
					"type": "assistant", "parent_tool_use_id": "toolu_task",
					"message": map[string]any{"id": "msg_sub", "role": "assistant",
						"content": []any{text("subagent internals")}},
				})
				result("subagent done", false)
			case "withdraw":
				emit(map[string]any{
					"type": "control_request", "request_id": "withdrawn-request",
					"request": map[string]any{
						"subtype": "can_use_tool", "tool_name": "Bash",
						"input": map[string]any{"command": "sleep 100"}, "tool_use_id": "toolu_withdrawn",
					},
				})
				emit(map[string]any{"type": "control_cancel_request", "request_id": "withdrawn-request"})
				result("", false)
			case "fail":
				assistant("msg_fixture_fail", text("Not logged in · Please run /login"))
				result("Not logged in · Please run /login", true)
			default:
				os.Exit(4)
			}
		default:
			os.Exit(6)
		}
	}
	os.Exit(0)
}
