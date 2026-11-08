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
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

type acpRecord struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func runACPFixture(
	t *testing.T,
	mode string,
	config ACPConfig,
	initial adapterproto.Kind,
	resume string,
) (*adapterproto.Decoder, *adapterproto.Encoder, string, chan error) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "agent.jsonl")
	t.Setenv("MERIDIAN_ACP_HELPER", mode)
	t.Setenv("MERIDIAN_ACP_HELPER_LOG", log)
	config.Command = []string{os.Args[0], "-test.run=^TestACPAgentHelper$"}
	controller, adapter := net.Pipe()
	t.Cleanup(func() { _ = controller.Close() })
	done := make(chan error, 1)
	go func() { done <- RunACP(context.Background(), adapter, adapter, config) }()
	decoder, encoder := negotiateTestAdapterWith(t, controller, initial, resume, mode == "models")
	return decoder, encoder, log, done
}

func sendSetModel(t *testing.T, encoder *adapterproto.Encoder, id, model string) {
	t.Helper()
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindSetModel, ID: id, Model: model,
	})
}

func TestACPReportsAndSwitchesModels(t *testing.T) {
	decoder, encoder, log, _ := runACPFixture(t, "models", ACPConfig{Permissions: true}, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	models := readKind(t, decoder, adapterproto.KindModels).Models
	if models.Current != "gw/flash" || len(models.Options) != 3 ||
		models.Options[0].ID != "gw/flash" || models.Options[0].Label != "Gateway · Flash" ||
		models.Options[1].ID != "gw/pro" || models.Options[1].Description != "Slower, smarter" ||
		models.Options[2].ID != "solo" || models.Options[2].Label != "Solo" {
		t.Fatalf("models = %#v", models)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendSetModel(t, encoder, "switch-1", "not-offered")
	if failure := readKind(t, decoder, adapterproto.KindError); failure.Code != "model_unavailable" {
		t.Fatalf("unknown model = %#v", failure)
	}
	sendSetModel(t, encoder, "switch-2", "gw/pro")
	switched := readKind(t, decoder, adapterproto.KindModels).Models
	if switched.Current != "gw/pro" || len(switched.Options) != 3 {
		t.Fatalf("switched = %#v", switched)
	}
	sendSetModel(t, encoder, "switch-3", "solo")
	if failure := readKind(t, decoder, adapterproto.KindError); failure.Code != "model_unavailable" ||
		!strings.Contains(failure.Summary, "quota") {
		t.Fatalf("refused switch = %#v", failure)
	}
	sendTestMessage(t, encoder, "message-1", "hello")
	var updated *adapterproto.ModelSet
	for _, frame := range readUntil(t, decoder, adapterproto.KindAssistantMessage) {
		if frame.Type == adapterproto.KindModels {
			updated = frame.Models
		}
	}
	if updated == nil || updated.Current != "solo" || len(updated.Options) != 1 {
		t.Fatalf("config_option_update = %#v", updated)
	}
	call := findACP(t, readACPLog(t, log), func(record acpRecord) bool {
		return record.Method == "session/set_config_option" && strings.Contains(string(record.Params), `"gw/pro"`)
	})
	var params map[string]string
	if json.Unmarshal(call.Params, &params) != nil || params["sessionId"] != "sess-1" ||
		params["configId"] != "model" || params["value"] != "gw/pro" {
		t.Fatalf("set_config_option params = %s", call.Params)
	}
}

func TestACPWithoutModelsCapabilityReportsNothing(t *testing.T) {
	controller, adapter := net.Pipe()
	t.Cleanup(func() { _ = controller.Close() })
	log := filepath.Join(t.TempDir(), "agent.jsonl")
	t.Setenv("MERIDIAN_ACP_HELPER", "models")
	t.Setenv("MERIDIAN_ACP_HELPER_LOG", log)
	config := ACPConfig{Permissions: true, Command: []string{os.Args[0], "-test.run=^TestACPAgentHelper$"}}
	go func() { _ = RunACP(context.Background(), adapter, adapter, config) }()
	decoder, _ := negotiateTestAdapter(t, controller, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	if next := readStatus(t, decoder, adapterproto.StatusIdle); next.Type != adapterproto.KindStatus {
		t.Fatalf("next = %#v", next)
	}
}

func readACPLog(t *testing.T, path string) []acpRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []acpRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var record acpRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			t.Fatalf("agent log line %q", scanner.Text())
		}
		records = append(records, record)
	}
	return records
}

