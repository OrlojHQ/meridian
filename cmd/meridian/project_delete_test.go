package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestProjectDeleteRequiresConfirmationAndShowsWhatWillBeRemoved(t *testing.T) {
	const project = `{"id":"project-1","name":"demo","setup":["echo","setup"],"harnessImages":[],` +
		`"harnessSecretNames":["TOKEN"],"gitSecretName":"GIT_TOKEN","state":"active",` +
		`"createdAt":"2026-11-10T00:00:00Z","updatedAt":"2026-11-10T00:00:00Z","resourceVersion":4}`
	const capsules = `{"items":[{"id":"capsule-1","projectId":"project-1","timelineId":"timeline-1","name":"work",` +
		`"state":"Ready","desiredState":"Ready","restoreComplete":true,` +
		`"createdAt":"2026-11-10T00:00:00Z","updatedAt":"2026-11-10T00:00:00Z","resourceVersion":2},` +
		`{"id":"capsule-2","projectId":"project-1","timelineId":"timeline-2","name":"gone",` +
		`"state":"Deleted","desiredState":"Deleted","restoreComplete":true,` +
		`"createdAt":"2026-11-10T00:00:00Z","updatedAt":"2026-11-10T00:00:00Z","resourceVersion":3}]}`
	var paths []string
	var method, key, body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/projects/demo":
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"error":{"code":"not_found","message":"resource not found"}}`)
		case request.Method == http.MethodGet && request.URL.Path == "/projects":
			_, _ = io.WriteString(writer, `{"items":[`+project+`]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/projects/project-1":
			_, _ = io.WriteString(writer, project)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/projects/project-1/capsules"):
			_, _ = io.WriteString(writer, capsules)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/capsules/capsule-1/threads"):
			_, _ = io.WriteString(writer, `{"items":[`+
				`{"id":"thread-1","capsuleId":"capsule-1","state":"active","harness":"mock","encryptedAtRest":true,"messageCount":1,"encryptedBytes":32,"createdAt":"2026-11-10T00:00:00Z","updatedAt":"2026-11-10T00:00:00Z","resourceVersion":1},`+
				`{"id":"thread-2","capsuleId":"capsule-1","state":"archived","harness":"mock","encryptedAtRest":true,"messageCount":0,"encryptedBytes":0,"createdAt":"2026-11-10T00:00:00Z","updatedAt":"2026-11-10T00:00:00Z","resourceVersion":2}]}`)
		case request.Method == http.MethodPost && request.URL.Path == "/projects/project-1/delete":
			method = request.Method
			key = request.Header.Get("Idempotency-Key")
			payload, _ := io.ReadAll(request.Body)
			body = string(payload)
			writer.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(writer, strings.Replace(project, `"state":"active"`, `"state":"deleting"`, 1))
		default:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"error":{"code":"not_found","message":"resource not found"}}`)
		}
	}))
	defer server.Close()

	run := func(args []string, stdin string) (string, error) {
		t.Helper()
		var output bytes.Buffer
		config := &cliConfig{
			server: server.URL, tokenFile: testAPITokenFile(t), stdout: &output,
			stdin: strings.NewReader(stdin),
		}
		root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(newProjectCommand(config))
		root.SetArgs(args)
		err := root.Execute()
		return output.String(), err
	}

	paths = nil
	output, err := run([]string{"project", "delete", "demo"}, "nope\n")
	if err == nil || !strings.Contains(err.Error(), "confirmation did not match") {
		t.Fatalf("unconfirmed delete error = %v", err)
	}
	if strings.Contains(strings.Join(paths, ","), "POST") {
		t.Fatalf("delete was sent without confirmation: %v", paths)
	}
	if !strings.Contains(output, "1 Capsules") || !strings.Contains(output, "2 threads") || !strings.Contains(output, "2 secrets") {
		t.Fatalf("summary = %q", output)
	}

	paths = nil
	output, err = run([]string{"project", "delete", "demo", "--yes", "--idempotency-key", "delete-demo"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || key != "delete-demo" || !strings.Contains(body, `"expectedResourceVersion":4`) {
		t.Fatalf("delete request method=%s key=%s body=%s paths=%v", method, key, body, paths)
	}
	if !strings.Contains(output, "state=deleting") || !strings.Contains(output, "1 Capsules") {
		t.Fatalf("confirmed output = %q", output)
	}
}
