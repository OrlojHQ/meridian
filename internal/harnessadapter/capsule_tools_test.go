package harnessadapter

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/coordtool"
)

// Each structured adapter must hand the session the capsule tools. Pi has no
// MCP client; its profile arguments carry the skill, and the adapter keeps them.
func TestCapsuleToolsAreRegisteredForEachAdapter(t *testing.T) {
	server := MCPServer{Name: "meridian-coord", Command: "/usr/local/bin/meridian-coord", Arguments: []string{"mcp"}}

	t.Run("claude", func(t *testing.T) {
		arguments, err := claudeCommand(ClaudeConfig{
			Command: []string{"/usr/local/bin/claude"}, Permissions: true, MCPServers: []MCPServer{server},
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(arguments, "\n")
		if !strings.Contains(joined, "--mcp-config") || !strings.Contains(joined, "meridian-coord") {
			t.Fatalf("claude command = %q", arguments)
		}
		if argumentAfter(t, arguments, "--append-system-prompt") != coordtool.CapsuleInstruction {
			t.Fatalf("claude prompt = %q", arguments)
		}
		without, err := claudeCommand(ClaudeConfig{Command: []string{"/usr/local/bin/claude"}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(without, "\n"), "--append-system-prompt") {
			t.Fatalf("prompt appended without the capsule server: %q", without)
		}
	})

	t.Run("codex", func(t *testing.T) {
		command := codexCommand(CodexConfig{
			Command: []string{"/usr/local/bin/codex"}, MCPServers: []MCPServer{server},
		})
		joined := strings.Join(command, " ")
		for _, want := range []string{
			`mcp_servers.meridian-coord.command="/usr/local/bin/meridian-coord"`,
			`mcp_servers.meridian-coord.args=["mcp"]`,
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("codex command %q missing %s", command, want)
			}
		}
	})

	t.Run("acp", func(t *testing.T) {
		session := &acpSession{config: ACPConfig{MCPServers: []MCPServer{server}}}
		encoded, err := json.Marshal(session.mcpServers())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"name":"meridian-coord"`) ||
			!strings.Contains(string(encoded), `"/usr/local/bin/meridian-coord"`) {
			t.Fatalf("acp servers = %s", encoded)
		}
	})

	t.Run("pi", func(t *testing.T) {
		skill := "/usr/local/share/meridian/pi/skills/meridian-coord"
		command := piCommand(PiConfig{Command: []string{"/usr/local/bin/pi", "--skill", skill}, SessionDir: "/home/capsule/.pi-sessions"})
		joined := strings.Join(command, " ")
		if !strings.Contains(joined, "--skill "+skill) || !strings.Contains(joined, "--mode rpc") ||
			!strings.Contains(joined, "--session-dir /home/capsule/.pi-sessions") {
			t.Fatalf("pi command = %q", command)
		}
	})
}