func findACP(t *testing.T, records []acpRecord, match func(acpRecord) bool) acpRecord {
	t.Helper()
	for _, record := range records {
		if match(record) {
			return record
		}
	}
	t.Fatalf("no matching agent record in %d records", len(records))
	return acpRecord{}
}

func respondPermission(t *testing.T, encoder *adapterproto.Encoder, request adapterproto.Frame, choice string) {
	t.Helper()
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "response-" + request.ID, ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: request.Permission.Kind, Choice: choice},
	})
}

func TestACPContract(t *testing.T) {
	decoder, encoder, log, done := runACPFixture(t, "new", ACPConfig{
		Permissions: true, SecretEnv: []string{"FIXTURE_SERVER_PASSWORD"},
		MCPServers: []MCPServer{{Name: "tools", Command: "/usr/local/bin/tools", Arguments: []string{"serve"}}},
	}, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusStarting)
	idle := readStatus(t, decoder, adapterproto.StatusIdle)
	if idle.ResumeState != `{"driver":"acp","sessionId":"sess-1"}` {
		t.Fatalf("resume state = %q", idle.ResumeState)
	}
	commands := readKind(t, decoder, adapterproto.KindCommands)
	if len(commands.Commands) != 2 || commands.Commands[0].Name != "review" ||
		commands.Commands[0].ArgumentHint != "[commit|branch]" ||
		commands.Commands[0].Kind != adapterproto.CommandPrompt || commands.Commands[1].Name != "init" ||
		strings.ContainsAny(commands.Commands[1].Description, "\n\x1b") {
		t.Fatalf("commands = %#v", commands.Commands)
	}

	sendTestMessage(t, encoder, "message-1", "hello")
	frames := readUntil(t, decoder, adapterproto.KindAssistantMessage)
	assertKinds(t, frames, adapterproto.KindStatus, adapterproto.KindAssistantDelta)
	if final := frames[len(frames)-1]; final.Content != "hello there" || final.MessageID != "m1" {
		t.Fatalf("final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-2", "tool")
	frames = readUntil(t, decoder, adapterproto.KindPermissionRequest)
	start := frames[len(frames)-2]
	if start.Type != adapterproto.KindToolStart || start.ToolName != "bash" ||
		!strings.Contains(start.Summary, "echo hi") || start.ToolCallID != "call-1" {
		t.Fatalf("tool start = %#v", start)
	}
	request := frames[len(frames)-1]
	if request.Permission.Kind != "execute" || !strings.Contains(request.Permission.Summary, "echo hi") ||
		strings.Join(request.Permission.Options, "|") != "Allow once|Always allow|Reject" {
		t.Fatalf("permission request = %#v", request)
	}
	respondPermission(t, encoder, request, "Allow once")
	result := readKind(t, decoder, adapterproto.KindToolResult)
	if result.ToolCallID != "call-1" || result.Result != "hi\n" {
		t.Fatalf("tool result = %#v", result)
	}
	if final := readKind(t, decoder, adapterproto.KindAssistantMessage); final.Content != "ran it" {
		t.Fatalf("tool turn final = %#v", final)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	// A choice that names no offered option must never allow.
	sendTestMessage(t, encoder, "message-3", "tool")
	request = readKind(t, decoder, adapterproto.KindPermissionRequest)
	respondPermission(t, encoder, request, "allow everything forever")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	sendTestMessage(t, encoder, "message-4", "plan")
	frames = readUntil(t, decoder, adapterproto.KindStatus)
	frames = append(frames, readUntil(t, decoder, adapterproto.KindStatus)...)
	var plans []adapterproto.Frame
	for _, frame := range frames {
		if frame.Type == adapterproto.KindToolResult && strings.HasPrefix(frame.ToolCallID, "acp-plan-") {
			plans = append(plans, frame)
		}
	}
	if len(plans) != 2 || plans[0].Result != "[x] Read the code\n[~] Fix the bug\n[ ] Ship it\n" ||
		!strings.HasPrefix(plans[1].Result, "[x] Read the code\n[x] Fix the bug") {
		t.Fatalf("plans = %#v", plans)
	}

	sendTestMessage(t, encoder, "message-5", "hostile")
	frames = readUntil(t, decoder, adapterproto.KindStatus)
	for frames[len(frames)-1].Status != adapterproto.StatusIdle {
		frames = append(frames, readUntil(t, decoder, adapterproto.KindStatus)...)
	}
	sanitized, digested := false, false
	for _, frame := range frames {
		if strings.Contains(frame.Content, "other session") {
			t.Fatalf("another session's update leaked: %#v", frame)
		}
		if frame.Type == adapterproto.KindAssistantMessage {
			if strings.ContainsRune(frame.Content, 0) || !utf8.ValidString(frame.Content) {
				t.Fatalf("unsafe text = %q", frame.Content)
			}
			sanitized = frame.Content == "bad\uFFFD text"
		}
		if frame.Type == adapterproto.KindToolResult && frame.Result == "Edited /w/x.go" {
			digested = strings.HasPrefix(frame.ToolCallID, "acp-tool-")
		}
	}
	if !sanitized || !digested {
		t.Fatalf("hostile turn frames = %#v", frames)
	}

	sendTestMessage(t, encoder, "message-6", "refuse")
	if failure := readKind(t, decoder, adapterproto.KindError); failure.Code != "acp_refusal" {
		t.Fatalf("refusal = %#v", failure)
	}
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	// A message sent during a turn waits for it.
	sendTestMessage(t, encoder, "message-7", "slow")
	sendTestMessage(t, encoder, "message-8", "hello")
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

	records := readACPLog(t, log)
	initialize := findACP(t, records, func(r acpRecord) bool { return r.Method == "initialize" })
	var init struct {
		ProtocolVersion    int `json:"protocolVersion"`
		ClientCapabilities struct {
			FS struct {
				Read  bool `json:"readTextFile"`
				Write bool `json:"writeTextFile"`
			} `json:"fs"`
			Terminal bool `json:"terminal"`
		} `json:"clientCapabilities"`
	}
	if json.Unmarshal(initialize.Params, &init) != nil || init.ProtocolVersion != 1 ||
		init.ClientCapabilities.FS.Read || init.ClientCapabilities.FS.Write || init.ClientCapabilities.Terminal {
		t.Fatalf("initialize = %s", initialize.Params)
	}
	created := findACP(t, records, func(r acpRecord) bool { return r.Method == "session/new" })
	if !strings.Contains(string(created.Params), `"cwd":"`+capsuleWorkspace+`"`) ||
		strings.Contains(string(created.Params), `/home/`) ||
		strings.Contains(string(created.Params), `/Users/`) ||
		!strings.Contains(string(created.Params), `"mcpServers":[{"args":["serve"],"command":"/usr/local/bin/tools","env":[],"name":"tools"}]`) {
		t.Fatalf("session/new = %s", created.Params)
	}
	findACP(t, records, func(r acpRecord) bool { return r.Method == "secret-ok" })
	var outcomes []string
	for _, record := range records {
		if string(record.ID) == "7" && record.Method == "" {
			outcomes = append(outcomes, string(record.Result))
		}
	}
	if len(outcomes) != 2 || !strings.Contains(outcomes[0], `"optionId":"once"`) ||
		!strings.Contains(outcomes[1], `"optionId":"reject"`) {
		t.Fatalf("permission outcomes = %v", outcomes)
	}
	refused := findACP(t, records, func(r acpRecord) bool { return string(r.ID) == `"fs-1"` && r.Method == "" })
	if refused.Error == nil || refused.Error.Code != acpErrorMethodNotFound {
		t.Fatalf("fs request reply = %#v", refused)
	}
	findACP(t, records, func(r acpRecord) bool { return r.Method == "session/cancel" })
	hellos := 0
	for _, record := range records {
		if record.Method == "session/prompt" && strings.Contains(string(record.Params), `"text":"hello"`) {
			hellos++
		}
	}
	if hellos != 1 {
		t.Fatalf("hello prompts = %d; a queued message ran after cancel", hellos)
	}
}

func TestACPCancelAnswersOpenPermission(t *testing.T) {
	decoder, encoder, log, done := runACPFixture(t, "new", ACPConfig{Permissions: true}, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	sendTestMessage(t, encoder, "message-1", "tool")
	_ = readKind(t, decoder, adapterproto.KindPermissionRequest)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	if end := readKind(t, decoder, adapterproto.KindEnd); end.Status != adapterproto.StatusCancelled {
		t.Fatalf("end = %#v", end)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	answer := findACP(t, readACPLog(t, log), func(r acpRecord) bool { return string(r.ID) == "7" && r.Method == "" })
	if string(answer.Result) != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("permission answer = %s", answer.Result)
	}
}

func TestACPDisabledPermissionsRejectWithoutPrompt(t *testing.T) {
	decoder, encoder, log, _ := runACPFixture(t, "new", ACPConfig{Permissions: false}, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	sendTestMessage(t, encoder, "message-1", "tool")
	frames := readUntil(t, decoder, adapterproto.KindStatus)
	for frames[len(frames)-1].Status != adapterproto.StatusIdle {
		frames = append(frames, readUntil(t, decoder, adapterproto.KindStatus)...)
	}
	for _, frame := range frames {
		if frame.Type == adapterproto.KindPermissionRequest {
			t.Fatalf("permission request with permissions disabled: %#v", frame)
		}
	}
	answer := findACP(t, readACPLog(t, log), func(r acpRecord) bool { return string(r.ID) == "7" && r.Method == "" })
	if !strings.Contains(string(answer.Result), `"optionId":"reject"`) {
		t.Fatalf("permission answer = %s", answer.Result)
	}
}

func TestACPResume(t *testing.T) {
	state := `{"driver":"acp","sessionId":"sess-1"}`
	for _, mode := range []string{"resume", "load"} {
		t.Run(mode, func(t *testing.T) {
			decoder, encoder, log, _ := runACPFixture(t, mode, ACPConfig{}, adapterproto.KindResume, state)
			_ = readStatus(t, decoder, adapterproto.StatusStarting)
			if idle := readStatus(t, decoder, adapterproto.StatusIdle); idle.ResumeState != state {
				t.Fatalf("resume state = %q", idle.ResumeState)
			}
			sendTestMessage(t, encoder, "message-1", "hello")
			frames := readUntil(t, decoder, adapterproto.KindAssistantMessage)
			for _, frame := range frames {
				if strings.Contains(frame.Content, "old history") {
					t.Fatalf("replayed history leaked: %#v", frame)
				}
			}
			method := "session/" + mode
			opened := findACP(t, readACPLog(t, log), func(r acpRecord) bool { return r.Method == method })
			if !strings.Contains(string(opened.Params), `"sessionId":"sess-1"`) {
				t.Fatalf("%s = %s", method, opened.Params)
			}
		})
	}
}

func TestACPFailsClosed(t *testing.T) {
	for _, test := range []struct {
		mode, code, resume string
	}{
		{mode: "new", code: "acp_resume_unsupported", resume: `{"driver":"acp","sessionId":"sess-1"}`},
		{mode: "new", code: "acp_resume_invalid", resume: `{"driver":"opencode-server","sessionId":"sess-1"}`},
		{mode: "missing", code: "acp_resume_failed", resume: `{"driver":"acp","sessionId":"sess-1"}`},
		{mode: "auth", code: "acp_auth_required"},
		{mode: "version", code: "acp_protocol_unsupported"},
		{mode: "garbage", code: "acp_protocol_failed"},
		{mode: "uncorrelated", code: "acp_response_invalid"},
	} {
		t.Run(test.code, func(t *testing.T) {
			initial := adapterproto.KindStart
			if test.resume != "" {
				initial = adapterproto.KindResume
			}
			decoder, _, _, done := runACPFixture(t, test.mode, ACPConfig{}, initial, test.resume)
			if failure := readKind(t, decoder, adapterproto.KindError); failure.Code != test.code {
				t.Fatalf("error = %#v", failure)
			}
			if end := readKind(t, decoder, adapterproto.KindEnd); end.Status != adapterproto.StatusFailed {
				t.Fatalf("end = %#v", end)
			}
			if err := <-done; err == nil {
				t.Fatal("adapter succeeded")
			}
		})
	}
}

func TestACPRejectsInvalidConfiguration(t *testing.T) {
	for _, config := range []ACPConfig{
		{},
		{Command: []string{"opencode", "acp"}},
		{Command: []string{"/bin/agent"}, SecretEnv: []string{"PATH"}},
		{Command: []string{"/bin/agent"}, SecretEnv: []string{"MERIDIAN_PROVIDER_TOKEN"}},
		{Command: []string{"/bin/agent"}, SecretEnv: []string{"lower"}},
		{Command: []string{"/bin/agent"}, SecretEnv: []string{"A", "A"}},
		{Command: []string{"/bin/agent"}, MCPServers: []MCPServer{{Name: "x"}, {Name: "x"}}},
	} {
		if err := validateACPConfig(config); err == nil {
			t.Fatalf("config accepted: %#v", config)
		}
	}
}

func TestACPAgentHelper(t *testing.T) {
	mode := os.Getenv("MERIDIAN_ACP_HELPER")
	if mode == "" {
		return
	}
	log, err := os.OpenFile(os.Getenv("MERIDIAN_ACP_HELPER_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	writer := json.NewEncoder(os.Stdout)
	send := func(record map[string]any) {
		record["jsonrpc"] = "2.0"
		_ = writer.Encode(record)
	}
	session := func(id string, update map[string]any) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": id, "update": update}})
	}
	update := func(value map[string]any) { session("sess-1", value) }
	text := func(id, value string) map[string]any {
		chunk := map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": value}}
		if id != "" {
			chunk["messageId"] = id
		}
		return chunk
	}
	plan := func(second string) map[string]any {
		return map[string]any{"sessionUpdate": "plan", "entries": []map[string]any{
			{"content": "Read the code", "priority": "high", "status": "completed"},
			{"content": "Fix the bug", "priority": "high", "status": second},
			{"content": "Ship it", "priority": "low", "status": "pending"},
		}}
	}
	if len(os.Getenv("FIXTURE_SERVER_PASSWORD")) == 43 {
		_, _ = log.WriteString(`{"method":"secret-ok"}` + "\n")
	}
	var prompt json.RawMessage
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		_, _ = log.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		var record acpRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			os.Exit(3)
		}
		switch record.Method {
		case "initialize":
			capabilities := map[string]any{"loadSession": mode == "load", "sessionCapabilities": map[string]any{}}
			if mode == "resume" || mode == "missing" {
				capabilities["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			}
			version := 1
			switch mode {
			case "version":
				version = 2
			case "garbage":
				_, _ = os.Stdout.WriteString("not json\n")
				continue
			case "uncorrelated":
				send(map[string]any{"id": 99, "result": map[string]any{}})
				continue
			}
			send(map[string]any{"id": record.ID, "result": map[string]any{
				"protocolVersion": version, "agentCapabilities": capabilities, "authMethods": []any{},
			}})
		case "session/new":
			if mode == "auth" {
				send(map[string]any{"id": record.ID, "error": map[string]any{"code": -32000, "message": "log in first"}})
				continue
			}
			if mode == "models" {
				send(map[string]any{"id": record.ID, "result": map[string]any{"sessionId": "sess-1", "configOptions": []any{
					map[string]any{"id": "mode", "category": "mode", "type": "select", "currentValue": "build",
						"options": []any{map[string]any{"value": "build", "name": "build"}}},
					map[string]any{"id": "model", "category": "model", "type": "select", "currentValue": "gw/flash",
						"options": []any{
							map[string]any{"group": "gw", "name": "Gateway", "options": []any{
								map[string]any{"value": "gw/flash", "name": "Flash"},
								map[string]any{"value": "gw/pro", "name": "Pro", "description": "Slower,\n smarter"},
								map[string]any{"value": "", "name": "No value"},
								map[string]any{"value": "gw/flash", "name": "Duplicate"},
							}},
							map[string]any{"value": "solo", "name": "Solo"},
							map[string]any{"value": "bad\u0007id", "name": "Control"},
						}},
				}}})
				continue
			}
			send(map[string]any{"id": record.ID, "result": map[string]any{"sessionId": "sess-1"}})
			update(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{
				map[string]any{"name": "review", "description": "Review changes", "input": map[string]any{"hint": "[commit|branch]"}},
				map[string]any{"name": "bad name", "description": "dropped"},
				map[string]any{"name": "init", "description": "Set up\n\x1b[31mAGENTS.md"},
			}})
		case "session/resume":
			if mode == "missing" {
				send(map[string]any{"id": record.ID, "error": map[string]any{"code": -32002, "message": "no such session"}})
				continue
			}
			send(map[string]any{"id": record.ID, "result": map[string]any{}})
		case "session/load":
			update(text("old", "old history"))
			send(map[string]any{"id": record.ID, "result": nil})
		case "session/prompt":
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(record.Params, &params)
			prompt = record.ID
			end := func(reason string) {
				send(map[string]any{"id": prompt, "result": map[string]any{"stopReason": reason}})
				prompt = nil
			}
			switch params.Prompt[0].Text {
			case "hello":
				update(text("m1", "hello "))
				update(text("m1", "there"))
				if mode == "models" {
					update(map[string]any{"sessionUpdate": "config_option_update", "configOptions": []any{
						map[string]any{"id": "model", "category": "model", "type": "select", "currentValue": "solo",
							"options": []any{map[string]any{"value": "solo", "name": "Solo"}}},
					}})
				}
				update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "secret thoughts"}})
				end("end_turn")
			case "tool":
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "call-1", "title": "bash",
					"kind": "execute", "status": "pending", "rawInput": map[string]any{"cwd": "/w"}})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-1",
					"title": "echo hi", "rawInput": map[string]any{"command": "echo hi"}})
				send(map[string]any{"id": 7, "method": "session/request_permission", "params": map[string]any{
					"sessionId": "sess-1",
					"toolCall":  map[string]any{"toolCallId": "call-1", "title": "echo hi", "kind": "execute"},
					"options": []any{
						map[string]any{"optionId": "once", "kind": "allow_once", "name": "Allow once"},
						map[string]any{"optionId": "always", "kind": "allow_always", "name": "Always allow"},
						map[string]any{"optionId": "reject", "kind": "reject_once", "name": "Reject"},
						map[string]any{"optionId": "weird", "kind": "allow_forever", "name": "Unknown kind"},
					},
				}})
			case "plan":
				update(plan("in_progress"))
				update(plan("in_progress"))
				update(plan("completed"))
				end("end_turn")
			case "hostile":
				send(map[string]any{"id": "fs-1", "method": "fs/read_text_file", "params": map[string]any{
					"sessionId": "sess-1", "path": "/etc/shadow",
				}})
				session("sess-other", text("x", "other session"))
				_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","messageId":"h1","content":{"type":"text","text":"bad\u0000` + "\xff" + ` text"}}}}` + "\n")
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "a/b c", "title": "read",
					"status": "completed", "content": []any{map[string]any{"type": "diff", "path": "/w/x.go", "newText": "x"}}})
				update(map[string]any{"sessionUpdate": "future_update_kind", "anything": true})
				end("end_turn")
			case "refuse":
				end("refusal")
			case "slow":
			}
		case "session/set_config_option":
			var params struct {
				Value string `json:"value"`
			}
			_ = json.Unmarshal(record.Params, &params)
			if params.Value == "solo" {
				send(map[string]any{"id": record.ID, "error": map[string]any{"code": -32000, "message": "over quota for solo"}})
				continue
			}
			send(map[string]any{"id": record.ID, "result": map[string]any{"configOptions": []any{
				map[string]any{"id": "model", "category": "model", "type": "select", "currentValue": params.Value,
					"options": []any{
						map[string]any{"group": "gw", "name": "Gateway", "options": []any{
							map[string]any{"value": "gw/flash", "name": "Flash"},
							map[string]any{"value": "gw/pro", "name": "Pro", "description": "Slower, smarter"},
						}},
						map[string]any{"value": "solo", "name": "Solo"},
					}},
			}}})
		case "session/cancel":
			if prompt != nil {
				send(map[string]any{"id": prompt, "result": map[string]any{"stopReason": "cancelled"}})
				prompt = nil
			}
		case "":
			if string(record.ID) == "7" && prompt != nil {
				if strings.Contains(string(record.Result), `"once"`) {
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-1", "status": "completed",
						"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "hi\n"}}}})
					update(text("", "ran it"))
				} else if strings.Contains(string(record.Result), "cancelled") {
					continue
				} else {
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "call-1", "status": "failed"})
				}
				send(map[string]any{"id": prompt, "result": map[string]any{"stopReason": "end_turn"}})
				prompt = nil
			}
		}
	}
	os.Exit(0)
}
