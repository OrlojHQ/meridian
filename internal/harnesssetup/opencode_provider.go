package harnesssetup

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// OpenCodeModel is one model from an imported OpenCode provider. Name is the
// label from the setup; Context and Output are token limits when declared.
type OpenCodeModel struct {
	ID      string
	Name    string
	Context int
	Output  int
	// Entry is the model object with credential fields removed, so a Capsule
	// can keep names and limits without a copied secret.
	Entry map[string]any
	// Reasoning is "yes" or "no" when the model configuration says whether
	// it accepts an effort level, and empty when it does not say.
	Reasoning string
}

// OpenCodeProvider is one provider entry from an imported OpenCode config.
type OpenCodeProvider struct {
	ID         string
	Name       string
	BaseURL    string
	Kind       string
	Credential string
	Models     []OpenCodeModel
}

// Unsupplied reports that the Capsule cannot authenticate this provider.
// A gateway token reference is the only credential Meridian can supply.
// An environment variable, a file reference such as an authentication file,
// a literal key, or no key at all all count as unsupplied.
func (p OpenCodeProvider) Unsupplied() bool {
	return p.Credential != "supplied"
}

// SameEndpoint reports whether two provider base URLs name the same
// endpoint. A trailing slash or /v1 does not make them different.
func SameEndpoint(a, b string) bool {
	left, right := endpointKey(a), endpointKey(b)
	return left != "" && left == right
}

func endpointKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + path
}

// OpenCodeProviders lists provider entries from an OpenCode config object.
func OpenCodeProviders(config map[string]any) []OpenCodeProvider {
	if config == nil {
		return nil
	}
	var out []OpenCodeProvider
	seen := map[string]bool{}
	for _, key := range []string{"providers", "provider"} {
		providers, _ := config[key].(map[string]any)
		ids := make([]string, 0, len(providers))
		for id := range providers {
			ids = append(ids, id)
		}
		// Stable order keeps warnings and fold ids deterministic.
		sort.Strings(ids)
		for _, id := range ids {
			if seen[id] {
				continue
			}
			entry, _ := providers[id].(map[string]any)
			if entry == nil {
				continue
			}
			seen[id] = true
			out = append(out, describeOpenCodeProvider(id, entry))
		}
	}
	return out
}

func describeOpenCodeProvider(id string, entry map[string]any) OpenCodeProvider {
	name, _ := entry["name"].(string)
	name = cleanDisplay(name)
	if name == "" {
		name = id
	}
	kind := "openai-compatible"
	if strings.Contains(strings.ToLower(openCodeProviderPackage(entry)), "anthropic") {
		kind = "anthropic-compatible"
	}
	return OpenCodeProvider{
		ID: id, Name: name, BaseURL: openCodeProviderBase(entry),
		Kind: kind, Credential: openCodeCredential(entry), Models: openCodeModels(entry),
	}
}

func openCodeProviderBase(entry map[string]any) string {
	for _, key := range []string{"settings", "options"} {
		object, _ := entry[key].(map[string]any)
		if base, ok := object["baseURL"].(string); ok && base != "" {
			return base
		}
	}
	if base, ok := entry["api"].(string); ok && (strings.HasPrefix(base, "https://") || strings.HasPrefix(base, "http://")) {
		return base
	}
	return ""
}

func openCodeCredential(entry map[string]any) string {
	if names, ok := entry["env"].([]any); ok {
		for _, name := range names {
			text, _ := name.(string)
			if text != "" && text != "MERIDIAN_PROVIDER_TOKEN" {
				return "env"
			}
		}
	}
	for _, key := range []string{"settings", "options"} {
		object, _ := entry[key].(map[string]any)
		apiKey, _ := object["apiKey"].(string)
		switch {
		case apiKey == "{env:MERIDIAN_PROVIDER_TOKEN}":
			return "supplied"
		case strings.Contains(apiKey, "{env:"):
			return "env"
		case strings.Contains(apiKey, "{file:"):
			return "file"
		case apiKey != "":
			return "literal"
		}
	}
	return "none"
}

func openCodeModels(entry map[string]any) []OpenCodeModel {
	raw, _ := entry["models"].(map[string]any)
	ids := make([]string, 0, len(raw))
	for id := range raw {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]OpenCodeModel, 0, len(ids))
	for _, id := range ids {
		if !ValidModel(id) {
			continue
		}
		model, _ := raw[id].(map[string]any)
		item := OpenCodeModel{ID: id, Name: id}
		if model != nil {
			if name, ok := model["name"].(string); ok && cleanDisplay(name) != "" {
				item.Name = cleanDisplay(name)
			}
			limit, _ := model["limit"].(map[string]any)
			item.Context = numberField(limit, "context")
			item.Output = numberField(limit, "output")
			item.Reasoning = ModelReasoning(model)
			item.Entry = copyModelEntry(model)
		}
		if item.Entry == nil {
			item.Entry = map[string]any{}
		}
		if _, ok := item.Entry["name"]; !ok {
			item.Entry["name"] = item.Name
		}
		out = append(out, item)
	}
	return out
}

