package harnessadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

func TestMockMultiTurnPermissionResumeAndCancel(t *testing.T) {
	controller, adapter := net.Pipe()
	defer controller.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunMock(context.Background(), adapter, adapter, MockConfig{})
	}()
	decoder, encoder := negotiateTestAdapter(t, controller, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "message-1", SessionID: "session-1", Role: adapterproto.RoleUser,
		Content: "first",
	})
	frames := readUntil(t, decoder, adapterproto.KindAssistantMessage)
	assertKinds(t, frames, adapterproto.KindAssistantDelta, adapterproto.KindToolStart,
		adapterproto.KindToolResult, adapterproto.KindAssistantMessage)
	_ = readStatus(t, decoder, adapterproto.StatusIdle)

	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "message-2", SessionID: "session-1", Role: adapterproto.RoleUser,
		Content: "permission",
	})
	request := readKind(t, decoder, adapterproto.KindPermissionRequest)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "permission-response", ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: request.Permission.Kind, Choice: "allow"},
	})
	_ = readKind(t, decoder, adapterproto.KindAssistantMessage)
	idle := readKind(t, decoder, adapterproto.KindStatus)
	if idle.Status != adapterproto.StatusIdle || idle.ResumeState == "" {
		t.Fatalf("idle frame = %#v", idle)
	}
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	if end := readKind(t, decoder, adapterproto.KindEnd); end.Status != adapterproto.StatusCancelled {
		t.Fatalf("end = %#v", end)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	resumeController, resumeAdapter := net.Pipe()
	defer resumeController.Close()
	resumeDone := make(chan error, 1)
	go func() {
		resumeDone <- RunMock(context.Background(), resumeAdapter, resumeAdapter, MockConfig{})
	}()
	resumeDecoder, resumeEncoder := negotiateTestAdapter(
		t, resumeController, adapterproto.KindResume, idle.ResumeState,
	)
	_ = readStatus(t, resumeDecoder, adapterproto.StatusIdle)
	writeTestFrame(t, resumeEncoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "message-3", SessionID: "session-1", Role: adapterproto.RoleUser,
		Content: "resumed",
	})
	resumed := readKind(t, resumeDecoder, adapterproto.KindAssistantMessage)
	if resumed.MessageID != "mock-message-3" {
		t.Fatalf("resumed message = %#v", resumed)
	}
	_ = readStatus(t, resumeDecoder, adapterproto.StatusIdle)
	writeTestFrame(t, resumeEncoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-2",
	})
	_ = readKind(t, resumeDecoder, adapterproto.KindEnd)
	if err := <-resumeDone; err != nil {
		t.Fatal(err)
	}
}

