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

	"github.com/OrlojHQ/meridian/pkg/client"
)

func threadTestSnapshot() Snapshot {
	snapshot := testSnapshot()
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
	model = updated.(Model)
	if !strings.Contains(model.View(), "alpha") || !strings.Contains(model.View(), "connected") {
		t.Fatalf("loaded view = %q", model.View())
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
	if wide == narrow || !strings.Contains(narrow, "Resources: unavailable") {
		t.Fatal("responsive views did not retain explicit unavailable metrics")
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
	model = updated.(Model)
	if !strings.Contains(model.View(), "No Capsules") {
		t.Fatalf("empty view = %q", model.View())
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

func TestModelActionsConfirmationConflictAndAttachHandoff(t *testing.T) {
	api := &fakeAPI{snapshot: testSnapshot()}
	var attached string
	model := NewModel(Options{
		API: api,
		Attach: func(runID string, _ uint64) tea.Cmd {
			attached = runID
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: api.snapshot})
	model = updated.(Model)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = updated.(Model)
	if attached != "run-active" || cmd == nil {
		t.Fatalf("attach handoff = %q, cmd=%v", attached, cmd)
	}
	updated, _ = model.Update(runCmd(t, cmd))
	model = updated.(Model)
	if !strings.Contains(model.status, "dashboard resumed") {
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
	model = updated.(Model)
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
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(Model)
	view := model.View()
	if model.selectedThreadID != "thread-1" ||
		!strings.Contains(view, "authoritative") ||
		strings.Contains(view, "partial") ||
		strings.Contains(view, "\x1b") ||
		!strings.Contains(view, "permission requested") ||
		!strings.Contains(view, "Harness: structured") {
		t.Fatalf("Thread view = %q", view)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(updated.(Model).View(), "Capsules / Threads") {
		t.Fatalf("narrow fleet pane = %q", updated.(Model).View())
	}
}

func TestThreadCreateSendPermissionAndLifecycleActions(t *testing.T) {
	snapshot := threadTestSnapshot()
	api := &fakeAPI{snapshot: snapshot}
	model := NewModel(Options{API: api})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)

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
	model.move(1)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model = updated.(Model)
	model.overlay.fields[0].value = "next\nmessage"
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	model = updated.(Model)
	runCmd(t, cmd)
	if got := api.requests[len(api.requests)-1]; got.Action != ActionThreadSend ||
		got.Content != "next\nmessage" || got.ResourceVersion != 4 {
		t.Fatalf("send request = %#v", got)
	}

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

	refresh := threadTestSnapshot()
	refresh.Capsules[0].Threads[0].Cursor = 5
	refresh.Capsules[0].Threads[0].Thread.MessageCount = 5
	updated, _ = model.Update(loadMsg{snapshot: refresh})
	model = updated.(Model)
	if model.unread["thread-1"] != 2 || !strings.Contains(model.listView(), "unread=2") {
		t.Fatalf("unread state = %d / %q", model.unread["thread-1"], model.listView())
	}
	model.move(1)
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
		Attach: func(runID string, _ uint64) tea.Cmd {
			attached = runID
			return func() tea.Msg { return attachFinishedMsg{} }
		},
	})
	updated, _ := model.Update(loadMsg{snapshot: snapshot})
	model = updated.(Model)
	model.move(1)
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
		!strings.Contains(model.status, "structured") {
		t.Fatalf("structured Run attach = %q / %v / %q", attached, cmd, model.status)
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
		!strings.Contains(specialView, "tool start · shell") ||
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
	view := model.threadView(snapshot.Capsules[0], *thread)
	if !strings.Contains(view, "TRANSCRIPT LOCKED") ||
		!strings.Contains(view, "Replay gap") ||
		strings.Contains(view, "\x9b") {
		t.Fatalf("locked/gap view = %q", view)
	}
}

func TestRunRejectsNonTTYWithoutEscapes(t *testing.T) {
	var output bytes.Buffer
	if err := Run(context.Background(), &fakeAPI{}, "", nil, &output); err == nil {
		t.Fatal("non-TTY dashboard unexpectedly started")
	}
	if output.Len() != 0 {
		t.Fatalf("non-TTY output = %q", output.String())
	}
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
					RestoreComplete: true, CreatedAt: now.Add(-time.Hour), ResourceVersion: 7,
				},
				Runs: []client.Run{active},
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
