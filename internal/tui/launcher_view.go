package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func (m Model) launcherBody(contract viewContract) string {
	theme := currentTheme()
	if m.pendingNew {
		formWidth := min(62, contract.contentWidth)
		form := panel(theme, "NEW CAPSULE", m.newCapsuleView(), formWidth, 0, true)
		return lipgloss.Place(
			contract.contentWidth, contract.bodyHeight,
			lipgloss.Center, lipgloss.Center, form,
		)
	}

	fleet := m.launcherFleet(contract.fleetWidth, contract.bodyHeight, contract.showInspector)
	if !contract.showInspector {
		return fleet
	}
	inspector := m.launcherInspector(contract.inspectorWidth, contract.bodyHeight)
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		fleet,
		lipgloss.NewStyle().Width(2).Render(""),
		inspector,
	)
}

func (m Model) launcherFleet(width, height int, framed bool) string {
	theme := currentTheme()
	visible := m.visibleCapsules()
	contentWidth := width
	if framed {
		contentWidth = max(1, width-2)
	}
	rows := make([]string, 0, len(visible)+2)
	rows = append(rows, pageLine(theme.section.Render("CAPSULES"), theme.muted.Render(fmt.Sprintf("%d", len(visible))), contentWidth))
	rows = append(rows, "")
	for _, index := range visible {
		rows = append(rows, m.launcherRow(m.snapshot.Capsules[index], contentWidth, index == m.selected))
	}
	if len(visible) == 0 {
		rows = append(rows, theme.muted.Render("No Capsules yet"))
	}
	content := strings.Join(rows, "\n")
	if framed {
		return panel(theme, "FLEET", content, width, height, m.focus == focusCapsules)
	}
	return lipgloss.NewStyle().Width(width).Height(height).Render(clipToHeight(content, height))
}

func (m Model) launcherRow(detail CapsuleDetail, width int, selected bool) string {
	theme := currentTheme()
	marker := "  "
	if selected {
		marker = theme.primary.Render("▌ ")
	}
	nameWidth := max(10, width-29)
	harnessWidth := 12
	if width < 56 {
		harnessWidth = 10
		nameWidth = max(8, width-25)
	}
	name := padCell(truncateWidth(safeInline(detail.Capsule.Name), nameWidth), nameWidth)
	harness := "—"
	if value, ok := detail.Capsule.Harness.Get(); ok && strings.TrimSpace(value) != "" {
		harness = safeInline(value)
	}
	harness = padCell(truncateWidth(harness, harnessWidth), harnessWidth)
	label, tone := launcherState(detail)
	row := marker + name + "  " + theme.muted.Render(harness) + "  " + badge(theme, label, tone)
	if selected {
		row = theme.selected.Render(row)
	}
	return truncateWidth(row, width)
}

func (m Model) launcherInspector(width, height int) string {
	theme := currentTheme()
	detail := m.selectedDetail()
	if detail == nil {
		return panel(theme, "CAPSULE", theme.muted.Render("Select a Capsule"), width, height, false)
	}
	label, tone := launcherState(*detail)
	harness := "Unavailable"
	if value, ok := detail.Capsule.Harness.Get(); ok && value != "" {
		harness = safeInline(value)
	}
	rows := []string{
		theme.title.Render(truncateWidth(safeInline(detail.Capsule.Name), width-2)),
		"",
		inspectorRow(theme, "State", badge(theme, label, tone), width-2),
		inspectorRow(theme, "Harness", harness, width-2),
		inspectorRow(theme, "Activity", relativeActivity(m.now(), capsuleActivity(*detail)), width-2),
	}
	if run := detail.ActiveRun(); run != nil {
		rows = append(rows, inspectorRow(theme, "Run", shortID(run.ID), width-2))
	}
	if len(detail.Moments) > 0 {
		rows = append(rows, inspectorRow(theme, "Moments", fmt.Sprintf("%d", len(detail.Moments)), width-2))
	}
	if failure, ok := detail.Capsule.Failure.Get(); ok && failure != "" {
		rows = append(
			rows, "", divider(theme, "NEEDS ATTENTION", width-2),
			theme.error.Render(truncateWidth("Failure: "+safeInline(failure), width-2)),
		)
	}
	if detail.EventError != "" {
		rows = append(
			rows, "", divider(theme, "RUNTIME WARNING", width-2),
			theme.error.Render(truncateWidth("Events: "+safeInline(detail.EventError), width-2)),
		)
	}
	rows = append(rows, "", divider(theme, "PRIMARY ACTION", width-2))
	if detail.RunningRun() != nil {
		rows = append(rows, keyHint(theme, "Enter", "open harness"))
	} else {
		rows = append(rows, keyHint(theme, "Enter", "start harness"))
	}
	return panel(theme, "CAPSULE", strings.Join(rows, "\n"), width, height, false)
}

func launcherState(detail CapsuleDetail) (string, string) {
	if detail.RunningRun() != nil {
		return "Running", "running"
	}
	if run := detail.ActiveRun(); run != nil {
		switch run.State {
		case client.RunStateQueued, client.RunStateStarting:
			return "Starting", "waiting"
		case client.RunStateCancelling:
			return "Stopping", "waiting"
		}
	}
	switch detail.Capsule.State {
	case client.CapsuleStateReady:
		return "Ready", "ready"
	case client.CapsuleStatePaused:
		return "Paused", "paused"
	case client.CapsuleStateFailed:
		return "Failed", "failed"
	case client.CapsuleStateCreating, client.CapsuleStatePreparing:
		return "Starting", "waiting"
	default:
		return string(detail.Capsule.State), ""
	}
}

func inspectorRow(theme theme, label, value string, width int) string {
	labelWidth := min(10, max(7, width/3))
	left := padCell(theme.muted.Render(label), labelWidth)
	return truncateWidth(left+"  "+value, width)
}

func relativeActivity(now, activity time.Time) string {
	if activity.IsZero() {
		return "Unavailable"
	}
	age := now.Sub(activity)
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(age/(24*time.Hour)))
	}
}

func padCell(value string, width int) string {
	padding := width - lipgloss.Width(value)
	if padding <= 0 {
		return value
	}
	return value + strings.Repeat(" ", padding)
}
