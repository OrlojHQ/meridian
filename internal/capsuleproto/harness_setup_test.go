package capsuleproto

import (
	"context"
	"encoding/json"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessSetupPrivateHomesAndRehydrate(t *testing.T) {
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-1", Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "personal instructions"}}}}
	a := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	b := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	envA, err := a.prepareHarnessSetup(context.Background(), setup, "codex")
	if err != nil {
		t.Fatal(err)
	}
	envB, err := b.prepareHarnessSetup(context.Background(), setup, "codex")
	if err != nil {
		t.Fatal(err)
	}
	pathA := filepath.Join(envA["HOME"], ".codex", "AGENTS.md")
	if err := os.WriteFile(pathA, []byte("session edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareHarnessSetup(context.Background(), setup, "codex"); err != nil {
		t.Fatal(err)
	}
	own, _ := os.ReadFile(pathA)
	other, _ := os.ReadFile(filepath.Join(envB["HOME"], ".codex", "AGENTS.md"))
	if string(own) != "session edit" || string(other) != "personal instructions" {
		t.Fatal("setups are shared or session edits overwritten")
	}
	if err := os.RemoveAll(envA["HOME"]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.prepareHarnessSetup(context.Background(), setup, "codex"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(pathA)
	if string(restored) != "personal instructions" {
		t.Fatal("rehydration failed")
	}
}
func TestSetupRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, ".meridian-setup-rev")); err != nil {
		t.Fatal(err)
	}
	s := &Server{config: ServerConfig{HarnessHome: base}}
	_, err := s.prepareHarnessSetup(context.Background(), &harnesssetup.RuntimeSetup{Revision: "rev", Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "x"}}}}, "codex")
	if err == nil {
		t.Fatal("accepted external setup home")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside home")
	}
}

func TestDeepSeekHarnessSetupSetsHome(t *testing.T) {
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-dsh", Bundle: harnesssetup.Bundle{Harness: "dsh", Files: []harnesssetup.File{{Path: ".dsh/settings.yaml", Content: "model: example\n"}}}}
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	env, err := s.prepareHarnessSetup(context.Background(), setup, "dsh")
	if err != nil {
		t.Fatal(err)
	}
	if env["DSH_HOME"] != filepath.Join(env["HOME"], ".dsh") {
		t.Fatalf("DSH_HOME=%s HOME=%s", env["DSH_HOME"], env["HOME"])
	}
	raw, err := os.ReadFile(filepath.Join(env["DSH_HOME"], "settings.yaml"))
	if err != nil || !strings.Contains(string(raw), "example") {
		t.Fatalf("imported settings missing: %s %v", raw, err)
	}
}

