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

const codexFixtureThread = "01a1b386-a696-7321-a344-5534f6afeaa8"

func runCodexFixture(
	t *testing.T,
	config CodexConfig,
	initial adapterproto.Kind,
	resume string,
) (*adapterproto.Decoder, *adapterproto.Encoder, string, chan error) {
	t.Helper()
	return runCodexFixtureWith(t, config, initial, resume, false)
}

func runCodexFixtureWith(
	t *testing.T,
	config CodexConfig,
	initial adapterproto.Kind,
	resume string,
	models bool,
) (*adapterproto.Decoder, *adapterproto.Encoder, string, chan error) {
	t.Helper()
	directory := t.TempDir()
	arguments := filepath.Join(directory, "arguments.json")
	// The adapter places its subcommand right after the executable, which
	// would end Go test flag parsing, so the helper is reached through a shim.
	shim := filepath.Join(directory, "codex")
	script := "#!/bin/sh\nexec '" + os.Args[0] + "' '-test.run=^TestCodexAppServerHelper$' -- \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MERIDIAN_CODEX_HELPER", "1")
	t.Setenv("MERIDIAN_CODEX_HELPER_ARGUMENTS", arguments)
	config.Command = append([]string{shim}, config.Command...)
	controller, adapter := net.Pipe()
	t.Cleanup(func() { _ = controller.Close() })
	done := make(chan error, 1)
	go func() {
		done <- RunCodexAppServer(context.Background(), adapter, adapter, config)
	}()
	decoder, encoder := negotiateTestAdapterWith(t, controller, initial, resume, models)
	return decoder, encoder, arguments, done
}

func TestCodexAppServerReportsAndSwitchesModels(t *testing.T) {
	decoder, encoder, argumentsPath, _ := runCodexFixtureWith(t, CodexConfig{
		ApprovalPolicy: "on-request", Sandbox: "workspace-write",
	}, adapterproto.KindStart, "", true)
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	models := readKind(t, decoder, adapterproto.KindModels).Models
	if models.Current != "gpt-fixture" || len(models.Options) != 2 ||
		models.Options[0].ID != "gpt-fixture" || models.Options[1].ID != "gpt-fast" ||
		models.Options[1].Label != "GPT Fast" {
		t.Fatalf("models = %#v", models)
	}
	sendSetModel(t, encoder, "switch-1", "gpt-secret")
	if failure := readKind(t, decoder, adapterproto.KindError); failure.Code != "model_unavailable" {
		t.Fatalf("hidden model = %#v", failure)
	}
	sendSetModel(t, encoder, "switch-2", "gpt-fast")
	if switched := readKind(t, decoder, adapterproto.KindModels).Models; switched.Current != "gpt-fast" {
		t.Fatalf("switched = %#v", switched)
	}
	sendTestMessage(t, encoder, "message-1", "hello")
	_ = readUntil(t, decoder, adapterproto.KindAssistantMessage)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	sendTestMessage(t, encoder, "message-2", "hello")
	_ = readUntil(t, decoder, adapterproto.KindAssistantMessage)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	turns := 0
	for _, entry := range readHelperArguments(t, argumentsPath) {
		if strings.HasPrefix(entry, "turn model=") {
			turns++
			if entry != "turn model=gpt-fast" || turns > 1 {
				t.Fatalf("turn model entries = %v", readHelperArguments(t, argumentsPath))
			}
		}
	}
	if turns != 1 {
		t.Fatalf("model sent on %d turns, want the first only", turns)
	}
}

