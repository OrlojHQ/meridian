package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/OrlojHQ/meridian/pkg/client"
)

type paletteItem struct {
	id     string
	label  string
	hint   string
	hidden string
	slash  string
}

func (m Model) paletteItems() []paletteItem {
	var items []paletteItem
	detail := m.selectedDetail()
	thread := m.selectedThread()
	if detail != nil {
		maintenance := detail.Capsule.Maintenance.IsSet()
		if detail.Capsule.State == client.CapsuleStateReady && !maintenance {
			items = append(items, paletteItem{id: "pause", label: "Pause Capsule", hint: "Stop the Capsule until you come back", hidden: "p"})
		}
		if detail.Capsule.State == client.CapsuleStatePaused && !maintenance {
			items = append(items, paletteItem{id: "resume", label: "Resume Capsule", hint: "Wake the Capsule", hidden: "u"})
		}
		if detail.Capsule.State == client.CapsuleStateReady {
			items = append(items, paletteItem{id: "diff", label: "Show Git diff", hint: "Review the working tree", hidden: "g"})
		}
		if detail.Capsule.State == client.CapsuleStateReady && !maintenance && detail.ActiveRun() == nil {
			items = append(items, paletteItem{id: "moment", label: "Capture Moment", hint: "Snapshot this Capsule", hidden: "m"})
			items = append(items, paletteItem{id: "seal", label: "Seal Capsule", hint: "Make this Capsule read-only", hidden: "S"})
		}
		if detail.LatestMoment() != nil {
			items = append(items, paletteItem{id: "shard", label: "Create Shard", hint: "Branch from the latest Moment", hidden: "s"})
			items = append(items, paletteItem{id: "rewind", label: "Rewind Capsule", hint: "New timeline from a Moment; later history stays", hidden: "w"})
		}
		if detail.RunningRun() != nil {
			items = append(items, paletteItem{id: "attach", label: "Attach PTY", hint: "Hand the terminal to a native Run", hidden: "a"})
		}
		if mutable(string(detail.Capsule.State)) && !maintenance {
			items = append(items, paletteItem{id: "delete", label: "Delete Capsule", hint: "Remove this Capsule", hidden: "x"})
		}
	}
	if thread != nil {
		if block, _ := pendingRequest(thread.Blocks); block != nil {
			items = append(items, paletteItem{
				id: "answer", label: "Answer permission or input",
				hint: "Respond to the waiting request", hidden: "P",
			})
		}
		if thread.Thread.State == client.ThreadStateActive && !threadActiveRun(thread.Thread) {
			items = append(items, paletteItem{id: "start", label: "Start conversation session", hint: "Run the structured harness again", hidden: "e"})
		}
		if thread.Thread.State == client.ThreadStatePaused {
			items = append(items, paletteItem{id: "resume-thread", label: "Resume conversation", hint: "Continue from the last encrypted state", hidden: "e"})
		}
		if threadActiveRun(thread.Thread) {
			items = append(items, paletteItem{id: "cancel", label: "Cancel session", hint: "Stop the active adapter", hidden: "z"})
		}
		if thread.Thread.State != client.ThreadStateArchived && thread.Thread.State != client.ThreadStateDeleted {
			items = append(items, paletteItem{id: "archive", label: "Archive conversation", hint: "Keep history off the busy list", hidden: "A"})
		}
		if thread.Thread.State != client.ThreadStateDeleted {
			items = append(items, paletteItem{id: "shred", label: "Crypto-shred conversation", hint: "Irreversible", hidden: "D"})
		}
	}
	if detail != nil {
		for _, thread := range m.visibleThreads(*detail) {
			if thread.Thread.ID == m.selectedThreadID {
				continue
			}
			hint := threadStateLabel(thread)
			if preview := conversationPreview(thread); preview != "" {
				hint += " · " + preview
			}
			items = append(items, paletteItem{
				id:    "history:" + thread.Thread.ID,
				label: "Open history: " + conversationTitle(thread),
				hint:  hint,
			})
		}
	}
	if len(m.snapshot.Projects) > 1 {
		items = append(items, paletteItem{id: "switch-project", label: "Switch project", hint: "Change the Capsule list"})
	}
	items = append(items, paletteItem{
		id: "new-project", slash: "project", label: "New Project", hint: "Name a Project, repo, and harness",
	})
	if m.currentProject() != nil && len(m.snapshot.HarnessImages) > 0 {
		items = append(items, paletteItem{
			id: "apply-harness", slash: "harness", label: "Apply harnesses",
			hint: "Choose which official packs this Project can start",
		})
	}
	items = append(items, paletteItem{
		id: "capsule", label: "New empty Capsule", hint: "Create a Capsule without starting a chat",
	})
	if m.showDeleted {
		items = append(items, paletteItem{id: "hide-deleted", label: "Hide deleted Capsules", hint: "Return to the normal list"})
	} else {
		items = append(items, paletteItem{id: "show-deleted", label: "Show deleted Capsules", hint: "Deleted Capsules stay hidden by default"})
	}
	return items
}

func (m Model) slashItems() []paletteItem {
	items := []paletteItem{
		{id: "new", slash: "new", label: "New Capsule", hint: "Name a Capsule and open its harness"},
		{id: "new-project", slash: "project", label: "New Project", hint: "Name a Project, repo, and harness"},
		{id: "apply-harness", slash: "harness", label: "Apply harnesses", hint: "Choose packs this Project can start"},
		{id: "help", slash: "help", label: "Help", hint: "Keybindings and slash commands"},
		{id: "refresh", slash: "refresh", label: "Refresh", hint: "Reload Capsules now"},
	}
	if len(m.snapshot.Projects) > 1 {
		items = append(items, paletteItem{
			id: "switch-project", slash: "switch", label: "Switch Project",
			hint: "Choose the active Project",
		})
	}
	return items
}

