package harnesssetup

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

func credentialKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	for _, suffix := range []string{"apikey", "accesskey", "accesskeyid", "apitoken", "accesstoken", "refreshtoken", "authtoken", "password", "passwd", "clientsecret", "privatekey", "secretkey"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	switch normalized {
	case "token", "secret", "authorization", "proxyauthorization", "cookie", "setcookie", "credentials":
		return true
	}
	return false
}

// Sanitize preserves configuration by default and removes only individual
// credential values and explicit machine trust decisions. It never executes it.
func Sanitize(harness, p, content string) (string, []Issue, error) {
	parsed, err := parseConfig(p, content)
	if err != nil {
		return "", nil, err
	}
	issues := []Issue{}
	var clean func(string, string, any) (any, bool)
	clean = func(location, key string, item any) (any, bool) {
		reason := ""
		if credentialKey(key) || (strings.HasSuffix(location, ":env:KEY") || strings.HasSuffix(location, ":environment:KEY")) {
			reason = "Credential field excluded; configure authentication separately"
		}
		// Trust/approval decisions made on a laptop do not grant trust in a Capsule.
		switch strings.ToLower(key) {
		case "trust_level", "sandbox_mode", "approval_policy", "dangerouslyskippermissions", "bypasspermissions":
			reason = "Machine trust or approval override excluded"
		}
		if reason != "" {
			issues = append(issues, Issue{location, reason})
			return nil, false
		}
		switch v := item.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				replacement, keep := clean(location+":"+k, k, v[k])
				if keep {
					v[k] = replacement
				} else {
					delete(v, k)
				}
			}
		case []any:
			result := make([]any, 0, len(v))
			for i, element := range v {
				if replacement, keep := clean(fmt.Sprintf("%s[%d]", location, i), "", element); keep {
					result = append(result, replacement)
				}
			}
			return result, true
		case string:
			sensitive := Sensitive(v)
			if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") {
				if u, e := url.Parse(v); e == nil {
					if u.User != nil {
						sensitive = true
					}
					for k := range u.Query() {
						if credentialKey(k) || strings.EqualFold(k, "key") || strings.EqualFold(k, "signature") {
							sensitive = true
						}
					}
				}
			}
			if sensitive {
				issues = append(issues, Issue{location, "Credential-shaped value excluded; configure authentication separately"})
				return nil, false
			}
		}
		return item, true
	}
	value, _ := clean(p, "", parsed)
	// Imported npx dependencies must remain reproducible under the runtime's
	// pinned/offline dependency contract. Keep other portable command definitions.
	if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"mcp", "mcp_servers", "mcpServers"} {
			servers, _ := object[key].(map[string]any)
			for name, server := range servers {
				if _, _, err := npxPackage(server); err != nil {
					delete(servers, name)
					issues = append(issues, Issue{p + ":" + key + ":" + name, "MCP server excluded: npx requires an exact package version"})
				}
			}
		}
	}
	raw, err := encodeConfig(p, value)
	return raw, issues, err
}

// PortabilityWarnings describe retained values, never fetch endpoints or read
// referenced files. Messages contain field locations but not configuration values.
func PortabilityWarnings(p, content string) []Issue {
	value, err := parseConfig(p, content)
	if err != nil {
		return nil
	}
	issues := []Issue{}
	var visit func(string, any)
	visit = func(location string, item any) {
		switch v := item.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				visit(location+":"+k, v[k])
			}
		case []any:
			for i, x := range v {
				visit(fmt.Sprintf("%s[%d]", location, i), x)
			}
		case string:
			reason := ""
			if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
				if !strings.HasSuffix(location, ":$schema") {
					reason = "Endpoint kept; the Capsule needs network access and any required authentication"
				}
			} else if strings.Contains(v, "/Users/") || strings.Contains(v, "/home/") || strings.Contains(v, "~/") || strings.Contains(v, "C:\\") || strings.HasPrefix(v, "file://") || (strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "/workspace")) {
				reason = "Local path kept; the referenced file or tool must exist inside the Capsule"
			} else if strings.Contains(v, "{env:") || strings.Contains(v, "${") || strings.Contains(v, "{file:") {
				reason = "Reference kept; its environment variable or file must be provided inside the Capsule"
			}
			if reason != "" {
				issues = append(issues, Issue{location, reason})
			}
		}
	}
	visit(p, value)
	if strings.HasSuffix(p, "opencode.json") || strings.HasSuffix(p, "opencode.jsonc") {
		if warning, ok := openCodeDefaultProviderWarning(p, value); ok {
			issues = append(issues, warning)
		}
	}
	return issues
}

// openCodeDefaultProviderWarning tells the importer when the selected model
// depends on a host environment variable or a server the Capsule will not
// have. The connection, not that provider, is what reaches the gateway.
func openCodeDefaultProviderWarning(p string, value any) (Issue, bool) {
	config, ok := value.(map[string]any)
	if !ok {
		return Issue{}, false
	}
	id, ok := openCodeModelProvider(config["model"])
	if !ok || id == "meridian" {
		return Issue{}, false
	}
	provider, ok := openCodeProviderEntry(config, id)
	if !ok || !openCodeProviderLeavesTheCapsule(provider) {
		return Issue{}, false
	}
	kind := "openai-compatible"
	if strings.Contains(openCodeProviderPackage(provider), "anthropic") {
		kind = "anthropic-compatible"
	}
	return Issue{p + ":model", "Default model depends on an environment variable or a server outside the Capsule. Create a provider connection and select it on launch: meridian harness connect " + kind}, true
}

func openCodeModelProvider(model any) (string, bool) {
	switch value := model.(type) {
	case string:
		id, _, ok := strings.Cut(value, "/")
		if !ok || id == "" || strings.ContainsAny(id, "/#") {
			return "", false
		}
		return id, true
	case map[string]any:
		id, _ := value["providerID"].(string)
		if id == "" || strings.ContainsAny(id, "/#") {
			return "", false
		}
		return id, true
	}
	return "", false
}

func openCodeProviderEntry(config map[string]any, id string) (map[string]any, bool) {
	for _, key := range []string{"providers", "provider"} {
		providers, _ := config[key].(map[string]any)
		if entry, ok := providers[id].(map[string]any); ok {
			return entry, true
		}
	}
	return nil, false
}

func openCodeProviderPackage(provider map[string]any) string {
	for _, key := range []string{"package", "npm"} {
		if name, ok := provider[key].(string); ok && name != "" {
			return name
		}
	}
	return ""
}

func openCodeProviderLeavesTheCapsule(provider map[string]any) bool {
	if names, ok := provider["env"].([]any); ok {
		for _, name := range names {
			if text, ok := name.(string); ok && text != "" {
				return true
			}
		}
	}
	for _, key := range []string{"settings", "options"} {
		object, _ := provider[key].(map[string]any)
		if apiKey, ok := object["apiKey"].(string); ok && strings.Contains(apiKey, "{env:") {
			return true
		}
		if base, ok := object["baseURL"].(string); ok && (strings.HasPrefix(base, "https://") || strings.HasPrefix(base, "http://")) {
			return true
		}
	}
	if base, ok := provider["api"].(string); ok && (strings.HasPrefix(base, "https://") || strings.HasPrefix(base, "http://")) {
		return true
	}
	return false
}
