package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func (m Model) View() string {
	theme := currentTheme()
	if m.overlay.kind != overlayNone {
		return m.overlayView()
	}
	commandRows := 3
	if _, ok := m.slashQuery(); ok {
		commandRows += min(6, max(1, len(m.filteredSlash())))
	}
	contract := contractFor(m.width, m.height, commandRows)
	title := m.titleViewWidth(contract.contentWidth)
	var body string
	if !m.connected && m.loading && len(m.snapshot.Capsules) == 0 {
		body = splash(
			theme, transitMark(theme), "Loading Meridian",
			"Connecting to the local daemon and reading Capsules.", "",
			contract,
		)
	} else if !m.connected && m.lastErr != nil {
		body = emptyState(
			theme, "", "Disconnected",
			safeInline(m.lastErr.Error())+"\n\nReconnecting automatically. Press r to retry now.",
			keyHint(theme, "r", "retry now"), contract.contentWidth,
		)
	} else if len(m.snapshot.Projects) == 0 {
		body = splash(
			theme, transitMark(theme), "No projects yet",
			"Create a Project to connect a repository and choose its harness.",
			keyHint(theme, "/project", "create Project"), contract,
		)
	} else if m.place == placeHome {
		body = m.homeBody(contract)
	} else if len(m.visibleCapsules()) == 0 && !m.pendingNew {
		message := "Use /new to name a Capsule and choose its harness."
		if m.projectName() != "" {
			message = "No Capsules in " + m.projectName() + " yet."
		}
		copy := message
		if harness := m.projectHarnessStatus(); harness != "" {
			copy += "\n\n" + harness
		}
		body = emptyState(
			theme, transitMark(theme), "Capsules", copy,
			keyHint(theme, "/new", "create Capsule"), contract.contentWidth,
		)
	} else if m.mode == modeHistory && m.place == placeThread && m.selectedThreadID != "" {
		body = m.historyBody(contract)
	} else {
		body = m.launcherBody(contract)
	}
	body = lipgloss.NewStyle().Width(contract.contentWidth).Height(contract.bodyHeight).
		Render(clipToHeight(body, contract.bodyHeight))
	command := m.commandSurface(contract.contentWidth)
	if command == "" {
		command = "\n\n"
	} else {
		command = clipToHeight(command, commandRows)
	}
	page := title + "\n\n" + body + "\n" + command + "\n" + m.footerViewWidth(contract.contentWidth)
	return pageGutter(page, contract.gutter)
}

func (m Model) bodyHeight() int {
	if m.height <= 0 {
		return 16
	}
	reserved := 8
	if _, ok := m.slashQuery(); ok {
		items := len(m.filteredSlash())
		if items == 0 {
			items = 1
		}
		if items > 6 {
			items = 6
		}
		reserved += items
	}
	return max(6, m.height-reserved)
}

func (m Model) wide() bool { return m.width >= 100 }

func (m Model) contentWidth() int {
	if m.width > 6 {
		return m.width - 2
	}
	return 72
}

func (m Model) titleView() string {
	return m.titleViewWidth(m.contentWidth())
}

func (m Model) titleViewWidth(width int) string {
	theme := currentTheme()
	left := transitMarkCompact(theme) + " " + theme.wordmark.Render("MERIDIAN")
	right := ""
	if name := m.projectName(); name != "" {
		right = theme.muted.Render(safeInline(name))
	}
	return pageLine(left, right, width)
}

func (m Model) footerView() string {
	return m.footerViewWidth(m.contentWidth())
}

func (m Model) footerViewWidth(width int) string {
	theme := currentTheme()
	status := "connected"
	if m.reconnect {
		status = "reconnecting"
	} else if !m.connected {
		status = "disconnected"
	}
	if m.status != "" {
		status += "  ·  " + friendlyStatus(m.status)
	}
	if m.lastErr != nil && m.connected {
		status += "  ·  " + safeInline(m.lastErr.Error())
	}
	open := "open"
	if m.place == placeHome {
		open = "open Project"
	}
	hints := keyHint(theme, "Enter", open) + "   " +
		keyHint(theme, "/", "commands") + "   " +
		keyHint(theme, ":", "actions") + "   " +
		keyHint(theme, "?", "help")
	return pageLine(hints, theme.footer.Render(status), width)
}