func numberField(object map[string]any, key string) int {
	switch value := object[key].(type) {
	case float64:
		if value > 0 && value < 1<<31 {
			return int(value)
		}
	case int:
		if value > 0 {
			return value
		}
	}
	return 0
}

func copyModelEntry(model map[string]any) map[string]any {
	raw, err := json.Marshal(model)
	if err != nil {
		return map[string]any{}
	}
	var copy map[string]any
	if json.Unmarshal(raw, &copy) != nil || copy == nil {
		return map[string]any{}
	}
	stripCredentialFields(copy)
	return copy
}

func stripCredentialFields(value map[string]any) {
	for key, item := range value {
		if credentialKey(key) {
			delete(value, key)
			continue
		}
		child, ok := item.(map[string]any)
		if ok {
			stripCredentialFields(child)
		}
	}
}

func cleanDisplay(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

// OpenCodeProviderWarnings reports imported providers the Capsule cannot
// authenticate. The reason names the provider, never its key or endpoint.
// Connect carries the endpoint so the settings page can add the connection.
func OpenCodeProviderWarnings(p string, value any) []Issue {
	config, _ := value.(map[string]any)
	if config == nil {
		return nil
	}
	selected, _ := openCodeModelProvider(config["model"])
	var warnings []Issue
	for _, provider := range OpenCodeProviders(config) {
		if !provider.Unsupplied() || provider.ID == "meridian" {
			continue
		}
		model := ""
		if provider.ID == selected {
			_, modelID, ok := splitOpenCodeModel(config["model"])
			if ok {
				model = modelID
			}
		}
		if model == "" && len(provider.Models) == 1 {
			model = provider.Models[0].ID
		}
		reason := "Provider \"" + provider.Name + "\" needs a " + provider.Kind + " provider connection before its models can run in a Capsule."
		issue := Issue{Path: p + ":provider:" + provider.ID, Reason: reason}
		if action, ok := connectAction(provider, model); ok {
			issue.Connect = &action
		}
		warnings = append(warnings, issue)
	}
	return warnings
}

func splitOpenCodeModel(model any) (string, string, bool) {
	switch value := model.(type) {
	case string:
		id, rest, ok := strings.Cut(value, "/")
		if !ok || id == "" || !ValidModel(rest) || strings.ContainsAny(id, "/#") {
			return "", "", false
		}
		return id, rest, true
	case map[string]any:
		id, _ := value["providerID"].(string)
		modelID, _ := value["modelID"].(string)
		if modelID == "" {
			modelID, _ = value["model"].(string)
		}
		if id == "" || modelID == "" || strings.Contains(modelID, "/") {
			return "", "", false
		}
		return id, modelID, true
	}
	return "", "", false
}

func connectAction(provider OpenCodeProvider, model string) (ConnectAction, bool) {
	if provider.BaseURL == "" || (provider.Kind != "openai-compatible" && provider.Kind != "anthropic-compatible") {
		return ConnectAction{}, false
	}
	u, err := url.Parse(provider.BaseURL)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return ConnectAction{}, false
	}
	if model != "" && !ValidModel(model) {
		model = ""
	}
	action := ConnectAction{Name: provider.Name, Provider: provider.Kind, BaseURL: provider.BaseURL, Model: model}
	if !validConnect(action) {
		return ConnectAction{}, false
	}
	return action, true
}

// OpenCodeFoldIDs returns imported provider ids whose endpoint matches
// baseURL. The Capsule uses the ids to copy models onto the gateway
// provider. The address itself is not returned.
func OpenCodeFoldIDs(files []File, baseURL string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, file := range files {
		if !strings.HasSuffix(file.Path, "opencode.json") && !strings.HasSuffix(file.Path, "opencode.jsonc") {
			continue
		}
		value, err := parseConfig(file.Path, file.Content)
		if err != nil {
			continue
		}
		config, _ := value.(map[string]any)
		for _, provider := range OpenCodeProviders(config) {
			if seen[provider.ID] || !provider.Unsupplied() || !SameEndpoint(provider.BaseURL, baseURL) {
				continue
			}
			seen[provider.ID] = true
			ids = append(ids, provider.ID)
		}
	}
	return ids
}

// OpenCodeModelCatalog returns names and limits for models of imported
// providers that use baseURL, and the setup's selected model id when it
// belongs to one of them.
func OpenCodeModelCatalog(files []File, baseURL string) (suggested string, models map[string]OpenCodeModel) {
	models = map[string]OpenCodeModel{}
	for _, file := range files {
		if !strings.HasSuffix(file.Path, "opencode.json") && !strings.HasSuffix(file.Path, "opencode.jsonc") {
			continue
		}
		value, err := parseConfig(file.Path, file.Content)
		if err != nil {
			continue
		}
		config, _ := value.(map[string]any)
		selectedProvider, selectedModel, _ := splitOpenCodeModel(config["model"])
		for _, provider := range OpenCodeProviders(config) {
			if !SameEndpoint(provider.BaseURL, baseURL) {
				continue
			}
			if provider.ID == selectedProvider && selectedModel != "" && ValidModel(selectedModel) {
				suggested = selectedModel
			}
			for _, model := range provider.Models {
				if _, exists := models[model.ID]; !exists {
					models[model.ID] = model
				}
			}
		}
	}
	return suggested, models
}
