package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func TestWriteProjectJSONOmitsUnsetOptionalFields(t *testing.T) {
	var output bytes.Buffer
	config := cliConfig{json: true, stdout: &output}

	if err := config.writeProject(client.Project{
		ID: "project-id", Name: "project", ResourceVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}

	var value map[string]any
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatalf("invalid JSON output %q: %v", output.String(), err)
	}
	if _, ok := value["repositoryUrl"]; ok {
		t.Fatalf("unset repositoryUrl encoded in %s", output.String())
	}
}

func TestWriteCapsuleJSONIncludesLauncherHarness(t *testing.T) {
	var output bytes.Buffer
	config := cliConfig{json: true, stdout: &output}
	if err := config.writeCapsule(client.Capsule{
		ID: "capsule-id", ProjectId: "project-id", Name: "review",
		Harness: client.NewOptString("opencode"), State: client.CapsuleStateReady,
		DesiredState: client.CapsuleIntentReady, ResourceVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatalf("invalid JSON output %q: %v", output.String(), err)
	}
	if value["harness"] != "opencode" {
		t.Fatalf("launcher harness output = %#v", value)
	}
}
