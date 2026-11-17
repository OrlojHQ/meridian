package harnesssetup

import "slices"

// ModelReasoning reports whether an imported model configuration says the
// model accepts a reasoning effort. "yes" and "no" are explicit. "" means the
// configuration does not say.
func ModelReasoning(model map[string]any) string {
	if model == nil {
		return ""
	}
	yes, no := false, false
	note := func(value string) {
		switch value {
		case "yes":
			yes = true
		case "no":
			no = true
		}
	}
	note(boolReasoning(model["reasoning"]))
	if capabilities, ok := model["capabilities"].(map[string]any); ok {
		note(nestedReasoning(capabilities["reasoning"]))
	}
	if options, ok := model["options"].(map[string]any); ok {
		note(optionReasoning(options))
	}
	if variants, ok := model["variants"].(map[string]any); ok {
		for name, raw := range variants {
			if slices.Contains(EffortLevels, name) {
				yes = true
			}
			item, _ := raw.(map[string]any)
			if effortText(item, "reasoningEffort") || effortText(item, "reasoning_effort") {
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

func boolReasoning(value any) string {
	enabled, ok := value.(bool)
	if !ok {
		return ""
	}
	if enabled {
		return "yes"
	}
	return "no"
}

func nestedReasoning(value any) string {
	if text := boolReasoning(value); text != "" {
		return text
	}
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if text := boolReasoning(object["supported"]); text != "" {
		return text
	}
	return boolReasoning(object["reasoning"])
}

func optionReasoning(options map[string]any) string {
	if effortText(options, "reasoningEffort") || effortText(options, "reasoning_effort") {
		return "yes"
	}
	return boolReasoning(options["reasoning"])
}

func effortText(object map[string]any, key string) bool {
	text, _ := object[key].(string)
	return text != ""
}
