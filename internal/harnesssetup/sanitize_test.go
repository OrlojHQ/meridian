package harnesssetup

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestOpenCodeCustomProviderPreserved(t *testing.T) {
	input := `{// Personal endpoint, no credentials
 "$schema":"https://opencode.ai/config.json",
 "disabled_providers":[],
 "provider":{"custom":{"name":"Custom","npm":"@ai-sdk/openai-compatible","options":{"baseURL":"https://models.example/v1"},"models":{"example-model":{"name":"Example"}}}},
 "new_setting":{"enabled":true,"max_tokens":4096}}
 `
	preview, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.jsonc", Content: input}})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Issues) != 0 || len(preview.Warnings) == 0 || len(preview.Bundle.Files) != 1 {
		t.Fatalf("unexpected review: %#v", preview)
	}
	before, _ := parseConfig(".config/opencode/opencode.jsonc", input)
	after, _ := parseConfig(".config/opencode/opencode.jsonc", preview.Bundle.Files[0].Content)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("config changed: %s", preview.Bundle.Files[0].Content)
	}
	if err := preview.Bundle.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialsRemovedWithoutLosingSiblings(t *testing.T) {
	input := `{"provider":{"custom":{"name":"Custom","options":{"baseURL":"https://service.example/v1","apiKey":"private-sentinel","headers":{"Authorization":"Bearer private-sentinel","X-Feature":"enabled"}},"models":{"m":{"name":"Model"}}}},"env":{"OPENAI_API_KEY":"private-sentinel","AWS_SECRET_ACCESS_KEY":"private-sentinel","NODE_ENV":"development"},"extra":[{"password":"private-sentinel","keep":true}],"url":"https://service.example?api_key=private-sentinel"}`
	sanitized, issues, err := Sanitize("opencode", ".config/opencode/opencode.json", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 6 || strings.Contains(sanitized, "private-sentinel") {
		t.Fatalf("credentials not removed: %s %#v", sanitized, issues)
	}
	for _, wanted := range []string{"Custom", "baseURL", "Model", "X-Feature", "development", "keep"} {
		if !strings.Contains(sanitized, wanted) {
			t.Fatalf("lost %s: %s", wanted, sanitized)
		}
	}
	if _, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.json", Content: sanitized}}); err != nil {
		t.Fatal(err)
	}
	bundle := Bundle{Harness: "opencode", Files: []File{{Path: ".config/opencode/opencode.json", Content: input}}}
	if bundle.Validate() == nil {
		t.Fatal("direct API can bypass credential checks")
	}
}

func TestPortableWarningsDoNotExcludeLocalConfiguration(t *testing.T) {
	input := `{"instructions":["/Users/person/rules.md"],"mcp":{"local":{"url":"http://localhost:3000","headers":{"X-Feature":"enabled"}}},"plugin":["my-plugin"],"unknown":{"path":"~/tools/check"}}`
	preview, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.json", Content: input}})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Issues) != 0 || len(preview.Warnings) < 3 {
		t.Fatalf("unexpected warnings: %#v", preview)
	}
	var before, after any
	_ = json.Unmarshal([]byte(input), &before)
	_ = json.Unmarshal([]byte(preview.Bundle.Files[0].Content), &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("changed local configuration: %s", preview.Bundle.Files[0].Content)
	}
}