func TestGatewayConfigurationDoesNotWriteLeaseToken(t *testing.T) {
	for _, harness := range []string{"codex", "claude", "opencode", "pi", "dsh"} {
		t.Run(harness, func(t *testing.T) {
			base := t.TempDir()
			s := &Server{config: ServerConfig{HarnessHome: base}}
			provider, model := "openai", ""
			switch harness {
			case "claude":
				provider = "anthropic"
			case "dsh":
				provider, model = "openai-compatible", "m"
			}
			token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: "https://meridian.example/provider-gateway/" + provider, Token: token, Model: model}, nil, harness, nil)
			if err != nil {
				t.Fatal(err)
			}
			if env["MERIDIAN_PROVIDER_TOKEN"] != token {
				t.Fatal("missing ephemeral credential")
			}
			err = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				raw, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				if strings.Contains(string(raw), token) {
					t.Errorf("lease token persisted in %s", p)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCustomEndpointHarnessConfiguration(t *testing.T) {
	token := strings.Repeat("b", 64)
	read := func(t *testing.T, env map[string]string, p string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(env["HOME"], p))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), token) {
			t.Fatalf("lease written to %s", p)
		}
		value := map[string]any{}
		if strings.HasSuffix(p, ".toml") {
			err = toml.Unmarshal(raw, &value)
		} else {
			err = json.Unmarshal(raw, &value)
		}
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	prepare := func(t *testing.T, harness, provider, model string) (map[string]string, error) {
		s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
		return s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: "https://meridian.example/provider-gateway/" + provider, Token: token, Model: model}, nil, harness, nil)
	}
	gateway := "https://meridian.example/provider-gateway/openai-compatible/v1"

	t.Run("codex", func(t *testing.T) {
		env, err := prepare(t, "codex", "openai-compatible", "qwen3-coder")
		if err != nil {
			t.Fatal(err)
		}
		config := read(t, env, ".codex/config.toml")
		provider := config["model_providers"].(map[string]any)["meridian"].(map[string]any)
		if config["model_provider"] != "meridian" || config["model"] != "qwen3-coder" || provider["base_url"] != gateway || provider["wire_api"] != "responses" || provider["env_key"] != "MERIDIAN_PROVIDER_TOKEN" {
			t.Fatalf("unexpected codex config %v", config)
		}
		if config["web_search"] != "disabled" || config["features"].(map[string]any)["multi_agent"] != false {
			t.Fatalf("non-function Codex tools left enabled for a custom endpoint: %v", config)
		}
	})
	t.Run("opencode", func(t *testing.T) {
		env, err := prepare(t, "opencode", "openai-compatible", "qwen2.5-coder:7b")
		if err != nil {
			t.Fatal(err)
		}
		config := read(t, env, ".config/opencode/opencode.json")
		provider := config["providers"].(map[string]any)["meridian"].(map[string]any)
		settings := provider["settings"].(map[string]any)
		models := provider["models"].(map[string]any)
		if config["model"] != "meridian/qwen2.5-coder:7b" || provider["package"] != "@opencode/ai/providers/openai-compatible" || settings["baseURL"] != gateway || settings["apiKey"] != "{env:MERIDIAN_PROVIDER_TOKEN}" || models["qwen2.5-coder:7b"] == nil {
			t.Fatalf("unexpected opencode config %v", config)
		}
		if _, legacy := config["provider"]; legacy {
			t.Fatalf("legacy provider entry left beside the native one: %v", config["provider"])
		}
		allow := config["policies"].([]any)[0].(map[string]any)
		if allow["action"] != "provider.use" || allow["resource"] != "meridian" || allow["effect"] != "allow" {
			t.Fatalf("connection provider not allowed: %v", config["policies"])
		}
		if !strings.Contains(env["OPENCODE_CONFIG_CONTENT"], `"model":"meridian/qwen2.5-coder:7b"`) || strings.Contains(env["OPENCODE_CONFIG_CONTENT"], token) {
			t.Fatalf("connection document = %s", env["OPENCODE_CONFIG_CONTENT"])
		}
	})
	t.Run("pi", func(t *testing.T) {
		env, err := prepare(t, "pi", "openai-compatible", "qwen3-coder")
		if err != nil {
			t.Fatal(err)
		}
		provider := read(t, env, ".pi/agent/models.json")["providers"].(map[string]any)["meridian"].(map[string]any)
		models := provider["models"].([]any)
		settings := read(t, env, ".pi/agent/settings.json")
		if provider["baseUrl"] != gateway || provider["api"] != "openai-completions" || provider["apiKey"] != "$MERIDIAN_PROVIDER_TOKEN" || len(models) != 1 || models[0].(map[string]any)["id"] != "qwen3-coder" || settings["defaultProvider"] != "meridian" || settings["defaultModel"] != "qwen3-coder" {
			t.Fatalf("unexpected pi config %v %v", provider, settings)
		}
	})
	t.Run("claude", func(t *testing.T) {
		env, err := prepare(t, "claude", "anthropic-compatible", "qwen3-coder")
		if err != nil {
			t.Fatal(err)
		}
		if env["ANTHROPIC_BASE_URL"] != "https://meridian.example/provider-gateway/anthropic-compatible" || env["ANTHROPIC_API_KEY"] != token || env["ANTHROPIC_MODEL"] != "qwen3-coder" || env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "qwen3-coder" {
			t.Fatalf("unexpected claude environment %v", env)
		}
	})
	dsh := func(t *testing.T, provider, api, baseURL string) {
		env, err := prepare(t, "dsh-structured", provider, "deepseek-chat:v4")
		if err != nil {
			t.Fatal(err)
		}
		if env["MERIDIAN_PROVIDER_TOKEN"] != token || env["DSH_HOME"] != filepath.Join(env["HOME"], ".dsh") {
			t.Fatalf("unexpected dsh environment %v", env)
		}
		raw, err := os.ReadFile(filepath.Join(env["DSH_HOME"], "cordis.patch.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), token) || strings.Contains(string(raw), "!!") {
			t.Fatalf("unsafe dsh patch %s", raw)
		}
		var entries []map[string]any
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatal(err)
		}
		byID := map[string]map[string]any{}
		for _, entry := range entries {
			byID[entry["id"].(string)] = entry
		}
		route := byID["llm-pi-ai"]["config"].(map[string]any)["providers"].(map[string]any)["meridian"].(map[string]any)
		models := route["models"].([]any)
		if route["api"] != api || route["baseURL"] != baseURL || route["apiKeyEnv"] != "MERIDIAN_PROVIDER_TOKEN" || len(models) != 1 || models[0].(map[string]any)["id"] != "deepseek-chat:v4" {
			t.Fatalf("unexpected dsh route %v", route)
		}
		for _, id := range []string{"acp", "agent-default-model"} {
			config := byID[id]["config"].(map[string]any)
			if config["provider"] != "meridian" || config["model"] != "deepseek-chat:v4" {
				t.Fatalf("%s does not select the gateway route: %v", id, config)
			}
		}
		for _, id := range []string{"llm-deepseek", "llm-deepseek-account", "session-log-deepseek", "web-search-deepseek", "web-deepseek-api"} {
			if byID[id]["disabled"] != true {
				t.Fatalf("%s left enabled", id)
			}
		}
		if web := byID["tool-web"]["config"].(map[string]any); web["search"] != false || web["fetch"] != true {
			t.Fatalf("unexpected web tool config %v", web)
		}
	}
	t.Run("dsh openai-compatible", func(t *testing.T) {
		dsh(t, "openai-compatible", "openai-completions", gateway)
	})
	t.Run("dsh anthropic-compatible", func(t *testing.T) {
		dsh(t, "anthropic-compatible", "anthropic-messages", "https://meridian.example/provider-gateway/anthropic-compatible")
	})
	for _, tc := range []struct{ harness, provider, model string }{
		{"claude", "openai-compatible", "m"},
		{"codex", "anthropic-compatible", "m"},
		{"opencode", "openai-compatible", ""},
		{"pi", "anthropic-compatible", "m"},
		{"codex", "openai", "gpt"},
		{"codex", "openai-compatible", "bad\nmodel"},
		{"dsh", "openai", ""},
		{"dsh", "anthropic", ""},
		{"dsh", "openai-compatible", ""},
		{"dsh", "openai-compatible", `m"`},
	} {
		if _, err := prepare(t, tc.harness, tc.provider, tc.model); err == nil {
			t.Errorf("%s accepted %s with model %q", tc.harness, tc.provider, tc.model)
		}
	}
}

