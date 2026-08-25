package main

import "testing"

func TestRunAttachExposesLocalTerminalTitleControls(t *testing.T) {
	command := newRunAttachCommand(&cliConfig{})
	title := command.Flags().Lookup("title")
	restore := command.Flags().Lookup("restore-title")
	if title == nil || restore == nil {
		t.Fatalf("title flags missing: title=%v restore=%v", title, restore)
	}
	if title.DefValue != "" || restore.DefValue != "Meridian" {
		t.Fatalf("title defaults = %q / %q", title.DefValue, restore.DefValue)
	}
}
