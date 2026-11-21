package coordtool

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// fakeMailbox answers whoami as a lead and echoes spawn requests as links.
type fakeMailbox struct {
	mu       sync.Mutex
	role     string
	requests []coordproto.Request
}

func (f *fakeMailbox) serve(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "coord.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				record, err := coordproto.ReadRecord(bufio.NewReader(conn))
				if err != nil {
					return
				}
				request, err := coordproto.DecodeRequest(record)
				if err != nil {
					return
				}
				f.mu.Lock()
				f.requests = append(f.requests, request)
				f.mu.Unlock()
				response := coordproto.OK()
				switch request.Type {
				case coordproto.KindWhoAmI:
					role := f.role
					if role == "" {
						role = "lead"
					}
					response.Self = &coordproto.Self{Role: role, GrantID: "grant", Depth: 1}
				case coordproto.KindSpawn:
					response.Link = &coordproto.Link{ID: "link-" + request.Name, Name: request.Name, State: "active"}
				default:
					response = coordproto.Fail(coordproto.CodeNotFound, "no such link")
				}
				encoded, _ := coordproto.Encode(response)
				_, _ = conn.Write(encoded)
			}()
		}
	}()
	return path
}

func exchange(t *testing.T, socket string, messages ...string) []map[string]any {
	t.Helper()
	server := &MCPServer{Socket: socket, Version: "test"}
	var output strings.Builder
	var mu sync.Mutex
	writer := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return output.Write(p)
	})
	if err := server.Serve(context.Background(), strings.NewReader(strings.Join(messages, "\n")+"\n"), writer); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func byID(responses []map[string]any, id float64) map[string]any {
	for _, response := range responses {
		if response["id"] == id {
			return response
		}
	}
	return nil
}

