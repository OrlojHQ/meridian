package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/OrlojHQ/meridian/pkg/client"
)

const (
	pollCadence = 3 * time.Second
	callTimeout = 5 * time.Second
)

type loadMsg struct {
	snapshot Snapshot
	err      error
}

type tickMsg time.Time

type actionMsg struct {
	result ActionResult
	err    error
}

type attachFinishedMsg struct{ err error }

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayForm
	overlayConfirm
	overlayContent
	overlayHelp
)

type field struct {
	label    string
	value    string
	optional bool
	options  []string
	secret   bool
}

type overlay struct {
	kind      overlayKind
	title     string
	note      string
	action    ActionRequest
	fields    []field
	focus     int
	content   string
	multiline bool
}

// Options configures a dashboard model.
type Options struct {
	Context    context.Context
	API        API
	Server     string
	Executable string
	Now        func() time.Time
	Attach     func(runID string, after uint64) tea.Cmd
}

// Model is the Bubble Tea dashboard state.
type Model struct {
	ctx        context.Context
	api        API
	server     string
	executable string
	now        func() time.Time
	attach     func(string, uint64) tea.Cmd

	width            int
	height           int
	selected         int
	selectedID       string
	selectedThreadID string
	narrowPane       int
	scroll           int
	unread           map[string]int64
	seenCursor       map[string]int64
	snapshot         Snapshot
	loading          bool
	connected        bool
	reconnect        bool
	lastErr          error
	status           string
	overlay          overlay
}

func NewModel(options Options) Model {
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Executable == "" {
		options.Executable, _ = os.Executable()
	}
	model := Model{
		ctx: options.Context, api: options.API, server: options.Server,
		executable: options.Executable, now: options.Now, loading: true,
		narrowPane: 1, unread: make(map[string]int64), seenCursor: make(map[string]int64),
	}
	if options.Attach != nil {
		model.attach = options.Attach
	} else {
		model.attach = model.attachProcess
	}
	return model
}

func (m Model) Init() tea.Cmd { return m.loadCmd() }

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = value.Width, value.Height
		return m, nil
	case loadMsg:
		m.loading = false
		if value.err != nil {
			m.lastErr = value.err
			m.reconnect = m.connected
			return m, m.tickCmd()
		}
		m.connected = true
		m.reconnect = false
		m.lastErr = nil
		m.trackUnread(value.snapshot)
		m.snapshot = value.snapshot
		m.restoreSelection()
		return m, m.tickCmd()
	case tickMsg:
		if m.loading {
			return m, nil
		}
		m.loading = true
		return m, m.loadCmd()
	case actionMsg:
		m.loading = false
		if value.err != nil {
			m.status = "Action failed: " + safeInline(value.err.Error())
			if strings.Contains(strings.ToLower(value.err.Error()), "conflict") {
				m.status += " · refreshing current resource version"
				m.loading = true
				return m, m.loadCmd()
			}
			return m, nil
		}
		m.status = safeInline(value.result.Message)
		if value.result.ThreadID != "" {
			m.selectedThreadID = value.result.ThreadID
		}
		if value.result.Content != "" {
			m.overlay = overlay{
				kind: overlayContent, title: safeInline(value.result.Message), content: safeBlock(value.result.Content),
			}
			return m, nil
		}
		m.loading = true
		return m, m.loadCmd()
	case attachFinishedMsg:
		if value.err != nil {
			m.status = "Attach failed: " + safeInline(value.err.Error())
		} else {
			m.status = "Attach ended; dashboard resumed"
		}
		m.loading = true
		return m, m.loadCmd()
	case tea.KeyMsg:
		if m.overlay.kind != overlayNone {
			return m.updateOverlay(value)
		}
		return m.updateKey(value)
	}
	return m, nil
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.move(1)
	case "k", "up":
		m.move(-1)
	case "tab":
		if m.width < 100 {
			m.narrowPane = 1 - m.narrowPane
		}
	case "pgup":
		m.scroll += max(3, m.height/2)
	case "pgdown":
		m.scroll = max(0, m.scroll-max(3, m.height/2))
	case "r":
		if !m.loading {
			m.loading = true
			return m, m.loadCmd()
		}
	case "?":
		m.overlay = overlay{kind: overlayHelp, title: "Dashboard help", content: helpText}
	case "c":
		m.openCreate()
	case "t":
		m.openThreadCreate()
	case "n":
		m.openThreadMessage()
	case "e":
		if thread := m.selectedThread(); thread != nil {
			supported, known := thread.Thread.StructuredSupported.Get()
			if known && !supported {
				m.status = "Structured session unsupported; use a native PTY Run"
				return m, nil
			}
			switch thread.Thread.State {
			case client.ThreadStateActive:
				if threadActiveRun(thread.Thread) {
					m.status = "Thread session is already active"
					return m, nil
				}
				return m.execute(m.threadLifecycleRequest(ActionThreadStart))
			case client.ThreadStatePaused:
				return m.execute(m.threadLifecycleRequest(ActionThreadResume))
			default:
				m.status = "Start/resume is unavailable for this Thread state"
			}
		}
	case "P":
		m.openThreadResponse()
	case "z":
		if thread := m.selectedThread(); thread != nil && threadActiveRun(thread.Thread) {
			m.openThreadConfirm(ActionThreadCancel, "Cancel Thread session", "Cancel the active structured adapter session?")
		}
	case "A":
		m.openThreadConfirm(ActionThreadArchive, "Archive Thread", "Archive this retained Thread history?")
	case "D":
		m.openThreadDelete()
	case "p":
		if detail := m.selectedDetail(); detail != nil && detail.Capsule.State == "Ready" {
			return m.execute(m.lifecycleRequest(ActionPause))
		}
	case "u":
		if detail := m.selectedDetail(); detail != nil && detail.Capsule.State == "Paused" {
			return m.execute(m.lifecycleRequest(ActionResume))
		}
	case "a":
		if detail := m.selectedDetail(); detail != nil {
			if run := detail.ActiveRun(); run != nil {
				for _, profile := range detail.Profiles {
					if profile.Name == run.Harness && profile.Structured {
						m.status = "Selected Run is structured; choose a native PTY profile instead"
						return m, nil
					}
				}
				m.status = "Handing terminal to Run " + safeInline(run.ID)
				return m, m.attach(run.ID, 0)
			}
			m.status = "No active PTY Run to attach"
		}
	case "g":
		if detail := m.selectedDetail(); detail != nil && detail.Capsule.State == "Ready" {
			return m.execute(ActionRequest{Action: ActionDiff, CapsuleID: detail.Capsule.ID})
		}
	case "m":
		m.openNamed(ActionMoment, "Capture Moment", "Moment name", "")
	case "s":
		m.openDescendant(ActionShard, "Create Shard", "")
	case "w":
		m.openDescendant(
			ActionRewind,
			"Rewind Capsule",
			"Rewind creates a new Timeline and does not destroy history.",
		)
	case "S":
		m.openConfirm(ActionSeal, "Seal Capsule", "Seal is permanent and captures a final Moment.")
	case "x":
		m.openConfirm(ActionDelete, "Delete Capsule", "Delete this Capsule?")
	}
	return m, nil
}

