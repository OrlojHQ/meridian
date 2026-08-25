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

	"github.com/OrlojHQ/meridian/pkg/client"
)

const (
	pollCadence  = 3 * time.Second
	callTimeout  = 5 * time.Second
	blinkCadence = 530 * time.Millisecond
)

type loadMsg struct {
	snapshot Snapshot
	err      error
}

type tickMsg time.Time
type blinkMsg time.Time

type actionMsg struct {
	result ActionResult
	err    error
	action Action
}

type attachFinishedMsg struct{ err error }

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayForm
	overlayConfirm
	overlayContent
	overlayHelp
	overlayPalette
	overlayHarness
)

type place int

const (
	placeProject place = iota
	placeCapsule
	placeThread
)

type focusPane int

const (
	focusMain focusPane = iota
	focusCapsules
)

type navigationMode int

const (
	modeLauncher navigationMode = iota
	modeCommand
	modeHistory
)

type field struct {
	label        string
	value        string
	optional     bool
	options      []string
	optionValues []string
	optionIndex  int
	secret       bool
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
	choices   []harnessChoice
}

type harnessChoice struct {
	Name           string
	ImageReference string
	Applied        bool
}

type AttachRequest struct {
	RunID        string
	After        uint64
	Title        string
	RestoreTitle string
}

// Options configures a dashboard model.
type Options struct {
	Context    context.Context
	API        API
	Server     string
	TokenFile  string
	Executable string
	Now        func() time.Time
	Attach     func(AttachRequest) tea.Cmd
}

// Model is the Bubble Tea dashboard state.
type Model struct {
	ctx        context.Context
	api        API
	server     string
	tokenFile  string
	executable string
	now        func() time.Time
	attach     func(AttachRequest) tea.Cmd

	width            int
	height           int
	selected         int
	selectedID       string
	selectedThreadID string
	threadCursor     int
	place            place
	focus            focusPane
	mode             navigationMode
	projectID        string
	showDeleted      bool
	stayOnList       bool
	composer         string
	composerFocus    bool
	cursorOn         bool
	blinkStarted     bool
	pendingSend      string
	pendingNew       bool
	newName          string
	newNameFocus     bool
	newHarness       string
	newHarnesses     []string
	pendingAttachID  string
	sendInFlight     bool
	sendRetries      int
	paletteQuery     string
	paletteIndex     int
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
		ctx: options.Context, api: options.API, server: options.Server, tokenFile: options.TokenFile,
		executable: options.Executable, now: options.Now, loading: true,
		focus: focusCapsules, mode: modeLauncher, stayOnList: true, cursorOn: true,
		unread: make(map[string]int64), seenCursor: make(map[string]int64),
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
	next, cmd := m.dispatch(message)
	model, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if _, blink := message.(blinkMsg); blink || model.blinkStarted {
		return model, cmd
	}
	model.blinkStarted = true
	model.cursorOn = true
	return model, tea.Batch(cmd, model.blinkCmd())
}

func (m Model) dispatch(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case blinkMsg:
		m.cursorOn = !m.cursorOn
		return m, m.blinkCmd()
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
		if next, cmd, ok := m.advancePendingAttach(); ok {
			return next, cmd
		}
		if m.sendInFlight {
			return m, m.tickCmd()
		}
		if next, cmd, ok := m.flushPendingSend(); ok {
			return next, cmd
		}
		m.settleStatus()
		return m, m.tickCmd()
	case tickMsg:
		if m.loading {
			return m, nil
		}
		m.loading = true
		return m, m.loadCmd()
	case actionMsg:
		m.loading = false
		m.sendInFlight = false
		if value.err != nil {
			if value.action == ActionRunStart {
				m.pendingAttachID = ""
			}
			if isVersionConflict(value.err) && m.pendingSend != "" && m.sendRetries < 1 {
				m.sendRetries++
				m.status = "Sending…"
				m.loading = true
				return m, m.loadCmd()
			}
			if m.pendingSend != "" {
				m.composer = m.pendingSend
				m.pendingSend = ""
			}
			m.sendRetries = 0
			m.status = "Action failed: " + safeInline(value.err.Error())
			if isVersionConflict(value.err) {
				m.status += " · refreshing current resource version"
				m.loading = true
				return m, m.loadCmd()
			}
			return m, nil
		}
		m.sendRetries = 0
		m.status = friendlyStatus(value.result.Message)
		if value.action == ActionDelete {
			m.goToCapsuleList()
		}
		if value.result.ProjectID != "" {
			m.projectID = value.result.ProjectID
			m.goToCapsuleList()
		}
		if value.action == ActionCreate && value.result.CapsuleID != "" {
			m.pendingNew = false
			m.newName = ""
			m.newNameFocus = false
			m.newHarness = ""
			m.newHarnesses = nil
			m.pendingAttachID = value.result.CapsuleID
			m.selectedID = value.result.CapsuleID
			m.selectedThreadID = ""
			m.stayOnList = true
			m.place = placeProject
			m.focus = focusCapsules
			m.mode = modeLauncher
			m.composerFocus = false
			m.status = "Waiting for the native harness…"
		} else if value.action == ActionRunStart && value.result.CapsuleID != "" {
			m.pendingAttachID = value.result.CapsuleID
			m.status = "Waiting for the native harness…"
		} else if value.result.CapsuleID != "" || value.result.ThreadID != "" {
			m.openCreatedSession(value.result.CapsuleID, value.result.ThreadID)
		}
		if value.result.Content != "" {
			m.overlay = overlay{
				kind: overlayContent, title: safeInline(value.result.Message), content: safeBlock(value.result.Content),
			}
			return m, nil
		}
		if value.result.Message == "Message sent" || value.result.Message == "Thread created" ||
			value.result.Message == "Project Thread provisioning requested" {
			m.pendingSend = ""
		}
		m.loading = true
		return m, m.loadCmd()
	case attachFinishedMsg:
		m.mode = modeLauncher
		m.stayOnList = true
		m.place = placeProject
		m.focus = focusCapsules
		m.composerFocus = false
		if value.err != nil {
			m.status = "Attach failed: " + safeInline(value.err.Error())
		} else {
			m.status = "Detached · Run still active"
		}
		m.loading = true
		return m, m.loadCmd()
	case tea.KeyMsg:
		m.cursorOn = true
		if m.overlay.kind != overlayNone {
			return m.updateOverlay(value)
		}
		return m.updateKey(value)
	}
	return m, nil
}

func (m Model) composing() bool {
	if m.mode == modeCommand || m.composerFocus || m.pendingNew {
		return true
	}
	return m.mode == modeHistory && m.focus == focusMain &&
		(m.place == placeThread || m.place == placeCapsule)
}

func (m Model) slashQuery() (string, bool) {
	if m.mode != modeCommand || !strings.HasPrefix(m.composer, "/") {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(m.composer[1:])), true
}

