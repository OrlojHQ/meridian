package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
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

func TestProjectCreateDefaultsFromCurrentWorktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample-project")
	runMainGit(t, "", "init", "-q", root)
	runMainGit(t, root, "remote", "add", "origin", "git@github.com:Example/sample-project.git")
	t.Chdir(root)

	name, repositoryURL, err := projectCreateDefaults(
		context.Background(), nil, "", false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if name != "sample-project" ||
		repositoryURL != "https://github.com/Example/sample-project.git" {
		t.Fatalf("defaults = %q, %q", name, repositoryURL)
	}
}

func TestProjectCreateDefaultsRespectExplicitValues(t *testing.T) {
	name, repositoryURL, err := projectCreateDefaults(
		context.Background(), []string{"custom"}, "Example/project", true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if name != "custom" || repositoryURL != "https://github.com/Example/project" {
		t.Fatalf("defaults = %q, %q", name, repositoryURL)
	}
}

func runMainGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	if directory != "" {
		command.Dir = directory
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
