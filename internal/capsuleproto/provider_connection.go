package capsuleproto

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/pelletier/go-toml/v2"
	"github.com/tidwall/jsonc"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) prepareProviderConnection(ctx context.Context, connection *harnesssetup.Connection, setup *harnesssetup.RuntimeSetup, harness string, env map[string]string) (map[string]string, error) {
	if connection == nil {
		return env, nil
	}
	harness = strings.TrimSuffix(harness, "-structured")
	u, err := url.Parse(connection.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(connection.Token) != 64 || connection.Model != "" && !harnesssetup.ValidModel(connection.Model) {
		return nil, errors.New("invalid provider connection")
	}
	custom := connection.Provider == "openai-compatible" || connection.Provider == "anthropic-compatible"
	if !custom && connection.Model != "" || !harnesssetup.ConnectionSupported(harness, connection.Provider, connection.Model) {
		return nil, errors.New("provider does not support this harness")
	}
	if setup == nil {
		paths := map[string]string{"codex": ".codex/AGENTS.md", "claude": ".claude/CLAUDE.md", "opencode": ".config/opencode/AGENTS.md", "pi": ".pi/agent/AGENTS.md"}
		setup = &harnesssetup.RuntimeSetup{Revision: "connection-" + harness, Bundle: harnesssetup.Bundle{Harness: harness, Files: []harnesssetup.File{{Path: paths[harness], Content: ""}}}}
		env, err = s.prepareHarnessSetup(ctx, setup, harness)
		if err != nil {
			return nil, err
		}
	}
	env["MERIDIAN_PROVIDER_TOKEN"] = connection.Token
	base := s.config.HarnessHome
	if base == "" {
		base = "/home/capsule"
	}
	outer, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer outer.Close()
	root, err := outer.OpenRoot(".meridian-setup-" + setup.Revision)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	model := connection.Model
	switch harness {
	case "claude":
		env["ANTHROPIC_BASE_URL"] = connection.URL
		env["ANTHROPIC_API_KEY"] = connection.Token
		if model != "" {
			// Background and alias requests must also name a model the
			// custom endpoint serves.
			for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
				env[name] = model
			}
		}
	case "codex":
		err = editNativeConfig(root, ".codex/config.toml", func(config map[string]any) {
			config["model_provider"] = "meridian"
			if model != "" {
				config["model"] = model
			}
			providers := object(config, "model_providers")
			providers["meridian"] = map[string]any{"name": "Meridian", "base_url": connection.URL + "/v1", "wire_api": "responses", "env_key": "MERIDIAN_PROVIDER_TOKEN"}
		})
	case "opencode":
		p := ".config/opencode/opencode.json"
		if _, e := root.Stat(".config/opencode/opencode.jsonc"); e == nil {
			p = ".config/opencode/opencode.jsonc"
		}
		err = editNativeConfig(root, p, func(config map[string]any) {
			providers := object(config, "provider")
			if custom {
				providers["meridian"] = map[string]any{
					"npm": "@ai-sdk/openai-compatible", "name": "Meridian",
					"options": map[string]any{"baseURL": connection.URL + "/v1", "apiKey": "{env:MERIDIAN_PROVIDER_TOKEN}"},
					"models":  map[string]any{model: map[string]any{"name": model}},
				}
				config["model"] = "meridian/" + model
				return
			}
			provider := object(providers, connection.Provider)
			options := object(provider, "options")
			options["baseURL"] = connection.URL + "/v1"
			options["apiKey"] = "{env:MERIDIAN_PROVIDER_TOKEN}"
		})
	case "pi":
		err = editNativeConfig(root, ".pi/agent/models.json", func(config map[string]any) {
			providers := object(config, "providers")
			if custom {
				providers["meridian"] = map[string]any{"name": "Meridian", "baseUrl": connection.URL + "/v1", "api": "openai-completions", "apiKey": "$MERIDIAN_PROVIDER_TOKEN", "models": []any{map[string]any{"id": model}}}
				return
			}
			provider := object(providers, connection.Provider)
			provider["baseUrl"] = connection.URL + "/v1"
			provider["apiKey"] = "$MERIDIAN_PROVIDER_TOKEN"
		})
		if err == nil && custom {
			err = editNativeConfig(root, ".pi/agent/settings.json", func(config map[string]any) {
				config["defaultProvider"] = "meridian"
				config["defaultModel"] = model
			})
		}
	}
	return env, err
}
func object(parent map[string]any, key string) map[string]any {
	value, ok := parent[key].(map[string]any)
	if !ok {
		value = map[string]any{}
		parent[key] = value
	}
	return value
}
func editNativeConfig(root *os.Root, p string, edit func(map[string]any)) error {
	value := map[string]any{}
	f, err := root.Open(p)
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(f, (128<<10)+1))
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(raw) > 128<<10 {
			return errors.New("configuration exceeds limit")
		}
		if strings.HasSuffix(p, ".toml") {
			err = toml.Unmarshal(raw, &value)
		} else {
			err = json.Unmarshal(jsonc.ToJSON(raw), &value)
		}
		if err != nil {
			return errors.New("native configuration is invalid")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	edit(value)
	var raw []byte
	if strings.HasSuffix(p, ".toml") {
		raw, err = toml.Marshal(value)
	} else {
		raw, err = json.MarshalIndent(value, "", "  ")
	}
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	output, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := output.Write(raw)
	closeErr := output.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