func (m Model) updateOverlay(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay.kind {
	case overlayHelp, overlayContent:
		if key.String() == "esc" || key.String() == "q" || key.String() == "enter" {
			m.overlay = overlay{}
		}
		return m, nil
	case overlayConfirm:
		switch key.String() {
		case "esc", "n", "N":
			m.overlay = overlay{}
		case "y", "Y", "enter":
			request := m.overlay.action
			m.overlay = overlay{}
			return m.execute(request)
		}
		return m, nil
	case overlayForm:
		switch key.String() {
		case "esc":
			m.overlay = overlay{}
			return m, nil
		case "tab", "down":
			if len(m.overlay.fields) > 0 {
				m.overlay.focus = (m.overlay.focus + 1) % len(m.overlay.fields)
			}
		case "shift+tab", "up":
			if len(m.overlay.fields) > 0 {
				m.overlay.focus = (m.overlay.focus - 1 + len(m.overlay.fields)) % len(m.overlay.fields)
			}
		case "left", "right":
			if len(m.overlay.fields) > 0 {
				current := &m.overlay.fields[m.overlay.focus]
				if len(current.options) > 0 {
					index := 0
					for optionIndex, option := range current.options {
						if option == current.value {
							index = optionIndex
							break
						}
					}
					delta := 1
					if key.String() == "left" {
						delta = -1
					}
					current.value = current.options[(index+delta+len(current.options))%len(current.options)]
				}
			}
		case "backspace":
			if len(m.overlay.fields) > 0 {
				current := &m.overlay.fields[m.overlay.focus].value
				if len(*current) > 0 {
					_, size := utf8.DecodeLastRuneInString(*current)
					*current = (*current)[:len(*current)-size]
				}
			}
		case "enter", "ctrl+s":
			if key.String() == "enter" && m.overlay.multiline {
				current := &m.overlay.fields[m.overlay.focus]
				if len(current.options) > 0 {
					m.overlay.focus = (m.overlay.focus + 1) % len(m.overlay.fields)
				} else {
					current.value += "\n"
				}
				return m, nil
			}
			request, err := m.formRequest()
			if err != nil {
				m.status = err.Error()
				return m, nil
			}
			m.overlay = overlay{}
			return m.execute(request)
		default:
			if len(m.overlay.fields) > 0 && len(key.Runes) > 0 {
				current := &m.overlay.fields[m.overlay.focus]
				if len(current.options) == 0 {
					current.value += string(key.Runes)
				}
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.overlay.kind != overlayNone {
		return m.overlayView()
	}
	title := m.titleView()
	if !m.connected && m.loading && len(m.snapshot.Capsules) == 0 {
		return title + "\n\nLoading Capsules…\n\n" + footer
	}
	if !m.connected && m.lastErr != nil {
		return title + "\n\nDisconnected: " + safeInline(m.lastErr.Error()) +
			"\nReconnecting automatically; press r to retry now.\n\n" + footer
	}
	if len(m.snapshot.Capsules) == 0 {
		status := "No Capsules. Press c to create one."
		if len(m.snapshot.Projects) == 0 {
			status = "No projects. Create one with `meridian project create`, then press r."
		}
		return title + "\n\n" + status + "\n\n" + footer
	}

	list := m.listView()
	detail := m.detailView(*m.selectedDetail())
	var body string
	if m.width >= 100 {
		listWidth := 34
		body = lipgloss.JoinHorizontal(
			lipgloss.Top,
			boxStyle(listWidth).Render(list),
			boxStyle(max(40, m.width-listWidth-6)).Render(detail),
		)
	} else {
		if m.narrowPane == 0 {
			body = boxStyle(max(30, m.width-4)).Render(list)
		} else {
			body = boxStyle(max(30, m.width-4)).Render(detail)
		}
	}
	return title + "\n" + body + "\n" + footer
}

func (m Model) titleView() string {
	state := "connected"
	if m.reconnect {
		state = "reconnecting"
	} else if !m.connected {
		state = "disconnected"
	}
	if m.loading && m.connected {
		state = "refreshing"
	}
	line := "Meridian  •  " + state
	if m.status != "" {
		line += "  •  " + m.status
	}
	if m.lastErr != nil && m.connected {
		line += "  •  " + safeInline(m.lastErr.Error())
	}
	if os.Getenv("NO_COLOR") == "" {
		return lipgloss.NewStyle().Bold(true).Render(line)
	}
	return line
}

func (m Model) listView() string {
	lines := []string{"Capsules / Threads"}
	for index, detail := range m.snapshot.Capsules {
		prefix := "  "
		if index == m.selected && m.selectedThreadID == "" {
			prefix = "> "
		}
		lines = append(lines, fmt.Sprintf(
			"%s%s / %s  [%s]",
			prefix, safeInline(detail.Project.Name), safeInline(detail.Capsule.Name), detail.Capsule.State,
		))
		for _, thread := range detail.Threads {
			prefix = "    "
			if thread.Thread.ID == m.selectedThreadID {
				prefix = "  > "
			}
			unread := ""
			if count := m.unread[thread.Thread.ID]; count > 0 {
				unread = fmt.Sprintf(" unread=%d", count)
			}
			lines = append(lines, fmt.Sprintf(
				"%sThread %s  [%s]%s",
				prefix, shortID(thread.Thread.ID), threadStateLabel(thread), unread,
			))
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) detailView(detail CapsuleDetail) string {
	if thread := m.selectedThread(); thread != nil {
		return m.threadView(detail, *thread)
	}
	capsule := detail.Capsule
	maintenance, hasMaintenance := capsule.Maintenance.Get()
	if !hasMaintenance {
		maintenance = "none"
	}
	sealed := "no"
	if capsule.State == "Sealed" || capsule.DesiredState == "Sealed" {
		sealed = "yes"
	}
	lines := []string{
		safeInline(capsule.Name),
		"Project: " + safeInline(detail.Project.Name) + " (" + safeInline(detail.Project.ID) + ")",
		"State: " + string(capsule.State) + "  Desired: " + string(capsule.DesiredState),
		"Age: " + humanAge(m.now().Sub(capsule.CreatedAt)),
		"Provider: unavailable (not exposed by API)",
		"Maintenance: " + safeInline(maintenance),
		"Restore complete: " + strconv.FormatBool(capsule.RestoreComplete),
		"Sealed: " + sealed,
		"Resource version: " + strconv.FormatInt(capsule.ResourceVersion, 10),
		"Resources: unavailable (provider metrics not implemented)",
		"",
		"Runs",
	}
	if active := detail.ActiveRun(); active != nil {
		lines = append(lines, fmt.Sprintf("Active: %s  %s  harness=%s", safeInline(active.ID), active.State, safeInline(active.Harness)))
	} else {
		lines = append(lines, "Active: none")
	}
	if latest := detail.LatestRun(); latest != nil {
		lines = append(lines, fmt.Sprintf("Latest: %s  %s  age=%s", safeInline(latest.ID), latest.State, humanAge(m.now().Sub(latest.CreatedAt))))
	} else {
		lines = append(lines, "Latest: none")
	}
	lines = append(lines, "", "Recent Run events")
	if len(detail.Events) == 0 {
		lines = append(lines, "none")
	} else {
		for _, event := range detail.Events {
			lines = append(lines, fmt.Sprintf("#%d %s  %s", event.Sequence, safeInline(event.Type), humanAge(m.now().Sub(event.Timestamp))))
		}
	}
	lines = append(lines, "Lifecycle events: unavailable (not exposed by API)")
	lines = append(lines, "", "Timeline / Moments")
	if detail.Timeline == nil {
		lines = append(lines, "Timeline unavailable")
	} else {
		lines = append(lines, fmt.Sprintf(
			"%s  reason=%s  ancestry=%d",
			safeInline(detail.Timeline.Timeline.ID),
			detail.Timeline.Timeline.Reason,
			len(detail.Timeline.Ancestry),
		))
	}
	if moment := detail.LatestMoment(); moment != nil {
		lines = append(lines, fmt.Sprintf(
			"Latest Moment: %s (%s, %d bytes, final=%t)",
			safeInline(moment.Name), safeInline(moment.ID), moment.ArchiveSize, moment.Final,
		))
	} else {
		lines = append(lines, "Latest Moment: none")
	}
	lines = append(lines, "", "Actions: "+m.actions(detail))
	return strings.Join(lines, "\n")
}

func (m Model) actions(detail CapsuleDetail) string {
	actions := []string{"c create Capsule"}
	structured := false
	pty := false
	for _, profile := range detail.Profiles {
		structured = structured || profile.Structured
		pty = pty || profile.Pty
	}
	if structured {
		actions = append(actions, "t create Thread")
	} else {
		actions = append(actions, "Thread unsupported")
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

func (m Model) overlayView() string {
	lines := []string{m.overlay.title}
	if m.overlay.note != "" {
		lines = append(lines, "", m.overlay.note)
	}
	switch m.overlay.kind {
	case overlayForm:
		lines = append(lines, "")
		for index, item := range m.overlay.fields {
			prefix := "  "
			if index == m.overlay.focus {
				prefix = "> "
			}
			suffix := ""
			if len(item.options) > 0 {
				suffix = "  (←/→ select)"
			}
			value := safeBlock(item.value)
			if item.secret && value != "" {
				value = strings.Repeat("•", utf8.RuneCountInString(value))
			}
			lines = append(lines, prefix+item.label+": "+value+suffix)
		}
		if m.overlay.multiline {
			lines = append(lines, "", "tab: next  enter: newline  ctrl+s: submit  esc: cancel")
		} else {
			lines = append(lines, "", "tab: next  enter: submit  esc: cancel")
		}
	case overlayConfirm:
		lines = append(lines, "", "y/enter: confirm  n/esc: cancel")
	case overlayHelp, overlayContent:
		lines = append(lines, "", m.overlay.content, "", "enter/esc: close")
	}
	width := max(40, min(90, m.width-4))
	return boxStyle(width).Render(strings.Join(lines, "\n"))
}

func (m *Model) restoreSelection() {
	if m.selectedThreadID != "" {
		for capsuleIndex := range m.snapshot.Capsules {
			for _, thread := range m.snapshot.Capsules[capsuleIndex].Threads {
				if thread.Thread.ID == m.selectedThreadID {
					m.selected = capsuleIndex
					m.selectedID = m.snapshot.Capsules[capsuleIndex].Capsule.ID
					m.markThreadRead(thread)
					return
				}
			}
		}
		m.selectedThreadID = ""
	}
	if m.selectedID != "" {
		for index := range m.snapshot.Capsules {
			if m.snapshot.Capsules[index].Capsule.ID == m.selectedID {
				m.selected = index
				return
			}
		}
	}
	if len(m.snapshot.Capsules) == 0 {
		m.selected = 0
		m.selectedID = ""
		return
	}
	if m.selected >= len(m.snapshot.Capsules) {
		m.selected = len(m.snapshot.Capsules) - 1
	}
	m.selectedID = m.snapshot.Capsules[m.selected].Capsule.ID
}

func (m *Model) move(delta int) {
	items := m.navigation()
	if len(items) == 0 {
		return
	}
	current := 0
	for index, item := range items {
		if item.capsule == m.selected && item.threadID == m.selectedThreadID {
			current = index
			break
		}
	}
	item := items[(current+delta+len(items))%len(items)]
	m.selected = item.capsule
	m.selectedID = m.snapshot.Capsules[item.capsule].Capsule.ID
	m.selectedThreadID = item.threadID
	m.scroll = 0
	if thread := m.selectedThread(); thread != nil {
		m.markThreadRead(*thread)
	}
}

func (m *Model) selectedDetail() *CapsuleDetail {
	if m.selected < 0 || m.selected >= len(m.snapshot.Capsules) {
		return nil
	}
	return &m.snapshot.Capsules[m.selected]
}

func (m *Model) selectedThread() *ThreadDetail {
	if m.selectedThreadID == "" {
		return nil
	}
	detail := m.selectedDetail()
	if detail == nil {
		return nil
	}
	for index := range detail.Threads {
		if detail.Threads[index].Thread.ID == m.selectedThreadID {
			return &detail.Threads[index]
		}
	}
	return nil
}

type navigationItem struct {
	capsule  int
	threadID string
}

func (m Model) navigation() []navigationItem {
	var output []navigationItem
	for capsuleIndex, detail := range m.snapshot.Capsules {
		output = append(output, navigationItem{capsule: capsuleIndex})
		for _, thread := range detail.Threads {
			output = append(output, navigationItem{capsule: capsuleIndex, threadID: thread.Thread.ID})
		}
	}
	return output
}

func (m *Model) openCreate() {
	projectID := ""
	if detail := m.selectedDetail(); detail != nil {
		projectID = detail.Project.ID
	} else if len(m.snapshot.Projects) > 0 {
		projectID = m.snapshot.Projects[0].ID
	}
	if projectID == "" {
		m.status = "Create a project with the scriptable CLI first"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: "Create Capsule", action: ActionRequest{Action: ActionCreate},
		fields: []field{{label: "Project ID", value: projectID}, {label: "Capsule name"}},
	}
}

func (m *Model) openThreadCreate() {
	detail := m.selectedDetail()
	if detail == nil {
		m.status = "Select a Capsule first"
		return
	}
	harness := ""
	var harnesses []string
	for _, profile := range detail.Profiles {
		if profile.Structured {
			harnesses = append(harnesses, profile.Name)
			if harness == "" {
				harness = profile.Name
			}
		}
	}
	if harness == "" {
		m.status = "Structured Threads unsupported; use native PTY Run attach"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: "Create structured Thread",
		note:      "Select a structured harness profile. The optional first message can start the session atomically.",
		multiline: true,
		action:    ActionRequest{Action: ActionThreadCreate, CapsuleID: detail.Capsule.ID},
		fields: []field{
			{label: "Harness profile", value: harness, options: harnesses},
			{label: "First message", optional: true},
			{label: "Start", value: "yes", options: []string{"yes", "no"}},
		},
	}
}

func (m *Model) openThreadMessage() {
	thread := m.selectedThread()
	if thread == nil || thread.Thread.State != client.ThreadStateActive {
		m.status = "Select an active structured Thread"
		return
	}
	supported, present := thread.Thread.StructuredSupported.Get()
	if present && !supported {
		m.status = "Structured messaging unsupported; use the native PTY fallback"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: "Send Thread message", multiline: true,
		action: ActionRequest{
			Action: ActionThreadSend, ThreadID: thread.Thread.ID,
			ResourceVersion: thread.Thread.ResourceVersion,
		},
		fields: []field{{label: "Message"}},
	}
}

func (m *Model) openThreadResponse() {
	thread := m.selectedThread()
	if thread == nil {
		m.status = "Select a Thread with a pending request"
		return
	}
	block, event := pendingRequest(thread.Blocks)
	if block == nil || event == nil {
		m.status = "No pending permission or input request"
		return
	}
	request := ActionRequest{
		Action: ActionThreadRespond, ThreadID: thread.Thread.ID,
		ResourceVersion: thread.Thread.ResourceVersion, ResponseTo: block.MessageId,
	}
	if messageID, ok := event.MessageId.Get(); ok && messageID != "" {
		request.ResponseTo = messageID
	}
	title := "Answer permission request"
	label := "Choice"
	value := ""
	multiline := false
	note := "Confirm or select one of the adapter-provided choices."
	if event.Type == client.ThreadAdapterEventTypeInputRequest {
		title, label, multiline = "Answer input request", "Input", true
		note = "Input is sent only to the structured adapter and encrypted in the transcript."
	} else if permission, ok := event.Permission.Get(); ok && len(permission.Options) > 0 {
		value = permission.Options[0]
		note += " Options: " + strings.Join(permission.Options, ", ")
	} else {
		value = "allow"
		note += " Use allow or deny."
	}
	secret := false
	if input, ok := event.Input.Get(); ok {
		secret, _ = input.Secret.Get()
	}
	m.overlay = overlay{
		kind: overlayForm, title: title, note: safeInline(note), multiline: multiline,
		action: request, fields: []field{{label: label, value: value, secret: secret}},
	}
}

func (m *Model) openThreadConfirm(action Action, title, note string) {
	thread := m.selectedThread()
	if thread == nil || thread.Thread.State == client.ThreadStateDeleted {
		m.status = "Thread action is unavailable"
		return
	}
	m.overlay = overlay{
		kind: overlayConfirm, title: title, note: note,
		action: ActionRequest{
			Action: action, ThreadID: thread.Thread.ID,
			ResourceVersion: thread.Thread.ResourceVersion,
		},
	}
}

func (m *Model) openThreadDelete() {
	thread := m.selectedThread()
	if thread == nil || thread.Thread.State == client.ThreadStateDeleted {
		m.status = "Thread deletion is unavailable"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: "Crypto-shred Thread",
		note: "Irreversible: the wrapped transcript key is deleted. Type crypto-shred to confirm.",
		action: ActionRequest{
			Action: ActionThreadDelete, ThreadID: thread.Thread.ID,
			ResourceVersion: thread.Thread.ResourceVersion,
		},
		fields: []field{{label: "Confirmation"}},
	}
}

func (m *Model) openNamed(action Action, title, label, note string) {
	detail := m.selectedDetail()
	if detail == nil || action != ActionMoment || detail.Capsule.State != "Ready" ||
		detail.Capsule.Maintenance.IsSet() || detail.ActiveRun() != nil {
		m.status = "Action is unavailable for the selected Capsule"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: title, note: note,
		action: ActionRequest{
			Action: action, CapsuleID: detail.Capsule.ID,
			ResourceVersion: detail.Capsule.ResourceVersion,
		},
		fields: []field{{label: label}},
	}
}

func (m *Model) openDescendant(action Action, title, note string) {
	detail := m.selectedDetail()
	if detail == nil || detail.LatestMoment() == nil {
		m.status = "A Moment is required for this action"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: title, note: note,
		action: ActionRequest{Action: action, CapsuleID: detail.Capsule.ID},
		fields: []field{
			{label: "Moment ID", value: detail.LatestMoment().ID},
			{label: "New Capsule name"},
		},
	}
}

func (m *Model) openConfirm(action Action, title, note string) {
	detail := m.selectedDetail()
	available := detail != nil
	if available && action == ActionSeal {
		available = detail.Capsule.State == "Ready" &&
			!detail.Capsule.Maintenance.IsSet() && detail.ActiveRun() == nil
	}
	if available && action == ActionDelete {
		available = mutable(string(detail.Capsule.State)) && !detail.Capsule.Maintenance.IsSet()
	}
	if !available {
		m.status = "Action is unavailable for the selected Capsule"
		return
	}
	m.overlay = overlay{
		kind: overlayConfirm, title: title, note: note,
		action: ActionRequest{
			Action: action, CapsuleID: detail.Capsule.ID,
			ResourceVersion: detail.Capsule.ResourceVersion,
		},
	}
}

func (m Model) formRequest() (ActionRequest, error) {
	request := m.overlay.action
	for _, item := range m.overlay.fields {
		if strings.TrimSpace(item.value) == "" && !item.optional {
			return ActionRequest{}, fmt.Errorf("%s is required", item.label)
		}
		if utf8.RuneCountInString(item.value) > 128 &&
			item.label != "Project ID" && item.label != "Moment ID" &&
			item.label != "Message" && item.label != "First message" && item.label != "Input" {
			return ActionRequest{}, fmt.Errorf("%s exceeds 128 characters", item.label)
		}
	}
	switch request.Action {
	case ActionCreate:
		request.ProjectID = strings.TrimSpace(m.overlay.fields[0].value)
		request.Name = strings.TrimSpace(m.overlay.fields[1].value)
	case ActionMoment:
		request.Name = strings.TrimSpace(m.overlay.fields[0].value)
	case ActionShard, ActionRewind:
		request.MomentID = strings.TrimSpace(m.overlay.fields[0].value)
		request.Name = strings.TrimSpace(m.overlay.fields[1].value)
	case ActionThreadCreate:
		request.Harness = strings.TrimSpace(m.overlay.fields[0].value)
		request.Content = m.overlay.fields[1].value
		start := strings.ToLower(strings.TrimSpace(m.overlay.fields[2].value))
		if start != "yes" && start != "no" {
			return ActionRequest{}, errors.New("Start must be yes or no")
		}
		request.Start = start == "yes"
		if request.Start && request.Content == "" {
			return ActionRequest{}, errors.New("First message is required when Start is yes")
		}
	case ActionThreadSend:
		request.Content = m.overlay.fields[0].value
		if request.Content == "" {
			return ActionRequest{}, errors.New("Message is required")
		}
	case ActionThreadRespond:
		thread := m.selectedThread()
		if thread == nil {
			return ActionRequest{}, errors.New("selected Thread is no longer available")
		}
		if _, event := pendingRequest(thread.Blocks); event != nil &&
			event.Type == client.ThreadAdapterEventTypeInputRequest {
			request.Input = m.overlay.fields[0].value
		} else {
			request.Choice = strings.TrimSpace(m.overlay.fields[0].value)
		}
	case ActionThreadDelete:
		if strings.TrimSpace(m.overlay.fields[0].value) != "crypto-shred" {
			return ActionRequest{}, errors.New("type crypto-shred to confirm irreversible deletion")
		}
	}
	return request, nil
}

func (m Model) lifecycleRequest(action Action) ActionRequest {
	detail := m.selectedDetail()
	return ActionRequest{
		Action: action, CapsuleID: detail.Capsule.ID,
		ResourceVersion: detail.Capsule.ResourceVersion,
	}
}

func (m Model) threadLifecycleRequest(action Action) ActionRequest {
	thread := m.selectedThread()
	if thread == nil {
		return ActionRequest{}
	}
	return ActionRequest{
		Action: action, ThreadID: thread.Thread.ID,
		ResourceVersion: thread.Thread.ResourceVersion,
	}
}

func (m Model) threadView(capsule CapsuleDetail, detail ThreadDetail) string {
	thread := detail.Thread
	runID, _ := thread.CurrentRunId.Get()
	runState := "none"
	if value, ok := thread.CurrentRunState.Get(); ok {
		runState = string(value)
	}
	protocol := "unsupported"
	if value, ok := thread.Protocol.Get(); ok {
		protocol = string(value)
	}
	supported := "unknown"
	if value, ok := thread.StructuredSupported.Get(); ok {
		supported = strconv.FormatBool(value)
	}
	adapter := "unknown"
	for _, profile := range capsule.Profiles {
		if profile.Name != thread.Harness {
			continue
		}
		if value, ok := profile.AdapterKind.Get(); ok {
			adapter = string(value)
		} else if profile.Pty {
			adapter = "native PTY"
		}
		break
	}
	lines := []string{
		"Thread " + safeInline(shortID(thread.ID)),
		"Capsule: " + safeInline(capsule.Capsule.Name),
		"State: " + string(thread.State) + "  Run: " + safeInline(runState),
		"Harness: " + safeInline(thread.Harness) + "  Adapter: " + safeInline(adapter),
		"Protocol: " + safeInline(protocol) + "  structured=" + supported,
		"Run ID: " + safeInline(runID),
		"Age: " + humanAge(m.now().Sub(thread.CreatedAt)) +
			"  latest activity: " + humanAge(m.now().Sub(thread.UpdatedAt)),
		"Messages: " + strconv.FormatInt(thread.MessageCount, 10) +
			"  encrypted at rest: " + strconv.FormatBool(thread.EncryptedAtRest),
		"Resource version: " + strconv.FormatInt(thread.ResourceVersion, 10),
	}
	if value, ok := thread.StructuredSupported.Get(); ok && !value {
		lines = append(lines, "", "Unsupported: this harness is PTY-only. Use a native Run and attach.")
	}
	if detail.TranscriptCode == "transcript_locked" {
		lines = append(lines, "", "TRANSCRIPT LOCKED",
			"The installation key is missing or does not match. Restore the correct operator key; no ciphertext details are shown.")
	} else if detail.TranscriptCode == "transcript_corrupt" {
		lines = append(lines, "", "TRANSCRIPT CORRUPT",
			"Stop using this Thread and restore from a trusted backup. No key or ciphertext details are shown.")
	} else if detail.TranscriptError != "" {
		lines = append(lines, "", "Transcript unavailable: "+safeInline(detail.TranscriptError))
	}
	if detail.Gap != nil {
		requested, _ := detail.Gap.RequestedAfter.Get()
		available, _ := detail.Gap.AvailableFrom.Get()
		lines = append(lines, "", fmt.Sprintf(
			"Replay gap: requested after %d; available from %d. Earlier blocks cannot be reconstructed.",
			requested, available,
		))
	}
	if detail.More || detail.Truncated {
		lines = append(lines, "", "Transcript truncated to the newest retained rendering bound.")
	}
	lines = append(lines, "", "Transcript")
	rendered := renderThreadBlocks(detail.Blocks)
	if len(rendered) == 0 && detail.TranscriptError == "" {
		lines = append(lines, "No messages yet.")
	} else {
		lines = append(lines, rendered...)
	}
	lines = append(lines, "", "Actions: "+m.threadActions(detail))
	if m.scroll > 0 && len(lines) > 12 {
		end := max(0, len(lines)-m.scroll)
		start := max(0, end-max(8, m.height-15))
		lines = append([]string{"… scrolled …"}, lines[start:end]...)
	}
	return strings.Join(lines, "\n")
}

func (m Model) threadActions(detail ThreadDetail) string {
	thread := detail.Thread
	actions := []string{"t new Thread"}
	if thread.State == client.ThreadStateActive {
		actions = append(actions, "n message")
		if threadActiveRun(thread) {
			actions = append(actions, "z cancel")
		} else {
			actions = append(actions, "e start")
		}
		if block, _ := pendingRequest(detail.Blocks); block != nil {
			actions = append(actions, "P answer request")
		}
	}
	if thread.State == client.ThreadStatePaused {
		actions = append(actions, "e resume")
	}
	if thread.State != client.ThreadStateArchived && thread.State != client.ThreadStateDeleted {
		actions = append(actions, "A archive")
	}
	if thread.State != client.ThreadStateDeleted {
		actions = append(actions, "D crypto-shred")
	}
	actions = append(actions, "a PTY fallback")
	return strings.Join(actions, "  ")
}

func threadActiveRun(thread client.Thread) bool {
	state, ok := thread.CurrentRunState.Get()
	if !ok {
		return false
	}
	switch state {
	case client.RunStateQueued, client.RunStateStarting, client.RunStateRunning, client.RunStateCancelling:
		return true
	default:
		return false
	}
}

func renderThreadBlocks(blocks []client.ThreadBlock) []string {
	const maxRendered = 200
	if len(blocks) > maxRendered {
		blocks = blocks[len(blocks)-maxRendered:]
	}
	finalMessages := make(map[string]bool)
	for _, block := range blocks {
		if event, ok := block.Event.Get(); ok &&
			event.Type == client.ThreadAdapterEventTypeAssistantMessage {
			finalMessages[threadMessageKey(block, event)] = true
		}
	}
	var lines []string
	deltaStarted := make(map[string]bool)
	for _, block := range blocks {
		content, _ := block.Content.Get()
		content = boundedText(content, 16<<10)
		event, hasEvent := block.Event.Get()
		if hasEvent && event.Type == client.ThreadAdapterEventTypeAssistantDelta {
			key := threadMessageKey(block, event)
			if finalMessages[key] {
				continue
			}
			if summary, ok := event.Summary.Get(); ok {
				content = boundedText(summary, 16<<10)
			}
			prefix := ""
			if !deltaStarted[key] {
				prefix = "assistant (streaming): "
				deltaStarted[key] = true
			}
			lines = append(lines, prefix+safeBlock(content))
			continue
		}
		if hasEvent {
			switch event.Type {
			case client.ThreadAdapterEventTypeAssistantMessage:
				lines = append(lines, "assistant: "+safeBlock(content))
			case client.ThreadAdapterEventTypeToolStart:
				name, _ := event.ToolName.Get()
				summary, _ := event.Summary.Get()
				lines = append(lines, "tool start · "+safeInline(name)+": "+safeBlock(boundedText(summary, 4096)))
			case client.ThreadAdapterEventTypeToolResult:
				result, _ := event.Result.Get()
				lines = append(lines, "tool result: "+safeBlock(boundedText(result, 4096)))
			case client.ThreadAdapterEventTypeStatus:
				status, _ := event.Status.Get()
				lines = append(lines, "status: "+safeInline(string(status)))
			case client.ThreadAdapterEventTypeError:
				code, _ := event.Code.Get()
				reason, _ := event.Reason.Get()
				lines = append(lines, "error · "+safeInline(code)+": "+safeBlock(boundedText(reason, 4096)))
			case client.ThreadAdapterEventTypePermissionRequest:
				permission, _ := event.Permission.Get()
				summary, _ := permission.Summary.Get()
				lines = append(lines, "permission requested · "+safeInline(permission.Kind)+": "+
					safeBlock(boundedText(summary, 4096)))
			case client.ThreadAdapterEventTypeInputRequest:
				input, _ := event.Input.Get()
				prompt, _ := input.Prompt.Get()
				lines = append(lines, "input requested: "+safeBlock(boundedText(prompt, 4096)))
			case client.ThreadAdapterEventTypePermissionResponse, client.ThreadAdapterEventTypeInputResponse:
				lines = append(lines, "request answered")
			case client.ThreadAdapterEventTypeGap:
				lines = append(lines, "stream gap reported")
			case client.ThreadAdapterEventTypeEnd:
				lines = append(lines, "session ended")
			default:
				lines = append(lines, "unknown event "+safeInline(string(event.Type))+": "+safeBlock(content))
			}
			continue
		}
		if content != "" {
			if !knownThreadBlockKind(block.Kind) {
				lines = append(lines, "unknown block · "+safeInline(string(block.Kind))+": "+safeBlock(content))
			} else {
				lines = append(lines, string(block.Role)+": "+safeBlock(content))
			}
		} else {
			lines = append(lines, "unknown block · "+safeInline(string(block.Kind)))
		}
	}
	return lines
}

func threadMessageKey(block client.ThreadBlock, event client.ThreadAdapterEvent) string {
	if messageID, ok := event.MessageId.Get(); ok && messageID != "" {
		return messageID
	}
	return block.MessageId
}

func knownThreadBlockKind(kind client.ThreadBlockKind) bool {
	switch kind {
	case client.ThreadBlockKindText, client.ThreadBlockKindJSON,
		client.ThreadBlockKindToolCall, client.ThreadBlockKindToolResult,
		client.ThreadBlockKindError:
		return true
	default:
		return false
	}
}

func pendingRequest(blocks []client.ThreadBlock) (*client.ThreadBlock, *client.ThreadAdapterEvent) {
	var permissions []int
	var inputs []int
	for index := range blocks {
		event, ok := blocks[index].Event.Get()
		if !ok {
			continue
		}
		switch event.Type {
		case client.ThreadAdapterEventTypePermissionRequest:
			permissions = append(permissions, index)
		case client.ThreadAdapterEventTypeInputRequest:
			inputs = append(inputs, index)
		case client.ThreadAdapterEventTypePermissionResponse:
			if len(permissions) > 0 {
				permissions = permissions[:len(permissions)-1]
			}
		case client.ThreadAdapterEventTypeInputResponse:
			if len(inputs) > 0 {
				inputs = inputs[:len(inputs)-1]
			}
		}
	}
	index := -1
	if len(permissions) > 0 {
		index = permissions[len(permissions)-1]
	}
	if len(inputs) > 0 && inputs[len(inputs)-1] > index {
		index = inputs[len(inputs)-1]
	}
	if index >= 0 {
		event, _ := blocks[index].Event.Get()
		return &blocks[index], &event
	}
	return nil, nil
}

func (m *Model) trackUnread(snapshot Snapshot) {
	for _, capsule := range snapshot.Capsules {
		for _, thread := range capsule.Threads {
			previous := m.seenCursor[thread.Thread.ID]
			if previous > 0 && thread.Cursor > previous && thread.Thread.ID != m.selectedThreadID {
				m.unread[thread.Thread.ID] += thread.Cursor - previous
			}
			if thread.Thread.ID == m.selectedThreadID {
				m.seenCursor[thread.Thread.ID] = thread.Cursor
				m.unread[thread.Thread.ID] = 0
			} else if previous == 0 {
				m.seenCursor[thread.Thread.ID] = thread.Cursor
			}
		}
	}
}

func (m *Model) markThreadRead(thread ThreadDetail) {
	m.seenCursor[thread.Thread.ID] = thread.Cursor
	m.unread[thread.Thread.ID] = 0
}

func threadStateLabel(detail ThreadDetail) string {
	if detail.TranscriptCode == "transcript_locked" {
		return "locked"
	}
	if detail.TranscriptCode != "" {
		return "error"
	}
	if detail.Gap != nil {
		return "gap"
	}
	if state, ok := detail.Thread.CurrentRunState.Get(); ok && state == client.RunStateFailed {
		return "error"
	}
	if threadActiveRun(detail.Thread) {
		return "active"
	}
	return string(detail.Thread.State)
}

func shortID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n… content truncated …"
}

func (m Model) execute(request ActionRequest) (tea.Model, tea.Cmd) {
	m.loading = true
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, callTimeout)
		defer cancel()
		result, err := m.api.Execute(ctx, request)
		return actionMsg{result: result, err: err}
	}
}

func (m Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		if m.api == nil {
			return loadMsg{err: errors.New("dashboard API is not configured")}
		}
		ctx, cancel := context.WithTimeout(m.ctx, callTimeout)
		defer cancel()
		snapshot, err := m.api.Snapshot(ctx)
		return loadMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(pollCadence, func(value time.Time) tea.Msg { return tickMsg(value) })
}

func (m Model) attachProcess(runID string, after uint64) tea.Cmd {
	arguments := []string{
		"--server", m.server, "run", "attach", runID, "--after", strconv.FormatUint(after, 10),
	}
	command := exec.CommandContext(m.ctx, m.executable, arguments...)
	return tea.ExecProcess(command, func(err error) tea.Msg { return attachFinishedMsg{err: err} })
}

func mutable(state string) bool {
	return state != "Deleted" && state != "Deleting" && state != "Sealed"
}

func humanAge(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	switch {
	case duration < time.Minute:
		return strconv.Itoa(int(duration.Seconds())) + "s"
	case duration < time.Hour:
		return strconv.Itoa(int(duration.Minutes())) + "m"
	case duration < 24*time.Hour:
		return strconv.Itoa(int(duration.Hours())) + "h"
	default:
		return strconv.Itoa(int(duration/(24*time.Hour))) + "d"
	}
}

func safeInline(value string) string {
	return strings.Map(func(current rune) rune {
		if unicode.IsControl(current) {
			return -1
		}
		return current
	}, value)
}

func safeBlock(value string) string {
	return strings.Map(func(current rune) rune {
		if current == '\n' || current == '\t' {
			return current
		}
		if unicode.IsControl(current) {
			return -1
		}
		return current
	}, value)
}

func boxStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(width)
}

const footer = "j/k: fleet  tab: pane  t: Thread  n: message  a: PTY  ?: help  q: quit"

const helpText = `Navigation
  j/k or arrows  select Capsule or retained Thread
  tab            switch fleet/detail pane on narrow terminals
  PgUp/PgDown    scroll transcript
  r              refresh
  q              quit

Structured Threads
  t create Thread      n send multi-line message (Ctrl-S submits)
  e start/resume       P answer permission or input request
  z cancel session     A archive
  D crypto-shred after typing the irreversible confirmation

Capsule / PTY
  c create Capsule     p pause       u resume
  a native PTY attach  g Git diff    m capture Moment
  s create Shard       w Rewind      S Seal
  x delete

Delete and Seal require confirmation. Rewind creates a new Timeline and
does not destroy history. Native PTY attach temporarily owns the terminal;
press Ctrl-] to detach and return to the dashboard. Structured text is read
only through encrypted Thread block APIs and is never scraped from a PTY.`
