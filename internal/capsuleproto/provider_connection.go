package capsuleproto

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(connection.Token) != 64 || connection.Model != "" && !harnesssetup.ValidModel(connection.Model) || !validGatewayCA(connection.CA) {
		return nil, errors.New("invalid provider connection")
	}
	custom := connection.Provider == "openai-compatible" || connection.Provider == "anthropic-compatible"
	if !custom && connection.Model != "" || !harnesssetup.ConnectionSupported(harness, connection.Provider, connection.Model) {
		return nil, errors.New("provider does not support this harness")
	}
	efforts := connection.Efforts
	if len(efforts) != 0 {
		ordered, ok := harnesssetup.ValidEfforts(efforts)
		if !ok || !custom || connection.Model == "" {
			return nil, errors.New("invalid provider connection")
		}
		efforts = ordered
	}
	// Chat Completions servers take the level as reasoning_effort; Pi and
	// DeepSeek Harness send it only when told the server supports it.
	completions := connection.Provider == "openai-compatible"
	revision := "connection-" + harness
	manifest, _ := harnesssetup.LookupManifest(harness)
	switch {
	case setup != nil:
		revision = setup.Revision
	case manifest.Instructions == "":
		// A manifest without an instructions file (DeepSeek Harness) still
		// gets an empty Meridian-owned home without a saved setup.
		env, err = s.materializeSetupHome(ctx, revision, nil, nil)
	default:
		placeholder := manifest.Root.Path + "/" + manifest.Instructions
		env, err = s.prepareHarnessSetup(ctx, &harnesssetup.RuntimeSetup{Revision: revision, Bundle: harnesssetup.Bundle{Harness: harness, Files: []harnesssetup.File{{Path: placeholder, Content: ""}}}}, harness)
	}
	if err != nil {
		return nil, err
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
	root, err := outer.OpenRoot(".meridian-setup-" + revision)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if connection.CA != "" {
		if err := trustGatewayCA(root, filepath.Join(base, ".meridian-setup-"+revision), connection.CA, env); err != nil {
			return nil, err
		}
	}
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
			if custom {
				// Codex's built-in web search and multi-agent tools are not
				// function tools, which most compatible servers reject. An
				// imported setting still wins.
				if _, set := config["web_search"]; !set {
					config["web_search"] = "disabled"
				}
				if features := object(config, "features"); features["multi_agent"] == nil {
					features["multi_agent"] = false
				}
			}
			providers := object(config, "model_providers")
			providers["meridian"] = map[string]any{"name": "Meridian", "base_url": connection.URL + "/v1", "wire_api": "responses", "env_key": "MERIDIAN_PROVIDER_TOKEN"}
			if len(efforts) != 0 {
				// Codex sends reasoning settings only for models it knows
				// reason; the connection says this one does. An imported
				// setting still wins.
				if _, set := config["model_supports_reasoning_summaries"]; !set {
					config["model_supports_reasoning_summaries"] = true
				}
			}
		})
		if len(efforts) != 0 {
			// Codex's catalog never lists a connection's model, so the
			// adapter takes that model's levels from here.
			env["MERIDIAN_MODEL_EFFORTS"] = strings.Join(efforts, ",")
		}
	case "opencode":
		p := ".config/opencode/opencode.json"
		if _, e := root.Stat(".config/opencode/opencode.jsonc"); e == nil {
			p = ".config/opencode/opencode.jsonc"
		}
		err = editNativeConfig(root, p, func(config map[string]any) {
			providers := object(config, "provider")
			if custom {
				entry := map[string]any{"name": model}
				if len(efforts) != 0 {
					// OpenCode offers a model's variants as its ACP effort
					// option; each sends its level as reasoning_effort.
					variants := map[string]any{}
					for _, level := range efforts {
						variants[level] = map[string]any{"reasoningEffort": level}
					}
					entry["reasoning"] = true
					entry["variants"] = variants
				}
				providers["meridian"] = map[string]any{
					"npm": "@ai-sdk/openai-compatible", "name": "Meridian",
					"options": map[string]any{"baseURL": connection.URL + "/v1", "apiKey": "{env:MERIDIAN_PROVIDER_TOKEN}"},
					"models":  map[string]any{model: entry},
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
				entry := map[string]any{"id": model}
				if len(efforts) != 0 {
					entry["reasoning"] = true
					entry["thinkingLevelMap"] = piThinkingLevelMap(efforts)
					entry["compat"] = map[string]any{"supportsReasoningEffort": true}
				}
				providers["meridian"] = map[string]any{"name": "Meridian", "baseUrl": connection.URL + "/v1", "api": "openai-completions", "apiKey": "$MERIDIAN_PROVIDER_TOKEN", "models": []any{entry}}
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
	case "dsh":
		var patch []byte
		patch, err = dshProviderPatch(connection, efforts, completions)
		if err == nil {
			err = writeSetupFile(root, ".dsh/cordis.patch.yml", patch)
		}
	}
	return env, err
}

// dshProviderPatch is DeepSeek Harness's home-level Cordis patch for a
// gateway route. It is JSON, which the YAML loader accepts without
// evaluating tags. Each entry replaces that entry's whole configuration.
// The DeepSeek adapters, DeepSeek web search, and the session-log upload are
// disabled so the gateway route is the only model endpoint.
func dshProviderPatch(connection *harnesssetup.Connection, efforts []string, completions bool) ([]byte, error) {
	api, base := "openai-completions", connection.URL+"/v1"
	if connection.Provider == "anthropic-compatible" {
		// The Anthropic client appends /v1/messages itself.
		api, base = "anthropic-messages", connection.URL
	}
	selection := map[string]any{"provider": "meridian", "model": connection.Model}
	model := map[string]any{"id": connection.Model}
	if len(efforts) != 0 {
		// Each declared level is sent under its own name; "off" sends
		// nothing, which leaves the server's default.
		declared := map[string]any{"off": nil}
		for _, level := range efforts {
			declared[level] = level
		}
		model["reasoningEfforts"] = declared
		if completions {
			model["compat"] = map[string]any{"supportsReasoningEffort": true}
		}
	}
	entries := []map[string]any{
		{"id": "llm-pi-ai", "config": map[string]any{"providers": map[string]any{"meridian": map[string]any{
			"displayName": "Meridian", "api": api, "baseURL": base,
			"apiKeyEnv": "MERIDIAN_PROVIDER_TOKEN", "models": []any{model},
		}}}},
		{"id": "acp", "config": selection},
		{"id": "agent-default-model", "config": selection},
		{"id": "tool-web", "config": map[string]any{"search": false, "fetch": true}},
	}
	for _, id := range []string{"llm-deepseek", "llm-deepseek-account", "session-log-deepseek", "web-search-deepseek", "web-deepseek-api"} {
		entries = append(entries, map[string]any{"id": id, "disabled": true})
	}
	return json.MarshalIndent(entries, "", "  ")
}

// systemCABundles are the image trust bundles the gateway CA is appended
// to, after an inherited SSL_CERT_FILE.
var systemCABundles = []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/pki/tls/certs/ca-bundle.crt", "/etc/ssl/cert.pem"}

const maxSystemCABundle = 4 << 20

// validGatewayCA accepts an empty value or one to four CA certificates in at
// most 16 KiB of PEM with nothing else in it.
func validGatewayCA(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > 16<<10 {
		return false
	}
	rest, count := []byte(raw), 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return false
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA {
			return false
		}
		count++
	}
	return count >= 1 && count <= 4 && len(bytes.TrimSpace(rest)) == 0
}

// trustGatewayCA makes the harness trust the local provider gateway
// (ADR 0037). The root filesystem is read-only, and Codex and Node ignore
// the system store, so trust goes through the environment: SSL_CERT_FILE
// names the image's bundle plus the CA (Codex, Bun, OpenSSL, Git), and
// NODE_EXTRA_CA_CERTS names the CA alone (Node, Bun).
func trustGatewayCA(root *os.Root, home, ca string, env map[string]string) error {
	system, err := readSystemCABundle()
	if err != nil {
		return err
	}
	if len(system) != 0 && !bytes.HasSuffix(system, []byte("\n")) {
		system = append(system, '\n')
	}
	if err := replaceSetupFile(root, ".meridian/provider-ca.pem", []byte(ca)); err != nil {
		return err
	}
	if err := replaceSetupFile(root, ".meridian/provider-ca-bundle.pem", append(system, ca...)); err != nil {
		return err
	}
	env["SSL_CERT_FILE"] = filepath.Join(home, ".meridian", "provider-ca-bundle.pem")
	env["NODE_EXTRA_CA_CERTS"] = filepath.Join(home, ".meridian", "provider-ca.pem")
	return nil
}
func readSystemCABundle() ([]byte, error) {
	candidates := systemCABundles
	if inherited := os.Getenv("SSL_CERT_FILE"); inherited != "" {
		candidates = append([]string{inherited}, candidates...)
	}
	for _, p := range candidates {
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, maxSystemCABundle+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(raw) > maxSystemCABundle {
			return nil, errors.New("system CA bundle exceeds limit")
		}
		return raw, nil
	}
	return nil, nil
}

// replaceSetupFile writes p atomically, so a harness starting beside this
// one never reads a partial trust file.
func replaceSetupFile(root *os.Root, p string, raw []byte) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temp := p + ".tmp-" + hex.EncodeToString(suffix)
	if err := writeSetupFile(root, temp, raw); err != nil {
		return err
	}
	if err := root.Rename(temp, p); err != nil {
		_ = root.Remove(temp)
		return err
	}
	return nil
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
	return writeSetupFile(root, p, raw)
}
func writeSetupFile(root *os.Root, p string, raw []byte) error {
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

// piThinkingLevelMap offers exactly the declared levels: Pi treats an absent
// base level as supported and an absent xhigh or max as unsupported, so
// every level is decided explicitly. "off" stays absent, which Pi reads as
// supported and sends nothing for.
func piThinkingLevelMap(efforts []string) map[string]any {
	levels := map[string]any{}
	for _, level := range harnesssetup.EffortLevels {
		levels[level] = nil
	}
	for _, level := range efforts {
		levels[level] = level
	}
	return levels
}
