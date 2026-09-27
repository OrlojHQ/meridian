package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func TestWriteRunJSONOmitsUnsetExitStatus(t *testing.T) {
	created := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, state := range []client.RunState{client.RunStateStarting, client.RunStateRunning} {
		t.Run(string(state), func(t *testing.T) {
			var output bytes.Buffer
			config := cliConfig{json: true, stdout: &output}
			if err := config.writeRun(client.Run{
				ID: "run-id", CapsuleId: "capsule-id", Harness: "codex", State: state,
				CreatedAt: created, UpdatedAt: created, ResourceVersion: 1,
			}); err != nil {
				t.Fatal(err)
			}
			value := decodeJSONObject(t, output.Bytes())
			for _, field := range []string{"exitStatus", "failure", "startedAt", "finishedAt"} {
				if _, ok := value[field]; ok {
					t.Fatalf("unset %s encoded in %s", field, output.String())
				}
			}
			if value["state"] != string(state) {
				t.Fatalf("state = %#v in %s", value["state"], output.String())
			}
		})
	}
}

func TestWriteRunJSONKeepsZeroExitStatus(t *testing.T) {
	var output bytes.Buffer
	config := cliConfig{json: true, stdout: &output}
	if err := config.writeRun(client.Run{
		ID: "run-id", State: client.RunStateSucceeded, ExitStatus: client.NewOptInt(0),
	}); err != nil {
		t.Fatal(err)
	}
	if value := decodeJSONObject(t, output.Bytes()); value["exitStatus"] != float64(0) {
		t.Fatalf("exitStatus = %#v in %s", value["exitStatus"], output.String())
	}
}

// TestJSONOutputMatchesGeneratedEncoder writes zero values, where every
// optional field is unset, and requires the CLI output to equal the API
// encoding produced by the generated client.
func TestJSONOutputMatchesGeneratedEncoder(t *testing.T) {
	cases := []struct {
		name  string
		write func(*cliConfig) error
		want  json.Marshaler
	}{
		{"run", func(c *cliConfig) error { return c.writeRun(client.Run{}) }, &client.Run{}},
		{"capsule", func(c *cliConfig) error { return c.writeCapsule(client.Capsule{}) }, &client.Capsule{}},
		{"capsule page", func(c *cliConfig) error { return c.writeCapsulePage(client.CapsulePage{Items: []client.Capsule{{}}}) }, &client.CapsulePage{Items: []client.Capsule{{}}}},
		{"project", func(c *cliConfig) error { return c.writeProject(client.Project{}) }, &client.Project{}},
		{"project page", func(c *cliConfig) error { return c.writeProjectPage(client.ProjectPage{Items: []client.Project{{}}}) }, &client.ProjectPage{Items: []client.Project{{}}}},
		{"secret", func(c *cliConfig) error { return c.writeSecret(client.Secret{}) }, &client.Secret{}},
		{"moment", func(c *cliConfig) error { return c.writeMoment(client.Moment{}) }, &client.Moment{}},
		{"descendant", func(c *cliConfig) error { return c.writeDescendant(client.DescendantResult{}) }, &client.DescendantResult{}},
		{"thread", func(c *cliConfig) error { return c.writeThread(client.Thread{}) }, &client.Thread{}},
		{"thread mutation", func(c *cliConfig) error { return c.writeThreadMutation(client.ThreadMutationResult{}) }, &client.ThreadMutationResult{}},
		{"project thread intent", func(c *cliConfig) error { return c.writeProjectThreadIntent(client.ProjectThreadIntent{}) }, &client.ProjectThreadIntent{}},
		{"run page", func(c *cliConfig) error { return writeJSON(c.stdout, &client.RunPage{Items: []client.Run{{}}}) }, &client.RunPage{Items: []client.Run{{}}}},
		{"delivery", func(c *cliConfig) error { return writeJSON(c.stdout, &client.Delivery{}) }, &client.Delivery{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tc.write(&cliConfig{json: true, stdout: &output}); err != nil {
				t.Fatal(err)
			}
			want, err := tc.want.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if got, expected := decodeJSONObject(t, output.Bytes()), decodeJSONObject(t, want); !reflect.DeepEqual(got, expected) {
				t.Fatalf("CLI JSON = %s, API JSON = %s", output.Bytes(), want)
			}
		})
	}
}

func decodeJSONObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("invalid JSON output %q: %v", data, err)
	}
	return value
}
