package harnesssetup

import "testing"

func TestModelReasoningReadsVariantsAndOptions(t *testing.T) {
	if got := ModelReasoning(map[string]any{
		"variants": map[string]any{"high": map[string]any{"reasoningEffort": "high"}},
	}); got != "yes" {
		t.Fatalf("variant = %q", got)
	}
	if got := ModelReasoning(map[string]any{
		"variants": map[string]any{"medium": map[string]any{}},
	}); got != "yes" {
		t.Fatalf("named variant = %q", got)
	}
	if got := ModelReasoning(map[string]any{
		"options": map[string]any{"reasoningEffort": "low"},
	}); got != "yes" {
		t.Fatalf("option = %q", got)
	}
	if got := ModelReasoning(map[string]any{
		"variants": []any{map[string]any{"id": "high", "settings": map[string]any{"reasoningEffort": "high"}}},
	}); got != "yes" {
		t.Fatalf("array variant = %q", got)
	}
	if got := ModelReasoning(map[string]any{
		"capabilities": map[string]any{"reasoning": map[string]any{"supported": false}},
	}); got != "no" {
		t.Fatalf("unsupported = %q", got)
	}
	if got := ModelReasoning(map[string]any{"name": "Alpha"}); got != "" {
		t.Fatalf("unspecified = %q", got)
	}
	// A variant that names an effort wins over an explicit negative flag.
	if got := ModelReasoning(map[string]any{
		"reasoning": false,
		"variants":  map[string]any{"low": map[string]any{"reasoning_effort": "low"}},
	}); got != "yes" {
		t.Fatalf("conflict = %q", got)
	}
}