func TestCodexAppServerListsAConfiguredModelOutsideItsCatalog(t *testing.T) {
	// A provider connection's default model is not in Codex's catalog; it
	// stays selectable so the operator can switch back to it.
	t.Setenv("MERIDIAN_CODEX_HELPER_MODEL", "glm-local")
	decoder, encoder, argumentsPath, _ := runCodexFixtureWith(t, CodexConfig{
		ApprovalPolicy: "on-request", Sandbox: "workspace-write",
	}, adapterproto.KindStart, "", true)
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	models := readKind(t, decoder, adapterproto.KindModels).Models
	if models.Current != "glm-local" || len(models.Options) != 3 ||
		models.Options[0].ID != "glm-local" || models.Options[0].Label != "glm-local" ||
		models.Options[1].ID != "gpt-fixture" {
		t.Fatalf("models = %#v", models)
	}
	sendSetModel(t, encoder, "switch-1", "gpt-fast")
	if switched := readKind(t, decoder, adapterproto.KindModels).Models; switched.Current != "gpt-fast" {
		t.Fatalf("switched = %#v", switched)
	}
	sendSetModel(t, encoder, "switch-2", "glm-local")
	if back := readKind(t, decoder, adapterproto.KindModels).Models; back.Current != "glm-local" {
		t.Fatalf("switched back = %#v", back)
	}
	sendTestMessage(t, encoder, "message-1", "hello")
	_ = readUntil(t, decoder, adapterproto.KindAssistantMessage)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	for _, entry := range readHelperArguments(t, argumentsPath) {
		if strings.HasPrefix(entry, "turn model=") {
			t.Fatalf("switching back to the opening model still sent %q", entry)
		}
	}
}

