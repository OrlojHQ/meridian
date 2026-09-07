package harnesssetup

import (
	"strings"
	"testing"
)

func TestPortableNpxDependencies(t *testing.T) {
	for _, content := range []string{
		`{"mcpServers":{"tool":{"command":"npx","args":["-y","@example/tool@1.2.3","--stdio"]}}}`,
		`{"mcp":{"tool":{"command":["npx","--yes","@example/tool@1.2.3","--stdio"]}}}`,
	} {
		files := []File{{Path: ".claude/settings.json", Content: content}}
		deps, err := DependenciesForFiles(files)
		if err != nil || len(deps) != 1 || deps[0] != "@example/tool@1.2.3" {
			t.Fatalf("deps=%v err=%v", deps, err)
		}
		output, err := OfflineNpx(files[0].Path, content)
		if err != nil || !strings.Contains(output, "/opt/meridian-node/bin/npx") || !strings.Contains(output, "--offline") || !strings.Contains(output, "--stdio") {
			t.Fatalf("output=%s err=%v", output, err)
		}
	}
}
func TestRejectUnpinnedNpxDependencies(t *testing.T) {
	for _, value := range []string{"tool", "tool@latest", "tool@^1.2.3", "https://example.com/tool", "--package=tool@1.2.3"} {
		_, _, err := npxPackage(map[string]any{"command": "npx", "args": []any{value}})
		if err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
