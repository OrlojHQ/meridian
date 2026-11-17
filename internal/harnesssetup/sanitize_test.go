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

func TestOpenCodeDefaultProviderSuggestsConnection(t *testing.T) {
	input := `{
  "model": "proxy/example-coder",
  "providers": {
    "proxy": {
      "name": "Work proxy",
      "package": "@opencode/ai/providers/openai-compatible",
      "env": ["PROXY_API_KEY"],
      "settings": {"baseURL": "https://proxy.internal.example/v1", "includeUsage": true}
    }
  }
}`
	preview, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.jsonc", Content: input}})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, warning := range preview.Warnings {
		if warning.Path != ".config/opencode/opencode.jsonc:provider:proxy" {
			continue
		}
		found = true
		if !strings.Contains(warning.Reason, "openai-compatible") || warning.Connect == nil || warning.Connect.Name != "Work proxy" || warning.Connect.Provider != "openai-compatible" || warning.Connect.BaseURL != "https://proxy.internal.example/v1" || warning.Connect.Model != "example-coder" {
			t.Fatalf("warning = %#v", warning)
		}
		if strings.Contains(warning.Reason, "proxy.internal.example") || strings.Contains(warning.Reason, "PROXY_API_KEY") {
			t.Fatalf("warning leaked the endpoint or key name: %#v", warning)
		}
	}
	if !found {
		t.Fatalf("missing connection warning: %#v", preview.Warnings)
	}
	if strings.Contains(preview.Bundle.Files[0].Content, "PROXY_API_KEY") == false || strings.Contains(preview.Bundle.Files[0].Content, "includeUsage") == false {
		t.Fatalf("import dropped the provider: %s", preview.Bundle.Files[0].Content)
	}
	anthropic := `{"model":{"providerID":"company","model":"claude-like"},"provider":{"company":{"npm":"@ai-sdk/anthropic","api":"https://anthropic.internal.example"}}}`
	anthropicPreview, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.json", Content: anthropic}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmtWarnings(anthropicPreview.Warnings), "anthropic-compatible") {
		t.Fatalf("warnings = %#v", anthropicPreview.Warnings)
	}
	plain, err := ReviewFiles("opencode", []File{{Path: ".config/opencode/opencode.json", Content: `{"model":"openai/gpt-test"}`}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmtWarnings(plain.Warnings), "provider connection") {
		t.Fatalf("catalog model warned: %#v", plain.Warnings)
	}
}

func fmtWarnings(warnings []Issue) string {
	parts := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		parts = append(parts, warning.Reason)
	}
	return strings.Join(parts, "\n")
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