func TestConnectionEffortLevelsReachEachHarness(t *testing.T) {
	token := strings.Repeat("c", 64)
	prepare := func(t *testing.T, harness, provider string, efforts []string) (map[string]string, error) {
		s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
		return s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
			Provider: provider, URL: "https://meridian.example/provider-gateway/" + provider,
			Token: token, Model: "glm-5", Efforts: efforts,
		}, nil, harness, nil)
	}
	readJSON := func(t *testing.T, path string, value any) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, value); err != nil {
			t.Fatal(err)
		}
	}
	// Levels arrive out of order and are written in canonical order.
	levels := []string{"max", "low", "high"}

	t.Run("pi", func(t *testing.T) {
		env, err := prepare(t, "pi", "openai-compatible", levels)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		readJSON(t, filepath.Join(env["HOME"], ".pi/agent/models.json"), &config)
		model := config["providers"].(map[string]any)["meridian"].(map[string]any)["models"].([]any)[0].(map[string]any)
		levels := model["thinkingLevelMap"].(map[string]any)
		if model["reasoning"] != true || model["compat"].(map[string]any)["supportsReasoningEffort"] != true ||
			levels["low"] != "low" || levels["high"] != "high" || levels["max"] != "max" ||
			levels["medium"] != nil || levels["minimal"] != nil || levels["xhigh"] != nil {
			t.Fatalf("pi model = %v", model)
		}
		if _, declared := levels["medium"]; !declared {
			t.Fatal("an undeclared base level must be pinned to null, or Pi offers it")
		}
	})
	t.Run("dsh", func(t *testing.T) {
		env, err := prepare(t, "dsh", "openai-compatible", levels)
		if err != nil {
			t.Fatal(err)
		}
		var entries []map[string]any
		readJSON(t, filepath.Join(env["DSH_HOME"], "cordis.patch.yml"), &entries)
		model := entries[0]["config"].(map[string]any)["providers"].(map[string]any)["meridian"].(map[string]any)["models"].([]any)[0].(map[string]any)
		efforts := model["reasoningEfforts"].(map[string]any)
		if _, off := efforts["off"]; !off || efforts["off"] != nil || efforts["low"] != "low" || efforts["max"] != "max" ||
			len(efforts) != 4 || model["compat"].(map[string]any)["supportsReasoningEffort"] != true {
			t.Fatalf("dsh model = %v", model)
		}
	})
	t.Run("opencode", func(t *testing.T) {
		env, err := prepare(t, "opencode", "openai-compatible", levels)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		readJSON(t, filepath.Join(env["HOME"], ".config/opencode/opencode.json"), &config)
		model := config["providers"].(map[string]any)["meridian"].(map[string]any)["models"].(map[string]any)["glm-5"].(map[string]any)
		reasoning := model["capabilities"].(map[string]any)["reasoning"].(map[string]any)
		effort := reasoning["effort"].([]any)
		if reasoning["supported"] != true || len(effort) != 3 || effort[0] != "low" || effort[1] != "high" || effort[2] != "max" {
			t.Fatalf("opencode model = %v", model)
		}
	})
	t.Run("codex", func(t *testing.T) {
		env, err := prepare(t, "codex", "openai-compatible", levels)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(env["HOME"], ".codex/config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		config := map[string]any{}
		if err := toml.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		if env["MERIDIAN_MODEL_EFFORTS"] != "low,high,max" || config["model_supports_reasoning_summaries"] != true {
			t.Fatalf("codex env %q config %v", env["MERIDIAN_MODEL_EFFORTS"], config)
		}
	})
	t.Run("no levels leave configs unchanged", func(t *testing.T) {
		env, err := prepare(t, "codex", "openai-compatible", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, set := env["MERIDIAN_MODEL_EFFORTS"]; set {
			t.Fatal("effort levels set without a declaration")
		}
	})
	for _, bad := range [][]string{{"ultra"}, {"low", "low"}, {"off"}} {
		if _, err := prepare(t, "pi", "openai-compatible", bad); err == nil {
			t.Errorf("accepted effort levels %v", bad)
		}
	}
}

func TestClaudeLoginAbsentFromHarnessEnvAndCodexUnchanged(t *testing.T) {
	const login = "sk-ant-oat-claude-login-canary"
	lease := strings.Repeat("ab", 32)
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
		Provider: "anthropic", URL: "https://meridian.example/provider-gateway/anthropic", Token: lease,
	}, nil, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if env["ANTHROPIC_API_KEY"] != lease || env["MERIDIAN_PROVIDER_TOKEN"] != lease || env["ANTHROPIC_BASE_URL"] != "https://meridian.example/provider-gateway/anthropic" {
		t.Fatalf("claude env = %v", env)
	}
	for name, value := range env {
		if strings.Contains(value, login) {
			t.Fatalf("claude env %s contains the login", name)
		}
	}
	if err := filepath.WalkDir(env["HOME"], func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), login) || strings.Contains(string(raw), lease) {
			t.Fatalf("claude home file %s contains credential material", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	codex, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
		Provider: "openai", URL: "https://meridian.example/provider-gateway/openai", Token: lease,
	}, nil, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if codex["MERIDIAN_PROVIDER_TOKEN"] != lease {
		t.Fatalf("codex token = %q", codex["MERIDIAN_PROVIDER_TOKEN"])
	}
	if _, ok := codex["ANTHROPIC_API_KEY"]; ok {
		t.Fatal("codex env gained ANTHROPIC_API_KEY")
	}
	raw, err := os.ReadFile(filepath.Join(codex["HOME"], ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), login) {
		t.Fatalf("codex config contains the claude login: %s", raw)
	}
	config := map[string]any{}
	if err := toml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	provider := config["model_providers"].(map[string]any)["meridian"].(map[string]any)
	if config["model_provider"] != "meridian" || provider["base_url"] != "https://meridian.example/provider-gateway/openai/v1" || provider["wire_api"] != "responses" || provider["env_key"] != "MERIDIAN_PROVIDER_TOKEN" {
		t.Fatalf("codex path changed: %v", config)
	}
	if _, ok := config["web_search"]; ok {
		t.Fatalf("official codex config changed: %v", config)
	}
}