func (m Model) homeBody(contract viewContract) string {
	theme := currentTheme()
	mark := transitMark(theme)
	title := theme.title.Render("Choose a Project")
	list := m.projectListView()
	hints := keyHint(theme, "Enter", "open") + "   " + keyHint(theme, "/project", "create")
	cardWidth := min(contract.contentWidth, max(
		24, blockWidth(mark), blockWidth(title), blockWidth(list), blockWidth(hints),
	))
	card := strings.Join([]string{
		centerBlock(mark, cardWidth),
		"",
		centerBlock(title, cardWidth),
		"",
		centerBlock(list, cardWidth),
		"",
		centerBlock(hints, cardWidth),
	}, "\n")
	return lipgloss.Place(
		contract.contentWidth, contract.bodyHeight,
		lipgloss.Center, lipgloss.Center, card,
	)
}

func (m Model) projectListView() string {
	theme := currentTheme()
	if len(m.snapshot.Projects) == 0 {
		return theme.muted.Render("None yet")
	}
	type row struct {
		marker string
		name   string
		meta   string
	}
	rows := make([]row, 0, len(m.snapshot.Projects))
	nameWidth := 0
	for index, project := range m.snapshot.Projects {
		item := row{marker: "  ", name: safeInline(project.Name)}
		if index == m.projectPicker {
			item.marker = theme.primary.Render("● ")
			item.name = theme.selected.Render(item.name)
		}
		count := m.projectCapsuleCount(project.ID)
		label := "Capsule"
		if count != 1 {
			label = "Capsules"
		}
		item.meta = theme.muted.Render(fmt.Sprintf("%d %s", count, label))
		if width := lipgloss.Width(item.name); width > nameWidth {
			nameWidth = width
		}
		rows = append(rows, item)
	}
	nameWidth = min(28, nameWidth)
	lines := make([]string, 0, len(rows))
	for _, item := range rows {
		name := item.name
		if lipgloss.Width(name) > nameWidth {
			name = truncateWidth(name, nameWidth)
		}
		lines = append(lines, item.marker+padCell(name, nameWidth)+"  "+item.meta)
	}
	return strings.Join(lines, "\n")
}

func (m Model) projectCapsuleCount(id string) int {
	count := 0
	for _, detail := range m.snapshot.Capsules {
		if detail.Project.ID != id {
			continue
		}
		if !m.showDeleted && detail.Capsule.State == client.CapsuleStateDeleted {
			continue
		}
		count++
	}
	return count
}

func (m Model) capsuleListView() string {
	return m.capsuleListViewWidth(32)
}