func (m Model) slashAliases() []paletteItem {
	return []paletteItem{
		{id: "spawn", slash: "spawn", label: "New Capsule", hint: "Same as /new"},
		{id: "capsule", slash: "capsule", label: "New empty Capsule", hint: "Capsule with no conversation yet"},
	}
}

func (m Model) allSlashItems() []paletteItem {
	items := m.slashItems()
	items = append(items, m.slashAliases()...)
	return items
}

func (m Model) filteredSlash() []paletteItem {
	query, ok := m.slashQuery()
	if !ok {
		return nil
	}
	source := m.slashItems()
	if query != "" {
		source = m.allSlashItems()
	}
	var prefixed, other []paletteItem
	for _, item := range source {
		name := strings.ToLower(item.slash)
		switch {
		case query == "" || strings.HasPrefix(name, query):
			prefixed = append(prefixed, item)
		case strings.Contains(strings.ToLower(item.label), query) ||
			strings.Contains(strings.ToLower(item.hint), query):
			other = append(other, item)
		}
	}
	if query == "" || len(prefixed) > 0 {
		return prefixed
	}
	return other
}

func (m Model) applySlash() (tea.Model, tea.Cmd) {
	items := m.filteredSlash()
	if len(items) == 0 {
		m.status = "No matching /command"
		return m, nil
	}
	if m.paletteIndex >= len(items) {
		m.paletteIndex = 0
	}
	item := items[m.paletteIndex]
	m.composer = ""
	m.composerFocus = false
	m.mode = modeLauncher
	m.paletteIndex = 0
	return m.runPaletteID(item.id)
}

func (m Model) filteredPalette() []paletteItem {
	query := strings.ToLower(strings.TrimSpace(m.paletteQuery))
	items := m.paletteItems()
	if query == "" {
		return items
	}
	var filtered []paletteItem
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.label), query) ||
			strings.Contains(strings.ToLower(item.hint), query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (m *Model) openPalette() {
	m.overlay = overlay{kind: overlayPalette, title: "More actions"}
	m.paletteQuery = ""
	m.paletteIndex = 0
}

func (m Model) runPaletteID(id string) (tea.Model, tea.Cmd) {
	if threadID, ok := strings.CutPrefix(id, "history:"); ok {
		m.openHistoryThread(threadID)
		return m, nil
	}
	switch id {
	case "pause":
		if detail := m.selectedDetail(); detail != nil && detail.Capsule.State == client.CapsuleStateReady {
			return m.execute(m.lifecycleRequest(ActionPause))
		}
	case "resume":
		if detail := m.selectedDetail(); detail != nil && detail.Capsule.State == client.CapsuleStatePaused {
			return m.execute(m.lifecycleRequest(ActionResume))
		}
	case "diff":
		if detail := m.selectedDetail(); detail != nil && detail.Capsule.State == client.CapsuleStateReady {
			return m.execute(ActionRequest{Action: ActionDiff, CapsuleID: detail.Capsule.ID})
		}
	case "moment":
		m.openNamed(ActionMoment, "Capture Moment", "Moment name", "")
	case "seal":
		m.openConfirm(ActionSeal, "Seal Capsule", "Seal is permanent and captures a final Moment.")
	case "shard":
		m.openDescendant(ActionShard, "Create Shard", "")
	case "rewind":
		m.openDescendant(ActionRewind, "Rewind Capsule", "Rewind creates a new Timeline and does not destroy history.")
	case "attach":
		return m.attachSelectedRun()
	case "delete":
		m.openConfirm(ActionDelete, "Delete Capsule", "Delete this Capsule?")
	case "answer":
		m.openThreadResponse()
	case "start", "resume-thread":
		return m.startOrResumeThread()
	case "cancel":
		if thread := m.selectedThread(); thread != nil && threadActiveRun(thread.Thread) {
			m.openThreadConfirm(ActionThreadCancel, "Cancel session", "Cancel the active structured adapter session?")
		}
	case "archive":
		m.openThreadConfirm(ActionThreadArchive, "Archive conversation", "Archive this conversation?")
	case "shred":
		m.openThreadDelete()
	case "switch-project":
		m.openSwitchProject()
	case "new-project":
		m.openCreateProject()
	case "apply-harness":
		m.openApplyHarness()
	case "show-deleted":
		m.showDeleted = true
		m.restoreSelection()
	case "hide-deleted":
		m.showDeleted = false
		m.restoreSelection()
	case "new", "spawn":
		return m, m.beginNewCapsule()
	case "capsule":
		m.openCreate()
	case "help":
		return m.openHelp()
	case "refresh":
		if !m.loading {
			m.loading = true
			return m, m.loadCmd()
		}
	}
	return m, nil
}

func (m Model) applyPalette() (tea.Model, tea.Cmd) {
	items := m.filteredPalette()
	if len(items) == 0 {
		m.overlay = overlay{}
		return m, nil
	}
	if m.paletteIndex >= len(items) {
		m.paletteIndex = 0
	}
	item := items[m.paletteIndex]
	m.overlay = overlay{}
	return m.runPaletteID(item.id)
}

func (m Model) runHiddenAlias(key string) (tea.Model, tea.Cmd, bool) {
	for _, item := range m.paletteItems() {
		if item.hidden == key {
			next, cmd := m.runPaletteID(item.id)
			return next, cmd, true
		}
	}
	return m, nil, false
}
