package tui

import (
	"strings"
	"testing"
)

func TestTransitMark(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := currentTheme()
	mark := transitMark(theme)
	if !strings.Contains(mark, "●") || strings.Count(mark, "│") != 4 ||
		!strings.HasPrefix(mark, "    ●") {
		t.Fatalf("transit mark = %q", mark)
	}
	if compact := transitMarkCompact(theme); compact != "│●│" {
		t.Fatalf("compact transit = %q", compact)
	}
}

func TestCenterBlockKeepsTransitAxis(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	mark := transitMark(currentTheme())
	centered := centerBlock(mark, 40)
	original := meridianColumns(mark)
	got := meridianColumns(centered)
	if len(original) == 0 || len(got) != len(original) {
		t.Fatalf("meridian columns original=%v centered=%v", original, got)
	}
	shift := got[0] - original[0]
	for index, column := range original {
		if got[index]-column != shift {
			t.Fatalf("transit axis bent: original=%v centered=%v", original, got)
		}
	}
}

func TestCenteredHomeKeepsTransitAxis(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	model := NewModel(Options{})
	model.connected = true
	model.snapshot = testSnapshot()
	body := model.homeBody(contractFor(100, 30, 3))
	columns := meridianColumns(body)
	if len(columns) != 4 {
		t.Fatalf("home transit columns = %v", columns)
	}
	for _, column := range columns[1:] {
		if column != columns[0] {
			t.Fatalf("home transit axis bent: %v", columns)
		}
	}
}

func meridianColumns(mark string) []int {
	var columns []int
	for _, line := range strings.Split(mark, "\n") {
		if index := strings.IndexRune(line, '│'); index >= 0 {
			columns = append(columns, index)
		}
	}
	return columns
}
