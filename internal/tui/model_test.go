package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func threadTestSnapshot() Snapshot {
	snapshot := testSnapshot()
	snapshot.Projects[0].HarnessImages = []client.HarnessImage{{
		Name: "native", ImageReference: "meridian-capsule-native:dev",
	}}
	snapshot.Capsules[0].Project = snapshot.Projects[0]
	snapshot.Capsules[0].Capsule.Harness = client.NewOptString("native")
	snapshot.Capsules[0].Profiles = []client.HarnessProfile{
		{
			Name: "structured", Structured: true,
			Protocol: client.NewOptHarnessProfileProtocol(
				client.HarnessProfileProtocolMeridianAdapterV1,
			),
		},
		{Name: "native", Pty: true},
	}
	snapshot.Capsules[0].Threads = []ThreadDetail{{
		Thread: client.Thread{
			ID: "thread-1", CapsuleId: "capsule-1", State: client.ThreadStateActive,
			Harness: "structured", Protocol: client.NewOptThreadProtocol(
				client.ThreadProtocolMeridianAdapterV1,
			),
			StructuredSupported: client.NewOptBool(true),
			EncryptedAtRest:     true, MessageCount: 3, CreatedAt: fixedNow().Add(-time.Hour),
			UpdatedAt: fixedNow().Add(-time.Minute), ResourceVersion: 4,
		},
		Cursor: 3,
		Blocks: []client.ThreadBlock{
			threadBlock("delta", "assistant-1", 1, "partial\x1b[31m", client.ThreadAdapterEventTypeAssistantDelta),
			threadBlock("final", "assistant-1", 2, "authoritative", client.ThreadAdapterEventTypeAssistantMessage),
			threadBlock("permission", "permission-1", 3, "", client.ThreadAdapterEventTypePermissionRequest),
		},
	}}
	permission := client.ThreadPermission{
		Kind: "filesystem", Summary: client.NewOptString("Allow write?"),
		Options: []string{"allow", "deny"},
	}
	event, _ := snapshot.Capsules[0].Threads[0].Blocks[2].Event.Get()
	event.MessageId = client.NewOptString("adapter-permission-1")
	event.Permission = client.NewOptThreadPermission(permission)
	snapshot.Capsules[0].Threads[0].Blocks[2].Event = client.NewOptThreadAdapterEvent(event)
	return snapshot
}

func threadBlock(
	id, messageID string,
	messageSequence int64,
	content string,
	eventType client.ThreadAdapterEventType,
) client.ThreadBlock {
	return client.ThreadBlock{
		ID: id, MessageId: messageID, MessageSequence: messageSequence, Sequence: 1,
		Role: client.ThreadMessageRoleAssistant, MessageKind: client.ThreadMessageKindResponse,
		Kind: client.ThreadBlockKindText, Content: client.NewOptString(content),
		Event: client.NewOptThreadAdapterEvent(client.ThreadAdapterEvent{
			Type: eventType, MessageId: client.NewOptString(messageID),
		}),
		CreatedAt: fixedNow(),
	}
}

type fakeAPI struct {
	mu        sync.Mutex
	snapshot  Snapshot
	loadErr   error
	actionErr error
	requests  []ActionRequest
}

