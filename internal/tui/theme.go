package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/OrlojHQ/meridian/pkg/client"
)

type theme struct {
	header         lipgloss.Style
	muted          lipgloss.Style
	selected       lipgloss.Style
	panel          lipgloss.Style
	panelFocused   lipgloss.Style
	border         lipgloss.Style
	borderStrong   lipgloss.Style
	surface        lipgloss.Style
	wordmark       lipgloss.Style
	wordmarkSoft   lipgloss.Style
	section        lipgloss.Style
	primary        lipgloss.Style
	user           lipgloss.Style
	assistant      lipgloss.Style
	tool           lipgloss.Style
	error          lipgloss.Style
	ready          lipgloss.Style
	failed         lipgloss.Style
	paused         lipgloss.Style
	footer         lipgloss.Style
	composer       lipgloss.Style
	composerActive lipgloss.Style
	banner         lipgloss.Style
	title          lipgloss.Style
	accent         lipgloss.Style
	color          bool
}

func currentTheme() theme {
	color := os.Getenv("NO_COLOR") == ""
	base := lipgloss.NewStyle()
	if !color {
		return theme{
			header:         base.Bold(true),
			muted:          base,
			selected:       base.Bold(true),
			panel:          base.Border(lipgloss.RoundedBorder()).Padding(0, 1),
			panelFocused:   base.Border(lipgloss.RoundedBorder()).Padding(0, 1),
			border:         base,
			borderStrong:   base.Bold(true),
			surface:        base,
			wordmark:       base.Bold(true),
			wordmarkSoft:   base,
			section:        base.Bold(true),
			primary:        base.Bold(true),
			user:           base.Bold(true),
			assistant:      base,
			tool:           base,
			error:          base.Bold(true),
			ready:          base,
			failed:         base.Bold(true),
			paused:         base,
			footer:         base,
			composer:       base,
			composerActive: base.Bold(true),
			banner:         base.Bold(true),
			title:          base.Bold(true),
			accent:         base,
			color:          false,
		}
	}
	accent := lipgloss.Color("121")
	accentSoft := lipgloss.Color("44")
	border := lipgloss.Color("238")
	borderStrong := lipgloss.Color("243")
	return theme{
		header:         base.Bold(true).Foreground(lipgloss.Color("252")),
		muted:          base.Foreground(lipgloss.Color("244")),
		selected:       base.Bold(true).Foreground(accent),
		panel:          base.Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1),
		panelFocused:   base.Border(lipgloss.RoundedBorder()).BorderForeground(accentSoft).Padding(0, 1),
		border:         base.Foreground(border),
		borderStrong:   base.Foreground(borderStrong),
		surface:        base.Foreground(lipgloss.Color("252")),
		wordmark:       base.Bold(true).Foreground(accent),
		wordmarkSoft:   base.Foreground(accentSoft),
		section:        base.Bold(true).Foreground(lipgloss.Color("250")),
		primary:        base.Bold(true).Foreground(accent),
		user:           base.Foreground(accent).Bold(true),
		assistant:      base.Foreground(lipgloss.Color("252")),
		tool:           base.Foreground(lipgloss.Color("110")),
		error:          base.Foreground(lipgloss.Color("203")).Bold(true),
		ready:          base.Foreground(lipgloss.Color("114")).Bold(true),
		failed:         base.Foreground(lipgloss.Color("203")).Bold(true),
		paused:         base.Foreground(lipgloss.Color("214")).Bold(true),
		footer:         base.Foreground(lipgloss.Color("242")),
		composer:       base.Foreground(lipgloss.Color("244")),
		composerActive: base.Foreground(accentSoft),
		banner:         base.Foreground(accent).Bold(true),
		title:          base.Bold(true).Foreground(lipgloss.Color("252")),
		accent:         base.Foreground(accent),
		color:          true,
	}
}

func (t theme) chip(state client.CapsuleState) lipgloss.Style {
	switch state {
	case client.CapsuleStateFailed:
		return t.failed
	case client.CapsuleStatePaused:
		return t.paused
	case client.CapsuleStateReady:
		return t.ready
	default:
		return t.muted
	}
}

func capsuleStatus(detail CapsuleDetail) string {
	state := string(detail.Capsule.State)
	switch detail.Capsule.State {
	case client.CapsuleStateReady:
		state = "Ready"
	case client.CapsuleStatePaused:
		state = "Paused"
	case client.CapsuleStateFailed:
		state = "Failed"
	case client.CapsuleStateCreating, client.CapsuleStatePreparing:
		state = "Starting"
		if p, ok := detail.Capsule.Preparation.Get(); ok {
			state = preparationLabel(string(p.Stage))
		}
	}
	if failure, ok := detail.Capsule.Failure.Get(); ok && failure != "" {
		return state + " · " + compactFailure(failure)
	}
	return state
}

func compactFailure(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.LastIndex(value, ": "); i >= 0 && i+2 < len(value) {
		return strings.TrimSpace(value[i+2:])
	}
	return value
}

func friendlyStatus(value string) string {
	switch value {
	case "Project Thread provisioning requested":
		return "Starting a new Capsule…"
	case "Project created":
		return "Project created"
	case "Capsule creation requested":
		return "Creating Capsule…"
	case "Message sent":
		return "Message sent"
	case "Thread created":
		return "Conversation started"
	case "Response sent":
		return "Answer sent"
	}
	if strings.HasPrefix(value, "Action failed: ") {
		detail := strings.TrimPrefix(value, "Action failed: ")
		if strings.Contains(detail, "not accepting input") || strings.Contains(detail, "no active structured session") {
			return "Session ended. Press Enter to start it and send."
		}
		if strings.Contains(detail, "already has an active Thread") {
			return "This Capsule already has a live session. Keep talking here, or /new for another Capsule."
		}
		if strings.Contains(strings.ToLower(detail), "resource version") ||
			strings.Contains(strings.ToLower(detail), "stale") {
			return "Session changed. Retry send."
		}
		return "Couldn’t complete that: " + detail
	}
	return value
}

func conversationTitle(detail ThreadDetail) string {
	if count := detail.Thread.MessageCount; count > 0 {
		return detail.Thread.Harness + " · " + shortID(detail.Thread.ID)
	}
	return "New conversation · " + shortID(detail.Thread.ID)
}

func conversationPreview(detail ThreadDetail) string {
	for index := len(detail.Blocks) - 1; index >= 0; index-- {
		block := detail.Blocks[index]
		if event, ok := block.Event.Get(); ok {
			switch event.Type {
			case client.ThreadAdapterEventTypeToolStart, client.ThreadAdapterEventTypeToolResult,
				client.ThreadAdapterEventTypeStatus, client.ThreadAdapterEventTypePermissionRequest,
				client.ThreadAdapterEventTypeInputRequest, client.ThreadAdapterEventTypePermissionResponse,
				client.ThreadAdapterEventTypeInputResponse, client.ThreadAdapterEventTypeGap,
				client.ThreadAdapterEventTypeEnd:
				continue
			}
		}
		content, ok := block.Content.Get()
		if !ok {
			continue
		}
		content = strings.TrimSpace(strings.Map(func(current rune) rune {
			if current == '\n' || current == '\t' {
				return ' '
			}
			if current < 32 {
				return -1
			}
			return current
		}, content))
		if content == "" {
			continue
		}
		runes := []rune(content)
		if len(runes) > 36 {
			return string(runes[:36]) + "…"
		}
		return content
	}
	if detail.Thread.MessageCount > 0 {
		return fmt.Sprintf("%d messages", detail.Thread.MessageCount)
	}
	return ""
}
