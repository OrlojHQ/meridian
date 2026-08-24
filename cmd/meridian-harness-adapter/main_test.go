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
	for _, driver := range []string{"generic", "pi-rpc", "opencode-server"} {
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
