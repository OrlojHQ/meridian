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

func TestGatewayConfigurationDoesNotWriteLeaseToken(t *testing.T) {
	for _, harness := range []string{"codex", "claude", "opencode", "pi"} {
		t.Run(harness, func(t *testing.T) {
			base := t.TempDir()
			s := &Server{config: ServerConfig{HarnessHome: base}}
			provider := "openai"
			if harness == "claude" {
				provider = "anthropic"
			}
			token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: "https://meridian.example/provider-gateway/" + provider, Token: token}, nil, harness, nil)
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
		provider := config["provider"].(map[string]any)["meridian"].(map[string]any)
		options := provider["options"].(map[string]any)
		models := provider["models"].(map[string]any)
		if config["model"] != "meridian/qwen2.5-coder:7b" || provider["npm"] != "@ai-sdk/openai-compatible" || options["baseURL"] != gateway || options["apiKey"] != "{env:MERIDIAN_PROVIDER_TOKEN}" || models["qwen2.5-coder:7b"] == nil {
			t.Fatalf("unexpected opencode config %v", config)
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
	for _, tc := range []struct{ harness, provider, model string }{
		{"claude", "openai-compatible", "m"},
		{"codex", "anthropic-compatible", "m"},
		{"opencode", "openai-compatible", ""},
		{"pi", "anthropic-compatible", "m"},
		{"codex", "openai", "gpt"},
		{"codex", "openai-compatible", "bad\nmodel"},
	} {
		if _, err := prepare(t, tc.harness, tc.provider, tc.model); err == nil {
			t.Errorf("%s accepted %s with model %q", tc.harness, tc.provider, tc.model)
		}
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
