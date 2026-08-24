package capsuleproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

func TestStructuredSessionNegotiationConcurrentSendReplayAndEnd(t *testing.T) {
	client := structuredTestClient(t, "echo", 64)
	start := adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStart,
		ID: "start-1", SessionID: "session-1",
	}
	status, err := client.StartStructured(context.Background(), StructuredStartRequest{
		RunID: "structured-1", Harness: "structured", Frame: start,
	})
	if err != nil || !status.StructuredStatus() {
		t.Fatalf("start = %#v, %v", status, err)
	}
	waitStructuredReady(t, client, "structured-1")

	const messages = 12
	var wait sync.WaitGroup
	errs := make(chan error, messages)
	for index := 0; index < messages; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id := "message-" + strconv.Itoa(index)
			errs <- client.SendStructured(context.Background(), "structured-1", adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
				ID: id, SessionID: "session-1", Role: adapterproto.RoleUser, Content: id,
			})
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	events := waitStructuredEvents(t, client, "structured-1", 0, messages+1)
	seen := make(map[uint64]struct{}, len(events.Events))
	for _, event := range events.Events {
		if event.Sequence == 0 || event.Sequence != event.Frame.Cursor {
			t.Fatalf("invalid cursor: %#v", event)
		}
		if _, duplicate := seen[event.Sequence]; duplicate {
			t.Fatalf("duplicate sequence %d", event.Sequence)
		}
		seen[event.Sequence] = struct{}{}
	}
	replay, err := client.StructuredEvents(
		context.Background(), "structured-1", events.NextCursor, 10*time.Millisecond,
	)
	if err != nil || len(replay.Events) != 0 {
		t.Fatalf("duplicate replay = %#v, %v", replay, err)
	}

	if err := client.SendStructured(context.Background(), "structured-1", adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
		ID: "finish", SessionID: "session-1", Role: adapterproto.RoleUser, Content: "finish",
	}); err != nil {
		t.Fatal(err)
	}
	final := waitStructuredTerminal(t, client, "structured-1")
	if final.State != RunSucceeded {
		t.Fatalf("final status = %#v", final)
	}
}

