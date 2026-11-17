package app

import "testing"

func TestResolveConnectionReasoning(t *testing.T) {
	efforts, mode := resolveConnectionReasoning([]string{"high", "low"}, "no", true)
	if mode != "yes" || len(efforts) != 2 || efforts[0] != "high" {
		t.Fatalf("saved = %v %s", efforts, mode)
	}
	efforts, mode = resolveConnectionReasoning(nil, "yes", false)
	if mode != "yes" || len(efforts) != 6 || efforts[2] != "medium" {
		t.Fatalf("detected = %v %s", efforts, mode)
	}
	efforts, mode = resolveConnectionReasoning(nil, "", false)
	if mode != "" || efforts != nil {
		t.Fatalf("unknown = %v %s", efforts, mode)
	}
	efforts, mode = resolveConnectionReasoning(nil, "yes", true)
	if mode != "no" || efforts != nil {
		t.Fatalf("blocked = %v %s", efforts, mode)
	}
}