func TestPiRPCContract(t *testing.T) {
	t.Setenv("MERIDIAN_PI_HELPER", "1")
	controller, adapter := net.Pipe()
	defer controller.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunPiRPC(context.Background(), adapter, adapter, PiConfig{
			Command: []string{os.Args[0], "-test.run=TestPiRPCContractHelper", "--"},
		})
	}()
	decoder, encoder := negotiateTestAdapter(t, controller, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "message-1", SessionID: "session-1", Role: adapterproto.RoleUser,
		Content: "pi prompt",
	})
	frames := readUntil(t, decoder, adapterproto.KindPermissionRequest)
	assertKinds(t, frames, adapterproto.KindAssistantDelta, adapterproto.KindToolStart,
		adapterproto.KindToolResult, adapterproto.KindAssistantMessage,
		adapterproto.KindPermissionRequest)
	request := frames[len(frames)-1]
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "pi-permission-response", ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: "confirm", Choice: "allow"},
	})
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	_ = readKind(t, decoder, adapterproto.KindEnd)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPiRPCContractHelper(t *testing.T) {
	if os.Getenv("MERIDIAN_PI_HELPER") != "1" {
		return
	}
	foundMode := false
	for index, argument := range os.Args {
		if argument == "--mode" && index+1 < len(os.Args) && os.Args[index+1] == "rpc" {
			foundMode = true
		}
	}
	if !foundMode {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	writer := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var command map[string]any
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			os.Exit(3)
		}
		commandType, _ := command["type"].(string)
		id, _ := command["id"].(string)
		switch commandType {
		case "get_state":
			_ = writer.Encode(map[string]any{
				"type": "response", "id": id, "command": "get_state", "success": true,
				"data": map[string]any{
					"sessionFile": "/tmp/pi-session.jsonl", "sessionId": "pi-session",
					"sessionName": "contract",
				},
			})
		case "prompt":
			_ = writer.Encode(map[string]any{
				"type": "response", "id": id, "command": "prompt", "success": true,
			})
			_ = writer.Encode(map[string]any{"type": "agent_start"})
			_ = writer.Encode(map[string]any{
				"type":    "message_start",
				"message": map[string]any{"role": "assistant", "content": []any{}},
			})
			_ = writer.Encode(map[string]any{
				"type": "message_update",
				"assistantMessageEvent": map[string]any{
					"type": "text_delta", "contentIndex": 0, "delta": "hello",
				},
			})
			_ = writer.Encode(map[string]any{
				"type": "tool_execution_start", "toolCallId": "tool-1",
				"toolName": "read", "args": map[string]any{"path": "fixture"},
			})
			_ = writer.Encode(map[string]any{
				"type": "tool_execution_end", "toolCallId": "tool-1",
				"toolName": "read", "result": map[string]any{"content": "done"},
			})
			_ = writer.Encode(map[string]any{
				"type": "message_end",
				"message": map[string]any{
					"role":    "assistant",
					"content": []any{map[string]any{"type": "text", "text": "hello"}},
				},
			})
			_ = writer.Encode(map[string]any{
				"type": "extension_ui_request", "id": "ui-confirm",
				"method": "confirm", "title": "Allow?",
			})
		case "extension_ui_response":
			_ = writer.Encode(map[string]any{"type": "agent_settled"})
		case "abort":
			_ = writer.Encode(map[string]any{
				"type": "response", "id": id, "command": "abort", "success": true,
			})
		default:
			os.Exit(4)
		}
	}
	os.Exit(0)
}

