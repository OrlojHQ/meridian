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
	value, err := parseConfig(p, content)
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
	clean(p, "", value)
	// Imported npx dependencies must remain reproducible under the runtime's
	// pinned/offline dependency contract. Keep other portable command definitions.
	for _, key := range []string{"mcp", "mcp_servers", "mcpServers"} {
		servers, _ := value[key].(map[string]any)
		for name, server := range servers {
			if _, _, err := npxPackage(server); err != nil {
				delete(servers, name)
				issues = append(issues, Issue{p + ":" + key + ":" + name, "MCP server excluded: npx requires an exact package version"})
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
	return issues
}