func TestMCPServerExposesToolsOnlyToLeads(t *testing.T) {
	mailbox := &fakeMailbox{}
	socket := mailbox.serve(t)
	responses := exchange(t, socket,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"meridian_spawn","arguments":{"name":"subagent-1","harness":"mock","prompt":"do it"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"meridian_spawn","arguments":{"name":"x","harness":"mock","prompt":"p","capsule":"other"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"meridian_promote"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`,
		`not json`,
	)
	if len(responses) != 7 {
		t.Fatalf("responses = %d: %v", len(responses), responses)
	}
	initialize := byID(responses, 1)["result"].(map[string]any)
	if initialize["protocolVersion"] != "2025-06-18" ||
		initialize["serverInfo"].(map[string]any)["name"] != "meridian-coord" {
		t.Fatalf("initialize = %v", initialize)
	}
	tools := byID(responses, 2)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 10 {
		t.Fatalf("tools = %d", len(tools))
	}
	spawn := byID(responses, 3)["result"].(map[string]any)
	if spawn["isError"] != false || !strings.Contains(spawn["content"].([]any)[0].(map[string]any)["text"].(string), "link-subagent-1") {
		t.Fatalf("spawn = %v", spawn)
	}
	if byID(responses, 4)["result"].(map[string]any)["isError"] != true {
		t.Fatal("unknown tool argument accepted")
	}
	if byID(responses, 5)["error"] == nil || byID(responses, 6)["error"] == nil {
		t.Fatal("unknown tool or method accepted")
	}
	mailbox.mu.Lock()
	defer mailbox.mu.Unlock()
	var spawns []coordproto.Request
	for _, request := range mailbox.requests {
		if request.Type == coordproto.KindSpawn {
			spawns = append(spawns, request)
		}
	}
	if len(spawns) != 1 || spawns[0].Key != "spawn-subagent-1" {
		t.Fatalf("spawn requests = %+v", spawns)
	}
}

func TestMCPServerListsReportToolsForSubagents(t *testing.T) {
	mailbox := &fakeMailbox{role: "sub"}
	socket := mailbox.serve(t)
	responses := exchange(t, socket, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := responses[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("subagent tools = %d", len(tools))
	}
	names := map[string]bool{}
	for _, item := range tools {
		names[item.(map[string]any)["name"].(string)] = true
	}
	if !names["meridian_report"] || !names["meridian_whoami"] || !names["meridian_propose_delivery"] ||
		names["meridian_spawn"] {
		t.Fatalf("subagent tools = %v", names)
	}
}

func TestMCPServerHidesToolsWithoutAMailbox(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sock")
	responses := exchange(t, missing, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if tools := responses[0]["result"].(map[string]any)["tools"].([]any); len(tools) != 0 {
		t.Fatalf("tools without a mailbox = %d", len(tools))
	}
	responses = exchange(t, missing,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"meridian_list"}}`)
	result := responses[0]["result"].(map[string]any)
	if result["isError"] != true || !strings.Contains(result["content"].([]any)[0].(map[string]any)["text"].(string), coordproto.CodeCoordinationDisabled) {
		t.Fatalf("call without a mailbox = %v", result)
	}
}

func TestMCPServerPassesToFileForPatchAndResult(t *testing.T) {
	mailbox := &fakeMailbox{}
	socket := mailbox.serve(t)
	responses := exchange(t, socket,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"meridian_patch","arguments":{"link":"link-1","toFile":true}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"meridian_result","arguments":{"link":"link-1","toFile":true}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"meridian_status","arguments":{"link":"link-1","toFile":true}}}`,
	)
	tools := byID(responses, 1)["result"].(map[string]any)["tools"].([]any)
	withToFile := map[string]bool{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if property, ok := properties["toFile"].(map[string]any); ok && property["type"] == "boolean" {
			withToFile[tool["name"].(string)] = true
		}
	}
	if len(withToFile) != 2 || !withToFile["meridian_patch"] || !withToFile["meridian_result"] {
		t.Fatalf("tools with toFile = %v", withToFile)
	}
	if byID(responses, 4)["result"].(map[string]any)["isError"] != true {
		t.Fatal("toFile accepted on status")
	}
	mailbox.mu.Lock()
	defer mailbox.mu.Unlock()
	kinds := map[coordproto.Kind]bool{}
	for _, request := range mailbox.requests {
		if request.ToFile {
			kinds[request.Type] = true
		}
	}
	if len(kinds) != 2 || !kinds[coordproto.KindPatch] || !kinds[coordproto.KindResult] {
		t.Fatalf("toFile requests = %v", kinds)
	}
}

func TestMCPServerPassesContextFilesForSpawnOnly(t *testing.T) {
	mailbox := &fakeMailbox{}
	socket := mailbox.serve(t)
	responses := exchange(t, socket,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"meridian_spawn","arguments":{"name":"a","harness":"mock","prompt":"task","contextFiles":["conventions.md","decisions/api.md"]}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"meridian_spawn","arguments":{"name":"b","harness":"mock","prompt":"task","contextFiles":[]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"meridian_send","arguments":{"link":"link-a","prompt":"more","contextFiles":["conventions.md"]}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"meridian_spawn","arguments":{"name":"c","harness":"mock","prompt":"task","contextFiles":["../coord.sock"]}}}`,
	)
	tools := byID(responses, 1)["result"].(map[string]any)["tools"].([]any)
	withContext := map[string]map[string]any{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if property, ok := properties["contextFiles"].(map[string]any); ok {
			withContext[tool["name"].(string)] = property
		}
	}
	spawn := withContext["meridian_spawn"]
	if len(withContext) != 1 || spawn["type"] != "array" || spawn["maxItems"] != float64(coordproto.MaxContextFiles) ||
		!strings.Contains(spawn["description"].(string), "$HOME/.meridian/notes/") {
		t.Fatalf("tools with contextFiles = %v", withContext)
	}
	for id, wantError := range map[float64]bool{2: false, 3: false, 4: true, 5: true} {
		if got := byID(responses, id)["result"].(map[string]any)["isError"] == true; got != wantError {
			t.Errorf("call %v isError = %v", id, got)
		}
	}
	mailbox.mu.Lock()
	defer mailbox.mu.Unlock()
	spawned := map[string][]string{}
	for _, request := range mailbox.requests {
		if request.Type == coordproto.KindSpawn {
			spawned[request.Name] = request.ContextFiles
		}
	}
	if len(spawned) != 2 || strings.Join(spawned["a"], ",") != "conventions.md,decisions/api.md" || spawned["b"] != nil {
		t.Fatalf("spawn requests = %v", spawned)
	}
}