func TestOpenCodeContractAuthEventsPermissionAndCancel(t *testing.T) {
	t.Setenv("MERIDIAN_OPENCODE_HELPER", "1")
	controller, adapter := net.Pipe()
	defer controller.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunOpenCode(context.Background(), adapter, adapter, OpenCodeConfig{
			Command:     []string{os.Args[0], "-test.run=TestOpenCodeContractHelper", "--"},
			Permissions: true,
		})
	}()
	decoder, encoder := negotiateTestAdapter(t, controller, adapterproto.KindStart, "")
	_ = readStatus(t, decoder, adapterproto.StatusIdle)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "message-1", SessionID: "session-1", Role: adapterproto.RoleUser,
		Content: "OpenCode prompt",
	})
	frames := readUntil(t, decoder, adapterproto.KindPermissionRequest)
	assertKinds(t, frames, adapterproto.KindAssistantDelta, adapterproto.KindToolStart,
		adapterproto.KindToolResult, adapterproto.KindAssistantMessage,
		adapterproto.KindPermissionRequest)
	request := frames[len(frames)-1]
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindPermissionResponse,
		ID: "permission-response", ResponseTo: request.ID,
		Permission: &adapterproto.Permission{Kind: "edit", Choice: "once"},
	})
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCancel, ID: "cancel-1",
	})
	_ = readKind(t, decoder, adapterproto.KindEnd)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeSSEReconnectCursorAndDedup(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	sawCursor := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if username, password, ok := request.BasicAuth(); !ok ||
			username != "user" || password != "password" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		requests++
		current := requests
		if current > 1 && request.Header.Get("Last-Event-ID") == "1" {
			sawCursor = true
		}
		mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		_, _ = io.WriteString(writer,
			"id: 1\ndata: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
		flusher.Flush()
		if current == 1 {
			return
		}
		_, _ = io.WriteString(writer,
			"id: 2\ndata: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses\"}}\n\n")
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &openCodeClient{
		base: base, username: "user", password: "password", dir: t.TempDir(),
		client: &http.Client{Transport: http.DefaultTransport, Timeout: time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan openCodeEvent, 4)
	eventErrors := make(chan error, 1)
	go client.readEvents(ctx, events, eventErrors)
	first := <-events
	second := <-events
	cancel()
	if first.Type != "server.connected" || second.Type != "session.idle" {
		t.Fatalf("events = %#v, %#v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests < 2 || !sawCursor {
		t.Fatalf("requests = %d, cursor = %v", requests, sawCursor)
	}
}

func TestOpenCodeContractHelper(t *testing.T) {
	if os.Getenv("MERIDIAN_OPENCODE_HELPER") != "1" {
		return
	}
	port := ""
	for index, argument := range os.Args {
		if argument == "--port" && index+1 < len(os.Args) {
			port = os.Args[index+1]
		}
	}
	if port == "" || os.Getenv("OPENCODE_SERVER_PASSWORD") == "" {
		os.Exit(2)
	}
	eventChannel := make(chan string, 32)
	var eventMu sync.Mutex
	lastEventID := 0
	authenticated := func(request *http.Request) bool {
		username, password, ok := request.BasicAuth()
		return ok && username == "meridian" &&
			password == os.Getenv("OPENCODE_SERVER_PASSWORD") &&
			request.Header.Get("X-OpenCode-Directory") != ""
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !authenticated(request) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case request.URL.Path == "/global/health":
			writeTestJSON(writer, map[string]any{"healthy": true, "version": "1.1.0"})
		case request.URL.Path == "/session" && request.Method == http.MethodPost:
			writeTestJSON(writer, map[string]any{"id": "ses_contract"})
		case request.URL.Path == "/event":
			writer.Header().Set("Content-Type", "text/event-stream")
			flusher := writer.(http.Flusher)
			_, _ = io.WriteString(writer, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
			flusher.Flush()
			for event := range eventChannel {
				eventMu.Lock()
				lastEventID++
				id := lastEventID
				eventMu.Unlock()
				_, _ = fmt.Fprintf(writer, "id: %d\ndata: %s\n\n", id, event)
				flusher.Flush()
			}
		case request.URL.Path == "/session/ses_contract/prompt_async":
			var prompt map[string]json.RawMessage
			if json.NewDecoder(request.Body).Decode(&prompt) != nil || prompt["messageID"] != nil {
				http.Error(writer, "caller-supplied messageID is incompatible", http.StatusBadRequest)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			events := []map[string]any{
				{"type": "plugin.added", "properties": map[string]any{}},
				{"type": "catalog.updated", "properties": map[string]any{}},
				{"type": "reference.updated", "properties": map[string]any{}},
				{"type": "integration.updated", "properties": map[string]any{}},
				{"type": "message.part.delta", "properties": map[string]any{
					"sessionID": "ses_contract", "messageID": "msg_contract",
					"field": "text", "delta": "hello",
				}},
				{"type": "message.part.updated", "properties": map[string]any{
					"part": map[string]any{
						"id": "part_contract", "callID": "tool_contract", "sessionID": "ses_contract",
						"messageID": "msg_contract", "type": "tool", "tool": "read",
						"state": map[string]any{"status": "running", "input": map[string]any{"path": "fixture"}},
					},
				}},
				{"type": "message.part.updated", "properties": map[string]any{
					"part": map[string]any{
						"id": "part_contract", "callID": "tool_contract", "sessionID": "ses_contract",
						"messageID": "msg_contract", "type": "tool", "tool": "read",
						"state": map[string]any{"status": "completed", "output": "done"},
					},
				}},
				{"type": "message.updated", "properties": map[string]any{
					"info": map[string]any{
						"id": "msg_contract", "sessionID": "ses_contract",
						"role": "assistant", "finish": "stop",
					},
				}},
				{"type": "permission.asked", "properties": map[string]any{
					"id": "perm_contract", "sessionID": "ses_contract",
					"permission": "edit", "patterns": []string{"fixture"},
				}},
			}
			go func() {
				for _, event := range events {
					value, _ := json.Marshal(event)
					eventChannel <- string(value)
				}
			}()
		case request.URL.Path == "/session/ses_contract/message/msg_contract":
			writeTestJSON(writer, map[string]any{
				"info": map[string]any{
					"id": "msg_contract", "sessionID": "ses_contract", "role": "assistant",
				},
				"parts": []map[string]any{{"type": "text", "text": "hello"}},
			})
		case request.URL.Path == "/session/ses_contract/permissions/perm_contract":
			writeTestJSON(writer, true)
		case request.URL.Path == "/session/ses_contract/abort":
			writeTestJSON(writer, true)
		default:
			http.NotFound(writer, request)
		}
	})
	server := &http.Server{Addr: "127.0.0.1:" + port, Handler: handler}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		os.Exit(3)
	}
}

func negotiateTestAdapter(
	t *testing.T,
	connection net.Conn,
	initialType adapterproto.Kind,
	resume string,
) (*adapterproto.Decoder, *adapterproto.Encoder) {
	t.Helper()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	decoder := adapterproto.NewDecoder(connection)
	encoder := adapterproto.NewEncoder(connection)
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindHello, ID: "test-hello",
		Capabilities: &adapterproto.Capabilities{
			Resume: true, ToolEvents: true, Permissions: true,
			InputRequests: true, Heartbeat: true, OpaqueMetadata: true,
		},
	})
	if hello, err := decoder.Decode(); err != nil || hello.Type != adapterproto.KindHello {
		t.Fatalf("hello = %#v, %v", hello, err)
	}
	writeTestFrame(t, encoder, adapterproto.Frame{
		Protocol: adapterproto.Version, Type: initialType, ID: "start-1",
		SessionID: "session-1", ResumeState: resume,
	})
	return decoder, encoder
}