func (m Model) openHelp() (tea.Model, tea.Cmd) {
	m.overlay = overlay{kind: overlayHelp, title: "Help", content: helpText}
	return m, nil
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.composing() {
		if key.String() == "/" {
			m.mode = modeCommand
			m.composerFocus = true
			m.composer = "/"
			m.paletteIndex = 0
			return m, nil
		}
		if text := composerTyped(key); strings.HasPrefix(text, "/") {
			m.mode = modeCommand
			m.composerFocus = true
			m.composer = text
			m.paletteIndex = 0
			return m, nil
		}
	}
	if m.composing() {
		return m.updateComposer(key)
	}
	switch key.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m.goBack()
	case "tab":
		if m.mode == modeHistory && m.focus == focusMain {
			m.focus = focusCapsules
			m.composerFocus = false
		}
		return m, nil
	case "pgup":
		m.scroll += max(3, m.height/2)
		return m, nil
	case "pgdown":
		m.scroll = max(0, m.scroll-max(3, m.height/2))
		return m, nil
	case "r":
		if !m.loading {
			m.loading = true
			return m, m.loadCmd()
		}
		return m, nil
	case "?", "ctrl+o":
		return m.openHelp()
	case ":":
		m.openPalette()
		return m, nil
	case "enter":
		return m.enterSelection()
	case "n", "c":
		m.openCreate()
		return m, nil
	case "t":
		m.openThreadCreate()
		return m, nil
	case "T":
		m.beginNewCapsule()
		return m, nil
	case "P":
		m.openThreadResponse()
		return m, nil
	case "j", "down":
		m.move(1)
		return m, nil
	case "k", "up":
		m.move(-1)
		return m, nil
	}
	if next, cmd, ok := m.runHiddenAlias(key.String()); ok {
		return next, cmd
	}
	return m, nil
}

func (m Model) updateComposer(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingNew && m.newNameFocus {
		return m.updateNewName(key)
	}
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+o":
		return m.openHelp()
	case "esc":
		if m.mode == modeCommand {
			m.mode = modeLauncher
			m.composer = ""
			m.composerFocus = false
			m.paletteIndex = 0
			return m, nil
		}
		if m.composer != "" {
			m.composer = ""
			m.paletteIndex = 0
			return m, nil
		}
		m.composerFocus = false
		return m.goBack()
	case "tab":
		if m.pendingNew {
			m.newNameFocus = true
			return m, nil
		}
		if m.place == placeProject {
			m.composer = ""
			m.paletteIndex = 0
			m.mode = modeLauncher
		}
		m.composerFocus = false
		m.focus = focusCapsules
		return m, nil
	case "left":
		if m.pendingNew {
			m.cycleNewHarness(-1)
			return m, nil
		}
	case "right":
		if m.pendingNew {
			m.cycleNewHarness(1)
			return m, nil
		}
	case "enter":
		if _, slash := m.slashQuery(); slash {
			return m.applySlash()
		}
		if m.pendingNew {
			return m.sendComposer()
		}
		if m.place == placeProject {
			if strings.TrimSpace(m.composer) == "" {
				m.composerFocus = false
				m.focus = focusCapsules
				return m.enterSelection()
			}
			m.status = "Project home accepts / commands; press Tab to return to Capsules"
			return m, nil
		}
		return m.sendComposer()
	case "ctrl+j":
		if _, slash := m.slashQuery(); slash {
			return m, nil
		}
		m.composer += "\n"
		return m, nil
	case "backspace":
		if m.composer != "" {
			_, size := utf8.DecodeLastRuneInString(m.composer)
			m.composer = m.composer[:len(m.composer)-size]
			m.paletteIndex = 0
		}
		return m, nil
	case "pgup":
		m.scroll += max(3, m.height/2)
		return m, nil
	case "pgdown":
		m.scroll = max(0, m.scroll-max(3, m.height/2))
		return m, nil
	case "down":
		if _, slash := m.slashQuery(); slash {
			if items := m.filteredSlash(); len(items) > 0 {
				m.paletteIndex = (m.paletteIndex + 1) % len(items)
			}
			return m, nil
		}
	case "up":
		if _, slash := m.slashQuery(); slash {
			if items := m.filteredSlash(); len(items) > 0 {
				m.paletteIndex = (m.paletteIndex - 1 + len(items)) % len(items)
			}
			return m, nil
		}
	case ":":
		if strings.TrimSpace(m.composer) == "" {
			m.openPalette()
			return m, nil
		}
		m.composer += ":"
		return m, nil
	}
	if text := composerTyped(key); text != "" {
		m.composer += text
		m.paletteIndex = 0
	}
	return m, nil
}

func composerTyped(key tea.KeyMsg) string {
	if len(key.Runes) > 0 {
		return string(key.Runes)
	}
	switch key.String() {
	case "space":
		return " "
	default:
		if value := key.String(); utf8.RuneCountInString(value) == 1 && !unicode.IsControl([]rune(value)[0]) {
			return value
		}
	}
	return ""
}

func (m Model) updateNewName(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.newName == "" {
		if key.String() == "/" || strings.HasPrefix(composerTyped(key), "/") {
			m.pendingNew = false
			m.newNameFocus = false
			m.mode = modeCommand
			m.composerFocus = true
			m.composer = "/"
			m.paletteIndex = 0
			return m, nil
		}
	}
	switch key.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+o":
		return m.openHelp()
	case "esc":
		if m.newName != "" {
			m.newName = ""
			return m, nil
		}
		return m.goBack()
	case "tab":
		m.newNameFocus = false
		return m, nil
	case "enter":
		if strings.TrimSpace(m.newName) == "" {
			m.status = "Name this Capsule first"
			return m, nil
		}
		m.newNameFocus = false
		return m, nil
	case "left":
		m.cycleNewHarness(-1)
		return m, nil
	case "right":
		m.cycleNewHarness(1)
		return m, nil
	case "backspace":
		if m.newName != "" {
			_, size := utf8.DecodeLastRuneInString(m.newName)
			m.newName = m.newName[:len(m.newName)-size]
		}
		return m, nil
	}
	if text := composerTyped(key); text != "" && !strings.ContainsAny(text, "\n\r") {
		if utf8.RuneCountInString(m.newName)+utf8.RuneCountInString(text) > 128 {
			m.status = "Capsule name exceeds 128 characters"
			return m, nil
		}
		m.newName += text
	}
	return m, nil
}