func (m Model) capsuleListViewWidth(width int) string {
	theme := currentTheme()
	visible := m.visibleCapsules()
	if len(visible) == 0 {
		return theme.muted.Render("None yet")
	}
	lines := make([]string, 0, len(visible))
	for _, index := range visible {
		detail := m.snapshot.Capsules[index]
		row := m.capsuleSessionRow(detail, width, index == m.selected)
		if m.focus == focusCapsules && index == m.selected {
			row = theme.selected.Render(row)
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func (m Model) capsuleSessionRow(detail CapsuleDetail, width int, selected bool) string {
	theme := currentTheme()
	marker := "  "
	if selected {
		marker = "● "
	}
	name := safeInline(detail.Capsule.Name)
	preview := ""
	if thread := m.primaryThread(detail); thread != nil {
		preview = conversationPreview(*thread)
	}
	if count := m.unreadForCapsule(detail); count > 0 {
		if preview != "" {
			preview = fmt.Sprintf("%d new · %s", count, preview)
		} else {
			preview = fmt.Sprintf("%d new", count)
		}
	}
	dot := theme.chip(detail.Capsule.State).Render("·")
	row := marker + name
	if preview != "" {
		row += "  " + theme.muted.Render(preview)
	}
	row += "  " + dot
	return truncateWidth(row, width)
}

func (m Model) mainView(transcriptHeight int) string {
	if m.pendingNew {
		return m.newCapsuleView()
	}
	if m.place == placeThread {
		if detail := m.selectedDetail(); detail != nil {
			if thread := m.selectedThread(); thread != nil {
				return m.threadView(*detail, *thread, transcriptHeight)
			}
		}
		if m.selectedThreadID != "" {
			theme := currentTheme()
			return theme.title.Render("New Capsule") + "\n" +
				theme.muted.Render("Starting this Capsule…")
		}
	}
	if detail := m.selectedDetail(); detail != nil {
		return m.capsuleHomeView(*detail)
	}
	theme := currentTheme()
	return theme.muted.Render("Select a Capsule and press Enter to open its harness.")
}

func (m Model) newCapsuleView() string {
	theme := currentTheme()
	lines := []string{theme.title.Render("New Capsule"), ""}
	lines = append(lines, theme.muted.Render("Name"))
	name := strings.TrimSpace(m.newName)
	switch {
	case name == "" && m.newNameFocus:
		lines = append(lines, theme.selected.Render("→ ")+caretSlot(m.cursorOn)+theme.muted.Render("Capsule name"))
	case name == "":
		lines = append(lines, theme.muted.Render("  Capsule name"))
	case m.newNameFocus:
		lines = append(lines, theme.selected.Render("→ "+safeInline(m.newName)+caret(m.cursorOn)))
	default:
		lines = append(lines, "  "+safeInline(m.newName))
	}
	lines = append(lines, "")
	if len(m.newHarnesses) == 0 && m.newHarness == "" {
		lines = append(lines, theme.muted.Render("No harness pack is applied to this Project."))
	} else {
		lines = append(lines, theme.muted.Render("Harness"))
		choices := m.newHarnesses
		if len(choices) == 0 && m.newHarness != "" {
			choices = []string{m.newHarness}
		}
		for _, name := range choices {
			row := "  " + name
			if name == m.newHarness {
				row = theme.selected.Render("→ " + name)
			} else {
				row = theme.muted.Render(row)
			}
			lines = append(lines, row)
		}
		if len(choices) > 1 {
			lines = append(lines, "", theme.muted.Render("← → change harness"))
		}
	}
	lines = append(lines, "", theme.muted.Render("Tab returns to the name. Enter creates and opens this Capsule."))
	return strings.Join(lines, "\n")
}

func (m Model) capsuleHomeView(detail CapsuleDetail) string {
	theme := currentTheme()
	lines := []string{theme.title.Render(safeInline(detail.Capsule.Name))}
	if failure, ok := detail.Capsule.Failure.Get(); ok && failure != "" {
		lines = append(lines, theme.error.Render("Failure: "+safeInline(failure)))
	}
	if harness, ok := detail.Capsule.Harness.Get(); ok && harness != "" {
		lines = append(lines, "", theme.muted.Render("Press Enter to open "+safeInline(harness)+"."))
	} else {
		lines = append(lines, "", theme.muted.Render("This legacy Capsule has no native launcher harness."))
	}
	return strings.Join(lines, "\n")
}

func (m Model) composerChrome(width int) string {
	menu := m.slashMenuView(width)
	box := m.composerView(width)
	if menu == "" {
		return box
	}
	return menu + "\n" + box
}

func (m Model) commandSurface(width int) string {
	if _, slash := m.slashQuery(); slash {
		return m.composerChrome(width)
	}
	if m.mode == modeHistory && m.focus == focusMain &&
		(m.place == placeThread || m.place == placeCapsule) {
		return m.composerChrome(width)
	}
	return ""
}

func (m Model) slashMenuView(width int) string {
	if _, ok := m.slashQuery(); !ok {
		return ""
	}
	theme := currentTheme()
	items := m.filteredSlash()
	if len(items) == 0 {
		return theme.muted.Render("No matching /command")
	}
	selected := m.paletteIndex
	if selected >= len(items) {
		selected = 0
	}
	if len(items) > 6 {
		items = items[:6]
	}
	lines := make([]string, 0, len(items))
	for index, item := range items {
		row := "/" + item.slash + "  " + item.label
		if index == selected {
			row = theme.selected.Render(row)
		} else {
			row = theme.muted.Render(row)
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func (m Model) projectHarnessStatus() string {
	theme := currentTheme()
	project := m.currentProject()
	if project == nil {
		return ""
	}
	lines := []string{theme.muted.Render("Harnesses")}
	for _, item := range project.HarnessImages {
		if item.Name == "" {
			continue
		}
		lines = append(lines, "  "+item.Name)
	}
	if len(lines) == 1 {
		lines = append(lines, theme.muted.Render("  none"))
	}
	lines = append(lines, "", theme.muted.Render("/harness applies packs this Project can start."))
	return strings.Join(lines, "\n")
}

func (m Model) composerView(width int) string {
	theme := currentTheme()
	text := m.composer
	switch {
	case text == "" && m.place == placeThread:
		text = theme.muted.Render("Message this structured Thread")
	case text == "":
		text = "/"
	default:
		text = safeBlock(text)
	}
	hint := ""
	if _, slash := m.slashQuery(); slash {
		hint = "Enter run  ↑↓ select  Esc clear"
	} else if !m.stayOnList {
		hint = composerHint
	}
	return commandBar(
		theme, text, m.composerModeLabel(), m.composerContextLabel(), hint,
		width, m.composing() && m.cursorOn,
	)
}

func (m Model) composerModeLabel() string {
	if _, slash := m.slashQuery(); slash {
		return "COMMAND"
	}
	if thread := m.selectedThread(); thread != nil && threadActiveRun(thread.Thread) {
		return "HISTORY · LIVE"
	}
	if m.place == placeThread {
		return "HISTORY"
	}
	return ""
}

func (m Model) composerContextLabel() string {
	parts := make([]string, 0, 2)
	if name := m.projectName(); name != "" {
		parts = append(parts, name)
	}
	if m.pendingNew {
		parts = append(parts, "new Capsule")
	} else if detail := m.selectedDetail(); detail != nil {
		parts = append(parts, safeInline(detail.Capsule.Name))
	}
	return strings.Join(parts, " / ")
}

func labeledFrame(width int, border lipgloss.Style, topLeft, topRight, body, bottomLeft, bottomRight string) string {
	if width < 24 {
		width = 24
	}
	inner := width - 2
	var lines []string
	lines = append(lines, border.Render("╭"+frameBar(inner, topLeft, topRight)+"╮"))
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, border.Render("│")+padFrameCell(line, inner)+border.Render("│"))
	}
	lines = append(lines, border.Render("╰"+frameBar(inner, bottomLeft, bottomRight)+"╯"))
	return strings.Join(lines, "\n")
}

func frameBar(inner int, left, right string) string {
	if left != "" {
		left = " " + left + " "
	}
	if right != "" {
		right = " " + right + " "
	}
	fill := inner - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 1 {
		right = ""
		fill = inner - lipgloss.Width(left)
	}
	if fill < 1 {
		left = ""
		fill = inner
	}
	return left + strings.Repeat("─", fill) + right
}

func padFrameCell(value string, width int) string {
	visible := lipgloss.Width(value)
	if visible > width {
		return value
	}
	return value + strings.Repeat(" ", width-visible)
}

func clipToHeight(content string, height int) string {
	if height < 1 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= height {
		return content
	}
	return strings.Join(lines[len(lines)-height:], "\n")
}

func truncateWidth(value string, width int) string {
	if width < 4 || lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func wrapText(value string, width int) string {
	if width < 1 {
		return value
	}
	var lines []string
	for _, paragraph := range strings.Split(value, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				line = word
				continue
			}
			if lipgloss.Width(line+" "+word) <= width {
				line += " " + word
				continue
			}
			lines = append(lines, line)
			line = word
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) overlayView() string {
	theme := currentTheme()
	if m.overlay.kind == overlayPalette {
		return m.paletteView()
	}
	width := max(40, min(72, m.width-4))
	if m.overlay.kind == overlayHelp || m.overlay.kind == overlayContent {
		width = max(40, min(90, m.width-4))
	}
	if width < 40 {
		width = 40
	}
	contentWidth := width - 6
	var lines []string
	if m.overlay.note != "" {
		lines = append(lines, theme.muted.Render(wrapText(safeBlock(m.overlay.note), contentWidth)), "")
	}
	switch m.overlay.kind {
	case overlayForm:
		if m.overlay.action.Action == ActionProjectCreate {
			lines = append(lines, theme.section.Render("REPOSITORY"))
		}
		for index, item := range m.overlay.fields {
			if m.overlay.action.Action == ActionProjectCreate && item.label == "Name" {
				lines = append(lines, "", theme.section.Render("PROJECT"))
			}
			prefix := "  "
			if index == m.overlay.focus {
				prefix = "> "
			}
			suffix := ""
			if len(item.options) > 0 {
				suffix = "  ←/→"
			} else if item.readOnly {
				suffix = "  detected"
			}
			value := safeBlock(item.value)
			placeholder := value == "" && item.placeholder != ""
			if placeholder {
				value = item.placeholder
			}
			if item.secret && value != "" {
				value = strings.Repeat("•", runeCount(value))
			}
			if index == m.overlay.focus && !item.readOnly && len(item.options) == 0 {
				if placeholder {
					value = caret(m.cursorOn) + value
				} else {
					value += caret(m.cursorOn)
				}
			}
			row := truncateWidth(prefix+item.label+": "+value+suffix, contentWidth)
			if index == m.overlay.focus {
				row = theme.selected.Render(row)
			} else if item.readOnly || placeholder {
				row = theme.muted.Render(row)
			}
			lines = append(lines, row)
		}
		if m.overlay.multiline {
			lines = append(lines, "", theme.muted.Render("tab next  enter submit  ctrl+j newline  esc cancel"))
		} else {
			lines = append(lines, "", theme.muted.Render("tab next  enter submit  esc cancel"))
		}
	case overlayHarness:
		lines = append(lines, "")
		for index, choice := range m.overlay.choices {
			mark := "[ ]"
			if choice.Applied {
				mark = "[x]"
			}
			row := "  " + mark + "  " + choice.Name
			if index == m.overlay.focus {
				row = theme.selected.Render("→ " + mark + "  " + choice.Name)
			} else {
				row = theme.muted.Render(row)
			}
			lines = append(lines, row, "      "+theme.muted.Render(choice.ImageReference))
		}
		lines = append(lines, "", theme.muted.Render("↑↓ move  space apply  enter save  esc cancel"))
	case overlayConfirm:
		lines = append(lines, "", theme.muted.Render("y/enter confirm  n/esc cancel"))
	case overlayHelp, overlayContent:
		lines = append(lines, "", m.overlay.content, "", theme.muted.Render("enter/esc close"))
	}
	return m.placeModal(modal(theme, strings.ToUpper(m.overlay.title), strings.Join(lines, "\n"), width))
}

func (m Model) paletteView() string {
	theme := currentTheme()
	items := m.filteredPalette()
	var lines []string
	if m.paletteQuery != "" {
		lines = append(lines, theme.muted.Render("Filter: "+m.paletteQuery))
	} else {
		lines = append(lines, theme.muted.Render("Type to filter  enter run  esc close"))
	}
	if len(items) == 0 {
		lines = append(lines, theme.muted.Render("No matching actions"))
	}
	group := ""
	for index, item := range items {
		if next := paletteGroup(item.id); next != group {
			group = next
			lines = append(lines, "", theme.section.Render(group))
		}
		row := "  " + item.label
		if index == m.paletteIndex {
			row = "> " + item.label
			row = theme.selected.Render(row)
		}
		lines = append(lines, row, "    "+theme.muted.Render(item.hint))
	}
	width := max(40, min(80, m.width-4))
	return m.placeModal(modal(theme, "ACTIONS", strings.Join(lines, "\n"), width))
}

func paletteGroup(id string) string {
	switch {
	case strings.HasPrefix(id, "history:"):
		return "HISTORY"
	case id == "answer" || id == "start" || id == "resume-thread" ||
		id == "cancel" || id == "archive" || id == "shred":
		return "STRUCTURED THREAD"
	case id == "switch-project" || id == "new-project" || id == "apply-harness":
		return "PROJECT"
	case id == "show-deleted" || id == "hide-deleted":
		return "VIEW"
	default:
		return "CAPSULE"
	}
}

func (m Model) placeModal(content string) string {
	width := m.width
	height := m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
}

const caretGlyph = "▏"

func caret(on bool) string {
	if on {
		return caretGlyph
	}
	return ""
}

func caretSlot(on bool) string {
	if on {
		return caretGlyph
	}
	return " "
}

func runeCount(value string) int {
	return len([]rune(value))
}

func (m Model) unreadForCapsule(detail CapsuleDetail) int64 {
	var total int64
	for _, thread := range detail.Threads {
		total += m.unread[thread.Thread.ID]
	}
	return total
}

func (m Model) actions(detail CapsuleDetail) string {
	actions := []string{"c create Capsule"}
	if m.capsuleHasStructured(detail) {
		actions = append(actions, "t create Thread")
	} else {
		actions = append(actions, "Thread unsupported")
	}
	pty := false
	for _, profile := range detail.Profiles {
		pty = pty || profile.Pty
	}
	if pty && detail.ActiveRun() == nil {
		actions = append(actions, "PTY profile available")
	}
	maintenance := detail.Capsule.Maintenance.IsSet()
	if detail.Capsule.State == "Ready" {
		actions = append(actions, "g diff")
	}
	if detail.Capsule.State == "Ready" && !maintenance {
		actions = append(actions, "p pause")
	}
	if detail.Capsule.State == "Paused" && !maintenance {
		actions = append(actions, "u resume")
	}
	if detail.ActiveRun() != nil {
		actions = append(actions, "a attach")
	}
	if detail.Capsule.State == "Ready" && !maintenance && detail.ActiveRun() == nil {
		actions = append(actions, "m moment", "S seal")
	}
	if mutable(string(detail.Capsule.State)) && !maintenance {
		actions = append(actions, "x delete")
	}
	if detail.LatestMoment() != nil {
		actions = append(actions, "s shard", "w rewind")
	}
	return strings.Join(actions, "  ")
}

func (m Model) listView() string {
	return m.capsuleListView()
}
