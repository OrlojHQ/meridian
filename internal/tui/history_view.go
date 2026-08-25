package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func (m Model) historyBody(contract viewContract) string {
	theme := currentTheme()
	detail := m.selectedDetail()
	thread := m.selectedThread()
	if detail == nil || thread == nil {
		return emptyState(
			theme, "", "History unavailable",
			"The selected structured Thread is no longer available.",
			keyHint(theme, "Esc", "return to Capsules"), contract.contentWidth,
		)
	}
	header := pageLine(
		theme.section.Render("STRUCTURED HISTORY"),
		keyHint(theme, "Esc", "Capsules"),
		contract.contentWidth,
	)
	transcriptHeight := max(6, contract.bodyHeight-2)
	content := m.threadView(*detail, *thread, transcriptHeight-2)
	frame := panel(theme, conversationTitle(*thread), content, contract.contentWidth, transcriptHeight, true)
	return lipgloss.NewStyle().Width(contract.contentWidth).Render(
		strings.Join([]string{header, "", frame}, "\n"),
	)
}

func (m Model) threadView(capsule CapsuleDetail, detail ThreadDetail, transcriptHeight int) string {
	theme := currentTheme()
	thread := detail.Thread
	runState := ""
	if threadActiveRun(thread) {
		runState = "talking"
	} else if value, ok := thread.CurrentRunState.Get(); ok && value == client.RunStateFailed {
		runState = "session ended"
	} else if thread.State == client.ThreadStatePaused {
		runState = "paused"
	}
	lines := []string{theme.title.Render(safeInline(capsule.Capsule.Name))}
	if runState != "" && runState != "talking" {
		lines = append(lines, theme.muted.Render(runState))
	}
	if value, ok := thread.StructuredSupported.Get(); ok && !value {
		lines = append(lines, theme.muted.Render("This harness is PTY-only. Use : to attach."))
	}
	if detail.TranscriptCode == "transcript_locked" {
		lines = append(lines, "", theme.error.Render("TRANSCRIPT LOCKED"),
			theme.muted.Render("The installation key is missing or does not match. Restore the correct operator key; no ciphertext details are shown."))
	} else if detail.TranscriptCode == "transcript_corrupt" {
		lines = append(lines, "", theme.error.Render("TRANSCRIPT CORRUPT"),
			theme.muted.Render("Stop using this conversation and restore from a trusted backup."))
	} else if detail.TranscriptError != "" {
		lines = append(lines, theme.error.Render("Transcript unavailable: "+safeInline(detail.TranscriptError)))
	}
	if detail.Gap != nil {
		requested, _ := detail.Gap.RequestedAfter.Get()
		available, _ := detail.Gap.AvailableFrom.Get()
		lines = append(lines, theme.muted.Render(fmt.Sprintf(
			"Replay gap: requested after %d; available from %d. Earlier blocks cannot be reconstructed.",
			requested, available,
		)))
	}
	if detail.More || detail.Truncated {
		lines = append(lines, theme.muted.Render("Transcript truncated to the newest retained messages."))
	}
	if block, _ := pendingRequest(detail.Blocks); block != nil {
		lines = append(lines, theme.banner.Render("A permission or input request is waiting. Use /answer."))
	}
	header := len(lines)
	lines = append(lines, "")
	rendered := renderThreadBlocks(detail.Blocks)
	if len(rendered) == 0 && detail.TranscriptError == "" && m.pendingSend == "" {
		lines = append(lines, theme.muted.Render("No messages yet. Type below and press Enter."))
	} else {
		for _, line := range rendered {
			lines = append(lines, styleTranscriptLine(theme, line))
		}
	}
	if m.pendingSend != "" {
		if len(lines) > header && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, theme.user.Render(safeBlock(m.pendingSend)))
	}
	if transcriptHeight < 1 {
		transcriptHeight = 8
	}
	bodyHeight := max(1, transcriptHeight-header)
	body := lines[header:]
	head := lines[:header]
	if m.scroll > 0 && len(body) > bodyHeight {
		end := max(1, len(body)-m.scroll)
		start := max(0, end-bodyHeight)
		clipped := body[start:end]
		if start > 0 {
			clipped = append([]string{theme.muted.Render("…")}, clipped...)
		}
		body = clipped
	} else if len(body) > bodyHeight {
		body = body[len(body)-bodyHeight:]
	}
	return strings.Join(append(head, body...), "\n")
}

func styleTranscriptLine(theme theme, line string) string {
	switch {
	case strings.HasPrefix(line, transcriptUser):
		return theme.user.Render(strings.TrimPrefix(line, transcriptUser))
	case strings.HasPrefix(line, transcriptTool):
		return theme.tool.Render(strings.TrimPrefix(line, transcriptTool))
	case strings.HasPrefix(line, transcriptError):
		return theme.error.Render(strings.TrimPrefix(line, transcriptError))
	case strings.HasPrefix(line, transcriptAssistant):
		return theme.assistant.Render(strings.TrimPrefix(line, transcriptAssistant))
	default:
		return theme.assistant.Render(line)
	}
}