func (a *fakeAPI) Snapshot(context.Context) (Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshot, a.loadErr
}
func (a *fakeAPI) Execute(_ context.Context, request ActionRequest) (ActionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, request)
	return ActionResult{Message: "done"}, a.actionErr
}
func (a *fakeAPI) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func TestModelLoadingNavigationResizeAndSelection(t *testing.T) {
	api := &fakeAPI{snapshot: testSnapshot()}
	model := NewModel(Options{API: api, Now: fixedNow})
	if !strings.Contains(model.View(), "Loading") {
		t.Fatalf("initial view = %q", model.View())
	}
	loaded := runCmd(t, model.Init())
	updated, _ := model.Update(loaded)
	model = enterSelectedProject(t, updated.(Model))
	if view := model.View(); !strings.Contains(view, "alpha") ||
		!strings.Contains(view, "connected") ||
		!strings.Contains(view, "/ commands") {
		t.Fatalf("loaded view = %q", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if model.selected != 1 || model.selectedID != "capsule-2" {
		t.Fatalf("selection = %d %q", model.selected, model.selectedID)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	wide := updated.(Model).View()
	updated, _ = updated.(Model).Update(tea.WindowSizeMsg{Width: 70, Height: 40})
	narrow := updated.(Model).View()
	if wide == narrow || !strings.Contains(wide, "alpha") || !strings.Contains(narrow, "alpha") {
		t.Fatal("responsive views lost the Capsule list")
	}

	refresh := testSnapshot()
	refresh.Capsules[0], refresh.Capsules[1] = refresh.Capsules[1], refresh.Capsules[0]
	updated, _ = updated.(Model).Update(loadMsg{snapshot: refresh})
	model = updated.(Model)
	if model.selectedID != "capsule-2" || model.selected != 0 {
		t.Fatalf("selection was not preserved: %d %q", model.selected, model.selectedID)
	}
}

func TestModelEmptyDisconnectedAndReconnecting(t *testing.T) {
	api := &fakeAPI{snapshot: Snapshot{Projects: []client.Project{{ID: "project-1"}}}}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(runCmd(t, model.Init()))
	model = enterSelectedProject(t, updated.(Model))
	if !strings.Contains(model.View(), "Capsules") || !strings.Contains(model.View(), "/new") {
		t.Fatalf("empty view = %q", model.View())
	}
	if model.focus != focusCapsules || model.composerFocus || model.composing() {
		t.Fatalf("empty Project should not focus command entry: focus=%d composer=%q", model.focus, model.composer)
	}
	api.loadErr = errors.New("connection refused")
	updated, _ = model.Update(loadMsg{err: api.loadErr})
	model = updated.(Model)
	if !model.reconnect || !strings.Contains(model.View(), "reconnecting") {
		t.Fatalf("reconnect view = %q", model.View())
	}

	fresh := NewModel(Options{API: api})
	updated, _ = fresh.Update(loadMsg{err: api.loadErr})
	fresh = updated.(Model)
	if !strings.Contains(fresh.View(), "Disconnected") ||
		!strings.Contains(fresh.View(), "Reconnecting automatically") {
		t.Fatalf("disconnected view = %q", fresh.View())
	}
}

func TestEmptyProjectShowsAppliedHarnessesOnly(t *testing.T) {
	project := client.Project{
		ID: "project-1", Name: "opencode test",
		HarnessImages: []client.HarnessImage{{
			Name: "opencode", ImageReference: "meridian-capsule-opencode:dev",
		}},
	}
	snapshot := Snapshot{
		Projects: []client.Project{project},
		HarnessImages: []client.HarnessImage{
			{Name: "mock", ImageReference: "meridian-capsule:dev"},
			{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
		},
	}
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	view := enterSelectedProject(t, updated.(Model)).View()
	if !strings.Contains(view, "Capsules") || !strings.Contains(view, "opencode") {
		t.Fatalf("empty Project view = %q", view)
	}
	if strings.Contains(view, "New Capsule") || strings.Contains(view, "mock") ||
		strings.Contains(view, "not applied") || strings.Contains(view, "applied") {
		t.Fatalf("empty Project view includes catalog state: %q", view)
	}
}

func TestModelActionsConfirmationConflictAndAttachHandoff(t *testing.T) {
	api := &fakeAPI{snapshot: testSnapshot()}
	var attached string
	model := NewModel(Options{
		API: api,
		Attach: func(request AttachRequest) tea.Cmd {
			attached = request.RunID
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: api.snapshot})
	model = enterSelectedProject(t, updated.(Model))

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = updated.(Model)
	if attached != "run-active" || cmd == nil {
		t.Fatalf("attach handoff = %q, cmd=%v", attached, cmd)
	}
	updated, _ = model.Update(runCmd(t, cmd))
	model = updated.(Model)
	if !strings.Contains(model.status, "Detached") {
		t.Fatalf("attach status = %q", model.status)
	}
	model.snapshot.Capsules[0].Runs[0].State = client.RunStateSucceeded

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	model = updated.(Model)
	if model.overlay.kind != overlayConfirm || api.requestCount() != 0 {
		t.Fatal("Seal did not require confirmation")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model = updated.(Model)
	if api.requestCount() != 0 {
		t.Fatal("cancelled Seal executed")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
	model = updated.(Model)
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("confirmed Seal returned no command")
	}
	updated, _ = model.Update(runCmd(t, cmd))
	model = updated.(Model)
	if api.requestCount() != 1 {
		t.Fatalf("Seal requests = %d", api.requestCount())
	}

	api.actionErr = errors.New("conflict: stale resource version")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	model = updated.(Model)
	updated, _ = model.Update(runCmd(t, cmd))
	model = updated.(Model)
	if !strings.Contains(model.status, "stale resource version") {
		t.Fatalf("conflict status = %q", model.status)
	}
}

func TestModelFormsValidationRewindWarningAndHelp(t *testing.T) {
	api := &fakeAPI{snapshot: testSnapshot()}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: api.snapshot})
	model = enterSelectedProject(t, updated.(Model))
	model.snapshot.Capsules[0].Runs[0].State = client.RunStateSucceeded

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !strings.Contains(model.status, "required") || model.overlay.kind != overlayForm {
		t.Fatalf("form validation state = %q / %v", model.status, model.overlay.kind)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	model = updated.(Model)
	if !strings.Contains(model.View(), "new Timeline") || !strings.Contains(model.View(), "not destroy history") {
		t.Fatalf("rewind overlay = %q", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	model = updated.(Model)
	if !strings.Contains(model.View(), "Ctrl-]") {
		t.Fatalf("help view = %q", model.View())
	}
}

func TestModelShowsCapsuleFailure(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Capsules[0].Capsule.State = client.CapsuleStateFailed
	snapshot.Capsules[0].Capsule.Failure = client.NewOptString("permission denied while trying to connect to the docker API")
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if view := model.View(); !strings.Contains(view, "Failure:") ||
		!strings.Contains(view, "permission denied") {
		t.Fatalf("failed capsule view = %q", view)
	}
}

func TestActionAvailability(t *testing.T) {
	model := NewModel(Options{API: &fakeAPI{}})
	snapshot := testSnapshot()
	model.snapshot = snapshot
	ready := model.actions(snapshot.Capsules[0])
	paused := model.actions(snapshot.Capsules[1])
	if !strings.Contains(ready, "p pause") || strings.Contains(ready, "u resume") ||
		!strings.Contains(ready, "a attach") || strings.Contains(ready, "S seal") ||
		!strings.Contains(paused, "u resume") || strings.Contains(paused, "p pause") ||
		strings.Contains(paused, "g diff") {
		t.Fatalf("ready=%q paused=%q", ready, paused)
	}
}

func TestThreadFleetRenderingNavigationAndSafety(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	model = openFirstConversation(t, model)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	view := model.View()
	if model.selectedThreadID != "thread-1" ||
		!strings.Contains(view, "authoritative") ||
		strings.Contains(view, "partial") ||
		strings.Contains(view, "\x1b") ||
		!strings.Contains(view, "Need permission") {
		t.Fatalf("Thread view = %q", view)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(updated.(Model).View(), "alpha") {
		t.Fatalf("narrow fleet pane = %q", updated.(Model).View())
	}
}

func TestThreadCreateSendPermissionAndLifecycleActions(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	model = updated.(Model)
	if model.overlay.action.Action != ActionThreadCreate {
		t.Fatal("Thread create did not open")
	}
	model.overlay.fields[1].value = "first\nmessage"
	request, err := model.formRequest()
	if err != nil || !request.Start || request.Harness != "structured" ||
		request.Content != "first\nmessage" {
		t.Fatalf("create request = %#v, %v", request, err)
	}

	model.overlay = overlay{}
	model = openFirstConversation(t, model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("next")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("message")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionThreadSend ||
		got.Content != "next\nmessage" || got.ResourceVersion != 4 {
		t.Fatalf("send request = %#v", got)
	}

	model = focusCapsuleList(t, model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	model = updated.(Model)
	if model.overlay.action.ResponseTo != "adapter-permission-1" {
		t.Fatalf("permission response target = %q", model.overlay.action.ResponseTo)
	}
	request, err = model.formRequest()
	if err != nil || request.Choice != "allow" {
		t.Fatalf("permission request = %#v, %v", request, err)
	}

	model.overlay = overlay{}
	model = focusCapsuleList(t, model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	model = updated.(Model)
	if model.overlay.kind != overlayConfirm || model.overlay.action.Action != ActionThreadArchive {
		t.Fatal("archive did not require confirmation")
	}
	model.overlay = overlay{}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	model = updated.(Model)
	if _, err := model.formRequest(); err == nil {
		t.Fatal("crypto-shred accepted without strong confirmation")
	}
	model.overlay.fields[0].value = "crypto-shred"
	request, err = model.formRequest()
	if err != nil || request.Action != ActionThreadDelete {
		t.Fatalf("delete request = %#v, %v", request, err)
	}
}

func TestThreadUnreadTrackingAndSecretInputMasking(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)

	refresh := threadTestSnapshot()
	refresh.Capsules[0].Threads[0].Cursor = 5
	refresh.Capsules[0].Threads[0].Thread.MessageCount = 5
	updated, _ = model.Update(loadMsg{snapshot: refresh})
	model = updated.(Model)
	if model.unread["thread-1"] != 2 || !strings.Contains(model.listView(), "2 new") {
		t.Fatalf("unread state = %d / %q", model.unread["thread-1"], model.listView())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model = openFirstConversation(t, updated.(Model))
	if model.unread["thread-1"] != 0 {
		t.Fatalf("selected Thread unread = %d", model.unread["thread-1"])
	}

	inputEvent := client.ThreadAdapterEvent{
		Type:      client.ThreadAdapterEventTypeInputRequest,
		MessageId: client.NewOptString("input-1"),
		Input: client.NewOptThreadInput(client.ThreadInput{
			Prompt: client.NewOptString("Secret token"),
			Secret: client.NewOptBool(true),
		}),
	}
	model.snapshot.Capsules[0].Threads[0].Blocks = []client.ThreadBlock{{
		ID: "input", MessageId: "input-1", MessageSequence: 6, Sequence: 1,
		Role: client.ThreadMessageRoleSystem, MessageKind: client.ThreadMessageKindStatus,
		Kind: client.ThreadBlockKindJSON, Event: client.NewOptThreadAdapterEvent(inputEvent),
		CreatedAt: fixedNow(),
	}}
	model = focusCapsuleList(t, model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	model = updated.(Model)
	if !model.overlay.fields[0].secret {
		t.Fatal("secret input was not marked for masking")
	}
	model.overlay.fields[0].value = "never-display"
	view := model.View()
	if strings.Contains(view, "never-display") || !strings.Contains(view, "••••") {
		t.Fatalf("secret overlay = %q", view)
	}
	request, err := model.formRequest()
	if err != nil || request.Input != "never-display" {
		t.Fatalf("secret response request = %#v, %v", request, err)
	}
}

func TestThreadPTYHandoffRejectsStructuredRun(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Capsules[0].Runs[0].Harness = "native"
	api := &fakeAPI{snapshot: snapshot}
	var attached string
	model := NewModel(Options{
		API: api,
		Attach: func(request AttachRequest) tea.Cmd {
			attached = request.RunID
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = updated.(Model)
	if attached != "run-active" || cmd == nil {
		t.Fatalf("native PTY handoff = %q / %v", attached, cmd)
	}

	model.snapshot.Capsules[0].Runs[0].Harness = "structured"
	attached = ""
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = updated.(Model)
	if attached != "" || cmd != nil ||
		!strings.Contains(model.status, "native PTY") {
		t.Fatalf("structured Run attach = %q / %v / %q", attached, cmd, model.status)
	}
}

func TestTranscriptRendersAsChatTurns(t *testing.T) {
	blocks := []client.ThreadBlock{
		threadBlock("idle", "status-1", 1, "", client.ThreadAdapterEventTypeStatus),
		{
			ID: "user-1", MessageId: "user-1", MessageSequence: 2, Sequence: 1,
			Role: client.ThreadMessageRoleUser, MessageKind: client.ThreadMessageKindPrompt,
			Kind: client.ThreadBlockKindText, Content: client.NewOptString("hello"),
			CreatedAt: fixedNow(),
		},
		threadBlock("run", "status-2", 3, "", client.ThreadAdapterEventTypeStatus),
		func() client.ThreadBlock {
			block := threadBlock("tool", "tool-1", 4, "", client.ThreadAdapterEventTypeToolStart)
			event, _ := block.Event.Get()
			event.ToolName = client.NewOptString("mock deterministic tool")
			block.Event = client.NewOptThreadAdapterEvent(event)
			return block
		}(),
		func() client.ThreadBlock {
			block := threadBlock("result", "tool-1", 5, "", client.ThreadAdapterEventTypeToolResult)
			event, _ := block.Event.Get()
			event.Result = client.NewOptString("ok")
			block.Event = client.NewOptThreadAdapterEvent(event)
			return block
		}(),
		threadBlock("reply", "assistant-1", 6, "mock:hello", client.ThreadAdapterEventTypeAssistantMessage),
		threadBlock("idle-2", "status-3", 7, "", client.ThreadAdapterEventTypeStatus),
	}
	event, _ := blocks[0].Event.Get()
	event.Status = client.NewOptThreadAdapterEventStatus(client.ThreadAdapterEventStatusIdle)
	blocks[0].Event = client.NewOptThreadAdapterEvent(event)
	got := strings.Join(renderThreadBlocks(blocks), "\n")
	if strings.Contains(got, "idle") || strings.Contains(got, "running") ||
		strings.Contains(got, transcriptTool+"  ·") || strings.Contains(got, "you|›") {
		t.Fatalf("transcript still looks like a run log: %q", got)
	}
	if !strings.Contains(got, transcriptUser+"hello") ||
		!strings.Contains(got, "Used mock deterministic tool") ||
		!strings.Contains(got, transcriptAssistant+"mock:hello") {
		t.Fatalf("chat turns = %q", got)
	}
}

func TestThreadLockedGapUnknownAndBoundedTranscript(t *testing.T) {
	snapshot := threadTestSnapshot()
	thread := &snapshot.Capsules[0].Threads[0]
	thread.TranscriptCode = "transcript_locked"
	thread.Gap = &client.ThreadAdapterEvent{
		Type:           client.ThreadAdapterEventTypeGap,
		RequestedAfter: client.NewOptInt64(2), AvailableFrom: client.NewOptInt64(5),
	}
	thread.Blocks = append(thread.Blocks, client.ThreadBlock{
		ID: "unknown", MessageId: "unknown", MessageSequence: 4, Sequence: 1,
		Role: "future-role", MessageKind: "future-kind", Kind: "future-kind",
		Content:   client.NewOptString("future\x9b31m"),
		CreatedAt: fixedNow(),
	})
	tool := threadBlock("tool", "tool-1", 5, "", client.ThreadAdapterEventTypeToolStart)
	toolEvent, _ := tool.Event.Get()
	toolEvent.ToolName = client.NewOptString("shell")
	toolEvent.Summary = client.NewOptString("bounded summary")
	tool.Event = client.NewOptThreadAdapterEvent(toolEvent)
	special := renderThreadBlocks(append(thread.Blocks, tool))
	specialView := strings.Join(special, "\n")
	if !strings.Contains(specialView, "unknown block") ||
		!strings.Contains(specialView, "Using shell") ||
		strings.Contains(specialView, "\x9b") {
		t.Fatalf("special block rendering = %q", specialView)
	}
	for index := 0; index < 500; index++ {
		thread.Blocks = append(thread.Blocks, threadBlock(
			fmt.Sprintf("block-%d", index), fmt.Sprintf("message-%d", index),
			int64(index+5), "bounded", client.ThreadAdapterEventTypeStatus,
		))
	}
	rendered := renderThreadBlocks(thread.Blocks)
	if len(rendered) > 200 {
		t.Fatalf("rendered %d blocks", len(rendered))
	}
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	model.snapshot = snapshot
	model.selectedThreadID = "thread-1"
	view := model.threadView(snapshot.Capsules[0], *thread, 16)
	if !strings.Contains(view, "TRANSCRIPT LOCKED") ||
		!strings.Contains(view, "Replay gap") ||
		strings.Contains(view, "\x9b") {
		t.Fatalf("locked/gap view = %q", view)
	}
}

func TestRunRejectsNonTTYWithoutEscapes(t *testing.T) {
	var output bytes.Buffer
	if err := Run(context.Background(), &fakeAPI{}, "", "", nil, &output); err == nil {
		t.Fatal("non-TTY dashboard unexpectedly started")
	}
	if output.Len() != 0 {
		t.Fatalf("non-TTY output = %q", output.String())
	}
}

func TestHiddenDeletedCapsulesAndPalette(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Capsules[1].Capsule.State = client.CapsuleStateDeleted
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	if strings.Contains(model.View(), "beta") {
		t.Fatalf("deleted Capsule shown by default: %q", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model = updated.(Model)
	if model.overlay.kind != overlayPalette {
		t.Fatal("colon did not open the command palette")
	}
	model.paletteQuery = "show deleted"
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !model.showDeleted || !strings.Contains(model.View(), "beta") {
		t.Fatalf("show deleted = %v / %q", model.showDeleted, model.View())
	}
}

func TestDeleteReturnsToCapsuleList(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	updated, _ = model.Update(actionMsg{
		action: ActionDelete,
		result: ActionResult{Message: "delete requested"},
	})
	model = updated.(Model)
	if !model.stayOnList || model.place != placeProject || model.focus != focusCapsules {
		t.Fatalf("delete should return to the list: stay=%v place=%d focus=%d", model.stayOnList, model.place, model.focus)
	}

	remaining := testSnapshot()
	updated, _ = model.Update(loadMsg{snapshot: remaining})
	model = updated.(Model)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 70, Height: 40})
	model = updated.(Model)
	if model.place != placeProject || model.focus != focusCapsules {
		t.Fatalf("refresh reopened a chat: place=%d focus=%d", model.place, model.focus)
	}
	if view := model.View(); strings.Contains(view, "authoritative") || strings.Contains(view, "Need permission") {
		t.Fatalf("delete left the conversation open: %q", view)
	}
	if !strings.Contains(model.View(), "alpha") {
		t.Fatalf("list missing after delete: %q", model.View())
	}
}

func TestWideLayoutPutsFleetBesideInspectorWithoutIdleComposer(t *testing.T) {
	snapshot := testSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	view := updated.(Model).View()
	fleet := strings.Index(view, "FLEET")
	inspector := strings.Index(view, "CAPSULE")
	if fleet < 0 || inspector < 0 || fleet > inspector || strings.Contains(view, "Type / for commands") {
		t.Fatalf("wide layout fleet=%d inspector=%d view=%q", fleet, inspector, view)
	}
}

func TestLauncherResponsiveContractsAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))

	for _, size := range []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 100, Height: 30},
		{Width: 140, Height: 40},
	} {
		updated, _ = model.Update(size)
		model = updated.(Model)
		view := model.View()
		if !strings.Contains(view, "MERIDIAN") || !strings.Contains(view, "CAPSULES") ||
			!strings.Contains(view, "native") || !strings.Contains(view, "RUNNING") {
			t.Fatalf("%dx%d launcher contract = %q", size.Width, size.Height, view)
		}
		if strings.Contains(view, "\x1b[") || strings.Contains(view, "authoritative") ||
			strings.Contains(view, "Type / for commands") {
			t.Fatalf("%dx%d launcher leaked color/history/composer = %q", size.Width, size.Height, view)
		}
		if size.Width >= 100 && (!strings.Contains(view, "FLEET") ||
			!strings.Contains(view, "PRIMARY ACTION")) {
			t.Fatalf("%dx%d wide inspector missing = %q", size.Width, size.Height, view)
		}
	}
}

func TestLauncherBoundsLongNamesAndBorderLabels(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	snapshot := testSnapshot()
	snapshot.Capsules[0].Capsule.Name = strings.Repeat("capsule-", 30)
	snapshot.Capsules[0].Capsule.Harness = client.NewOptString(strings.Repeat("harness-", 20))
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))

	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 100, Height: 30}} {
		updated, _ = model.Update(size)
		model = updated.(Model)
		for lineNumber, line := range strings.Split(model.View(), "\n") {
			if got := lipgloss.Width(line); got > size.Width {
				t.Fatalf("%dx%d line %d width=%d: %q", size.Width, size.Height, lineNumber, got, line)
			}
		}
	}
	component := panel(currentTheme(), strings.Repeat("VERY LONG LABEL ", 20), "body", 40, 6, true)
	for lineNumber, line := range strings.Split(component, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("component line %d width=%d: %q", lineNumber, got, line)
		}
	}
}

func TestCommandSurfaceKeepsLauncherAnchor(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	model = updated.(Model)
	before := model.View()
	beforeRow := strings.Index(before, "alpha")

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	after := model.View()
	if model.mode != modeCommand || beforeRow < 0 || strings.Index(after, "alpha") != beforeRow ||
		!strings.Contains(after, "/new") || !strings.Contains(after, "COMMAND") {
		t.Fatalf("command surface shifted launcher: before=%q after=%q", before, after)
	}
}

func TestLoadOpensProjectHome(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	if model.place != placeHome || model.projectID != "" || model.selectedThreadID != "" {
		t.Fatalf("load should stay on Projects: place=%d project=%q thread=%q",
			model.place, model.projectID, model.selectedThreadID)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	view := model.View()
	if !strings.Contains(view, "│●│ MERIDIAN") || !strings.Contains(view, "Choose a Project") ||
		!strings.Contains(view, "project") || !strings.Contains(view, "2 Capsules") ||
		!strings.Contains(view, "·   │   ·") || strings.Contains(view, "alpha") {
		t.Fatalf("project home view = %q", view)
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.place != placeProject || model.projectID != "project-1" {
		t.Fatalf("enter should open the Project: place=%d project=%q", model.place, model.projectID)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	if view := model.View(); !strings.Contains(view, "Enter open harness") ||
		!strings.Contains(view, "alpha") {
		t.Fatalf("opened Project view = %q", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.place != placeHome || model.projectID != "" || !strings.Contains(model.View(), "Choose a Project") {
		t.Fatalf("Esc should return to Projects: place=%d project=%q view=%q",
			model.place, model.projectID, model.View())
	}
}

func TestLoadFocusesCapsuleLauncher(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	if model.place != placeProject || model.selectedThreadID != "" {
		t.Fatalf("load should stay on the launcher: place=%d thread=%q", model.place, model.selectedThreadID)
	}
	if model.focus != focusCapsules || model.composerFocus {
		t.Fatal("Capsule list should stay focused after load")
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	if view := model.View(); !strings.Contains(view, "Enter open harness") {
		t.Fatalf("loaded launcher view = %q", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	if model.mode != modeCommand || !model.composing() || model.composer != "/" ||
		model.focus != focusCapsules {
		t.Fatalf("slash did not open command entry: mode=%d composer=%q focus=%d", model.mode, model.composer, model.focus)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.mode != modeLauncher || model.composing() || model.composer != "" ||
		model.focus != focusCapsules {
		t.Fatalf("Esc did not return to Capsules: mode=%d composer=%q focus=%d", model.mode, model.composer, model.focus)
	}
}

func TestEscReturnsHistoryToLauncher(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.mode != modeLauncher || model.place != placeProject || model.focus != focusCapsules {
		t.Fatalf("Esc should return to launcher: mode=%d place=%d focus=%d", model.mode, model.place, model.focus)
	}
	if view := model.View(); strings.Contains(view, "authoritative") || !strings.Contains(view, "alpha") {
		t.Fatalf("history remained visible after Esc: %q", view)
	}
}

func TestEmptyProjectRequiresAppliedHarnessForNewCapsule(t *testing.T) {
	snapshot := Snapshot{Projects: []client.Project{{ID: "project-1", Name: "project"}}}
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/new")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayNone || !model.pendingNew {
		t.Fatalf("/new should enter launcher form: kind=%v pending=%v", model.overlay.kind, model.pendingNew)
	}
	if !strings.Contains(model.View(), "No harness pack is applied") {
		t.Fatalf("missing harness guidance = %q", model.View())
	}
}

func TestEnterAttachesNativeHarnessAndHistoryRemainsInPalette(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Capsules[0].Runs[0].Harness = "native"
	second := snapshot.Capsules[0].Threads[0]
	second.Thread.ID = "thread-2"
	second.Thread.State = client.ThreadStatePaused
	second.Thread.MessageCount = 1
	second.Blocks = []client.ThreadBlock{
		threadBlock("user-2", "user-2", 1, "second hello", client.ThreadAdapterEventTypeAssistantMessage),
	}
	snapshot.Capsules[0].Threads = append(snapshot.Capsules[0].Threads, second)
	var attached AttachRequest
	model := NewModel(Options{
		API: &fakeAPI{snapshot: snapshot}, Now: fixedNow,
		Attach: func(request AttachRequest) tea.Cmd {
			attached = request
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if attached.RunID != "run-active" || !strings.Contains(attached.Title, "alpha") ||
		!strings.Contains(attached.Title, "native") ||
		!strings.Contains(attached.RestoreTitle, "project") || cmd == nil {
		t.Fatalf("Enter attach = %#v / %v", attached, cmd)
	}
	updated, _ = model.Update(runCmd(t, cmd))
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model = updated.(Model)
	if model.overlay.kind != overlayPalette {
		t.Fatal("colon did not open history palette")
	}
	view := model.View()
	if !strings.Contains(view, "thread-2") || !strings.Contains(view, "second hello") {
		t.Fatalf("earlier chat should be in : %q", view)
	}
	model.paletteQuery = "thread-2"
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.selectedThreadID != "thread-2" || model.mode != modeHistory ||
		!strings.Contains(model.View(), "STRUCTURED HISTORY") {
		t.Fatalf("history pick = %q mode=%d", model.selectedThreadID, model.mode)
	}
}

func TestEmptyProjectState(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	model := NewModel(Options{API: &fakeAPI{}})
	updated, _ := model.Update(loadMsg{snapshot: Snapshot{}})
	model = updated.(Model)
	view := model.View()
	if !strings.Contains(view, "│●│ MERIDIAN") || !strings.Contains(view, "No projects yet") ||
		!strings.Contains(view, "·   │   ·") {
		t.Fatalf("empty projects view = %q", view)
	}
}

func TestComposerOwnsCommandLetters(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))

	for _, letter := range []rune{'q', 'n', 't', 'c', 'P', 'j', '?'} {
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{letter}})
		model = updated.(Model)
		if cmd != nil {
			t.Fatalf("letter %q returned a command", string(letter))
		}
		if model.overlay.kind != overlayNone {
			t.Fatalf("letter %q opened overlay %v", string(letter), model.overlay.kind)
		}
	}
	if model.composer != "qntcPj?" {
		t.Fatalf("composer = %q", model.composer)
	}
	if view := model.View(); !strings.Contains(view, composerHint) ||
		!strings.Contains(view, "qntcPj?") ||
		strings.Contains(view, "n new") {
		t.Fatalf("composer chrome = %q", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	model = updated.(Model)
	if model.overlay.kind != overlayHelp {
		t.Fatal("ctrl-o did not open help from the composer")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model = updated.(Model)
	if model.overlay.kind != overlayNone || !strings.HasSuffix(model.composer, ":") {
		t.Fatalf("colon with text opened palette: overlay=%v composer=%q", model.overlay.kind, model.composer)
	}

	model.composer = ""
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
	model = updated.(Model)
	if model.overlay.kind != overlayPalette {
		t.Fatal("empty composer colon did not open the palette")
	}
}

func TestComposerShowsCursorAndPendingSend(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	if !strings.Contains(model.View(), caretGlyph) {
		t.Fatalf("composing view missing cursor: %q", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	if !strings.Contains(model.View(), "hello"+caretGlyph) {
		t.Fatalf("typed text not visible: %q", model.View())
	}
}

func TestSendRestartsEndedSessionThenDelivers(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Capsules[0].Threads[0].Thread.CurrentRunState = client.NewOptRunState(client.RunStateFailed)
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.pendingSend != "hello" || !strings.Contains(model.View(), "hello") {
		t.Fatalf("pending send not shown: pending=%q view=%q", model.pendingSend, model.View())
	}
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionThreadStart {
		t.Fatalf("ended session send = %#v", got)
	}

	updated, _ = model.Update(actionMsg{result: ActionResult{Message: "thread-start requested", ThreadID: "thread-1"}})
	model = updated.(Model)
	ready := threadTestSnapshot()
	ready.Capsules[0].Threads[0].Thread.CurrentRunState = client.NewOptRunState(client.RunStateRunning)
	ready.Capsules[0].Threads[0].Thread.ResourceVersion = 8
	updated, cmd = model.Update(loadMsg{snapshot: ready})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionThreadSend ||
		got.Content != "hello" || got.ResourceVersion != 8 {
		t.Fatalf("follow-up send = %#v", got)
	}
}

func TestFailedSendRestoresComposer(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot, actionErr: errors.New("illegal_transition: Thread session is not accepting input")}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(runCmd(t, cmd))
	model = updated.(Model)
	if model.composer != "hello" || model.pendingSend != "" {
		t.Fatalf("composer after failure = %q pending=%q", model.composer, model.pendingSend)
	}
	if !strings.Contains(model.View(), "hello"+caretGlyph) {
		t.Fatalf("restored draft not visible: %q", model.View())
	}
}

func TestSendConflictRetriesWithFreshVersion(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, cmd := model.Update(actionMsg{err: errors.New("conflict: resource version conflict")})
	model = updated.(Model)
	if model.pendingSend != "hello" || model.composer != "" || model.sendRetries != 1 {
		t.Fatalf("conflict retry state pending=%q composer=%q retries=%d", model.pendingSend, model.composer, model.sendRetries)
	}
	if !strings.Contains(model.status, "Sending") {
		t.Fatalf("conflict status = %q", model.status)
	}
	fresh := threadTestSnapshot()
	fresh.Capsules[0].Threads[0].Thread.ResourceVersion = 9
	updated, cmd = model.Update(loadMsg{snapshot: fresh})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionThreadSend ||
		got.Content != "hello" || got.ResourceVersion != 9 {
		t.Fatalf("retried send = %#v", got)
	}
}

func TestInFlightSendIgnoresStaleRefresh(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, sendCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !model.sendInFlight || sendCmd == nil {
		t.Fatal("expected an in-flight send")
	}
	updated, _ = model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	if !model.sendInFlight {
		t.Fatal("refresh cleared the in-flight send")
	}
	runCmd(t, sendCmd)
	if api.requestCount() != 1 {
		t.Fatalf("stale refresh double-sent: %d", api.requestCount())
	}
}

func TestCursorBlinks(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = openFirstConversation(t, updated.(Model))
	if !strings.Contains(model.View(), caretGlyph) {
		t.Fatal("expected a visible caret")
	}
	updated, _ = model.Update(blinkMsg(fixedNow()))
	model = updated.(Model)
	if model.cursorOn || strings.Contains(model.View(), caretGlyph) {
		t.Fatalf("caret should blink off: on=%v view=%q", model.cursorOn, model.View())
	}
}

func TestSlashKeepsCapsuleList(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 70, Height: 40})
	model = updated.(Model)
	if model.focus != focusCapsules {
		t.Fatal("expected the Capsule list after load")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	if model.focus != focusCapsules || !model.composing() || model.composer != "/" {
		t.Fatalf("slash should stay on the list: focus=%d composing=%v composer=%q", model.focus, model.composing(), model.composer)
	}
	view := model.View()
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "/new") {
		t.Fatalf("slash menu on list missing: %q", view)
	}
	if strings.Contains(view, "Need permission") {
		t.Fatalf("slash opened the conversation pane: %q", view)
	}
}

func TestSlashSurvivesRefreshOnList(t *testing.T) {
	snapshot := threadTestSnapshot()
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}, Now: fixedNow})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, _ = model.Update(actionMsg{
		action: ActionDelete,
		result: ActionResult{Message: "delete requested"},
	})
	model = updated.(Model)
	updated, _ = model.Update(loadMsg{snapshot: testSnapshot()})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	updated, _ = model.Update(loadMsg{snapshot: testSnapshot()})
	model = updated.(Model)
	if !model.composing() || model.composer != "/" {
		t.Fatalf("refresh stole the slash draft: composing=%v composer=%q focus=%d",
			model.composing(), model.composer, model.focus)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new")})
	model = updated.(Model)
	if model.composer != "/new" {
		t.Fatalf("could not type after refresh: composer=%q", model.composer)
	}
}

func TestSlashCommandsFromProjectHome(t *testing.T) {
	snapshot := testSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	model = updated.(Model)
	if !model.composing() || model.composer != "/" {
		t.Fatalf("slash did not focus composer: focus=%v composer=%q", model.composing(), model.composer)
	}
	if !strings.Contains(model.View(), "/new") || !strings.Contains(model.View(), "/help") ||
		!strings.Contains(model.View(), "/project") || !strings.Contains(model.View(), "/harness") {
		t.Fatalf("slash menu missing: %q", model.View())
	}
	if strings.Contains(model.View(), "/capsule") || strings.Contains(model.View(), "/spawn") ||
		strings.Contains(model.View(), "/pause") {
		t.Fatalf("slash menu still lists duplicate creates: %q", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hel")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayHelp {
		t.Fatalf(" /help did not open help: overlay=%v composer=%q", model.overlay.kind, model.composer)
	}
}

func TestSlashProjectCreatesProject(t *testing.T) {
	snapshot := Snapshot{Projects: nil}
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	if !strings.Contains(model.View(), "/project") && !strings.Contains(model.View(), "No projects") {
		t.Fatalf("empty project home = %q", model.View())
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/project")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayForm || model.overlay.action.Action != ActionProjectCreate {
		t.Fatalf(" /project should open a form: overlay=%v action=%q", model.overlay.kind, model.overlay.action.Action)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://github.com/you/app.git")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("demo")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionProjectCreate ||
		got.Name != "demo" || got.RepositoryURL != "https://github.com/you/app.git" {
		t.Fatalf("create project = %#v", got)
	}

	updated, _ = model.Update(actionMsg{
		action: ActionProjectCreate,
		result: ActionResult{Message: "Project created", ProjectID: "project-new"},
	})
	model = updated.(Model)
	if model.projectID != "project-new" || model.place != placeProject {
		t.Fatalf("create did not select the Project: project=%q place=%d", model.projectID, model.place)
	}
}

func TestProjectFormPrefillsCurrentRepository(t *testing.T) {
	model := NewModel(Options{
		SuggestedProjectName:   "meridian",
		SuggestedRepositoryURL: "https://github.com/OrlojHQ/meridian.git",
	})
	model.openCreateProject()
	if got := model.overlayField("Name").value; got != "meridian" {
		t.Fatalf("suggested Project name = %q", got)
	}
	if got := model.overlayField("Repository").value; got != "https://github.com/OrlojHQ/meridian.git" {
		t.Fatalf("suggested repository = %q", got)
	}
	if got := selectedOptionValue(model.overlayField("Repository source")); got != "current" {
		t.Fatalf("repository source = %q", got)
	}
	if !strings.Contains(model.overlay.note, "current Git origin") {
		t.Fatalf("Project note = %q", model.overlay.note)
	}

	model.overlayField("Repository source").optionIndex = 1
	model.syncProjectRepositorySource()
	model.overlayField("Repository").value = "OrlojHQ/compass"
	request, err := model.formRequest()
	if err != nil {
		t.Fatal(err)
	}
	if request.RepositoryURL != "https://github.com/OrlojHQ/compass" {
		t.Fatalf("normalized repository = %q", request.RepositoryURL)
	}
}

func TestProjectFormExplainsMissingOrigin(t *testing.T) {
	model := NewModel(Options{SuggestedProjectName: "meridian"})
	model.openCreateProject()

	if got := selectedOptionValue(model.overlayField("Repository source")); got != "manual" {
		t.Fatalf("repository source = %q", got)
	}
	if got := model.overlayField("Name").value; got != "meridian" {
		t.Fatalf("suggested Project name = %q", got)
	}
	if !strings.Contains(model.overlay.note, "No Git origin was found") {
		t.Fatalf("Project note = %q", model.overlay.note)
	}
	view := model.View()
	if !strings.Contains(view, "GitHub") ||
		!strings.Contains(view, "No Git origin was found") {
		t.Fatalf("Project form = %q", view)
	}
}

func TestSlashSwitchChangesProjectWithoutStaleCapsule(t *testing.T) {
	snapshot := testSnapshot()
	nextProject := client.Project{
		ID: "project-new", Name: "new project",
		HarnessImages: []client.HarnessImage{{Name: "mock", ImageReference: "meridian-capsule:dev"}},
	}
	snapshot.Projects = append(snapshot.Projects, nextProject)
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/switch")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayForm || model.overlay.action.Action != Action("switch-project") {
		t.Fatalf(" /switch should open the switcher: %#v", model.overlay)
	}
	if got := model.overlay.fields[0].value; got != snapshot.Projects[0].Name {
		t.Fatalf("initial project choice = %q", got)
	}
	for _, option := range model.overlay.fields[0].options {
		if strings.Contains(option, "project-") {
			t.Fatalf("switcher exposed a Project ID: %q", option)
		}
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	updated, titleCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if titleCmd == nil {
		t.Fatal("switching Projects did not update the terminal title")
	}
	if model.projectID != nextProject.ID || model.projectName() != nextProject.Name {
		t.Fatalf("switched project = %q / %q", model.projectID, model.projectName())
	}
	if model.selectedDetail() != nil || strings.Contains(model.View(), "alpha") {
		t.Fatalf("new Project retained a stale Capsule: %q", model.View())
	}
	model.openCreate()
	if got := model.overlay.fields[0].value; got != nextProject.ID {
		t.Fatalf("new Capsule form targets Project %q", got)
	}
}

func TestCreateProjectPicksHarness(t *testing.T) {
	snapshot := Snapshot{
		HarnessImages: []client.HarnessImage{
			{Name: "mock", ImageReference: "meridian-capsule:dev"},
			{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
		},
	}
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/project")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayForm || len(model.overlay.fields) != 4 ||
		model.overlayField("Harness").value != "mock" {
		t.Fatalf("create form = %#v", model.overlay.fields)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("demo")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	if model.overlayField("Harness").value != "opencode" {
		t.Fatalf("harness = %q", model.overlayField("Harness").value)
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	runCmd(t, cmd)
	got := api.requests[len(api.requests)-1]
	if got.Action != ActionProjectCreate || got.Name != "demo" ||
		got.Image != "meridian-capsule-opencode:dev" ||
		len(got.HarnessImages) != 1 || got.HarnessImages[0] != "opencode=meridian-capsule-opencode:dev" {
		t.Fatalf("create = %#v", got)
	}
}

func TestApplyHarnessSavesAllowlist(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.HarnessImages = []client.HarnessImage{
		{Name: "mock", ImageReference: "meridian-capsule:dev"},
		{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
	}
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/harness")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayHarness {
		t.Fatalf(" /harness overlay = %v", model.overlay.kind)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	model = updated.(Model)
	if !model.overlay.choices[0].Applied {
		t.Fatal("space should apply the selected pack")
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	runCmd(t, cmd)
	got := api.requests[len(api.requests)-1]
	if got.Action != ActionProjectApplyHarnesses || len(got.HarnessImages) != 1 ||
		got.HarnessImages[0] != "mock=meridian-capsule:dev" {
		t.Fatalf("apply = %#v", got)
	}
}

func TestSlashNewSpawnsWorkspace(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/new")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay.kind != overlayNone || !model.pendingNew || model.focus != focusMain {
		t.Fatalf("/new should open a new Capsule: overlay=%v pending=%v focus=%d", model.overlay.kind, model.pendingNew, model.focus)
	}
	if model.newHarness != "native" {
		t.Fatalf("harness = %q", model.newHarness)
	}
	if view := model.View(); !strings.Contains(view, "New Capsule") ||
		!strings.Contains(view, "native") || !strings.Contains(view, "Capsule name") ||
		!strings.Contains(view, "Enter creates and opens") {
		t.Fatalf("/new view = %q", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil || api.requestCount() != 0 || !model.newNameFocus {
		t.Fatalf("prompt without a name should not spawn: cmd=%v requests=%d nameFocus=%v",
			cmd, api.requestCount(), model.newNameFocus)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("review")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.newNameFocus || strings.TrimSpace(model.newName) != "review" {
		t.Fatalf("name should stick after Enter: name=%q focus=%v", model.newName, model.newNameFocus)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionCreate ||
		got.Content != "" || got.Harness != "native" || got.Name != "review" {
		t.Fatalf("/new send = %#v", got)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("second Enter should not spawn again")
	}
	if api.requestCount() != 1 {
		t.Fatalf("spawned %d Capsules", api.requestCount())
	}
}

func TestEmptyCommandEntryEnterOpensSelectedCapsule(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Capsules[0].Runs[0].Harness = "native"
	var attached string
	model := NewModel(Options{
		API: &fakeAPI{snapshot: snapshot},
		Attach: func(request AttachRequest) tea.Cmd {
			attached = request.RunID
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	model.focus = focusMain
	model.composerFocus = true
	model.composer = ""

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if attached != "run-active" || cmd == nil || model.focus != focusCapsules ||
		model.composerFocus {
		t.Fatalf("empty command Enter = attach %q cmd=%v focus=%d composing=%v",
			attached, cmd, model.focus, model.composerFocus)
	}
}

func TestCreateWaitsForNativeRunAndAutoAttaches(t *testing.T) {
	snapshot := threadTestSnapshot()
	var attached string
	model := NewModel(Options{
		API: &fakeAPI{snapshot: snapshot}, Now: fixedNow,
		Attach: func(request AttachRequest) tea.Cmd {
			attached = request.RunID
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(actionMsg{
		action: ActionCreate,
		result: ActionResult{Message: "Capsule creation requested", CapsuleID: "capsule-new"},
	})
	model = updated.(Model)
	if model.pendingAttachID != "capsule-new" || model.selectedID != "capsule-new" ||
		model.place != placeProject {
		t.Fatalf("create wait state: pending=%q id=%q place=%d",
			model.pendingAttachID, model.selectedID, model.place)
	}

	created := threadTestSnapshot()
	newbie := created.Capsules[0]
	newbie.Capsule.ID = "capsule-new"
	newbie.Capsule.Name = "review"
	newbie.Capsule.Harness = client.NewOptString("native")
	newbie.Runs = []client.Run{{
		ID: "run-new", CapsuleId: "capsule-new", Harness: "native", State: client.RunStateRunning,
	}}
	created.Capsules = append(created.Capsules, newbie)
	updated, cmd := model.Update(loadMsg{snapshot: created})
	model = updated.(Model)
	if attached != "run-new" || cmd == nil || model.pendingAttachID != "" {
		t.Fatalf("auto attach = %q / %v / pending=%q", attached, cmd, model.pendingAttachID)
	}
}

func TestNewCapsuleCyclesHarness(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Projects[0].HarnessImages = append(snapshot.Projects[0].HarnessImages, client.HarnessImage{
		Name: "other", ImageReference: "meridian-capsule-other:dev",
	})
	snapshot.Capsules[0].Project = snapshot.Projects[0]
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/new")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.newHarness != "native" {
		t.Fatalf("initial harness = %q", model.newHarness)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	if model.newHarness != "other" {
		t.Fatalf("cycled harness = %q", model.newHarness)
	}
}

func TestNewCapsuleUsesProjectHarnessImages(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Projects[0].HarnessImages = []client.HarnessImage{
		{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
		{Name: "mock", ImageReference: "meridian-capsule:dev"},
	}
	model := NewModel(Options{API: &fakeAPI{snapshot: snapshot}})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/new")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.newHarness != "opencode" {
		t.Fatalf("allowlisted harness = %q", model.newHarness)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(Model)
	if model.newHarness != "mock" {
		t.Fatalf("cycled allowlisted harness = %q", model.newHarness)
	}
	if view := model.View(); !strings.Contains(view, "opencode") || !strings.Contains(view, "mock") {
		t.Fatalf("/new allowlist view = %q", view)
	}
}

func TestEnterOnIdleCapsuleStartsFrozenNativeHarness(t *testing.T) {
	snapshot := threadTestSnapshot()
	snapshot.Capsules[0].Threads = nil
	snapshot.Capsules[0].Runs = nil
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = enterSelectedProject(t, updated.(Model))
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionRunStart ||
		got.CapsuleID != "capsule-1" || got.Harness != "native" {
		t.Fatalf("idle native start = %#v", got)
	}
	if model.pendingAttachID != "capsule-1" {
		t.Fatalf("pending attach = %q", model.pendingAttachID)
	}
}

func focusCapsuleList(t *testing.T, model Model) Model {
	t.Helper()
	if model.focus == focusCapsules {
		return model
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(Model)
	if model.focus != focusCapsules {
		t.Fatal("did not move focus to the Capsule list")
	}
	return model
}

func enterSelectedProject(t *testing.T, model Model) Model {
	t.Helper()
	if model.place != placeHome {
		return model
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.place != placeProject {
		t.Fatalf("enter did not open Project: place=%d project=%q", model.place, model.projectID)
	}
	return model
}

func openFirstConversation(t *testing.T, model Model) Model {
	t.Helper()
	model = enterSelectedProject(t, model)
	model.openCapsuleSession()
	if model.place != placeThread {
		t.Fatalf("did not enter conversation: place=%d thread=%q", model.place, model.selectedThreadID)
	}
	return model
}

func runCmd(t *testing.T, command tea.Cmd) tea.Msg {
	t.Helper()
	if command == nil {
		t.Fatal("expected command")
	}
	return command()
}

func fixedNow() time.Time {
	return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
}

func testSnapshot() Snapshot {
	now := fixedNow()
	project := client.Project{ID: "project-1", Name: "project"}
	active := client.Run{
		ID: "run-active", CapsuleId: "capsule-1", Harness: "mock",
		State: client.RunStateRunning, CreatedAt: now.Add(-time.Minute), ResourceVersion: 2,
	}
	timeline := &client.TimelineView{
		Timeline: client.Timeline{
			ID: "timeline-1", ProjectId: project.ID, CapsuleId: "capsule-1",
			Reason: client.TimelineReasonRoot, CreatedAt: now.Add(-time.Hour),
		},
	}
	return Snapshot{
		Projects: []client.Project{project},
		LoadedAt: now,
		Capsules: []CapsuleDetail{
			{
				Project: project,
				Capsule: client.Capsule{
					ID: "capsule-1", ProjectId: project.ID, TimelineId: "timeline-1",
					Name: "alpha", State: client.CapsuleStateReady, DesiredState: client.CapsuleIntentReady,
					Harness:         client.NewOptString("mock"),
					RestoreComplete: true, CreatedAt: now.Add(-time.Hour), ResourceVersion: 7,
				},
				Profiles: []client.HarnessProfile{{Name: "mock", Pty: true}},
				Runs:     []client.Run{active},
				Events: []client.RunEvent{{
					Sequence: 1, Type: "started", Timestamp: now.Add(-time.Minute),
				}},
				Moments: []client.Moment{{
					ID: "moment-1", Name: "checkpoint", ArchiveSize: 42, CreatedAt: now.Add(-time.Minute),
				}},
				Timeline: timeline,
			},
			{
				Project: project,
				Capsule: client.Capsule{
					ID: "capsule-2", ProjectId: project.ID, TimelineId: "timeline-2",
					Name: "beta", State: client.CapsuleStatePaused, DesiredState: client.CapsuleIntentPaused,
					RestoreComplete: true, CreatedAt: now.Add(-2 * time.Hour), ResourceVersion: 3,
				},
			},
		},
	}
}
