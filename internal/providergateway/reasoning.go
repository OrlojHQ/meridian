package providergateway

import (
	"encoding/json"
	"strings"
)

// applyReasoning edits a provider request for one model.
//
// mode "yes" leaves a reasoning effort the harness chose. mode "no", or
// strip, removes it. An empty mode means support is unknown: a chat or
// responses request with no effort is sent at medium. The bool reports
// whether the request that will be sent still carries an effort.
func applyReasoning(body []byte, route, mode string, strip bool) ([]byte, bool) {
	var object map[string]any
	if json.Unmarshal(body, &object) != nil || object == nil {
		return body, false
	}
	changed := false
	if strip || mode == "no" {
		changed = removeReasoning(object)
	} else if mode == "" && !hasReasoning(object) {
		changed = addDefaultReasoning(object, route)
	}
	if !changed {
		return body, hasReasoning(object)
	}
	next, err := json.Marshal(object)
	if err != nil {
		return body, hasReasoning(object)
	}
	return next, hasReasoning(object)
}

func hasReasoning(object map[string]any) bool {
	if _, ok := object["reasoning_effort"]; ok {
		return true
	}
	reasoning, ok := object["reasoning"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = reasoning["effort"]
	return ok
}

func removeReasoning(object map[string]any) bool {
	_, had := object["reasoning_effort"]
	delete(object, "reasoning_effort")
	reasoning, ok := object["reasoning"].(map[string]any)
	if !ok {
		return had
	}
	if _, ok := reasoning["effort"]; ok {
		had = true
		delete(reasoning, "effort")
	}
	if len(reasoning) == 0 {
		delete(object, "reasoning")
	}
	return had
}

func addDefaultReasoning(object map[string]any, route string) bool {
	switch route {
	case "/v1/chat/completions":
		object["reasoning_effort"] = "medium"
		return true
	case "/v1/responses":
		reasoning, _ := object["reasoning"].(map[string]any)
		if reasoning == nil {
			reasoning = map[string]any{}
			object["reasoning"] = reasoning
		}
		reasoning["effort"] = "medium"
		return true
	default:
		return false
	}
}

func requestModel(body []byte) string {
	var object struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &object) != nil {
		return ""
	}
	return object.Model
}

// reasoningRejected reports whether an upstream error is about the reasoning
// effort parameter. Other failures are left alone.
func reasoningRejected(message string) bool {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "reasoning_effort") || strings.Contains(lower, "reasoning.effort") {
		return true
	}
	if !strings.Contains(lower, "reasoning") {
		return false
	}
	for _, phrase := range []string{"not support", "unsupported", "unknown parameter", "unexpected", "unrecognized", "invalid parameter", "extra field"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// readReasoningFlags collects explicit per-model answers from a model list
// or a model-info document. Absent metadata is omitted. A model id is never
// invented from an empty object.
func readReasoningFlags(body []byte) map[string]string {
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	out := map[string]string{}
	for _, item := range payload.Data {
		id := firstString(item, "id", "model_name", "model")
		if id == "" {
			continue
		}
		state := itemReasoning(item)
		if state == "" {
			continue
		}
		if out[id] == "yes" {
			continue
		}
		out[id] = state
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func itemReasoning(item map[string]any) string {
	yes, no := false, false
	note := func(value string) {
		switch value {
		case "yes":
			yes = true
		case "no":
			no = true
		}
	}
	for _, key := range []string{"supports_reasoning", "supports_reasoning_effort", "reasoning"} {
		note(boolText(item[key]))
	}
	if info, ok := item["model_info"].(map[string]any); ok {
		for _, key := range []string{"supports_reasoning", "supports_reasoning_effort"} {
			note(boolText(info[key]))
		}
	}
	if capabilities, ok := item["capabilities"].(map[string]any); ok {
		note(boolText(capabilities["reasoning"]))
		if object, ok := capabilities["reasoning"].(map[string]any); ok {
			note(boolText(object["supported"]))
		}
	}
	if parameters, ok := item["supported_parameters"].([]any); ok {
		for _, parameter := range parameters {
			name, _ := parameter.(string)
			if name == "reasoning_effort" || name == "reasoning.effort" {
				yes = true
			}
		}
	}
	if yes {
		return "yes"
	}
	if no {
		return "no"
	}
	return ""
}

func boolText(value any) string {
	enabled, ok := value.(bool)
	if !ok {
		return ""
	}
	if enabled {
		return "yes"
	}
	return "no"
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		text, _ := object[key].(string)
		if text != "" {
			return text
		}
	}
	return ""
}
