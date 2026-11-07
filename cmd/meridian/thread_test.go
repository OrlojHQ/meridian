package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
	"github.com/spf13/cobra"
)

func TestThreadCreateJSONReadsPromptFromStdin(t *testing.T) {
	var received, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		value, _ := io.ReadAll(request.Body)
		received = string(value)
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, `{"thread":{"id":"thread-1","capsuleId":"capsule-1","state":"active","harness":"mock","protocol":"meridian.adapter.v1","structuredSupported":true,"encryptedAtRest":true,"messageCount":1,"encryptedBytes":64,"createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":3},"currentRun":{"id":"run-1","capsuleId":"capsule-1","harness":"mock","state":"Running","createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":2},"messageId":"message-1"}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	tokenFile := testAPITokenFile(t)
	tokenValue, err := apiauth.ReadTokenFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	config := &cliConfig{
		server: server.URL, tokenFile: tokenFile,
		json: true, stdout: &output, stdin: strings.NewReader("stdin secret"),
	}
	command := threadTestRoot(config)
	command.SetArgs([]string{
		"thread", "create", "--capsule", "capsule-1", "--harness", "mock",
		"--prompt-stdin", "--start", "--idempotency-key", "create-key",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, `"firstMessage":"stdin secret"`) ||
		!strings.Contains(received, `"start":true`) {
		t.Fatalf("create request = %s", received)
	}
	if authorization != "Bearer "+tokenValue {
		t.Fatalf("authorization header was not sourced from token file")
	}
	if !strings.Contains(output.String(), `"id":"thread-1"`) ||
		!strings.Contains(output.String(), `"messageId":"message-1"`) {
		t.Fatalf("JSON output = %s", output.String())
	}
}

func TestThreadSpawnParsesProjectPromptAndName(t *testing.T) {
	var path, received, idempotency string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		value, _ := io.ReadAll(request.Body)
		received = string(value)
		idempotency = request.Header.Get("Idempotency-Key")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(writer, `{"id":"intent-1","projectId":"project-1","capsuleId":"capsule-1","capsuleName":"named","threadId":"thread-1","runId":"run-1","messageId":"message-1","harness":"mock","state":"provisioning","createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":1}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{
		server: server.URL, tokenFile: testAPITokenFile(t), json: true,
		stdout: &output, stdin: strings.NewReader("spawn prompt"),
	}
	command := threadTestRoot(config)
	command.SetArgs([]string{
		"thread", "spawn", "project-1", "--harness", "mock",
		"--prompt-stdin", "--name", "named", "--idempotency-key", "spawn-key",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if path != "/projects/project-1/threads" ||
		!strings.Contains(received, `"prompt":"spawn prompt"`) ||
		!strings.Contains(received, `"name":"named"`) ||
		idempotency != "spawn-key" {
		t.Fatalf("spawn request = %s %s key=%q", path, received, idempotency)
	}
	if !strings.Contains(output.String(), `"id":"intent-1"`) ||
		!strings.Contains(output.String(), `"threadId":"thread-1"`) {
		t.Fatalf("spawn JSON = %s", output.String())
	}
}

func TestThreadDeleteRequiresExplicitConfirmationAndVersion(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	config := &cliConfig{
		server: server.URL, tokenFile: testAPITokenFile(t),
		stdout: io.Discard, stdin: strings.NewReader(""),
	}
	command := threadTestRoot(config)
	command.SetArgs([]string{"thread", "delete", "thread-1", "--expected-version", "2"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "confirm-crypto-shred") {
		t.Fatalf("delete without confirmation error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("delete sent %d requests without confirmation", requests)
	}
	command = threadTestRoot(config)
	command.SetArgs([]string{"thread", "send", "thread-1", "--prompt", "value"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "expected-version") {
		t.Fatalf("send without version error = %v", err)
	}
}

func testAPITokenFile(t *testing.T) string {
	t.Helper()
	path := apiauth.DefaultTokenPath(t.TempDir())
	if _, err := apiauth.OpenOrCreateToken(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultAPITokenFileUsesEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.token")
	t.Setenv("MERIDIAN_TOKEN_FILE", path)
	if got := defaultAPITokenFile(); got != path {
		t.Fatalf("default token file = %q, want %q", got, path)
	}
}

func TestThreadDeferredCreateStartAndCancelFailureCLI(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/capsules/capsule-1/threads":
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"thread":{"id":"thread-1","capsuleId":"capsule-1","state":"active","harness":"mock","protocol":"meridian.adapter.v1","structuredSupported":true,"encryptedAtRest":true,"messageCount":1,"encryptedBytes":64,"createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":2},"messageId":"message-1"}`)
		case "/threads/thread-1/start":
			writer.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(writer, `{"thread":{"id":"thread-1","capsuleId":"capsule-1","state":"active","harness":"mock","protocol":"meridian.adapter.v1","structuredSupported":true,"encryptedAtRest":true,"messageCount":1,"encryptedBytes":64,"createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":3},"currentRun":{"id":"run-1","capsuleId":"capsule-1","harness":"mock","state":"Running","createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":2},"messageId":"message-1"}`)
		case "/threads/thread-1/cancel":
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(writer, `{"error":{"code":"internal_error","message":"internal server error"}}`)
		}
	}))
	defer server.Close()
	config := &cliConfig{
		server: server.URL, tokenFile: testAPITokenFile(t),
		stdout: io.Discard, stdin: strings.NewReader(""),
	}
	command := threadTestRoot(config)
	command.SetArgs([]string{
		"thread", "create", "--capsule", "capsule-1", "--harness", "mock",
		"--prompt", "deferred", "--idempotency-key", "create",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	command = threadTestRoot(config)
	command.SetArgs([]string{
		"thread", "start", "thread-1", "--expected-version", "2", "--idempotency-key", "start",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	command = threadTestRoot(config)
	command.SetArgs([]string{
		"thread", "cancel", "thread-1", "--expected-version", "3", "--idempotency-key", "cancel",
	})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "internal_error") {
		t.Fatalf("cancel runtime failure = %v", err)
	}
	if strings.Join(requests, ",") !=
		"POST /capsules/capsule-1/threads,POST /threads/thread-1/start,POST /threads/thread-1/cancel" {
		t.Fatalf("requests = %v", requests)
	}
}

func TestAdminTranscriptKeyRotateCommand(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	key, err := transcripts.OpenOrCreateKey(transcripts.DefaultKeyPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	key.Zero()
	var output bytes.Buffer
	config := &cliConfig{stdout: &output, stdin: strings.NewReader("")}
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newAdminCommand(config))
	root.SetArgs([]string{
		"admin", "transcript-key", "rotate", "--data-dir", dataDir, "--yes",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"key":`) || !strings.Contains(output.String(), "threads=0") {
		t.Fatalf("rotation output = %q", output.String())
	}
}

func threadTestRoot(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newThreadCommand(config))
	return root
}

func sseBlock(sequence int64, role, messageKind, kind, content, event string) string {
	block := `{"id":"block-` + strconv.FormatInt(sequence, 10) + `","messageId":"message-` +
		strconv.FormatInt(sequence, 10) + `","messageSequence":` + strconv.FormatInt(sequence, 10) +
		`,"sequence":0,"role":"` + role + `","messageKind":"` + messageKind + `","kind":"` + kind + `"`
	if content != "" {
		block += `,"content":` + strconv.Quote(content)
	}
	if event != "" {
		block += `,"event":` + event
	}
	return "data: " + block + `,"createdAt":"2026-08-24T12:00:00Z"}` + "\n\n"
}

func statusBlock(sequence int64, status string) string {
	return sseBlock(sequence, "system", "status", "json", "", `{"type":"status","status":"`+status+`"}`)
}

func TestStreamThreadSSEUntilIdleIgnoresStartupIdle(t *testing.T) {
	stream := sseBlock(1, "user", "prompt", "text", "inspect", "") +
		statusBlock(2, "running") + statusBlock(3, "idle") + statusBlock(4, "running") +
		sseBlock(5, "assistant", "response", "text", "done",
			`{"type":"assistant_message","messageId":"answer"}`) +
		statusBlock(6, "idle") +
		sseBlock(7, "assistant", "response", "text", "after idle", `{"type":"assistant_message"}`)
	var output bytes.Buffer
	if err := streamThreadSSE(strings.NewReader(stream), &cliConfig{stdout: &output}, true); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "inspect\n2\tstatus\n3\tstatus\n4\tstatus\ndone\n6\tstatus\n" {
		t.Fatalf("until-idle output = %q", got)
	}
	output.Reset()
	if err := streamThreadSSE(strings.NewReader(stream), &cliConfig{stdout: &output}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(output.String(), "\nafter idle\n") {
		t.Fatalf("continuous output = %q", output.String())
	}
}

func TestStreamThreadSSEUntilIdleWaitsForTurnAfterLatestPrompt(t *testing.T) {
	stream := sseBlock(1, "assistant", "response", "text", "earlier", `{"type":"assistant_message"}`) +
		sseBlock(2, "user", "prompt", "text", "next", "") +
		statusBlock(3, "idle") +
		sseBlock(4, "system", "status", "json", "",
			`{"type":"error","code":"auth_required","retryable":false}`) +
		statusBlock(5, "idle") + statusBlock(6, "running")
	var output bytes.Buffer
	if err := streamThreadSSE(strings.NewReader(stream), &cliConfig{stdout: &output}, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "6\t") || !strings.HasSuffix(output.String(), "5\tstatus\n") {
		t.Fatalf("until-idle output = %q", output.String())
	}
	output.Reset()
	end := sseBlock(1, "user", "prompt", "text", "go", "") +
		sseBlock(2, "system", "status", "json", "", `{"type":"end","reason":"exited"}`) + statusBlock(3, "idle")
	if err := streamThreadSSE(strings.NewReader(end), &cliConfig{stdout: &output}, true); err != nil {
		t.Fatal(err)
	}
	if output.String() != "go\n2\tend\n" {
		t.Fatalf("end output = %q", output.String())
	}
}

func TestThreadFollowUntilIdleExitsOnOpenStream(t *testing.T) {
	var after string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		after = request.URL.Query().Get("after")
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, sseBlock(4, "user", "prompt", "text", "again", "")+
			sseBlock(5, "assistant", "response", "text", "answer", `{"type":"assistant_message"}`)+
			statusBlock(6, "idle"))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{server: server.URL, tokenFile: testAPITokenFile(t), stdout: &output}
	command := threadTestRoot(config)
	command.SetArgs([]string{"thread", "follow", "thread-1", "--after", "3", "--until-idle"})
	done := make(chan error, 1)
	go func() { done <- command.Execute() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follow --until-idle did not exit after the turn went idle")
	}
	if after != "3" || output.String() != "again\nanswer\n6\tstatus\n" {
		t.Fatalf("after = %q, output = %q", after, output.String())
	}
}

func TestBoardListsRowsAndAsksForEveryHelper(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query = request.URL.Path + "?" + request.URL.RawQuery
		writer.Header().Set("Content-Type", "application/json")
		capsule := func(id, name, role string) string {
			return `{"id":"` + id + `","projectId":"project-1","timelineId":"timeline-1","name":"` + name +
				`","harness":"mock","role":"` + role + `","state":"Ready","desiredState":"Ready","restoreComplete":true,` +
				`"createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":1}`
		}
		_, _ = io.WriteString(writer, `{"truncated":false,"items":[`+
			`{"capsule":`+capsule("capsule-2", "helper-capsule", "sub")+`,"status":"needs_you","needsYou":"permission",`+
			`"since":"2026-08-24T12:01:00Z","surfaced":true,"link":{"id":"link-1","grantId":"grant-1",`+
			`"parentThreadId":"thread-1","parentCapsuleId":"capsule-1","childThreadId":"thread-2",`+
			`"childCapsuleId":"capsule-2","childIntentId":"intent-2","depth":1,"name":"tests","harness":"mock",`+
			`"state":"active","phase":"working","reportSeq":0,"reportDeliveredSeq":0,"messagesForwarded":0,`+
			`"operatorMessages":0,"createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":1}},`+
			`{"capsule":`+capsule("capsule-1", "lead", "main")+`,"status":"working","since":"2026-08-24T12:00:00Z",`+
			`"surfaced":false,"childRollup":{"total":2,"working":1,"idle":0,"done":0,"needsYou":1,"failed":0,"archived":0}}]}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{server: server.URL, tokenFile: testAPITokenFile(t), stdout: &output}
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newBoardCommand(config))
	root.SetArgs([]string{"board", "--all", "--project", "project-1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if query != "/board?projectId=project-1&expand=all" && query != "/board?expand=all&projectId=project-1" {
		t.Fatalf("board request = %s", query)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "needs_you(permission)\ttests (helper)\tmock\tcapsule=capsule-2") ||
		!strings.HasPrefix(lines[1], "working\tlead\tmock\tcapsule=capsule-1") ||
		!strings.Contains(lines[1], "helpers=2 working=1 idle=0 done=0 needs_you=1 failed=0 archived=0") {
		t.Fatalf("board output =\n%s", output.String())
	}

	root.SetArgs([]string{"board", "--all", "--expand", "none"})
	if err := root.Execute(); err == nil {
		t.Fatal("--all with --expand was accepted")
	}
}

func TestLeadArchiveAfterAndCapsulePromote(t *testing.T) {
	var path, received, idempotency string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		value, _ := io.ReadAll(request.Body)
		received = string(value)
		idempotency = request.Header.Get("Idempotency-Key")
		writer.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(path, "/promote") {
			_, _ = io.WriteString(writer, `{"id":"capsule-2","projectId":"project-1","timelineId":"timeline-2",`+
				`"name":"helper","role":"main","promotedAt":"2026-08-24T12:05:00Z","state":"Ready",`+
				`"desiredState":"Ready","restoreComplete":true,"createdAt":"2026-08-24T12:00:00Z",`+
				`"updatedAt":"2026-08-24T12:05:00Z","resourceVersion":5}`)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(writer, `{"id":"intent-1","projectId":"project-1","capsuleId":"capsule-1","capsuleName":"lead","threadId":"thread-1","runId":"run-1","messageId":"message-1","harness":"mock","state":"provisioning","createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":1}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{
		server: server.URL, tokenFile: testAPITokenFile(t), json: true,
		stdout: &output, stdin: strings.NewReader("lead prompt"),
	}
	command := threadTestRoot(config)
	command.SetArgs([]string{
		"thread", "spawn", "project-1", "--harness", "mock", "--prompt-stdin", "--lead", "--archive-after", "0",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, `"archiveAfterSeconds":0`) {
		t.Fatalf("spawn request = %s", received)
	}
	command = threadTestRoot(config)
	command.SetArgs([]string{"thread", "spawn", "project-1", "--harness", "mock", "--prompt", "x", "--archive-after", "1h"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--archive-after requires --lead") {
		t.Fatalf("--archive-after without --lead = %v", err)
	}

	output.Reset()
	config.json = false
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	capsule := &cobra.Command{Use: "capsule"}
	capsule.AddCommand(newCapsulePromoteCommand(config))
	root.AddCommand(capsule)
	root.SetArgs([]string{"capsule", "promote", "capsule-2", "--expected-version", "4", "--detach", "--idempotency-key", "p"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if path != "/capsules/capsule-2/promote" || idempotency != "p" ||
		!strings.Contains(received, `"expectedResourceVersion":4`) || !strings.Contains(received, `"detach":true`) {
		t.Fatalf("promote request = %s %s key=%q", path, received, idempotency)
	}
	if line := output.String(); !strings.Contains(line, "role=main\tpromoted") || strings.Contains(line, "lead=") {
		t.Fatalf("promote output = %q", line)
	}
}