func TestCodexToolDefaultsApplyOnlyToCustomEndpoints(t *testing.T) {
	token := strings.Repeat("c", 64)
	render := func(t *testing.T, provider, imported string) map[string]any {
		t.Helper()
		s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
		var setup *harnesssetup.RuntimeSetup
		var env map[string]string
		if imported != "" {
			setup = &harnesssetup.RuntimeSetup{Revision: "rev-" + provider, Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/config.toml", Content: imported}}}}
			var err error
			if env, err = s.prepareHarnessSetup(context.Background(), setup, "codex"); err != nil {
				t.Fatal(err)
			}
		}
		env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: "https://meridian.example/provider-gateway/" + provider, Token: token}, setup, "codex", env)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(env["HOME"], ".codex", "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		config := map[string]any{}
		if err := toml.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		return config
	}
	official := render(t, "openai", "")
	if _, ok := official["web_search"]; ok {
		t.Fatalf("official OpenAI config changed: %v", official)
	}
	if _, ok := official["features"]; ok {
		t.Fatalf("official OpenAI config changed: %v", official)
	}
	imported := render(t, "openai-compatible", "web_search = \"live\"\n\n[features]\nmulti_agent = true\nshell_snapshot = true\n")
	features := imported["features"].(map[string]any)
	if imported["web_search"] != "live" || features["multi_agent"] != true || features["shell_snapshot"] != true {
		t.Fatalf("imported Codex settings overridden: %v", imported)
	}
}

