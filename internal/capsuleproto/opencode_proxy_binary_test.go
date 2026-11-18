package capsuleproto

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

// TestPinnedOpenCodeUsesCompatibleConnection runs the OpenCode binary pinned
// in images/capsule-opencode against the config a connection writes. Docker is
// not required. Set MERIDIAN_OPENCODE_BINARY to that binary.
func TestPinnedOpenCodeUsesCompatibleConnection(t *testing.T) {
	if os.Getenv("MERIDIAN_OPENCODE_RUN") != "1" {
		t.Skip("MERIDIAN_OPENCODE_RUN=1 is not set")
	}
	binary := os.Getenv("MERIDIAN_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("MERIDIAN_OPENCODE_BINARY is not set")
	}
	caPEM, leaf := localGatewayCert(t)
	var recorded []map[string]any
	var recordedMu sync.Mutex
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["_path"] = r.URL.Path
		body["_auth"] = r.Header.Get("Authorization")
		recordedMu.Lock()
		recorded = append(recorded, body)
		recordedMu.Unlock()
		writeChatStream(w, body)
	})}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{leaf}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener)
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	gateway := "https://127.0.0.1:" + strconv.Itoa(port)

	token := strings.Repeat("ab", 32)
	imported := `{
  // Personal note.
  "model": "proxy/example-coder",
  "disabled_providers": ["openai"],
  "enabled_providers": ["proxy"],
  "providers": {
    "proxy": {
      "name": "Work proxy",
      "package": "@opencode/ai/providers/openai-compatible",
      "env": ["PROXY_API_KEY"],
      "settings": {"baseURL": "https://proxy.internal.example/v1", "includeUsage": true},
      "models": {"example-coder": {"name": "Example"}}
    }
  },
  "mcp": {"notes": {"type": "remote", "url": "https://mcp.internal.example/mcp", "headers": {"X-Feature": "{env:NOTES_TOKEN}"}}},
  "instructions": ["Reply with exactly pong."]
}`
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-binary", Bundle: harnesssetup.Bundle{Harness: "opencode", Files: []harnesssetup.File{{Path: ".config/opencode/opencode.jsonc", Content: imported}}}}
	env, err := s.prepareHarnessSetup(context.Background(), setup, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	env, err = s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
		Provider: "openai-compatible", URL: gateway, Token: token, Model: "example-coder",
		Efforts: []string{"low", "high"}, CA: caPEM,
	}, setup, "opencode", env)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), []byte(`{"model":"proxy/example-coder"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	processEnv := []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir()}
	for key, value := range env {
		processEnv = append(processEnv, key+"="+value)
	}

	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workspace
		cmd.Env = processEnv
		output, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(output), "pong") {
			t.Fatalf("run %v did not finish: %v\n%s", args, err, output)
		}
		return output
	}
	run("--standalone", "run", "--auto", "--format", "json", "reply with the word pong")
	recordedMu.Lock()
	first := append([]map[string]any(nil), recorded...)
	recorded = nil
	recordedMu.Unlock()
	var sawTools, sawModel, sawAuth bool
	for _, body := range first {
		if body["_path"] != "/v1/chat/completions" {
			t.Errorf("path = %v", body["_path"])
		}
		if body["model"] == "example-coder" {
			sawModel = true
		} else {
			t.Errorf("model = %v", body["model"])
		}
		if body["_auth"] == "Bearer "+token {
			sawAuth = true
		}
		if tools, _ := body["tools"].([]any); len(tools) > 0 {
			sawTools = true
		}
		if _, set := body["reasoning_effort"]; set {
			t.Errorf("default turn sent reasoning_effort %#v", body["reasoning_effort"])
		}
	}
	if !sawModel || !sawAuth || !sawTools {
		t.Fatalf("upstream requests missing model=%v auth=%v tools=%v", sawModel, sawAuth, sawTools)
	}
	run("--standalone", "run", "--auto", "--format", "json", "-m", "meridian/example-coder#high", "reply with the word pong")
	recordedMu.Lock()
	second := recorded
	recordedMu.Unlock()
	var sawEffort bool
	for _, body := range second {
		if body["model"] != "example-coder" || body["_auth"] != "Bearer "+token {
			t.Fatalf("effort turn left the connection: model=%v auth=%v", body["model"], body["_auth"])
		}
		if body["reasoning_effort"] == "high" {
			sawEffort = true
		}
	}
	if !sawEffort {
		t.Fatal("high effort was not sent as reasoning_effort")
	}

	acpCtx, acpCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer acpCancel()
	acp := exec.CommandContext(acpCtx, binary, "acp")
	acp.Dir = workspace
	acp.Env = processEnv
	stdin, err := acp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := acp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	acp.Stderr = nil
	if err := acp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = acp.Wait()
	}()
	reader := bufio.NewReader(stdout)
	write := func(value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stdin.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "meridian-test", "version": "0"},
	}})
	if _, err := readACPResult(t, reader, stdin, 1); err != nil {
		t.Fatal(err)
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
		"cwd": workspace, "mcpServers": []any{},
	}})
	created, err := readACPResult(t, reader, stdin, 2)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(created)
	if !strings.Contains(string(encoded), "meridian/example-coder") || !strings.Contains(string(encoded), `"low"`) || !strings.Contains(string(encoded), `"high"`) {
		t.Fatalf("ACP session did not select the connection model: %s", encoded)
	}
}

// TestOpenCodeGeneratedConfigOpensSession feeds Meridian's generated config
// to the OpenCode binary pinned in images/capsule-opencode. It checks that
// the connection's models are listed and that ACP initialize + session/new
// succeeds. The stub records a call but is not required to answer a turn.
func TestOpenCodeGeneratedConfigOpensSession(t *testing.T) {
	binary := os.Getenv("MERIDIAN_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("MERIDIAN_OPENCODE_BINARY is not set")
	}
	caPEM, leaf := localGatewayCert(t)
	called := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		http.Error(w, "model calls are out of scope", http.StatusBadGateway)
	})}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{leaf}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go server.Serve(listener)
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	gateway := "https://127.0.0.1:" + strconv.Itoa(port)

	token := strings.Repeat("ab", 32)
	imported := `{
  "model": "proxy/example-coder",
  "disabled_providers": ["opencode", "litellm"],
  "enabled_providers": ["proxy"],
  "providers": {
    "proxy": {
      "name": "Work proxy",
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": {"baseURL": "https://proxy.internal.example/v1", "includeUsage": true},
      "models": {
        "example-coder": {
          "name": "Example",
          "variants": {"high": {"reasoningEffort": "high"}, "low": {"reasoningEffort": "low"}}
        }
      }
    }
  }
}`
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-schema", Bundle: harnesssetup.Bundle{Harness: "opencode", Files: []harnesssetup.File{{Path: ".config/opencode/opencode.jsonc", Content: imported}}}}
	env, err := s.prepareHarnessSetup(context.Background(), setup, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	env, err = s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
		Provider: "openai-compatible", URL: gateway, Token: token, Model: "example-coder",
		Efforts: []string{"low", "high"}, Name: "Work proxy", Fold: []string{"proxy"}, CA: caPEM,
	}, setup, "opencode", env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(env["HOME"], ".config/opencode/opencode.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"variants": [`) || strings.Contains(string(raw), `"variants": {`) {
		t.Fatalf("generated variants are not an array:\n%s", raw)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), []byte(`{"model":"proxy/example-coder"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	processEnv := []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir()}
	for key, value := range env {
		processEnv = append(processEnv, key+"="+value)
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workspace
		cmd.Env = processEnv
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, output)
		}
		return output
	}
	models := run("models", "--standalone")
	if !strings.Contains(string(models), "meridian/example-coder") {
		t.Fatalf("models = %s", models)
	}

	acpCtx, acpCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer acpCancel()
	acp := exec.CommandContext(acpCtx, binary, "acp")
	acp.Dir = workspace
	acp.Env = processEnv
	stdin, err := acp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := acp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	acp.Stderr = nil
	if err := acp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = acp.Wait()
	}()
	reader := bufio.NewReader(stdout)
	write := func(value any) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stdin.Write(append(encoded, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "meridian-test", "version": "0"},
	}})
	if _, err := readACPResult(t, reader, stdin, 1); err != nil {
		t.Fatal(err)
	}
	write(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
		"cwd": workspace, "mcpServers": []any{},
	}})
	created, err := readACPResult(t, reader, stdin, 2)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(created)
	if !strings.Contains(string(encoded), "meridian/example-coder") || !strings.Contains(string(encoded), `"low"`) || !strings.Contains(string(encoded), `"high"`) {
		t.Fatalf("ACP session did not select the connection model: %s", encoded)
	}
	select {
	case <-called:
		t.Fatal("session startup called the provider")
	default:
	}
}

func readACPResult(t *testing.T, reader *bufio.Reader, stdin interface{ Write([]byte) (int, error) }, id int) (map[string]any, error) {
	t.Helper()
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatalf("ACP line: %s", line)
		}
		if method, _ := message["method"].(string); method != "" && message["id"] != nil {
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": map[string]any{}})
			if _, err := stdin.Write(append(raw, '\n')); err != nil {
				return nil, err
			}
			continue
		}
		switch got := message["id"].(type) {
		case float64:
			if int(got) != id {
				continue
			}
		default:
			continue
		}
		if message["error"] != nil {
			t.Fatalf("ACP error: %s", line)
		}
		result, _ := message["result"].(map[string]any)
		return result, nil
	}
}

func writeChatStream(w http.ResponseWriter, body map[string]any) {
	messages, _ := body["messages"].([]any)
	called := false
	for _, message := range messages {
		item, _ := message.(map[string]any)
		if item["role"] == "tool" {
			called = true
		}
	}
	tools, _ := body["tools"].([]any)
	name, arguments := "", "{}"
	if len(tools) > 0 && !called {
		name = toolName(tools[0])
		for _, tool := range tools {
			if toolName(tool) == "read" {
				name = "read"
				arguments = `{"path":"hello.txt"}`
				break
			}
		}
	}
	deltas := []map[string]any{{}}
	finish := "stop"
	if name != "" {
		finish = "tool_calls"
		deltas = []map[string]any{{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": name, "arguments": arguments},
			}},
		}, {}}
	} else {
		deltas = []map[string]any{{"role": "assistant", "content": "pong"}, {}}
	}
	var builder strings.Builder
	for i, delta := range deltas {
		choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
		if i == len(deltas)-1 {
			choice["finish_reason"] = finish
		}
		payload := map[string]any{
			"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 0, "model": body["model"],
			"choices": []any{choice},
		}
		raw, _ := json.Marshal(payload)
		builder.WriteString("data: ")
		builder.Write(raw)
		builder.WriteString("\n\n")
	}
	builder.WriteString("data: [DONE]\n\n")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(builder.String()))
}

func toolName(tool any) string {
	function, _ := tool.(map[string]any)["function"].(map[string]any)
	name, _ := function["name"].(string)
	return name
}

func localGatewayCert(t *testing.T) (string, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: mustMarshalEC(t, leafKey)}))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), leaf
}

func mustMarshalEC(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
