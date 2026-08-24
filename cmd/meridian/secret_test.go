package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSecretPutReadsStdinAndNeverPrintsValue(t *testing.T) {
	const plaintext = "cli-secret-canary"
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		value, _ := io.ReadAll(request.Body)
		requestBody = string(value)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"secret-1","name":"TOKEN","purpose":"harness_env","createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":1}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{
		server: server.URL, tokenFile: testAPITokenFile(t),
		stdout: &output, stdin: strings.NewReader(plaintext),
	}
	command := secretTestRoot(config)
	command.SetArgs([]string{
		"secret", "put", "TOKEN", "--purpose", "harness_env", "--stdin",
		"--idempotency-key", "put-secret",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestBody, plaintext) {
		t.Fatalf("request did not contain stdin value: %s", requestBody)
	}
	if strings.Contains(output.String(), plaintext) {
		t.Fatalf("secret value disclosed in output: %s", output.String())
	}
}

func TestSecretDeleteRequiresConfirmation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	config := &cliConfig{
		server: server.URL, tokenFile: testAPITokenFile(t),
		stdout: io.Discard, stdin: strings.NewReader(""),
	}
	command := secretTestRoot(config)
	command.SetArgs([]string{"secret", "delete", "TOKEN", "--expected-version", "1"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "confirm-crypto-shred") {
		t.Fatalf("delete error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("delete sent %d requests without confirmation", requests)
	}
}

func secretTestRoot(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newSecretCommand(config))
	return root
}