func TestOpenCodeImportedProxyConnection(t *testing.T) {
	token := strings.Repeat("d", 64)
	imported := `{
  // Keep this instruction.
  "model": "proxy/example-coder",
  "disabled_providers": ["openai", "meridian"],
  "enabled_providers": ["proxy"],
  "providers": {
    "proxy": {
      "name": "Work proxy",
      "package": "@opencode/ai/providers/openai-compatible",
      "env": ["PROXY_API_KEY"],
      "settings": {"baseURL": "https://proxy.internal.example/v1", "includeUsage": true},
      "models": {"example-coder": {"name": "Example"}}
    }
  },
  "policies": [
    {"action": "provider.use", "resource": "*", "effect": "deny"},
    {"action": "provider.use", "resource": "proxy", "effect": "allow"}
  ],
  "mcp": {
    "notes": {
      "type": "remote",
      "url": "https://mcp.internal.example/mcp",
      "headers": {"X-Feature": "{env:NOTES_TOKEN}"}
    }
  },
  "instructions": ["Keep answers short."]
}`
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-proxy", Bundle: harnesssetup.Bundle{Harness: "opencode", Files: []harnesssetup.File{{Path: ".config/opencode/opencode.jsonc", Content: imported}}}}
	env, err := s.prepareHarnessSetup(context.Background(), setup, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	env, err = s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
		Provider: "openai-compatible", URL: "https://meridian.example/provider-gateway/openai-compatible",
		Token: token, Model: "example-coder", Efforts: []string{"high", "low"},
		Name: "Work proxy", Fold: []string{"proxy"},
	}, setup, "opencode", env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(env["HOME"], ".config/opencode/opencode.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("lease written into the imported config")
	}
	config := map[string]any{}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config["model"] != "meridian/example-coder" {
		t.Fatalf("model = %v", config["model"])
	}
	enabled := config["enabled_providers"].([]any)
	if len(enabled) != 1 || enabled[0] != "meridian" {
		t.Fatalf("enabled_providers = %v", enabled)
	}
	disabled := config["disabled_providers"].([]any)
	if len(disabled) != 2 || disabled[0] != "openai" || disabled[1] != "proxy" {
		t.Fatalf("disabled_providers = %v", disabled)
	}
	policies := config["policies"].([]any)
	last := policies[len(policies)-1].(map[string]any)
	if len(policies) != 3 || last["resource"] != "meridian" || last["effect"] != "allow" {
		t.Fatalf("policies = %v", policies)
	}
	providers := config["providers"].(map[string]any)
	if _, ok := providers["proxy"]; ok {
		t.Fatalf("unauthenticated provider kept: %v", providers["proxy"])
	}
	meridian := providers["meridian"].(map[string]any)
	gateway := meridian["settings"].(map[string]any)
	model := meridian["models"].(map[string]any)["example-coder"].(map[string]any)
	effort := model["capabilities"].(map[string]any)["reasoning"].(map[string]any)["effort"].([]any)
	if meridian["name"] != "Work proxy" || meridian["package"] != "@opencode/ai/providers/openai-compatible" || gateway["baseURL"] != "https://meridian.example/provider-gateway/openai-compatible/v1" || gateway["apiKey"] != "{env:MERIDIAN_PROVIDER_TOKEN}" || gateway["includeUsage"] != true || len(effort) != 2 || effort[0] != "low" || effort[1] != "high" {
		t.Fatalf("connection provider = %v", meridian)
	}
	if strings.Contains(string(raw), "PROXY_API_KEY") || strings.Contains(string(raw), "proxy.internal.example") {
		t.Fatalf("imported provider or its key reference was copied: %s", raw)
	}
	if _, ok := config["provider"]; ok {
		t.Fatalf("legacy provider key created: %v", config["provider"])
	}
	notes := config["mcp"].(map[string]any)["notes"].(map[string]any)
	headers := notes["headers"].(map[string]any)
	instructions := config["instructions"].([]any)
	if notes["url"] != "https://mcp.internal.example/mcp" || headers["X-Feature"] != "{env:NOTES_TOKEN}" || len(instructions) != 1 || instructions[0] != "Keep answers short." {
		t.Fatalf("imported settings dropped: %s", raw)
	}
	if !strings.Contains(env["OPENCODE_CONFIG_CONTENT"], `"model":"meridian/example-coder"`) || strings.Contains(env["OPENCODE_CONFIG_CONTENT"], token) || strings.Contains(env["OPENCODE_CONFIG_CONTENT"], "proxy.internal.example") {
		t.Fatalf("connection document = %s", env["OPENCODE_CONFIG_CONTENT"])
	}
}