func TestStructuredReplayGapCrashAndCancellation(t *testing.T) {
	t.Run("gap", func(t *testing.T) {
		client := structuredTestClient(t, "echo", 3)
		startStructuredTestRun(t, client, "gap-run")
		for index := 0; index < 8; index++ {
			id := "gap-" + strconv.Itoa(index)
			if err := client.SendStructured(context.Background(), "gap-run", adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
				ID: id, SessionID: "session-1", Role: adapterproto.RoleUser, Content: id,
			}); err != nil {
				t.Fatal(err)
			}
		}
		events := waitStructuredEvents(t, client, "gap-run", 0, 1)
		if !events.Gap || events.AvailableFrom <= 1 || len(events.Events) > 3 {
			t.Fatalf("gap response = %#v", events)
		}
		_, _ = client.CancelStructured(context.Background(), "gap-run")
		waitStructuredTerminal(t, client, "gap-run")
	})

	t.Run("crash", func(t *testing.T) {
		client := structuredTestClient(t, "crash", 16)
		startStructuredTestRun(t, client, "crash-run")
		status := waitStructuredTerminal(t, client, "crash-run")
		if status.State != RunFailed || status.Failure == "" {
			t.Fatalf("crash status = %#v", status)
		}
	})

	t.Run("incompatible hello", func(t *testing.T) {
		client := structuredTestClient(t, "incompatible", 16)
		_, err := client.StartStructured(context.Background(), StructuredStartRequest{
			RunID: "incompatible-run", Harness: "structured", Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindStart,
				ID: "start-1", SessionID: "session-1",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		status := waitStructuredTerminal(t, client, "incompatible-run")
		if status.State != RunFailed {
			t.Fatalf("incompatible status = %#v", status)
		}
	})

	t.Run("hang cancel and process group", func(t *testing.T) {
		client := structuredTestClient(t, "group", 16)
		startStructuredTestRun(t, client, "group-run")
		events := waitStructuredEvents(t, client, "group-run", 0, 2)
		var childPID int
		for _, event := range events.Events {
			if event.Frame.Type == adapterproto.KindStatus && len(event.Frame.Metadata) > 0 {
				var metadata struct {
					ChildPID int `json:"childPid"`
				}
				_ = json.Unmarshal(event.Frame.Metadata, &metadata)
				childPID = metadata.ChildPID
			}
		}
		if childPID <= 0 {
			t.Fatalf("child PID was not reported: %#v", events)
		}
		if _, err := client.CancelStructured(context.Background(), "group-run"); err != nil {
			t.Fatal(err)
		}
		status := waitStructuredTerminal(t, client, "group-run")
		if status.State != RunCancelled {
			t.Fatalf("cancel status = %#v", status)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			err := syscall.Kill(childPID, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("adapter child process %d survived cancellation: %v", childPID, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func TestStructuredAuthenticationAndVersion(t *testing.T) {
	client := structuredTestClient(t, "echo", 16)
	startStructuredTestRun(t, client, "auth-run")
	httpClient := client.http
	request, err := http.NewRequestWithContext(
		context.Background(), http.MethodGet,
		client.baseURL+"/v1/structured/sessions/auth-run", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer wrong-token")
	request.Header.Set(VersionHeader, "meridian.capsule.v0")
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 426 {
		t.Fatalf("version mismatch status = %d", response.StatusCode)
	}
	request, err = http.NewRequestWithContext(
		context.Background(), http.MethodGet,
		client.baseURL+"/v1/structured/sessions/auth-run", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer stale-token")
	request.Header.Set(VersionHeader, Version)
	response, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale token status = %d", response.StatusCode)
	}
}

func TestStructuredEventValidationRejectsAbuse(t *testing.T) {
	validFrame := func(sequence uint64) adapterproto.Frame {
		return adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
			Status: adapterproto.StatusRunning, Cursor: sequence,
		}
	}
	valid := StructuredEventsResponse{
		Events: []StructuredEvent{
			{Sequence: 2, Frame: validFrame(2)},
			{Sequence: 3, Frame: validFrame(3)},
		},
		NextCursor: 3,
	}
	if err := validateStructuredEvents(valid, 1); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	tests := map[string]StructuredEventsResponse{
		"duplicate": {
			Events: []StructuredEvent{
				{Sequence: 2, Frame: validFrame(2)},
				{Sequence: 2, Frame: validFrame(2)},
			},
			NextCursor: 2,
		},
		"out of order": {
			Events: []StructuredEvent{
				{Sequence: 3, Frame: validFrame(3)},
				{Sequence: 2, Frame: validFrame(2)},
			},
			NextCursor: 3,
		},
		"cursor mismatch": {
			Events:     []StructuredEvent{{Sequence: 2, Frame: validFrame(3)}},
			NextCursor: 2,
		},
		"controller frame": {
			Events: []StructuredEvent{{Sequence: 2, Frame: adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
				ID: "message", SessionID: "session", Role: adapterproto.RoleUser,
				Content: "not adapter output", Cursor: 2,
			}}},
			NextCursor: 2,
		},
		"invalid gap": {
			Gap: true, AvailableFrom: 1, NextCursor: 3,
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateStructuredEvents(response, 1); err == nil {
				t.Fatal("invalid structured response accepted")
			}
		})
	}
}

func TestStructuredReplayEvictsSingleOversizedEventWithoutPanic(t *testing.T) {
	session := &structuredSession{
		eventLimit: 1, outputLimit: 1, notify: make(chan struct{}),
	}
	session.addEvent(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
		Status: adapterproto.StatusRunning,
	})
	response := session.eventsAfter(0)
	if !response.Gap || response.NextCursor != 1 || response.AvailableFrom != 2 ||
		len(response.Events) != 0 {
		t.Fatalf("fully evicted replay = %#v", response)
	}
}

func TestStructuredAdapterHelper(t *testing.T) {
	mode := os.Getenv("MERIDIAN_TEST_ADAPTER")
	if mode == "" {
		return
	}
	decoder := adapterproto.NewDecoder(os.Stdin)
	encoder := adapterproto.NewEncoder(os.Stdout)
	hello, err := decoder.Decode()
	if err != nil || hello.Type != adapterproto.KindHello {
		os.Exit(20)
	}
	if mode == "incompatible" {
		_, _ = fmt.Fprintln(os.Stdout, `{"protocol":"meridian.adapter.v2","type":"hello","id":"peer","capabilities":{}}`)
		os.Exit(0)
	}
	if err := encoder.Encode(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindHello, ID: "peer-hello",
		Capabilities: &adapterproto.Capabilities{
			Resume: true, ToolEvents: true, Permissions: true,
			InputRequests: true, Heartbeat: true, OpaqueMetadata: true,
		},
	}); err != nil {
		os.Exit(21)
	}
	start, err := decoder.Decode()
	if err != nil || (start.Type != adapterproto.KindStart && start.Type != adapterproto.KindResume) {
		os.Exit(22)
	}
	if mode == "crash" {
		os.Exit(23)
	}
	if mode == "group" {
		child := exec.Command("/bin/sleep", "30")
		if child.Start() != nil {
			os.Exit(24)
		}
		metadata, _ := json.Marshal(map[string]int{"childPid": child.Process.Pid})
		_ = encoder.Encode(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStatus,
			Status: adapterproto.StatusRunning, Metadata: metadata,
		})
		for {
			if _, err := decoder.Decode(); err != nil {
				os.Exit(0)
			}
		}
	}
	for {
		frame, err := decoder.Decode()
		if err != nil {
			os.Exit(0)
		}
		if frame.Type == adapterproto.KindCancel {
			os.Exit(0)
		}
		if frame.Type != adapterproto.KindUserMessage {
			continue
		}
		_ = encoder.Encode(adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
			Role: adapterproto.RoleAssistant, MessageID: "assistant-" + frame.ID,
			Content: "response",
		})
		if frame.Content == "finish" {
			_ = encoder.Encode(adapterproto.Frame{
				Protocol: adapterproto.Version, Type: adapterproto.KindEnd,
				Status: adapterproto.StatusSucceeded,
			})
			for {
				time.Sleep(time.Hour)
			}
		}
	}
}

func structuredTestClient(t *testing.T, mode string, eventLimit int) *Client {
	t.Helper()
	old, hadOld := os.LookupEnv("MERIDIAN_TEST_ADAPTER")
	if err := os.Setenv("MERIDIAN_TEST_ADAPTER", mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv("MERIDIAN_TEST_ADAPTER", old)
		} else {
			_ = os.Unsetenv("MERIDIAN_TEST_ADAPTER")
		}
	})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	encodedExecutable, _ := json.Marshal(executable)
	workspace := runWorkspace(t, fmt.Sprintf(`
version: v1
harnesses:
  - name: structured
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: %s
      arguments: ["-test.run=^TestStructuredAdapterHelper$"]
    workingDirectory: .
    pty: false
    timeout: 30s
`, encodedExecutable))
	server, err := NewServer(ServerConfig{
		Token: testToken, Workspace: workspace, EventLimit: eventLimit,
		OutputLimit: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := newAuthenticatedTestServer(t, server)
	client, err := NewClient(httpServer.URL, testToken, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func startStructuredTestRun(t *testing.T, client *Client, runID string) {
	t.Helper()
	_, err := client.StartStructured(context.Background(), StructuredStartRequest{
		RunID: runID, Harness: "structured", Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStart,
			ID: "start-1", SessionID: "session-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStructuredReady(t, client, runID)
}

func waitStructuredReady(t *testing.T, client *Client, runID string) StructuredStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := client.StructuredStatus(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Capabilities != nil {
			return status
		}
		if status.State == RunFailed || status.State == RunCancelled || time.Now().After(deadline) {
			t.Fatalf("structured session did not negotiate: %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitStructuredEvents(
	t *testing.T,
	client *Client,
	runID string,
	after uint64,
	minimum int,
) StructuredEventsResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := client.StructuredEvents(context.Background(), runID, after, 100*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if len(events.Events) >= minimum {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatalf("structured events did not arrive: %#v", events)
		}
	}
}

func waitStructuredTerminal(t *testing.T, client *Client, runID string) StructuredStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := client.StructuredStatus(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == RunSucceeded || status.State == RunFailed || status.State == RunCancelled {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("structured session did not finish: %#v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s StructuredStatusResponse) StructuredStatus() bool {
	return s.Protocol == adapterproto.Version && !s.PTY
}
