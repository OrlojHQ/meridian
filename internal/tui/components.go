package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type viewContract struct {
	width          int
	height         int
	gutter         int
	contentWidth   int
	bodyHeight     int
	showInspector  bool
	fleetWidth     int
	inspectorWidth int
}

func contractFor(width, height, commandRows int) viewContract {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	gutter := 1
	if width >= 100 {
		gutter = 2
	}
	if width >= 140 {
		gutter = 4
	}
	contentWidth := max(36, width-(gutter*2))
	showInspector := width >= 100
	fleetWidth := contentWidth
	inspectorWidth := 0
	if showInspector {
		inspectorWidth = max(32, contentWidth*38/100)
		fleetWidth = max(42, contentWidth-inspectorWidth-2)
	}
	// Two header rows, one footer row, and a stable command slot keep the
	// content anchored while slash results appear above the command bar.
	reserved := 5 + commandRows
	return viewContract{
		width: width, height: height, gutter: gutter, contentWidth: contentWidth,
		bodyHeight: max(8, height-reserved), showInspector: showInspector,
		fleetWidth: fleetWidth, inspectorWidth: inspectorWidth,
	}
}

func panel(t theme, title, content string, width, height int, focused bool) string {
	frame := t.border
	if focused {
		frame = t.composerActive
	}
	if height > 0 {
		content = clipToHeight(content, max(1, height-2))
	}
	value := labeledFrame(width, frame, safeInline(title), "", content, "", "")
	if height <= 0 {
		return value
	}
	return lipgloss.NewStyle().Width(width).Height(height).Render(value)
}

func badge(t theme, label, tone string) string {
	style := t.muted
	switch tone {
	case "ready", "running":
		style = t.ready
	case "paused", "waiting":
		style = t.paused
	case "failed":
		style = t.failed
	}
	return style.Render(strings.ToUpper(label))
}

func keyHint(t theme, key, label string) string {
	return t.primary.Render(key) + " " + t.muted.Render(label)
}

func commandBar(t theme, value, mode, context, hint string, width int, cursor bool) string {
	if cursor {
		value += caret(true)
	}
	return labeledFrame(width, t.composerActive, mode, "", value, context, hint)
}

func modal(t theme, title, body string, width int) string {
	return panel(t, title, body, width, 0, true)
}

func emptyState(t theme, mark, title, copy, action string, width int) string {
	text := t.title.Render(title)
	if copy != "" {
		text += "\n\n" + t.muted.Render(copy)
	}
	if action != "" {
		text += "\n\n" + action
	}
	if mark == "" || width < 72 {
		return text
	}
	markWidth := min(30, max(18, width/3))
	textWidth := max(30, width-markWidth-4)
	return lipgloss.JoinHorizontal(
		lipgloss.Center,
		lipgloss.NewStyle().Width(markWidth).Render(mark),
		lipgloss.NewStyle().Width(4).Render(""),
		lipgloss.NewStyle().Width(textWidth).Render(text),
	)
}

func divider(t theme, label string, width int) string {
	if width < 4 {
		return ""
	}
	if label == "" {
		return t.border.Render(strings.Repeat("─", width))
	}
	label = " " + safeInline(label) + " "
	fill := max(1, width-lipgloss.Width(label))
	return t.border.Render(label + strings.Repeat("─", fill))
}

func orbitalMark(t theme) string {
	lines := []string{
		"          · · ·",
		"      · ● ● ● · ·",
		"   · ● ● ● ● · · ·",
		"  · ● ● ● · · ·",
		"   · ● ● · ·",
		"      · ·",
	}
	if !t.color {
		return strings.Join(lines, "\n")
	}
	styles := []lipgloss.Style{t.wordmark, t.wordmarkSoft, t.accent, t.muted}
	for index := range lines {
		lines[index] = styles[min(index/2, len(styles)-1)].Render(lines[index])
	}
	return strings.Join(lines, "\n")
}

func pageLine(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		rightWidth := lipgloss.Width(right)
		if rightWidth >= width {
			return truncateWidth(right, width)
		}
		left = truncateWidth(left, max(1, width-rightWidth-1))
		gap = max(1, width-lipgloss.Width(left)-rightWidth)
	}
	return left + strings.Repeat(" ", gap) + right
}

func pageGutter(content string, columns int) string {
	if columns <= 0 {
		return content
	}
	prefix := strings.Repeat(" ", columns)
	lines := strings.Split(content, "\n")
	for index := range lines {
		lines[index] = prefix + lines[index]
	}
	return strings.Join(lines, "\n")
}