func TestOpenCodeOfficialProviderKeepsNativeSettings(t *testing.T) {
	token := strings.Repeat("e", 64)
	imported := `{"model":"openai/gpt-test","providers":{"openai":{"name":"OpenAI","models":{"gpt-test":{"name":"GPT"}}}},"provider":{"openai":{"npm":"@ai-sdk/openai","options":{"baseURL":"https://old.example/v1","temperature":0}}}}`
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	setup := &harnesssetup.RuntimeSetup{Revision: "rev-openai", Bundle: harnesssetup.Bundle{Harness: "opencode", Files: []harnesssetup.File{{Path: ".config/opencode/opencode.json", Content: imported}}}}
	env, err := s.prepareHarnessSetup(context.Background(), setup, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	env, err = s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{
		Provider: "openai", URL: "https://meridian.example/provider-gateway/openai", Token: token,
	}, setup, "opencode", env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(env["HOME"], ".config/opencode/opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	native := config["providers"].(map[string]any)["openai"].(map[string]any)
	settings := native["settings"].(map[string]any)
	models := native["models"].(map[string]any)
	if settings["baseURL"] != "https://meridian.example/provider-gateway/openai/v1" || settings["apiKey"] != "{env:MERIDIAN_PROVIDER_TOKEN}" || native["name"] != "OpenAI" || models["gpt-test"] == nil {
		t.Fatalf("native openai provider = %v", native)
	}
	legacy := config["provider"].(map[string]any)["openai"].(map[string]any)
	options := legacy["options"].(map[string]any)
	if options["baseURL"] != "https://meridian.example/provider-gateway/openai/v1" || options["apiKey"] != "{env:MERIDIAN_PROVIDER_TOKEN}" || options["temperature"] != float64(0) {
		t.Fatalf("legacy openai provider = %v", legacy)
	}
	if config["model"] != "openai/gpt-test" {
		t.Fatalf("official connection replaced the model: %v", config["model"])
	}
	if strings.Contains(env["OPENCODE_CONFIG_CONTENT"], token) || !strings.Contains(env["OPENCODE_CONFIG_CONTENT"], "provider-gateway/openai/v1") {
		t.Fatalf("connection document = %s", env["OPENCODE_CONFIG_CONTENT"])
	}
}

func TestOpenCodeAllowPolicyIsAddedOnce(t *testing.T) {
	config := map[string]any{
		"enabled_providers": []any{"proxy"},
		"policies": []any{
			map[string]any{"action": "provider.use", "resource": "*", "effect": "deny"},
		},
	}
	for range 3 {
		allowOpenCodeProvider(config, "meridian")
	}
	policies := config["policies"].([]any)
	if len(policies) != 2 {
		t.Fatalf("policies = %#v", policies)
	}
	last := policies[len(policies)-1].(map[string]any)
	if last["resource"] != "meridian" || last["effect"] != "allow" {
		t.Fatalf("last policy = %#v", last)
	}
	if providers := config["enabled_providers"].([]any); len(providers) != 2 {
		t.Fatalf("enabled_providers = %#v", providers)
	}
}
