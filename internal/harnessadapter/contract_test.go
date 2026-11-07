package harnessadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
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

func TestPiRPCCommands(t *testing.T) {
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
	listed := readKind(t, decoder, adapterproto.KindCommands)
	var names []string
	for _, command := range listed.Commands {
		names = append(names, command.Name+":"+string(command.Kind))
	}
	if strings.Join(names, " ") != "compact:action fix-tests:prompt skill:ship:prompt llama:prompt" {
		t.Fatalf("commands = %#v", listed.Commands)
	}
	compact := func(id, content, want string) {
		t.Helper()
		writeTestFrame(t, encoder, adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: id, SessionID: "session-1", Role: adapterproto.RoleUser, Content: content,
		})
		_ = readStatus(t, decoder, adapterproto.StatusRunning)
		if start := readKind(t, decoder, adapterproto.KindToolStart); start.ToolName != "compact" {
			t.Fatalf("compaction start = %#v", start)
		}
		if result := readKind(t, decoder, adapterproto.KindToolResult); result.Result != want {
			t.Fatalf("compaction result = %#v", result)
		}
		_ = readStatus(t, decoder, adapterproto.StatusIdle)
	}
	compact("message-1", "/compact focus on tests", "compacted 150000 tokens to about 32000")
	compact("message-2", "/compact", "failed")
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
		case "get_commands":
			_ = writer.Encode(map[string]any{
				"type": "response", "id": id, "command": "get_commands", "success": true,
				"data": map[string]any{"commands": []any{
					map[string]any{"name": "fix-tests", "description": "Fix failing tests", "source": "prompt"},
					map[string]any{"name": "skill:ship", "description": "Ship it", "source": "skill"},
					map[string]any{"name": "compact", "description": "shadowed", "source": "prompt"},
					map[string]any{"name": "llama", "source": "extension"},
				}},
			})
		case "compact":
			_ = writer.Encode(map[string]any{"type": "compaction_start", "reason": "manual"})
			_ = writer.Encode(map[string]any{"type": "compaction_end", "reason": "manual", "aborted": false})
			if command["customInstructions"] != "focus on tests" {
				_ = writer.Encode(map[string]any{
					"type": "response", "id": id, "command": "compact", "success": false,
					"error": "Nothing to compact (session too small)",
				})
				continue
			}
			_ = writer.Encode(map[string]any{
				"type": "response", "id": id, "command": "compact", "success": true,
				"data": map[string]any{"summary": "s", "tokensBefore": 150000, "estimatedTokensAfter": 32000},
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
			Commands: true,
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