func writeTestFrame(t *testing.T, encoder *adapterproto.Encoder, frame adapterproto.Frame) {
	t.Helper()
	if err := encoder.Encode(frame); err != nil {
		t.Fatal(err)
	}
}

func readUntil(
	t *testing.T,
	decoder *adapterproto.Decoder,
	want adapterproto.Kind,
) []adapterproto.Frame {
	t.Helper()
	var frames []adapterproto.Frame
	for {
		frame, err := decoder.Decode()
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
		if frame.Type == want {
			return frames
		}
	}
}

func readKind(
	t *testing.T,
	decoder *adapterproto.Decoder,
	want adapterproto.Kind,
) adapterproto.Frame {
	t.Helper()
	for {
		frame, err := decoder.Decode()
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == want {
			return frame
		}
	}
}

func readStatus(
	t *testing.T,
	decoder *adapterproto.Decoder,
	want adapterproto.Status,
) adapterproto.Frame {
	t.Helper()
	for {
		frame := readKind(t, decoder, adapterproto.KindStatus)
		if frame.Status == want {
			return frame
		}
	}
}

func assertKinds(t *testing.T, frames []adapterproto.Frame, wants ...adapterproto.Kind) {
	t.Helper()
	have := map[adapterproto.Kind]bool{}
	for _, frame := range frames {
		have[frame.Type] = true
	}
	for _, want := range wants {
		if !have[want] {
			t.Fatalf("frames do not contain %s: %#v", want, frames)
		}
	}
}

func writeTestJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

var _ = strconv.Itoa
var _ = strings.Contains
