package harnessadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

type MockConfig struct {
	Fault string
}

// RunMock runs the deterministic, credential-free structured test adapter.
func RunMock(ctx context.Context, input io.Reader, output io.Writer, config MockConfig) error {
	stream := NewStream(input, output)
	if err := stream.Handshake(adapterproto.Capabilities{
		Resume: true, ToolEvents: true, Permissions: true,
		InputRequests: true, Heartbeat: true, OpaqueMetadata: true,
	}); err != nil {
		return err
	}
	initial, err := stream.Read()
	if err != nil {
		return err
	}
	if initial.Type != adapterproto.KindStart && initial.Type != adapterproto.KindResume {
		return errors.New("mock adapter requires start or resume")
	}
	if config.Fault == "crash-start" {
		return errors.New("injected child crash")
	}
	if err := stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusRunning,
	}); err != nil {
		return err
	}
	turn := 0
	resume := mockResumeState(0)
	if initial.Type == adapterproto.KindResume {
		turn, err = mockResumeTurn(initial.ResumeState)
		if err != nil {
			stream.Fail("mock_resume_invalid")
			return err
		}
		resume = initial.ResumeState
	}
	if err := stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusIdle, ResumeState: resume,
		Metadata: metadata(map[string]any{"driver": "mock"}),
	}); err != nil {
		return err
	}

	pendingPermission := ""
	pendingInput := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		frame, err := stream.Read()
		if err != nil {
			return err
		}
		switch frame.Type {
		case adapterproto.KindUserMessage:
			turn++
			if err := stream.Write(adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
				Status: adapterproto.StatusRunning,
			}); err != nil {
				return err
			}
			switch config.Fault {
			case "crash":
				return errors.New("injected child crash")
			case "hang":
				continue
			case "oversize":
				value := `{"protocol":"meridian.adapter.v1","type":"assistant_delta","messageId":"oversize","role":"assistant","content":"` +
					strings.Repeat("x", adapterproto.MaxFrameBytes) + `"}` + "\n"
				return stream.writeRaw([]byte(value))
			case "malformed":
				return stream.writeRaw([]byte("{malformed\n"))
			case "gap":
				for index := 0; index < 4096; index++ {
					if err := stream.Write(adapterproto.Frame{
						Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
						Status:   adapterproto.StatusRunning,
						Metadata: metadata(map[string]any{"index": index}),
					}); err != nil {
						return err
					}
				}
			}
			if strings.HasPrefix(frame.Content, "permission") {
				pendingPermission = "mock-permission-" + jsonNumber(turn)
				if err := stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest,
					ID: pendingPermission,
					Permission: &adapterproto.Permission{
						Kind: "mock-tool", Summary: "Allow deterministic mock tool?",
						Options: []string{"allow", "deny"},
					},
				}); err != nil {
					return err
				}
				continue
			}
			if strings.HasPrefix(frame.Content, "input") {
				pendingInput = "mock-input-" + jsonNumber(turn)
				if err := stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindInputRequest,
					ID:    pendingInput,
					Input: &adapterproto.Input{Prompt: "Provide deterministic mock input"},
				}); err != nil {
					return err
				}
				continue
			}
			if err := emitMockTurn(stream, turn, frame.Content, config.Fault == "duplicate"); err != nil {
				return err
			}
		case adapterproto.KindPermissionResponse:
			if pendingPermission == "" || frame.ResponseTo != pendingPermission {
				return errors.New("unexpected mock permission response")
			}
			pendingPermission = ""
			if err := emitMockTurn(stream, turn, "permission:"+frame.Permission.Choice, false); err != nil {
				return err
			}
		case adapterproto.KindInputResponse:
			if pendingInput == "" || frame.ResponseTo != pendingInput {
				return errors.New("unexpected mock input response")
			}
			pendingInput = ""
			if err := emitMockTurn(stream, turn, "input:"+frame.Input.Value, false); err != nil {
				return err
			}
		case adapterproto.KindHeartbeat:
			if frame.ID != "" {
				if err := stream.Write(adapterproto.Frame{
					Protocol: adapterproto.Version, Type: adapterproto.KindAck,
					ID: "mock-heartbeat-ack", ResponseTo: frame.ID,
				}); err != nil {
					return err
				}
			}
		case adapterproto.KindCancel:
			_ = stream.Write(adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
				Status: adapterproto.StatusCancelled,
			})
			return nil
		default:
			return errors.New("unsupported mock controller frame")
		}
	}
}

func emitMockTurn(stream *Stream, turn int, prompt string, duplicate bool) error {
	messageID := "mock-message-" + jsonNumber(turn)
	toolID := "mock-tool-" + jsonNumber(turn)
	frames := []adapterproto.Frame{
		{
			Protocol: adapterproto.Version, Type: adapterproto.KindAssistantDelta,
			Role: adapterproto.RoleAssistant, MessageID: messageID, Content: "mock:",
		},
		{
			Protocol: adapterproto.Version, Type: adapterproto.KindAssistantDelta,
			Role: adapterproto.RoleAssistant, MessageID: messageID, Content: bounded(prompt, 4096),
		},
		{
			Protocol: adapterproto.Version, Type: adapterproto.KindToolStart,
			Role: adapterproto.RoleAssistant, ToolCallID: toolID,
			ToolName: "mock", Summary: "deterministic tool",
		},
		{
			Protocol: adapterproto.Version, Type: adapterproto.KindToolResult,
			Role: adapterproto.RoleTool, ToolCallID: toolID, Result: "ok",
		},
		{
			Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
			Role: adapterproto.RoleAssistant, MessageID: messageID,
			Content: bounded("mock:"+prompt, adapterproto.MaxContentBytes),
		},
	}
	for _, frame := range frames {
		if err := stream.Write(frame); err != nil {
			return err
		}
		if duplicate {
			if err := stream.Write(frame); err != nil {
				return err
			}
		}
	}
	return stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusIdle, ResumeState: mockResumeState(turn),
	})
}

func mockResumeState(turn int) string {
	return boundedJSON(map[string]any{"driver": "mock", "turn": turn}, adapterproto.MaxResumeStateBytes)
}

func mockResumeTurn(raw string) (int, error) {
	var state struct {
		Driver string `json:"driver"`
		Turn   int    `json:"turn"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.Driver != "mock" || state.Turn < 0 {
		return 0, errors.New("mock resume state is invalid")
	}
	return state.Turn, nil
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