func TestCodexAppServerContract(t *testing.T) {
	decoder, encoder, argumentsPath, done := runCodexFixture(t, CodexConfig{
		Command:        []string{"-c", "model=\"gpt-fixture\""},
		ApprovalPolicy: "on-request", Sandbox: "workspace-write",
		MCPServers: []MCPServer{{
			Name: "meridian-coord", Command: "/usr/local/bin/meridian-coord", Arguments: []string{"mcp"},
		}},
	}, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	idle := readStatus(t, decoder, adapterproto.StatusIdle)
	wantResume := `{"driver":"codex-app-server","threadId":"` + codexFixtureThread + `"}`
	if idle.ResumeState != wantResume {
		t.Fatalf("resume state = %q", idle.ResumeState)
	}

	sendTestMessage(t, encoder, "message-1", "hello")
	frames := readUntil(t, decoder, adapterproto.KindAssistantMessage)
	assertKinds(t, frames, adapterproto.KindStatus, adapterproto.KindAssistantDelta)
	if final := frames[len(frames)-1]; final.Content != "hello" || final.MessageID != "msg_fixture" {
		t.Fatalf("final message = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-2", "tool")
	start := readKind(t, decoder, adapterproto.KindToolStart)
	if start.ToolName != "shell" || start.Summary != "echo meridian" {
		t.Fatalf("tool start = %#v", start)
	}
	request := readKind(t, decoder, adapterproto.KindPermissionRequest)
	if request.Permission.Kind != "command" || !strings.Contains(request.Permission.Summary, "echo meridian") {
		t.Fatalf("permission request = %#v", request)
	}
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "permission-response-1", ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: "command", Choice: "acceptForSession"},
	})
	result := readKind(t, decoder, adapterproto.KindToolResult)
	if result.ToolCallID != "call_fixture" || result.Result != "decision acceptForSession" {
		t.Fatalf("tool result = %#v", result)
	}
	_ = readKind(t, decoder, adapterproto.KindAssistantMessage)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-3", "mcp")
	request = readKind(t, decoder, adapterproto.KindPermissionRequest)
	if request.Permission.Kind != "mcp_tool_call" ||
		strings.Join(request.Permission.Options, ",") != "accept,acceptForSession,decline,cancel" {
		t.Fatalf("mcp approval = %#v", request)
	}
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "permission-response-2", ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: "mcp_tool_call", Choice: "allow"},
	})
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != "elicitation accept" {
		t.Fatalf("mcp final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-4", "input")
	first := readKind(t, decoder, adapterproto.KindInputRequest)
	second := readKind(t, decoder, adapterproto.KindInputRequest)
	if !second.Input.Secret || !strings.Contains(first.Input.Prompt, "- red (warm)") {
		t.Fatalf("questions = %#v %#v", first, second)
	}
	for _, answer := range []struct{ request, value string }{{first.ID, "red"}, {second.ID, "hunter2"}} {
		writeTestFrame(t, encoder, adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindInputResponse,
			ID: "input-" + answer.value, ResponseTo: answer.request,
			Input: &adapterproto.Input{Value: answer.value},
		})
	}
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != `{"color":{"answers":["red"]},"token":{"answers":["hunter2"]}}` {
		t.Fatalf("input final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	// A held turn queues a plain follow-up and accepts a steering message.
	sendTestMessage(t, encoder, "message-5", "hold")
	_ = readStatus(t, decoder, adapterproto.StatusRunning)
	sendTestMessage(t, encoder, "message-6", "hello")
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "message-7", SessionID: "session-1", Role: adapterproto.RoleUser,
		Content: "steered", Metadata: json.RawMessage(`{"codex":{"delivery":"steer"}}`),
	})
	frames = readUntil(t, decoder, adapterproto.KindAssistantMessage)
	if final := frames[len(frames)-1]; final.Content != "steered" {
		t.Fatalf("steered final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	_ = readStatus(t, decoder, adapterproto.StatusRunning)
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != "hello" {
		t.Fatalf("queued final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-8", "withdraw")
	withdrawn := readKind(t, decoder, adapterproto.KindPermissionRequest)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "late-response", ResponseTo: withdrawn.ID,
		Permission: &adapterproto.Permission{Kind: "command", Choice: "accept"},
	})

	sendTestMessage(t, encoder, "message-9", "fail")
	failure := readKind(t, decoder, adapterproto.KindError)
	if failure.Code != "codex_turn_failed" || failure.Summary != "unexpected status 401 Unauthorized" {
		t.Fatalf("failure = %#v", failure)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-10", "hold")
	_ = readStatus(t, decoder, adapterproto.StatusRunning)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	if end := readKind(t, decoder, adapterproto.KindEnd); end.Status != adapterproto.StatusCancelled {
		t.Fatalf("end = %#v", end)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	arguments := strings.Join(readHelperArguments(t, argumentsPath), " ")
	for _, want := range []string{
		"app-server --listen stdio:// -c model=\"gpt-fixture\"",
		`-c mcp_servers.meridian-coord.command="/usr/local/bin/meridian-coord"`,
		`-c mcp_servers.meridian-coord.args=["mcp"]`,
		"thread/start approvalPolicy=on-request sandbox=workspace-write",
	} {
		if !strings.Contains(arguments, want) {
			t.Fatalf("helper log %q does not contain %q", arguments, want)
		}
	}
}

func TestCodexAppServerResume(t *testing.T) {
	resume := `{"driver":"codex-app-server","threadId":"` + codexFixtureThread + `"}`
	decoder, encoder, argumentsPath, done := runCodexFixture(
		t, CodexConfig{ApprovalPolicy: "never"}, adapterproto.KindResume, resume,
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
	if log := strings.Join(readHelperArguments(t, argumentsPath), " "); !strings.Contains(
		log, "thread/resume "+codexFixtureThread+" approvalPolicy=never",
	) {
		t.Fatalf("helper log = %q", log)
	}
}

func TestCodexAppServerCommands(t *testing.T) {
	decoder, encoder, argumentsPath, done := runCodexFixture(t, CodexConfig{}, adapterproto.KindStart, "")
	listed := readKind(t, decoder, adapterproto.KindCommands)
	var names []string
	for _, command := range listed.Commands {
		names = append(names, command.Name+":"+string(command.Kind))
	}
	if strings.Join(names, " ") != "compact:action review:action ship:action" {
		t.Fatalf("commands = %#v", listed.Commands)
	}
	if listed.Commands[2].Description != "Ship the current branch with care" {
		t.Fatalf("skill description = %q", listed.Commands[2].Description)
	}

	sendTestMessage(t, encoder, "message-1", "/compact")
	if start := readKind(t, decoder, adapterproto.KindToolStart); start.ToolName != "compact" {
		t.Fatalf("compaction start = %#v", start)
	}
	_ = readKind(t, decoder, adapterproto.KindToolResult)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-2", "/review")
	frames := readUntil(t, decoder, adapterproto.KindAssistantMessage)
	if final := frames[len(frames)-1]; final.Content != "No findings." {
		t.Fatalf("review = %#v", final)
	}
	// The inner review turn completes first; only the outer turn ends ours.
	for _, frame := range frames {
		if frame.Type == adapterproto.KindStatus && frame.Status == adapterproto.StatusIdle {
			t.Fatalf("idle before the review finished: %#v", frames)
		}
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-3", "/review focus on locking")
	_ = readKind(t, decoder, adapterproto.KindAssistantMessage)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-4", "/ship now")
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != "$ship now" {
		t.Fatalf("skill turn = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	// Unknown commands are ordinary prompts.
	sendTestMessage(t, encoder, "message-5", "/hello")
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != "/hello" {
		t.Fatalf("plain prompt = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	_ = readKind(t, decoder, adapterproto.KindEnd)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	log := strings.Join(readHelperArguments(t, argumentsPath), "\n")
	for _, want := range []string{
		"compact " + codexFixtureThread,
		`review {"type":"uncommittedChanges"}`,
		`review {"instructions":"focus on locking","type":"custom"}`,
		"skill skill ship /workspace/.agents/skills/ship/SKILL.md",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("helper log %q does not contain %q", log, want)
		}
	}
}

func TestCodexAppServerRejectsInvalidConfiguration(t *testing.T) {
	for _, state := range []string{
		`{"driver":"codex-app-server","threadId":"x/../y"}`,
		`{"driver":"claude-stream","threadId":"` + codexFixtureThread + `"}`,
	} {
		if _, err := codexResumeID(state); err == nil {
			t.Fatalf("resume state %s was accepted", state)
		}
	}
	for _, config := range []CodexConfig{
		{Command: []string{"codex", "exec"}},
		{Command: []string{"codex", "--listen=ws://0.0.0.0:1"}},
		{Command: []string{"codex"}, ApprovalPolicy: "always"},
		{Command: []string{"codex"}, Sandbox: "none"},
		{Command: []string{"codex"}, MCPServers: []MCPServer{{Name: "a"}, {Name: "a"}}},
	} {
		if err := validateCodexConfig(config); err == nil {
			t.Fatalf("config %#v was accepted", config)
		}
	}
	if _, err := codexSteer(json.RawMessage(`{"codex":{"delivery":"interrupt"}}`)); err == nil {
		t.Fatal("unknown delivery was accepted")
	}
}

// TestCodexAppServerHelper replays JSON-RPC shapes captured from
// `codex app-server` 0.157.1 over stdio.
func TestCodexAppServerHelper(t *testing.T) {
	if os.Getenv("MERIDIAN_CODEX_HELPER") != "1" {
		return
	}
	log := helperArguments()
	save := func() {
		encoded, _ := json.Marshal(log)
		_ = os.WriteFile(os.Getenv("MERIDIAN_CODEX_HELPER_ARGUMENTS"), encoded, 0o600)
	}
	save()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	writer := json.NewEncoder(os.Stdout)
	notify := func(method string, params any) {
		_ = writer.Encode(map[string]any{"method": method, "params": params})
	}
	turn := 0
	turnID := ""
	begin := func() string {
		turn++
		turnID = "turn-" + string(rune('a'+turn))
		notify("turn/started", map[string]any{"threadId": codexFixtureThread, "turn": map[string]any{"id": turnID}})
		return turnID
	}
	message := func(id, text string) {
		notify("item/started", map[string]any{"item": map[string]any{"type": "agentMessage", "id": id, "text": ""}})
		notify("item/agentMessage/delta", map[string]any{"itemId": id, "delta": text})
		notify("item/completed", map[string]any{"item": map[string]any{"type": "agentMessage", "id": id, "text": text}})
	}
	complete := func(status string, failure any) {
		notify("turn/completed", map[string]any{
			"threadId": codexFixtureThread,
			"turn":     map[string]any{"id": turnID, "status": status, "error": failure},
		})
		turnID = ""
	}
	pending := map[string]string{}
	for scanner.Scan() {
		var record map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			os.Exit(3)
		}
		var method string
		_ = json.Unmarshal(record["method"], &method)
		var params map[string]any
		_ = json.Unmarshal(record["params"], &params)
		id := record["id"]
		reply := func(result any) { _ = writer.Encode(map[string]any{"id": id, "result": result}) }
		switch method {
		case "initialize":
			reply(map[string]any{"userAgent": "codex-fixture", "codexHome": "/tmp", "platformFamily": "unix"})
		case "initialized":
		case "thread/start", "thread/resume":
			entry := method
			if method == "thread/resume" {
				entry += " " + params["threadId"].(string)
			}
			if policy, exists := params["approvalPolicy"]; exists {
				entry += " approvalPolicy=" + policy.(string)
			}
			if sandbox, exists := params["sandbox"]; exists {
				entry += " sandbox=" + sandbox.(string)
			}
			log = append(log, entry)
			save()
			reply(map[string]any{"thread": map[string]any{"id": codexFixtureThread},
				"model": defaultString(os.Getenv("MERIDIAN_CODEX_HELPER_MODEL"), "gpt-fixture")})
			notify("thread/started", map[string]any{"thread": map[string]any{"id": codexFixtureThread}})
		case "model/list":
			reply(map[string]any{"data": []any{
				map[string]any{"id": "gpt-fixture", "model": "gpt-fixture", "displayName": "GPT Fixture", "hidden": false},
				map[string]any{"id": "gpt-fast", "model": "gpt-fast", "displayName": "GPT\nFast", "hidden": false},
				map[string]any{"id": "gpt-secret", "model": "gpt-secret", "displayName": "Hidden", "hidden": true},
			}, "nextCursor": nil})
		case "skills/list":
			reply(map[string]any{"data": []any{map[string]any{
				"cwd": "/workspace", "errors": []any{},
				"skills": []any{
					map[string]any{"name": "ship", "description": "Ship the current branch\nwith care",
						"path": "/workspace/.agents/skills/ship/SKILL.md", "scope": "repo", "enabled": true},
					map[string]any{"name": "compact", "description": "collides with the action",
						"path": "/workspace/.agents/skills/compact/SKILL.md", "scope": "repo", "enabled": true},
					map[string]any{"name": "disabled", "description": "off",
						"path": "/workspace/.agents/skills/disabled/SKILL.md", "scope": "repo", "enabled": false},
					map[string]any{"name": "bad name", "description": "unusable",
						"path": "/workspace/.agents/skills/bad/SKILL.md", "scope": "repo", "enabled": true},
					map[string]any{"name": "relative", "description": "relative path",
						"path": "skills/relative/SKILL.md", "scope": "repo", "enabled": true},
				},
			}}})
		case "thread/compact/start":
			log = append(log, "compact "+params["threadId"].(string))
			save()
			reply(map[string]any{})
			begin()
			compaction := map[string]any{"type": "contextCompaction", "id": "compaction-1"}
			notify("item/started", map[string]any{"item": compaction, "turnId": turnID})
			notify("item/completed", map[string]any{"item": compaction, "turnId": turnID})
			complete("completed", nil)
		case "review/start":
			target, _ := json.Marshal(params["target"])
			log = append(log, "review "+string(target))
			save()
			outer := begin()
			reply(map[string]any{"turn": map[string]any{"id": outer, "status": "inProgress"}, "reviewThreadId": codexFixtureThread})
			notify("item/started", map[string]any{"item": map[string]any{
				"type": "enteredReviewMode", "id": "review-entered", "review": "current changes"}, "turnId": outer})
			inner := "turn-review-inner"
			notify("turn/started", map[string]any{"threadId": codexFixtureThread, "turn": map[string]any{"id": inner}})
			notify("turn/completed", map[string]any{"threadId": codexFixtureThread, "turn": map[string]any{"id": inner, "status": "completed"}})
			notify("item/completed", map[string]any{"item": map[string]any{
				"type": "exitedReviewMode", "id": "review-exited", "review": "No findings."}, "turnId": outer})
			turnID = outer
			complete("completed", nil)
		case "turn/start":
			if model, exists := params["model"]; exists {
				log = append(log, "turn model="+model.(string))
				save()
			}
			inputs := params["input"].([]any)
			input := inputs[0].(map[string]any)
			if len(inputs) > 1 {
				skill := inputs[1].(map[string]any)
				log = append(log, "skill "+skill["type"].(string)+" "+skill["name"].(string)+" "+skill["path"].(string))
				save()
				reply(map[string]any{"turn": map[string]any{"id": begin()}})
				message("msg_skill", input["text"].(string))
				complete("completed", nil)
				continue
			}
			reply(map[string]any{"turn": map[string]any{"id": begin()}})
			switch input["text"] {
			case "hello":
				message("msg_fixture", "hello")
				complete("completed", nil)
			case "tool":
				notify("item/started", map[string]any{"item": map[string]any{
					"type": "commandExecution", "id": "call_fixture", "command": "echo meridian",
					"cwd": "/workspace", "status": "inProgress",
				}})
				pending["7"] = "tool"
				_ = writer.Encode(map[string]any{"id": 7, "method": "item/commandExecution/requestApproval", "params": map[string]any{
					"threadId": codexFixtureThread, "turnId": turnID, "itemId": "call_fixture",
					"command": "echo meridian", "cwd": "/workspace", "reason": nil,
				}})
			case "mcp":
				pending["8"] = "mcp"
				_ = writer.Encode(map[string]any{"id": 8, "method": "mcpServer/elicitation/request", "params": map[string]any{
					"threadId": codexFixtureThread, "turnId": turnID, "serverName": "meridian-coord",
					"mode": "form", "message": "Allow the meridian-coord MCP server to run tool \"ping\"?",
					"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					"_meta": map[string]any{
						"codex_approval_kind": "mcp_tool_call", "persist": []any{"session", "always"},
						"tool_params": map[string]any{},
					},
				}})
			case "input":
				pending["\"input-1\""] = "input"
				_ = writer.Encode(map[string]any{"id": "input-1", "method": "item/tool/requestUserInput", "params": map[string]any{
					"threadId": codexFixtureThread, "turnId": turnID, "itemId": "call_input",
					"questions": []any{
						map[string]any{"id": "color", "header": "Color", "question": "Which color?", "isOther": false,
							"isSecret": false, "options": []any{map[string]any{"label": "red", "description": "warm"}}},
						map[string]any{"id": "token", "header": "Token", "question": "Token?", "isOther": false,
							"isSecret": true, "options": nil},
					},
				}})
			case "/hello":
				message("msg_plain", "/hello")
				complete("completed", nil)
			case "hold":
			case "withdraw":
				_ = writer.Encode(map[string]any{"id": 9, "method": "item/commandExecution/requestApproval", "params": map[string]any{
					"threadId": codexFixtureThread, "turnId": turnID, "itemId": "call_withdrawn", "command": "sleep 100",
				}})
				notify("serverRequest/resolved", map[string]any{"threadId": codexFixtureThread, "requestId": 9})
				complete("interrupted", nil)
			case "fail":
				notify("error", map[string]any{"error": map[string]any{"message": "Reconnecting... 1/5"}, "willRetry": true})
				complete("failed", map[string]any{"message": "unexpected status 401 Unauthorized"})
			default:
				os.Exit(4)
			}
		case "turn/steer":
			if params["expectedTurnId"] != turnID {
				os.Exit(5)
			}
			reply(map[string]any{"turnId": turnID})
			input := params["input"].([]any)[0].(map[string]any)
			message("msg_steered", input["text"].(string))
			complete("completed", nil)
		case "turn/interrupt":
			reply(map[string]any{})
			complete("interrupted", nil)
		case "":
			var result map[string]any
			_ = json.Unmarshal(record["result"], &result)
			switch pending[string(id)] {
			case "tool":
				notify("serverRequest/resolved", map[string]any{"threadId": codexFixtureThread, "requestId": 7})
				exitCode := 0
				notify("item/completed", map[string]any{"item": map[string]any{
					"type": "commandExecution", "id": "call_fixture", "command": "echo meridian",
					"aggregatedOutput": "decision " + result["decision"].(string), "exitCode": exitCode,
					"status": "completed",
				}})
				message("msg_tool", "tool finished")
				complete("completed", nil)
			case "mcp":
				message("msg_mcp", "elicitation "+result["action"].(string))
				complete("completed", nil)
			case "input":
				answers, _ := json.Marshal(result["answers"])
				message("msg_input", string(answers))
				complete("completed", nil)
			default:
				os.Exit(6)
			}
			delete(pending, string(id))
		default:
			os.Exit(7)
		}
	}
	os.Exit(0)
}
