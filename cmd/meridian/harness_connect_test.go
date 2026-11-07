package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func harnessTestRoot(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newHarnessCommand(config))
	return root
}

func TestHarnessConnectCustomEndpointReadsKeyFromStdin(t *testing.T) {
	const key = "vllm-key-canary"
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"c1","provider":"openai-compatible","name":"DGX","baseUrl":"http://192.168.1.20:8000","defaultModel":"qwen3-coder","allowPrivateHttp":true,"createdAt":"2026-11-07T00:00:00Z","resourceVersion":1,"revoked":false}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{server: server.URL, tokenFile: testAPITokenFile(t), stdout: &output, stdin: strings.NewReader(key + "\n")}
	command := harnessTestRoot(config)
	command.SetArgs([]string{"harness", "connect", "openai-compatible", "--name", "DGX", "--base-url", "http://192.168.1.20:8000/v1", "--allow-private-http", "--model", "qwen3-coder", "--stdin"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if body["apiKey"] != key || body["baseUrl"] != "http://192.168.1.20:8000/v1" || body["allowPrivateHttp"] != true || body["defaultModel"] != "qwen3-coder" || body["provider"] != "openai-compatible" {
		t.Fatalf("unexpected request %v", body)
	}
	if strings.Contains(output.String(), key) {
		t.Fatal("API key printed")
	}
}

func TestHarnessConnectFlagRules(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		raw, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(raw), `"apiKey":""`) {
			t.Errorf("no-key request carried a key: %s", raw)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"id":"c1","provider":"anthropic-compatible","name":"x","createdAt":"2026-11-07T00:00:00Z","resourceVersion":1,"revoked":false}`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"openai-compatible", "--stdin"}, false},
		{[]string{"openai", "--base-url", "https://x.example", "--stdin"}, false},
		{[]string{"openai", "--no-api-key"}, false},
		{[]string{"openai-compatible", "--base-url", "https://x.example"}, false},
		{[]string{"openai-compatible", "--base-url", "https://x.example", "--stdin", "--no-api-key"}, false},
		{[]string{"ollama", "--stdin"}, false},
		{[]string{"anthropic-compatible", "--base-url", "https://litellm.example", "--no-api-key"}, true},
	} {
		config := &cliConfig{server: server.URL, tokenFile: testAPITokenFile(t), stdout: io.Discard, stdin: strings.NewReader("ignored")}
		command := harnessTestRoot(config)
		command.SetArgs(append([]string{"harness", "connect"}, tc.args...))
		if err := command.Execute(); (err == nil) != tc.ok {
			t.Errorf("%v: %v", tc.args, err)
		}
	}
	if requests != 1 {
		t.Fatalf("sent %d requests", requests)
	}
}
