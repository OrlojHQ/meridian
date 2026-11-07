package harnessadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
)

const (
	mockCoordinatePrefix       = "coordinate "
	mockCoordinateNoWaitPrefix = "coordinate-nowait "
	mockCoordinatorMaxHelpers  = 4
	mockCoordinatorWait        = 10 * time.Minute
)

// mockCoordination is a parsed scripted coordination prompt:
// "coordinate HARNESS COUNT TASK" spawns COUNT helpers, waits for all of
// them, and summarizes; "coordinate-nowait ..." ends the turn after spawning
// so reports arrive through Meridian's wake prompt instead.
type mockCoordination struct {
	harness string
	count   int
	task    string
	wait    bool
}

func parseMockCoordination(content string) (mockCoordination, bool) {
	var rest string
	var wait bool
	switch {
	case strings.HasPrefix(content, mockCoordinatePrefix):
		rest, wait = strings.TrimPrefix(content, mockCoordinatePrefix), true
	case strings.HasPrefix(content, mockCoordinateNoWaitPrefix):
		rest = strings.TrimPrefix(content, mockCoordinateNoWaitPrefix)
	default:
		return mockCoordination{}, false
	}
	fields := strings.SplitN(strings.TrimSpace(rest), " ", 3)
	if len(fields) != 3 {
		return mockCoordination{}, false
	}
	count, err := strconv.Atoi(fields[1])
	if err != nil || count < 1 || count > mockCoordinatorMaxHelpers || strings.TrimSpace(fields[2]) == "" {
		return mockCoordination{}, false
	}
	return mockCoordination{harness: fields[0], count: count, task: strings.TrimSpace(fields[2]), wait: wait}, true
}

// runMockCoordination drives meridian-coord exactly as an agent would and
// streams each call as a tool event.
func runMockCoordination(ctx context.Context, stream *Stream, turn int, plan mockCoordination) error {
	messageID := "mock-message-" + jsonNumber(turn)
	call := 0
	invoke := func(arguments []string, stdin string) (coordproto.Response, error) {
		call++
		toolID := fmt.Sprintf("mock-coord-%d-%d", turn, call)
		if err := stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindToolStart,
			Role: adapterproto.RoleAssistant, ToolCallID: toolID, ToolName: "meridian-coord",
			Summary: bounded("meridian-coord "+strings.Join(arguments, " "), adapterproto.MaxToolSummaryBytes),
		}); err != nil {
			return coordproto.Response{}, err
		}
		response, output := execCoord(ctx, arguments, stdin)
		if err := stream.Write(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindToolResult,
			Role: adapterproto.RoleTool, ToolCallID: toolID,
			Result: bounded(output, adapterproto.MaxResultBytes),
		}); err != nil {
			return coordproto.Response{}, err
		}
		return response, nil
	}
	var links []string
	var summary strings.Builder
	for index := 1; index <= plan.count; index++ {
		name := fmt.Sprintf("helper-%d", index)
		prompt := fmt.Sprintf("%s (part %d of %d)", plan.task, index, plan.count)
		response, err := invoke([]string{"spawn", "--name", name, "--harness", plan.harness}, prompt)
		if err != nil {
			return err
		}
		if !response.OK || response.Link == nil {
			fmt.Fprintf(&summary, "Could not spawn %s: %s\n", name, describeFailure(response))
			continue
		}
		links = append(links, response.Link.ID)
	}
	switch {
	case len(links) == 0:
		summary.WriteString("No helpers started.")
	case !plan.wait:
		fmt.Fprintf(&summary, "Spawned %d helpers; Meridian will deliver their reports.", len(links))
	default:
		arguments := append([]string{"wait", "--all", "--timeout", mockCoordinatorWait.String()}, links...)
		response, err := invoke(arguments, "")
		if err != nil {
			return err
		}
		if !response.OK {
			fmt.Fprintf(&summary, "Waiting failed: %s\n", describeFailure(response))
			break
		}
		if response.TimedOut {
			summary.WriteString("Timed out waiting for helpers.\n")
		}
		fmt.Fprintf(&summary, "Lead summary of %d helper reports:\n", len(response.Reports))
		for _, report := range response.Reports {
			fmt.Fprintf(&summary, "- %s (%s): %s\n", report.Name, report.Status, strings.TrimSpace(report.Summary))
		}
	}
	if err := stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
		Role: adapterproto.RoleAssistant, MessageID: messageID,
		Content: bounded(strings.TrimSpace(summary.String()), adapterproto.MaxContentBytes),
	}); err != nil {
		return err
	}
	return stream.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusIdle, ResumeState: mockResumeState(turn),
	})
}

func execCoord(ctx context.Context, arguments []string, stdin string) (coordproto.Response, string) {
	path, err := exec.LookPath("meridian-coord")
	if err != nil {
		return coordproto.Fail(coordproto.CodeCoordinationDisabled, "meridian-coord is not installed"),
			"meridian-coord is not installed"
	}
	callCtx, cancel := context.WithTimeout(ctx, mockCoordinatorWait+time.Minute)
	defer cancel()
	command := exec.CommandContext(callCtx, path, arguments...)
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	_ = command.Run()
	var response coordproto.Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "meridian-coord produced no response"
		}
		return coordproto.Fail(coordproto.CodeInternal, message), message
	}
	return response, stdout.String()
}

func describeFailure(response coordproto.Response) string {
	if response.Error == nil {
		return "unknown error"
	}
	return response.Error.Code + ": " + response.Error.Message
}
