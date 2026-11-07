package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
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

func TestProjectApplyHarnessAllowlistsCustomImageByProfileName(t *testing.T) {
	const project = `{"id":"project-1","name":"demo","setup":[],"imageReference":"capsule:dev",` +
		`"harnessImages":[{"name":"mock","imageReference":"capsule:dev"},` +
		`{"name":"mock-structured","imageReference":"capsule:old"}],"harnessSecretNames":[],` +
		`"createdAt":"2026-08-24T12:00:00Z","updatedAt":"2026-08-24T12:00:00Z","resourceVersion":7}`
	var patched string
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		if request.Method == http.MethodPatch {
			value, _ := io.ReadAll(request.Body)
			patched = string(value)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, project)
	}))
	defer server.Close()
	var output bytes.Buffer
	config := &cliConfig{server: server.URL, tokenFile: testAPITokenFile(t), json: true, stdout: &output}
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newProjectCommand(config))
	root.SetArgs([]string{"project", "apply-harness", "project-1", "mock-structured=capsule:dev"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "GET /projects/project-1,PATCH /projects/project-1" {
		t.Fatalf("requests = %v", paths)
	}
	if !strings.Contains(patched, `"expectedResourceVersion":7`) ||
		!strings.Contains(patched, `"harnessImages":[{"name":"mock","imageReference":"capsule:dev"},`+
			`{"name":"mock-structured","imageReference":"capsule:dev"}]`) {
		t.Fatalf("patch request = %s", patched)
	}
	root.SetArgs([]string{"project", "apply-harness", "project-1", "mock-structured="})
	if err := root.Execute(); err == nil {
		t.Fatal("empty custom image reference was accepted")
	}
}
