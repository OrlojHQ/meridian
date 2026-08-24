package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
