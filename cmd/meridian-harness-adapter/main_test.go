package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpAndVersion(t *testing.T) {
	for _, arguments := range [][]string{{"--help"}, {"--version"}} {
		command := newRootCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs(arguments)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "meridian-harness-adapter") {
			t.Fatalf("%v output = %q", arguments, output.String())
		}
	}
}

func TestDriverHelpDescribesDirectExecutableArrays(t *testing.T) {
	for _, driver := range []string{"generic", "pi-rpc", "acp", "claude-stream", "codex-app-server"} {
		command := newRootCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs([]string{driver, "--help"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "<") ||
			!strings.Contains(output.String(), "[arguments...]") {
			t.Fatalf("%s help = %q", driver, output.String())
		}
	}
}

func TestMCPServerFlagRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{"coord=relative", "bad name=/bin/x", "coord=/bin/x,\"quoted\""} {
		command := newRootCommand()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs([]string{"claude-stream", "--mcp-server", value, "--", "/bin/true"})
		if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "MCP server") {
			t.Fatalf("%q error = %v", value, err)
		}
	}
}