func (m Model) updateOverlay(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay.kind {
	case overlayHarness:
		return m.updateHarnessOverlay(key)
	case overlayPalette:
		return m.updatePalette(key)
	case overlayHelp, overlayContent:
		if key.String() == "esc" || key.String() == "q" || key.String() == "enter" || key.String() == "ctrl+o" {
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
					index := current.optionIndex
					if index < 0 || index >= len(current.options) ||
						current.options[index] != current.value {
						index = 0
						for optionIndex, option := range current.options {
							if option == current.value {
								index = optionIndex
								break
							}
						}
					}
					delta := 1
					if key.String() == "left" {
						delta = -1
					}
					current.optionIndex = (index + delta + len(current.options)) % len(current.options)
					current.value = current.options[current.optionIndex]
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
		case "ctrl+j":
			if m.overlay.multiline && len(m.overlay.fields) > 0 {
				current := &m.overlay.fields[m.overlay.focus]
				if len(current.options) == 0 {
					current.value += "\n"
				}
			}
			return m, nil
		case "enter", "ctrl+s":
			request, err := m.formRequest()
			if err != nil {
				m.status = err.Error()
				return m, nil
			}
			m.overlay = overlay{}
			if request.Action == Action("switch-project") {
				m.projectID = request.ProjectID
				m.selectedID = ""
				m.selectedThreadID = ""
				m.place = placeProject
				m.focus = focusCapsules
				m.restoreSelection()
				return m, nil
			}
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

func (m Model) updatePalette(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.filteredPalette()
	switch key.String() {
	case "esc":
		m.overlay = overlay{}
		return m, nil
	case "enter":
		return m.applyPalette()
	case "j", "down":
		if len(items) > 0 {
			m.paletteIndex = (m.paletteIndex + 1) % len(items)
		}
	case "k", "up":
		if len(items) > 0 {
			m.paletteIndex = (m.paletteIndex - 1 + len(items)) % len(items)
		}
	case "backspace":
		if m.paletteQuery != "" {
			_, size := utf8.DecodeLastRuneInString(m.paletteQuery)
			m.paletteQuery = m.paletteQuery[:len(m.paletteQuery)-size]
			m.paletteIndex = 0
		}
	default:
		if len(key.Runes) > 0 {
			m.paletteQuery += string(key.Runes)
			m.paletteIndex = 0
		}
	}
	return m, nil
}

func (m *Model) settleStatus() {
	if m.sendInFlight || m.pendingAttachID != "" || keepStatusAfterRefresh(m.status) {
		return
	}
	m.status = ""
}

func keepStatusAfterRefresh(status string) bool {
	if status == "" {
		return false
	}
	if strings.HasPrefix(status, "Action failed") || strings.HasPrefix(status, "Attach failed") ||
		strings.HasPrefix(status, "Launch failed") {
		return true
	}
	return false
}

func (m Model) advancePendingAttach() (tea.Model, tea.Cmd, bool) {
	if m.pendingAttachID == "" {
		return m, nil, false
	}
	var detail *CapsuleDetail
	for index := range m.snapshot.Capsules {
		if m.snapshot.Capsules[index].Capsule.ID == m.pendingAttachID {
			detail = &m.snapshot.Capsules[index]
			m.selected = index
			m.selectedID = m.pendingAttachID
			break
		}
	}
	if detail == nil {
		m.status = "Provisioning the Capsule…"
		return m, nil, false
	}
	if detail.Capsule.State == client.CapsuleStateFailed {
		m.pendingAttachID = ""
		failure, _ := detail.Capsule.Failure.Get()
		if failure == "" {
			failure = "Capsule provisioning failed"
		}
		m.status = "Launch failed: " + safeInline(failure)
		return m, nil, false
	}
	if run := detail.RunningRun(); run != nil {
		m.pendingAttachID = ""
		next, cmd := m.attachRun(*detail, *run)
		return next, cmd, cmd != nil
	}
	if detail.ActiveRun() != nil {
		m.status = "Starting the native harness…"
		return m, nil, false
	}
	if latest := detail.LatestRun(); latest != nil && latest.State != client.RunStateQueued &&
		latest.State != client.RunStateStarting && latest.State != client.RunStateRunning &&
		latest.State != client.RunStateCancelling {
		m.pendingAttachID = ""
		failure, _ := latest.Failure.Get()
		if failure == "" {
			failure = "native harness exited before attachment"
		}
		m.status = "Launch failed: " + safeInline(failure)
		return m, nil, false
	}
	switch detail.Capsule.State {
	case client.CapsuleStateCreating, client.CapsuleStatePreparing:
		m.status = "Provisioning the Capsule…"
	case client.CapsuleStateReady:
		m.status = "Waiting for the native harness…"
	default:
		m.status = "Waiting for the Capsule to become Ready…"
	}
	return m, nil, false
}

func (m *Model) openCreatedSession(capsuleID, threadID string) {
	m.stayOnList = false
	m.mode = modeHistory
	m.pendingNew = false
	m.newName = ""
	m.newNameFocus = false
	m.focus = focusMain
	m.composerFocus = true
	m.scroll = 0
	if capsuleID != "" {
		m.selectedID = capsuleID
	}
	if threadID != "" {
		m.selectedThreadID = threadID
		m.place = placeThread
		return
	}
	m.selectedThreadID = ""
	m.place = placeCapsule
}

func (m *Model) goToCapsuleList() {
	m.stayOnList = true
	m.mode = modeLauncher
	m.place = placeProject
	m.focus = focusCapsules
	m.composerFocus = false
	m.pendingNew = false
	m.newName = ""
	m.newNameFocus = false
	m.newHarness = ""
	m.newHarnesses = nil
	m.selectedThreadID = ""
	m.selectedID = ""
	m.scroll = 0
}

func (m *Model) restoreSelection() {
	if m.projectID == "" && len(m.snapshot.Projects) > 0 {
		m.projectID = m.snapshot.Projects[0].ID
	}
	if m.stayOnList {
		m.selectedThreadID = ""
		m.place = placeProject
		if m.mode != modeCommand {
			m.mode = modeLauncher
		}
		if !m.composerFocus && m.composer == "" && !m.pendingNew {
			m.focus = focusCapsules
			m.composerFocus = false
		}
		if m.selectedID != "" {
			for index := range m.snapshot.Capsules {
				if m.snapshot.Capsules[index].Capsule.ID == m.selectedID && m.capsuleVisible(index) {
					m.selected = index
					m.projectID = m.snapshot.Capsules[index].Project.ID
					return
				}
			}
		}
		visible := m.visibleCapsules()
		if len(visible) == 0 {
			m.selected = 0
			m.selectedID = ""
			return
		}
		m.selected = visible[0]
		m.selectedID = m.snapshot.Capsules[m.selected].Capsule.ID
		m.projectID = m.snapshot.Capsules[m.selected].Project.ID
		return
	}
	if m.selectedThreadID != "" {
		m.mode = modeHistory
		for capsuleIndex := range m.snapshot.Capsules {
			if !m.capsuleVisible(capsuleIndex) {
				continue
			}
			for threadIndex, thread := range m.snapshot.Capsules[capsuleIndex].Threads {
				if thread.Thread.ID == m.selectedThreadID {
					m.selected = capsuleIndex
					m.selectedID = m.snapshot.Capsules[capsuleIndex].Capsule.ID
					m.projectID = m.snapshot.Capsules[capsuleIndex].Project.ID
					m.threadCursor = threadIndex
					m.markThreadRead(thread)
					if m.place == placeProject {
						m.place = placeThread
						m.focus = focusMain
					}
					return
				}
			}
		}
		if m.selectedID != "" {
			for index := range m.snapshot.Capsules {
				if m.snapshot.Capsules[index].Capsule.ID == m.selectedID && m.capsuleVisible(index) {
					m.selected = index
					m.projectID = m.snapshot.Capsules[index].Project.ID
					m.focus = focusMain
					m.composerFocus = true
					return
				}
			}
		}
		return
	}
	if m.selectedID != "" {
		for index := range m.snapshot.Capsules {
			if m.snapshot.Capsules[index].Capsule.ID == m.selectedID && m.capsuleVisible(index) {
				m.selected = index
				m.projectID = m.snapshot.Capsules[index].Project.ID
				if m.place == placeProject {
					m.showCapsuleSession()
				}
				return
			}
		}
	}
	visible := m.visibleCapsules()
	if len(visible) == 0 {
		m.selected = 0
		m.selectedID = ""
		m.place = placeProject
		m.focus = focusMain
		m.composerFocus = true
		return
	}
	m.selected = m.newestVisibleCapsule()
	m.selectedID = m.snapshot.Capsules[m.selected].Capsule.ID
	m.projectID = m.snapshot.Capsules[m.selected].Project.ID
	m.showCapsuleSession()
}

func (m *Model) move(delta int) {
	if m.mode == modeHistory && (m.place == placeThread || m.place == placeCapsule) {
		if delta > 0 {
			m.scroll = max(0, m.scroll-max(3, m.height/2))
		} else {
			m.scroll += max(3, m.height/2)
		}
		return
	}
	visible := m.visibleCapsules()
	if len(visible) == 0 {
		return
	}
	current := 0
	for index, capsuleIndex := range visible {
		if capsuleIndex == m.selected {
			current = index
			break
		}
	}
	m.selected = visible[(current+delta+len(visible))%len(visible)]
	m.selectedID = m.snapshot.Capsules[m.selected].Capsule.ID
	m.projectID = m.snapshot.Capsules[m.selected].Project.ID
	m.threadCursor = 0
	m.scroll = 0
}

func (m *Model) selectedDetail() *CapsuleDetail {
	if m.selectedID == "" || m.selected < 0 || m.selected >= len(m.snapshot.Capsules) {
		return nil
	}
	detail := &m.snapshot.Capsules[m.selected]
	if detail.Capsule.ID != m.selectedID ||
		(m.projectID != "" && detail.Project.ID != m.projectID) {
		return nil
	}
	return detail
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

func (m Model) visibleCapsules() []int {
	var output []int
	for index, detail := range m.snapshot.Capsules {
		if m.projectID != "" && detail.Project.ID != m.projectID {
			continue
		}
		if !m.showDeleted && detail.Capsule.State == client.CapsuleStateDeleted {
			continue
		}
		output = append(output, index)
	}
	return output
}

func (m Model) capsuleVisible(index int) bool {
	for _, visible := range m.visibleCapsules() {
		if visible == index {
			return true
		}
	}
	return false
}

func (m Model) visibleThreads(detail CapsuleDetail) []ThreadDetail {
	var output []ThreadDetail
	for _, thread := range detail.Threads {
		if thread.Thread.State == client.ThreadStateDeleted {
			continue
		}
		output = append(output, thread)
	}
	return output
}

func (m Model) capsuleHasStructured(detail CapsuleDetail) bool {
	for _, profile := range detail.Profiles {
		if profile.Structured {
			return true
		}
	}
	return false
}

func (m Model) projectName() string {
	for _, project := range m.snapshot.Projects {
		if project.ID == m.projectID {
			return safeInline(project.Name)
		}
	}
	if detail := m.selectedDetail(); detail != nil {
		return safeInline(detail.Project.Name)
	}
	if len(m.snapshot.Projects) == 1 {
		return safeInline(m.snapshot.Projects[0].Name)
	}
	return ""
}

func (m Model) goBack() (tea.Model, tea.Cmd) {
	if m.mode == modeCommand {
		m.mode = modeLauncher
		m.composer = ""
		m.composerFocus = false
		m.paletteIndex = 0
		return m, nil
	}
	if m.mode == modeHistory {
		m.mode = modeLauncher
		m.stayOnList = true
		m.place = placeProject
		m.selectedThreadID = ""
		m.composer = ""
		m.scroll = 0
	}
	m.pendingNew = false
	m.newName = ""
	m.newNameFocus = false
	m.newHarness = ""
	m.newHarnesses = nil
	m.composerFocus = false
	m.focus = focusCapsules
	return m, nil
}

func (m Model) enterSelection() (tea.Model, tea.Cmd) {
	detail := m.selectedDetail()
	if detail == nil {
		return m, nil
	}
	if run := detail.RunningRun(); run != nil {
		return m.attachRun(*detail, *run)
	}
	if detail.ActiveRun() != nil {
		m.pendingAttachID = detail.Capsule.ID
		m.status = "Starting the native harness…"
		return m, nil
	}
	harness, ok := detail.Capsule.Harness.Get()
	if !ok || strings.TrimSpace(harness) == "" {
		m.status = "Launch failed: this Capsule has no native launcher harness"
		return m, nil
	}
	switch detail.Capsule.State {
	case client.CapsuleStateCreating, client.CapsuleStatePreparing:
		m.pendingAttachID = detail.Capsule.ID
		m.status = "Provisioning the Capsule…"
		return m, nil
	case client.CapsuleStateReady:
		for _, profile := range detail.Profiles {
			if profile.Name != harness {
				continue
			}
			if profile.Structured || !profile.Pty {
				m.status = "Launch failed: selected harness is not a native PTY profile"
				return m, nil
			}
			m.pendingAttachID = detail.Capsule.ID
			m.status = "Starting the native harness…"
			return m.execute(ActionRequest{
				Action: ActionRunStart, CapsuleID: detail.Capsule.ID, Harness: harness,
			})
		}
		m.status = "Launch failed: selected harness profile is unavailable"
		return m, nil
	case client.CapsuleStateFailed:
		failure, _ := detail.Capsule.Failure.Get()
		if failure == "" {
			failure = "Capsule provisioning failed"
		}
		m.status = "Launch failed: " + safeInline(failure)
		return m, nil
	default:
		m.status = "Launch failed: Capsule must be Ready"
		return m, nil
	}
}

func (m *Model) showCapsuleSession() {
	m.stayOnList = false
	m.mode = modeHistory
	detail := m.selectedDetail()
	if detail == nil {
		return
	}
	m.scroll = 0
	m.threadCursor = 0
	if thread := m.primaryThread(*detail); thread != nil {
		m.selectedThreadID = thread.Thread.ID
		m.place = placeThread
		m.markThreadRead(*thread)
		return
	}
	m.selectedThreadID = ""
	m.place = placeCapsule
}

func (m *Model) openCapsuleSession() {
	m.showCapsuleSession()
	m.focus = focusMain
	m.composerFocus = true
}

func (m Model) newestVisibleCapsule() int {
	visible := m.visibleCapsules()
	if len(visible) == 0 {
		return 0
	}
	best := visible[0]
	bestTime := capsuleActivity(m.snapshot.Capsules[best])
	for _, index := range visible[1:] {
		if when := capsuleActivity(m.snapshot.Capsules[index]); when.After(bestTime) {
			best = index
			bestTime = when
		}
	}
	return best
}

func capsuleActivity(detail CapsuleDetail) time.Time {
	latest := detail.Capsule.UpdatedAt
	if detail.Capsule.CreatedAt.After(latest) {
		latest = detail.Capsule.CreatedAt
	}
	for _, thread := range detail.Threads {
		if thread.Thread.UpdatedAt.After(latest) {
			latest = thread.Thread.UpdatedAt
		}
		if thread.Thread.CreatedAt.After(latest) {
			latest = thread.Thread.CreatedAt
		}
	}
	return latest
}

func (m *Model) bindThread(thread ThreadDetail) {
	m.stayOnList = false
	m.mode = modeHistory
	m.selectedThreadID = thread.Thread.ID
	m.place = placeThread
	m.focus = focusMain
	m.composerFocus = true
	m.scroll = 0
	m.markThreadRead(thread)
}

func (m *Model) openHistoryThread(threadID string) {
	detail := m.selectedDetail()
	if detail == nil {
		m.status = "Select a Capsule first"
		return
	}
	for _, thread := range detail.Threads {
		if thread.Thread.ID == threadID && thread.Thread.State != client.ThreadStateDeleted {
			m.bindThread(thread)
			return
		}
	}
	m.status = "That conversation is gone"
}

func (m Model) liveThread(detail CapsuleDetail) *ThreadDetail {
	var paused *ThreadDetail
	for index := range detail.Threads {
		thread := &detail.Threads[index]
		switch thread.Thread.State {
		case client.ThreadStateActive:
			return thread
		case client.ThreadStatePaused:
			if paused == nil {
				paused = thread
			}
		}
	}
	return paused
}

func (m Model) primaryThread(detail CapsuleDetail) *ThreadDetail {
	if live := m.liveThread(detail); live != nil {
		return live
	}
	for index := range detail.Threads {
		if detail.Threads[index].Thread.State != client.ThreadStateDeleted {
			return &detail.Threads[index]
		}
	}
	return nil
}

func (m *Model) sessionThread() *ThreadDetail {
	if thread := m.selectedThread(); thread != nil {
		if thread.Thread.State != client.ThreadStateArchived &&
			thread.Thread.State != client.ThreadStateDeleted {
			return thread
		}
	}
	detail := m.selectedDetail()
	if detail == nil {
		return nil
	}
	if live := m.liveThread(*detail); live != nil {
		m.bindThread(*live)
		return live
	}
	return nil
}

func (m Model) sendComposer() (tea.Model, tea.Cmd) {
	if m.sendInFlight {
		return m, nil
	}
	content := strings.TrimRight(m.composer, " \t")
	if m.pendingNew || m.selectedDetail() == nil {
		return m.startFreshWorkspace(content)
	}
	thread := m.sessionThread()
	if thread == nil {
		if strings.TrimSpace(content) == "" {
			if m.pendingSend == "" {
				return m, nil
			}
			return m.startWorkspaceChat(m.pendingSend)
		}
		m.pendingSend = content
		m.composer = ""
		return m.startWorkspaceChat(content)
	}
	if thread.Thread.State == client.ThreadStateArchived || thread.Thread.State == client.ThreadStateDeleted {
		m.status = "This conversation is closed"
		return m, nil
	}
	supported, present := thread.Thread.StructuredSupported.Get()
	if present && !supported {
		m.status = "This harness is PTY-only. Use : to attach."
		return m, nil
	}
	if strings.TrimSpace(content) == "" {
		if m.pendingSend == "" {
			return m, nil
		}
		if sessionNeedsStart(thread) {
			m.status = "Starting the session…"
			return m.startOrResumeThread()
		}
		if next, cmd, ok := m.flushPendingSend(); ok {
			return next, cmd
		}
		return m, nil
	}
	m.pendingSend = content
	m.composer = ""
	if sessionNeedsStart(thread) {
		m.status = "Starting the session…"
		return m.startOrResumeThread()
	}
	m.status = "Sending…"
	return m.dispatchSend(content, thread.Thread.ResourceVersion)
}

func isResourceConflict(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "conflict")
}

func isVersionConflict(err error) bool {
	if !isResourceConflict(err) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "resource version") || strings.Contains(message, "stale")
}

func (m Model) flushPendingSend() (Model, tea.Cmd, bool) {
	if m.pendingSend == "" || m.sendInFlight {
		return m, nil, false
	}
	thread := m.selectedThread()
	if thread == nil || sessionNeedsStart(thread) || thread.Thread.State != client.ThreadStateActive {
		return m, nil, false
	}
	m.status = "Sending…"
	next, cmd := m.dispatchSend(m.pendingSend, thread.Thread.ResourceVersion)
	return next.(Model), cmd, true
}

func (m Model) dispatchSend(content string, resourceVersion int64) (tea.Model, tea.Cmd) {
	thread := m.selectedThread()
	if thread == nil {
		return m, nil
	}
	m.sendInFlight = true
	return m.execute(ActionRequest{
		Action: ActionThreadSend, ThreadID: thread.Thread.ID,
		ResourceVersion: resourceVersion, Content: content,
	})
}

func sessionNeedsStart(thread *ThreadDetail) bool {
	if thread == nil {
		return false
	}
	if thread.Thread.State == client.ThreadStatePaused {
		return true
	}
	if thread.Thread.State != client.ThreadStateActive {
		return false
	}
	state, ok := thread.Thread.CurrentRunState.Get()
	if !ok {
		return false
	}
	switch state {
	case client.RunStateFailed, client.RunStateSucceeded, client.RunStateCancelled:
		return true
	default:
		return false
	}
}

func (m Model) startOrResumeThread() (tea.Model, tea.Cmd) {
	thread := m.selectedThread()
	if thread == nil {
		m.status = "Open a conversation first"
		return m, nil
	}
	supported, known := thread.Thread.StructuredSupported.Get()
	if known && !supported {
		m.status = "This harness is PTY-only. Use : to attach."
		return m, nil
	}
	switch thread.Thread.State {
	case client.ThreadStateActive:
		if threadActiveRun(thread.Thread) {
			if m.pendingSend != "" {
				return m.dispatchSend(m.pendingSend, thread.Thread.ResourceVersion)
			}
			m.status = "This conversation is already running"
			return m, nil
		}
		return m.execute(m.threadLifecycleRequest(ActionThreadStart))
	case client.ThreadStatePaused:
		return m.execute(m.threadLifecycleRequest(ActionThreadResume))
	default:
		m.status = "This conversation cannot start right now"
		return m, nil
	}
}

func (m Model) attachSelectedRun() (tea.Model, tea.Cmd) {
	detail := m.selectedDetail()
	if detail == nil {
		m.status = "Select a Capsule first"
		return m, nil
	}
	if run := detail.RunningRun(); run != nil {
		return m.attachRun(*detail, *run)
	}
	m.status = "No active PTY Run to attach"
	return m, nil
}

func (m Model) attachRun(detail CapsuleDetail, run client.Run) (tea.Model, tea.Cmd) {
	for _, profile := range detail.Profiles {
		if profile.Name != run.Harness {
			continue
		}
		if profile.Structured || !profile.Pty {
			m.status = "Launch failed: that Run is not a native PTY session"
			return m, nil
		}
		m.status = "Handing terminal to " + safeInline(run.Harness)
		title := "Meridian · " + safeInline(detail.Capsule.Name) + " · " + safeInline(run.Harness)
		restoreTitle := "Meridian"
		if project := safeInline(detail.Project.Name); project != "" {
			restoreTitle += " · " + project
		}
		return m, m.attach(AttachRequest{
			RunID: run.ID, Title: title, RestoreTitle: restoreTitle,
		})
	}
	m.status = "Launch failed: Run profile metadata is unavailable"
	return m, nil
}

func (m *Model) openCreateProject() {
	fields := []field{
		{label: "Name"},
		{label: "Repository URL", optional: true},
	}
	if names := m.installationHarnessNames(); len(names) > 0 {
		fields = append(fields, field{label: "Harness", value: names[0], options: names})
	}
	m.overlay = overlay{
		kind: overlayForm, title: "New Project",
		note:   "A Project is the recipe: a repo and a harness. ← → picks a pack. /new starts Capsules with it. Secrets stay in the scriptable CLI.",
		action: ActionRequest{Action: ActionProjectCreate},
		fields: fields,
	}
}

func (m *Model) openSwitchProject() {
	if len(m.snapshot.Projects) == 0 {
		m.openCreateProject()
		return
	}
	currentID := m.currentProjectID()
	currentIndex := 0
	options := make([]string, 0, len(m.snapshot.Projects))
	values := make([]string, 0, len(m.snapshot.Projects))
	for index, project := range m.snapshot.Projects {
		options = append(options, safeInline(project.Name))
		values = append(values, project.ID)
		if project.ID == currentID {
			currentIndex = index
		}
	}
	m.overlay = overlay{
		kind: overlayForm, title: "Switch project",
		action: ActionRequest{Action: "switch-project"},
		fields: []field{{
			label: "Project", value: options[currentIndex], options: options,
			optionValues: values, optionIndex: currentIndex,
		}},
	}
}

func (m *Model) openCreate() {
	projectID := m.currentProjectID()
	if projectID == "" {
		m.openCreateProject()
		return
	}
	harnesses := m.projectHarnessNames()
	if len(harnesses) == 0 {
		m.status = "Apply a harness to this Project before creating a Capsule"
		return
	}
	m.overlay = overlay{
		kind: overlayForm, title: "Create Capsule", action: ActionRequest{Action: ActionCreate},
		fields: []field{
			{label: "Project ID", value: projectID},
			{label: "Capsule name"},
			{label: "Harness", value: harnesses[0], options: harnesses},
		},
	}
}

func (m Model) startWorkspaceChat(content string) (tea.Model, tea.Cmd) {
	detail := m.selectedDetail()
	if detail == nil {
		m.status = "Select a Capsule first"
		if content != "" {
			m.composer = content
			m.pendingSend = ""
		}
		return m, nil
	}
	harness := ""
	for _, profile := range detail.Profiles {
		if profile.Structured {
			harness = profile.Name
			break
		}
	}
	if harness == "" {
		m.status = "This Capsule has no structured harness. Use : to attach a native PTY if one is running."
		if content != "" {
			m.composer = content
			m.pendingSend = ""
		}
		return m, nil
	}
	if strings.TrimSpace(content) == "" {
		return m, nil
	}
	m.pendingSend = content
	m.composer = ""
	m.place = placeCapsule
	m.focus = focusMain
	m.composerFocus = true
	m.status = "Starting the session…"
	return m.execute(ActionRequest{
		Action: ActionThreadCreate, CapsuleID: detail.Capsule.ID,
		Harness: harness, Content: content, Start: true,
	})
}

func (m *Model) beginNewCapsule() {
	if m.currentProjectID() == "" {
		m.openCreateProject()
		return
	}
	m.stayOnList = true
	m.mode = modeLauncher
	m.pendingNew = true
	m.newHarnesses = m.projectHarnessNames()
	m.newHarness = ""
	if m.newHarness == "" && len(m.newHarnesses) > 0 {
		m.newHarness = m.newHarnesses[0]
	}
	m.newName = ""
	m.newNameFocus = true
	m.composer = ""
	m.composerFocus = true
	m.focus = focusMain
	m.paletteIndex = 0
	m.status = "Name this Capsule and pick a harness"
}

func (m *Model) cycleNewHarness(delta int) {
	if len(m.newHarnesses) == 0 {
		return
	}
	index := 0
	for i, name := range m.newHarnesses {
		if name == m.newHarness {
			index = i
			break
		}
	}
	m.newHarness = m.newHarnesses[(index+delta+len(m.newHarnesses))%len(m.newHarnesses)]
}

func (m Model) currentProjectID() string {
	if m.projectID != "" {
		return m.projectID
	}
	if detail := m.selectedDetail(); detail != nil {
		return detail.Project.ID
	}
	if len(m.snapshot.Projects) > 0 {
		return m.snapshot.Projects[0].ID
	}
	return ""
}

func (m Model) currentProject() *client.Project {
	id := m.currentProjectID()
	if id == "" {
		return nil
	}
	for index := range m.snapshot.Projects {
		if m.snapshot.Projects[index].ID == id {
			return &m.snapshot.Projects[index]
		}
	}
	return nil
}

func (m *Model) openApplyHarness() {
	project := m.currentProject()
	if project == nil {
		m.status = "Create a Project first"
		return
	}
	applied := map[string]bool{}
	for _, item := range project.HarnessImages {
		applied[item.Name] = true
	}
	choices := make([]harnessChoice, 0, len(m.snapshot.HarnessImages))
	for _, item := range m.snapshot.HarnessImages {
		if item.Name == "" || item.ImageReference == "" {
			continue
		}
		choices = append(choices, harnessChoice{
			Name: item.Name, ImageReference: item.ImageReference, Applied: applied[item.Name],
		})
	}
	if len(choices) == 0 {
		m.status = "This installation does not advertise harness packs"
		return
	}
	m.overlay = overlay{
		kind: overlayHarness, title: "Apply harnesses",
		note: "Space applies or removes a pack. Enter saves. /new then picks among the applied names.",
		action: ActionRequest{
			Action: ActionProjectApplyHarnesses, ProjectID: project.ID,
			ResourceVersion: project.ResourceVersion,
		},
		choices: choices,
	}
}

func (m Model) updateHarnessOverlay(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.overlay = overlay{}
		return m, nil
	case "up", "k":
		if len(m.overlay.choices) > 0 {
			m.overlay.focus = (m.overlay.focus - 1 + len(m.overlay.choices)) % len(m.overlay.choices)
		}
	case "down", "j":
		if len(m.overlay.choices) > 0 {
			m.overlay.focus = (m.overlay.focus + 1) % len(m.overlay.choices)
		}
	case " ", "space":
		if m.overlay.focus >= 0 && m.overlay.focus < len(m.overlay.choices) {
			m.overlay.choices[m.overlay.focus].Applied = !m.overlay.choices[m.overlay.focus].Applied
		}
	case "enter":
		request := m.overlay.action
		kept := map[string]bool{}
		for _, choice := range m.overlay.choices {
			kept[choice.Name] = true
			if choice.Applied {
				request.HarnessImages = append(
					request.HarnessImages, choice.Name+"="+choice.ImageReference,
				)
			}
		}
		if project := m.currentProject(); project != nil {
			for _, item := range project.HarnessImages {
				if kept[item.Name] {
					continue
				}
				request.HarnessImages = append(
					request.HarnessImages, item.Name+"="+item.ImageReference,
				)
			}
		}
		m.overlay = overlay{}
		return m.execute(request)
	}
	return m, nil
}

func (m Model) installationHarnessNames() []string {
	names := make([]string, 0, len(m.snapshot.HarnessImages))
	for _, item := range m.snapshot.HarnessImages {
		if item.Name != "" && item.ImageReference != "" {
			names = append(names, item.Name)
		}
	}
	return names
}

func (m Model) installationHarness(name string) (client.HarnessImage, bool) {
	for _, item := range m.snapshot.HarnessImages {
		if item.Name == name && item.ImageReference != "" {
			return item, true
		}
	}
	return client.HarnessImage{}, false
}

func (m Model) projectHarnessNames() []string {
	project := m.currentProject()
	if project == nil || len(project.HarnessImages) == 0 {
		return nil
	}
	names := make([]string, 0, len(project.HarnessImages))
	for _, item := range project.HarnessImages {
		if item.Name != "" {
			names = append(names, item.Name)
		}
	}
	return names
}

func (m Model) defaultStructuredHarness() string {
	allowed := m.projectHarnessNames()
	if detail := m.selectedDetail(); detail != nil {
		for _, profile := range detail.Profiles {
			if profile.Structured && (len(allowed) == 0 || containsString(allowed, profile.Name)) {
				return profile.Name
			}
		}
	}
	if len(allowed) > 0 {
		return allowed[0]
	}
	for _, detail := range m.snapshot.Capsules {
		if m.projectID != "" && detail.Project.ID != m.projectID {
			continue
		}
		for _, profile := range detail.Profiles {
			if profile.Structured {
				return profile.Name
			}
		}
	}
	return ""
}

func (m Model) knownStructuredHarnesses() []string {
	if names := m.projectHarnessNames(); len(names) > 0 {
		return names
	}
	seen := map[string]bool{}
	var names []string
	for _, detail := range m.snapshot.Capsules {
		for _, profile := range detail.Profiles {
			if !profile.Structured || seen[profile.Name] {
				continue
			}
			seen[profile.Name] = true
			names = append(names, profile.Name)
		}
	}
	return names
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (m Model) startFreshWorkspace(_ string) (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.newName)
	if name == "" {
		if !m.pendingNew {
			m.beginNewCapsule()
		}
		m.newNameFocus = true
		m.status = "Name this Capsule first"
		return m, nil
	}
	projectID := m.currentProjectID()
	if projectID == "" {
		m.openCreateProject()
		return m, nil
	}
	harness := m.newHarness
	if harness == "" {
		m.status = "Apply a harness to this Project before creating a Capsule"
		return m, nil
	}
	m.pendingNew = false
	m.newName = ""
	m.newNameFocus = false
	m.pendingSend = ""
	m.composer = ""
	m.sendInFlight = true
	m.status = "Starting a new Capsule…"
	return m.execute(ActionRequest{
		Action: ActionCreate, ProjectID: projectID, Name: name, Harness: harness,
	})
}

func (m *Model) openNewCapsuleForm(_ string) {
	harnesses := m.projectHarnessNames()
	harness := ""
	if harness == "" && len(harnesses) > 0 {
		harness = harnesses[0]
	}
	name := strings.TrimSpace(m.newName)
	m.overlay = overlay{
		kind: overlayForm, title: "New Capsule",
		note:   "Name this Capsule and choose its native harness.",
		action: ActionRequest{Action: ActionCreate, ProjectID: m.currentProjectID()},
		fields: []field{
			{label: "Capsule name", value: name},
			{label: "Harness profile", value: harness, options: harnesses},
		},
		focus: 0,
	}
	if name != "" && harness != "" {
		m.overlay.focus = 1
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
			item.label != "Message" && item.label != "First message" &&
			item.label != "First prompt" && item.label != "Input" &&
			item.label != "Repository URL" && item.label != "Image" &&
			item.label != "Harness" {
			return ActionRequest{}, fmt.Errorf("%s exceeds 128 characters", item.label)
		}
	}
	switch request.Action {
	case Action("switch-project"):
		project := m.overlay.fields[0]
		if project.optionIndex >= 0 && project.optionIndex < len(project.optionValues) {
			request.ProjectID = project.optionValues[project.optionIndex]
		}
		if request.ProjectID == "" {
			return ActionRequest{}, errors.New("selected Project is unavailable")
		}
	case ActionProjectCreate:
		request.Name = strings.TrimSpace(m.overlay.fields[0].value)
		request.RepositoryURL = strings.TrimSpace(m.overlay.fields[1].value)
		if utf8.RuneCountInString(request.RepositoryURL) > 4096 {
			return ActionRequest{}, errors.New("Repository URL exceeds 4096 characters")
		}
		if len(m.overlay.fields) > 2 {
			harness := strings.TrimSpace(m.overlay.fields[2].value)
			pack, ok := m.installationHarness(harness)
			if !ok {
				return ActionRequest{}, fmt.Errorf("unknown harness %q", harness)
			}
			request.Image = pack.ImageReference
			request.HarnessImages = []string{pack.Name + "=" + pack.ImageReference}
		}
	case ActionCreate:
		if request.ProjectID != "" {
			request.Name = strings.TrimSpace(m.overlay.fields[0].value)
			request.Harness = strings.TrimSpace(m.overlay.fields[1].value)
		} else {
			request.ProjectID = strings.TrimSpace(m.overlay.fields[0].value)
			request.Name = strings.TrimSpace(m.overlay.fields[1].value)
			if len(m.overlay.fields) > 2 {
				request.Harness = strings.TrimSpace(m.overlay.fields[2].value)
			}
		}
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
	case ActionProjectThreadSpawn:
		if request.ProjectID == "" {
			request.ProjectID = m.currentProjectID()
		}
		switch len(m.overlay.fields) {
		case 3:
			request.Name = strings.TrimSpace(m.overlay.fields[0].value)
			request.Harness = strings.TrimSpace(m.overlay.fields[1].value)
			request.Content = m.overlay.fields[2].value
		default:
			request.ProjectID = strings.TrimSpace(m.overlay.fields[0].value)
			request.Name = strings.TrimSpace(m.overlay.fields[1].value)
			request.Harness = strings.TrimSpace(m.overlay.fields[2].value)
			request.Content = m.overlay.fields[3].value
		}
		if request.Name == "" {
			return ActionRequest{}, errors.New("Capsule name is required")
		}
		if request.Content == "" {
			return ActionRequest{}, errors.New("First prompt is required")
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
	var toolName, toolSummary string
	flushTool := func(done bool) {
		if toolName == "" && toolSummary == "" {
			return
		}
		label := toolName
		if label == "" {
			label = toolSummary
			toolSummary = ""
		}
		line := "  Used " + safeInline(label)
		if !done {
			line = "  Using " + safeInline(label) + "…"
		} else if toolSummary != "" && toolSummary != toolName && !trivialToolResult(toolSummary) {
			line += "  " + safeInline(toolSummary)
		}
		lines = append(lines, transcriptTool+line)
		toolName, toolSummary = "", ""
	}
	appendTurn := func(kind, content string) {
		flushTool(true)
		content = strings.TrimRight(content, "\n")
		if content == "" {
			return
		}
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		for _, line := range strings.Split(content, "\n") {
			lines = append(lines, kind+line)
		}
	}
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
			appendTurn(transcriptAssistant, safeBlock(content))
			continue
		}
		if hasEvent {
			switch event.Type {
			case client.ThreadAdapterEventTypeAssistantMessage:
				appendTurn(transcriptAssistant, safeBlock(content))
			case client.ThreadAdapterEventTypeToolStart:
				flushTool(true)
				toolName, _ = event.ToolName.Get()
				toolSummary, _ = event.Summary.Get()
			case client.ThreadAdapterEventTypeToolResult:
				if result, ok := event.Result.Get(); ok && !trivialToolResult(result) && toolSummary == "" {
					toolSummary = boundedText(result, 120)
				}
				flushTool(true)
			case client.ThreadAdapterEventTypeStatus:
				continue
			case client.ThreadAdapterEventTypeError:
				code, _ := event.Code.Get()
				reason, _ := event.Reason.Get()
				appendTurn(transcriptError, "error · "+safeInline(code)+": "+safeBlock(boundedText(reason, 4096)))
			case client.ThreadAdapterEventTypePermissionRequest:
				permission, _ := event.Permission.Get()
				summary, _ := permission.Summary.Get()
				appendTurn(transcriptError, "Need permission · "+safeInline(permission.Kind)+": "+
					safeBlock(boundedText(summary, 4096)))
			case client.ThreadAdapterEventTypeInputRequest:
				input, _ := event.Input.Get()
				prompt, _ := input.Prompt.Get()
				appendTurn(transcriptError, "Needs input: "+safeBlock(boundedText(prompt, 4096)))
			case client.ThreadAdapterEventTypePermissionResponse, client.ThreadAdapterEventTypeInputResponse,
				client.ThreadAdapterEventTypeGap, client.ThreadAdapterEventTypeEnd:
				continue
			default:
				appendTurn(transcriptError, "unknown event "+safeInline(string(event.Type))+": "+safeBlock(content))
			}
			continue
		}
		if content != "" {
			if !knownThreadBlockKind(block.Kind) {
				appendTurn(transcriptError, "unknown block · "+safeInline(string(block.Kind))+": "+safeBlock(content))
			} else if block.Role == client.ThreadMessageRoleUser {
				appendTurn(transcriptUser, safeBlock(content))
			} else {
				appendTurn(transcriptAssistant, safeBlock(content))
			}
		} else if !knownThreadBlockKind(block.Kind) {
			appendTurn(transcriptError, "unknown block · "+safeInline(string(block.Kind)))
		}
	}
	flushTool(false)
	return lines
}

const (
	transcriptUser      = "you|"
	transcriptAssistant = "ai|"
	transcriptTool      = "tool|"
	transcriptError     = "err|"
)

func trivialToolResult(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ok", "done", "success", "succeeded", "completed":
		return true
	default:
		return false
	}
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
		return actionMsg{result: result, err: err, action: request.Action}
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

func (m Model) blinkCmd() tea.Cmd {
	return tea.Tick(blinkCadence, func(value time.Time) tea.Msg { return blinkMsg(value) })
}

func (m Model) attachProcess(request AttachRequest) tea.Cmd {
	arguments := []string{
		"--server", m.server, "--token-file", m.tokenFile,
		"run", "attach", request.RunID,
		"--after", strconv.FormatUint(request.After, 10),
		"--title", request.Title,
		"--restore-title", request.RestoreTitle,
	}
	command := exec.CommandContext(m.ctx, m.executable, arguments...)
	return tea.ExecProcess(command, func(err error) tea.Msg { return attachFinishedMsg{err: err} })
}

func mutable(state string) bool {
	return state != "Deleted" && state != "Deleting" && state != "Sealed"
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

const footer = "ctrl-o help"
const composerHint = "Enter send  Ctrl-J nl  / cmds"

const helpText = `Launcher
  j/k or arrows  move in the focused list
  enter          open the selected Capsule's native harness
  /              open the temporary command surface
  :              actions, structured history, and maintenance
  esc            close a command, modal, or history view
  r              refresh now
  /project       create a Project (repo + harness)
  /switch        switch the active Project
  /harness       apply official harness packs on this Project
  Ctrl-O         this help
  ?              this help
  q / Ctrl-C     quit

Structured history
  Select a history entry from :. It opens outside the launcher.
  PageUp/PageDown scroll the bounded transcript.
  Ctrl-J inserts a newline while writing structured input.
  Esc returns to the same Capsule in the launcher.

/project creates a Project and picks its first harness pack. /harness applies
more packs. /new creates a Capsule and opens the selected harness when Ready.
Only one harness session can be live in a Capsule.

Operator actions and encrypted structured Thread history remain behind :.
Delete and Seal require confirmation. Rewind creates a new Timeline and does
not destroy history.

Native session
Meridian identifies the handoff in the terminal title when supported. The
native harness then owns the terminal. Press Ctrl-P then Ctrl-Q to detach
(Ctrl-\ and Ctrl-] are alternatives). PTY bytes are never treated as Thread
transcripts.`
