package harnessadapter

import "testing"

func TestClampEffortID(t *testing.T) {
	levels := []string{"low", "medium", "max"}
	if got := clampEffortID(levels, "high", ""); got != "medium" {
		t.Fatalf("clamp high = %q, want medium", got)
	}
	if got := clampEffortID(levels, "max", "low"); got != "max" {
		t.Fatalf("offered level ignores fallback, got %q", got)
	}
	if got := clampEffortID(levels, "high", "low"); got != "low" {
		t.Fatalf("model default = %q, want low", got)
	}
	if got := clampEffortID(levels, "minimal", ""); got != "low" {
		t.Fatalf("clamp below the scale = %q, want low", got)
	}
	if got := clampEffortID(levels, "xhigh", ""); got != "medium" {
		t.Fatalf("clamp xhigh = %q, want medium", got)
	}
	if got := clampEffortID(levels, "xhigh", "low"); got != "low" {
		t.Fatalf("fallback before scale clamp = %q, want low", got)
	}
	if got := clampEffortID(levels, "", "low"); got != "low" {
		t.Fatalf("fallback = %q, want low", got)
	}
	if got := clampEffortID(levels, "", ""); got != "" {
		t.Fatalf("unknown = %q, want empty", got)
	}
	if got := clampEffortID([]string{"custom"}, "high", ""); got != "custom" {
		t.Fatalf("unranked list = %q, want custom", got)
	}
}

func TestReportedEffortClampsAnUnsupportedCurrent(t *testing.T) {
	models := newModelSet()
	models.add("haiku", "Haiku", "")
	models.setCurrent("haiku")
	models.effort = effortLevels([]string{"low", "medium"}, "high")
	frame := models.set()
	if frame.Effort == nil || frame.Effort.Current != "medium" || len(frame.Effort.Options) != 2 {
		t.Fatalf("clamped report = %#v", frame.Effort)
	}
	models.effort = effortLevels([]string{"low", "high"}, "")
	frame = models.set()
	if frame.Effort == nil || frame.Effort.Current != "" {
		t.Fatalf("unknown report = %#v", frame.Effort)
	}
}
