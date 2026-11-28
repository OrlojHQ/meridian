package app_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (r *coordRuntime) emitModels(run domain.RunID, set adapterproto.ModelSet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := uint64(len(r.events[run]) + 1)
	copied := set
	r.events[run] = append(r.events[run], ports.RuntimeStructuredEvent{
		Sequence: next,
		Frame: adapterproto.Frame{
			Protocol: adapterproto.Version,
			Type:     adapterproto.KindModels,
			Models:   &copied,
		},
	})
}

func (r *coordRuntime) emitError(run domain.RunID, errCode, summary string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := uint64(len(r.events[run]) + 1)
	r.events[run] = append(r.events[run], ports.RuntimeStructuredEvent{
		Sequence: next,
		Frame: adapterproto.Frame{
			Protocol: adapterproto.Version,
			Type:     adapterproto.KindError,
			Code:     errCode,
			Summary:  summary,
		},
	})
}

func (r *coordRuntime) sentFrames(run domain.RunID) []adapterproto.Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]adapterproto.Frame(nil), r.sent[run]...)
}

func TestSubagentSpawnModelPrecedenceAndPersistence(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true

	lead, leadMailbox := f.spawnLead(t, domain.CoordinationPolicy{
		AllowedHarnesses: []string{"mock", "other"},
	})
	leadThread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	leadModels := adapterproto.ModelSet{
		Current:  "opus",
		Resolved: "Opus 4.6",
		Options: []adapterproto.ModelOption{
			{ID: "opus", Label: "Opus 4.6"},
			{ID: "sonnet", Label: "Sonnet 4.6"},
			{ID: "haiku", Label: "Haiku 4.5"},
		},
	}
	f.runtime.emitModels(leadThread.CurrentRunID, leadModels)
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}

	// Lead whoami reflects Match mode and the lead's current model.
	whoami := tool(t, leadMailbox, coordproto.Request{Type: coordproto.KindWhoAmI})
	if !whoami.OK || whoami.Self == nil || whoami.Self.WorkerDefaults == nil {
		t.Fatalf("whoami = %+v", whoami)
	}
	if whoami.Self.WorkerDefaults.Mode != "match" ||
		whoami.Self.WorkerDefaults.Harness != "mock" ||
		whoami.Self.WorkerDefaults.Model != "opus" ||
		whoami.Self.WorkerDefaults.ModelLabel != "Opus 4.6" {
		t.Fatalf("unexpected Match workerDefaults = %+v", whoami.Self.WorkerDefaults)
	}

	// 1. Match default: spawn with neither harness nor model uses lead's harness and current model.
	matchResp := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-match", Name: "worker-match", Prompt: "do work",
	})
	if !matchResp.OK || matchResp.Link == nil {
		t.Fatalf("match spawn = %+v", matchResp)
	}
	if matchResp.Link.Harness != "mock" || matchResp.Link.Model != "opus" {
		t.Fatalf("match link = %+v, want mock/opus", matchResp.Link)
	}

	// 2. Update grant to Custom Worker Model and verify persistence + mailbox update.
	coord, err := f.service.ThreadCoordinationOf(f.ctx, leadThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.service.GetCoordinationGrant(f.ctx, coord.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	custom := domain.WorkerModelSetting{Mode: domain.WorkerModelCustom, Model: "sonnet"}
	grant, err = f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{WorkerModel: &custom}, "set-custom")
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := f.service.GetCoordinationGrant(f.ctx, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.WorkerModel != custom {
		t.Fatalf("persisted worker model = %+v, want %+v", reloaded.WorkerModel, custom)
	}
	whoami = tool(t, leadMailbox, coordproto.Request{Type: coordproto.KindWhoAmI})
	if whoami.Self.WorkerDefaults.Mode != "custom" || whoami.Self.WorkerDefaults.Model != "sonnet" {
		t.Fatalf("custom whoami = %+v", whoami.Self.WorkerDefaults)
	}

	// Spawn with no override now gets Custom ("sonnet").
	customResp := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-custom", Name: "worker-custom", Prompt: "do work",
	})
	if !customResp.OK || customResp.Link == nil || customResp.Link.Harness != "mock" || customResp.Link.Model != "sonnet" {
		t.Fatalf("custom spawn = %+v, want mock/sonnet", customResp)
	}

	// 3. Explicit spawn override wins over Custom Worker Model.
	overrideResp := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-override", Name: "worker-fast", Model: "haiku", Prompt: "quick lookup",
	})
	if !overrideResp.OK || overrideResp.Link == nil || overrideResp.Link.Harness != "mock" || overrideResp.Link.Model != "haiku" {
		t.Fatalf("override spawn = %+v, want mock/haiku", overrideResp)
	}

	// 4. Explicit harness-only override to another allowed harness uses that harness's default (not mock's model).
	crossResp := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-other", Name: "worker-other", Harness: "other", Prompt: "cross harness",
	})
	if !crossResp.OK || crossResp.Link == nil || crossResp.Link.Harness != "other" || crossResp.Link.Model != "" {
		t.Fatalf("cross harness spawn = %+v, want other with default model", crossResp)
	}

	// 5. Switch back to Match and verify persistence clears model/harness.
	grant, err = f.service.GetCoordinationGrant(f.ctx, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	matchSetting := domain.WorkerModelSetting{Mode: domain.WorkerModelMatch}
	grant, err = f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{WorkerModel: &matchSetting}, "set-match")
	if err != nil {
		t.Fatal(err)
	}
	if grant.WorkerModel.Mode != domain.WorkerModelMatch || grant.WorkerModel.Model != "" || grant.WorkerModel.Harness != "" {
		t.Fatalf("reset to match = %+v", grant.WorkerModel)
	}
}

func TestSubagentSpawnRejectsInvalidModelAndHarnessWithoutFallback(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true

	lead, leadMailbox := f.spawnLead(t, domain.CoordinationPolicy{
		AllowedHarnesses: []string{"mock"},
	})
	leadThread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(leadThread.CurrentRunID, adapterproto.ModelSet{
		Current: "opus",
		Options: []adapterproto.ModelOption{
			{ID: "opus", Label: "Opus"},
			{ID: "sonnet", Label: "Sonnet"},
		},
	})
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}

	// Invalid harness returns harness_not_allowed naming allowed harnesses.
	badHarness := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-same", Name: "fixer", Harness: "other", Prompt: "fix",
	})
	if badHarness.OK || code(badHarness) != coordproto.CodeHarnessNotAllowed ||
		!strings.Contains(badHarness.Error.Message, "mock") {
		t.Fatalf("bad harness = %+v", badHarness)
	}

	// Invalid model returns model_unavailable naming available models.
	badModel := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-same", Name: "fixer", Model: "nonexistent", Prompt: "fix",
	})
	if badModel.OK || code(badModel) != coordproto.CodeModelUnavailable ||
		!strings.Contains(badModel.Error.Message, "sonnet") {
		t.Fatalf("bad model = %+v", badModel)
	}

	// Failed spawns are not cached under the idempotency key, so retrying the same
	// name/key with a valid model succeeds.
	okResp := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-same", Name: "fixer", Model: "sonnet", Prompt: "fix",
	})
	if !okResp.OK || okResp.Link == nil || okResp.Link.Model != "sonnet" {
		t.Fatalf("retry with valid model = %+v", okResp)
	}

	// Grant update rejects a custom worker harness that is not in allowedHarnesses.
	coord, err := f.service.ThreadCoordinationOf(f.ctx, leadThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.service.GetCoordinationGrant(f.ctx, coord.GrantID)
	if err != nil {
		t.Fatal(err)
	}
	badCustom := domain.WorkerModelSetting{Mode: domain.WorkerModelCustom, Harness: "other", Model: "gpt-5"}
	if _, err := f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{WorkerModel: &badCustom}, "bad-custom"); err == nil {
		t.Fatal("expected error when workerModel.harness is not in allowedHarnesses")
	}
}

func TestSubagentAppliesRequestedModelBeforeFirstPromptAndNeverFallsBack(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true

	lead, leadMailbox := f.spawnLead(t, domain.CoordinationPolicy{
		AllowedHarnesses: []string{"mock", "other"},
	})
	leadThread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(leadThread.CurrentRunID, adapterproto.ModelSet{
		Current: "opus",
		Options: []adapterproto.ModelOption{
			{ID: "opus", Label: "Opus"},
			{ID: "sonnet", Label: "Sonnet"},
		},
	})
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}

	// Case A: Subagent starts on "opus", switches to requested "sonnet", then receives prompt.
	spawned := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-a", Name: "worker-a", Model: "sonnet", Prompt: "implement feature",
	})
	if !spawned.OK || spawned.Link == nil {
		t.Fatalf("spawn = %+v", spawned)
	}
	linkA, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, linkA.ChildCapsuleID)
	childA, err := f.service.GetThread(f.ctx, linkA.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}

	// Before models arrive, no user prompt is delivered.
	for _, frame := range f.runtime.sentFrames(childA.CurrentRunID) {
		if frame.Type == adapterproto.KindUserMessage {
			t.Fatalf("prompt delivered before model was applied: %+v", frame)
		}
	}

	// Child emits initial models (current: opus). Tick sends set_model("sonnet"), still no prompt.
	f.runtime.emitModels(childA.CurrentRunID, adapterproto.ModelSet{
		Current: "opus",
		Options: []adapterproto.ModelOption{
			{ID: "opus", Label: "Opus"},
			{ID: "sonnet", Label: "Sonnet"},
		},
	})
	f.service.CoordinationTick(asSystem(f.ctx))

	var sawSetModel, sawPrompt bool
	for _, frame := range f.runtime.sentFrames(childA.CurrentRunID) {
		if frame.Type == adapterproto.KindSetModel && frame.Model == "sonnet" {
			sawSetModel = true
		}
		if frame.Type == adapterproto.KindUserMessage {
			sawPrompt = true
		}
	}
	if !sawSetModel || sawPrompt {
		t.Fatalf("after initial models: sawSetModel=%v sawPrompt=%v", sawSetModel, sawPrompt)
	}

	// Child confirms switch to "sonnet". Next tick delivers the prompt.
	f.runtime.emitModels(childA.CurrentRunID, adapterproto.ModelSet{
		Current: "sonnet",
		Options: []adapterproto.ModelOption{
			{ID: "opus", Label: "Opus"},
			{ID: "sonnet", Label: "Sonnet"},
		},
	})
	f.service.CoordinationTick(asSystem(f.ctx))

	var promptText string
	for _, frame := range f.runtime.sentFrames(childA.CurrentRunID) {
		if frame.Type == adapterproto.KindUserMessage {
			promptText = frame.Content
		}
	}
	if promptText != "implement feature" {
		t.Fatalf("delivered prompt = %q, want %q", promptText, "implement feature")
	}

	// Case B: Cross-harness spawn on "other" (no cached catalog) with a model the harness
	// does not offer fails cleanly with model_unavailable and NEVER delivers the prompt.
	spawnedB := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-b", Name: "worker-b", Harness: "other", Model: "missing-model", Prompt: "secret task",
	})
	if !spawnedB.OK || spawnedB.Link == nil {
		t.Fatalf("spawn-b = %+v", spawnedB)
	}
	linkB, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawnedB.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, linkB.ChildCapsuleID)
	childB, err := f.service.GetThread(f.ctx, linkB.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(childB.CurrentRunID, adapterproto.ModelSet{
		Current: "gpt-5",
		Options: []adapterproto.ModelOption{{ID: "gpt-5", Label: "GPT-5"}},
	})
	f.service.CoordinationTick(asSystem(f.ctx))

	linkB, err = f.service.GetThreadLink(f.ctx, linkB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if linkB.State != domain.LinkClosed || linkB.CloseReason != domain.LinkFailed ||
		linkB.LastReportReason != domain.ReportModelUnavailable {
		t.Fatalf("failed linkB = %+v", linkB)
	}
	for _, frame := range f.runtime.sentFrames(childB.CurrentRunID) {
		if frame.Type == adapterproto.KindUserMessage {
			t.Fatalf("prompt was sent despite missing model: %+v", frame)
		}
	}
}

func (r *coordRuntime) finishHandshake(run domain.RunID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.runs[run]
	current.Negotiated = true
	current.Models = r.modelsSupported
	current.Effort = r.effortSupported
	r.runs[run] = current
}

func (r *coordRuntime) failStructured(run domain.RunID, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.structuredErrs == nil {
		r.structuredErrs = make(map[domain.RunID]error)
	}
	r.structuredErrs[run] = err
}

func (r *coordRuntime) configureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.configures
}

// leadWithModels starts a lead on mock whose session reports opus and sonnet.
func leadWithModels(t *testing.T, f *coordFixture, allowed ...string) (domain.Thread, *capsuleproto.Mailbox) {
	t.Helper()
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{AllowedHarnesses: allowed})
	leadThread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(leadThread.CurrentRunID, adapterproto.ModelSet{
		Current: "opus",
		Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}, {ID: "sonnet", Label: "Sonnet 4.6"}},
	})
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}
	return leadThread, mailbox
}

func userPrompts(frames []adapterproto.Frame) []string {
	var prompts []string
	for _, frame := range frames {
		if frame.Type == adapterproto.KindUserMessage {
			prompts = append(prompts, frame.Content)
		}
	}
	return prompts
}

func TestSubagentModelSelectionWaitsForTheAdapterHandshake(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true
	_, leadMailbox := leadWithModels(t, f, "mock")

	// The child's adapter has not answered hello yet, so its runtime reports
	// Models false. That is not "unsupported" and must not fail the link.
	f.runtime.handshakePending = true
	spawned := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-slow", Name: "slow-start", Model: "sonnet", Prompt: "do it",
	})
	if !spawned.OK || spawned.Link == nil {
		t.Fatalf("spawn = %+v", spawned)
	}
	link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, link.ChildCapsuleID)
	child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	// A slow start uses more than the model wait without failing.
	for range 3 {
		f.advance(domain.CoordinationModelWait)
		f.service.CoordinationTick(asSystem(f.ctx))
	}
	if link, err = f.service.GetThreadLink(f.ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	if link.State != domain.LinkActive || link.ModelState != domain.LinkModelPending {
		t.Fatalf("link during handshake = %+v, want active and pending", link)
	}

	// The model wait starts at the handshake: just after it, the harness has
	// not reported models yet and the link still waits.
	f.runtime.finishHandshake(child.CurrentRunID)
	f.advance(domain.CoordinationModelWait / 2)
	f.service.CoordinationTick(asSystem(f.ctx))
	if link, err = f.service.GetThreadLink(f.ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	if link.State != domain.LinkActive {
		t.Fatalf("link right after handshake = %+v, want active", link)
	}

	f.runtime.emitModels(child.CurrentRunID, adapterproto.ModelSet{
		Current: "sonnet",
		Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}, {ID: "sonnet", Label: "Sonnet 4.6"}},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	if prompts := userPrompts(f.runtime.sentFrames(child.CurrentRunID)); !slices.Equal(prompts, []string{"do it"}) {
		t.Fatalf("prompts = %q, want the one task", prompts)
	}

	// The lead sees the child's model ID, not its label, so it can pass it
	// back to spawn.
	listed := tool(t, leadMailbox, coordproto.Request{Type: coordproto.KindList})
	if !listed.OK || len(listed.Links) != 1 || listed.Links[0].Model != "sonnet" {
		t.Fatalf("list = %+v, want model sonnet", listed)
	}
}

func TestSubagentModelSelectionFailsWhenItStalls(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true
	_, leadMailbox := leadWithModels(t, f, "mock")
	spawned := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-stuck", Name: "stuck", Model: "sonnet", Prompt: "do it",
	})
	if !spawned.OK || spawned.Link == nil {
		t.Fatalf("spawn = %+v", spawned)
	}
	link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, link.ChildCapsuleID)
	child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.failStructured(child.CurrentRunID, errors.New("runtime unreachable"))

	f.advance(time.Minute)
	f.service.CoordinationTick(asSystem(f.ctx))
	if link, err = f.service.GetThreadLink(f.ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	if link.State != domain.LinkActive {
		t.Fatalf("link after a transient error = %+v, want active", link)
	}

	f.advance(domain.CoordinationModelStall)
	f.service.CoordinationTick(asSystem(f.ctx))
	if link, err = f.service.GetThreadLink(f.ctx, link.ID); err != nil {
		t.Fatal(err)
	}
	if link.State != domain.LinkClosed || link.CloseReason != domain.LinkFailed ||
		link.LastReportReason != domain.ReportModelUnavailable {
		t.Fatalf("stalled link = %+v, want failed with model_unavailable", link)
	}
	if prompts := userPrompts(f.runtime.sentFrames(child.CurrentRunID)); len(prompts) != 0 {
		t.Fatalf("prompts = %q, want none", prompts)
	}
}

func TestSpawnRefusesModelsOnlyAgainstALiveCatalog(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true
	_, leadMailbox := leadWithModels(t, f, "mock", "other")

	// An operator session on "other" reports its catalog.
	other, err := f.service.CreateProjectThread(f.ctx, app.CreateProjectThreadInput{
		ProjectID: "project-coord", Harness: "other", Prompt: "hi", IdempotencyKey: "other-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, other.Intent.CapsuleID)
	otherThread, err := f.service.GetThread(f.ctx, other.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(otherThread.CurrentRunID, adapterproto.ModelSet{
		Current: "gpt-5", Options: []adapterproto.ModelOption{{ID: "gpt-5", Label: "GPT-5"}},
	})
	if err := f.service.SyncThread(f.ctx, otherThread.ID); err != nil {
		t.Fatal(err)
	}

	// While that session is live its catalog is authoritative.
	refused := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-new-live", Name: "new-live", Harness: "other",
		Model: "gpt-6", Prompt: "try it",
	})
	if refused.OK || code(refused) != coordproto.CodeModelUnavailable || !strings.Contains(refused.Error.Message, "gpt-5") {
		t.Fatalf("spawn against a live catalog = %+v, want model_unavailable", refused)
	}

	// Once it ends, the catalog may be stale: the spawn goes ahead and the
	// subagent checks its own catalog before its first prompt.
	f.runtime.endSession(otherThread.CurrentRunID)
	if err := f.service.SyncThread(f.ctx, otherThread.ID); err != nil {
		t.Fatal(err)
	}
	ended, err := f.service.GetThread(f.ctx, otherThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended.State == domain.ThreadActive && ended.CurrentRunID != "" {
		t.Fatalf("other session is still live: %+v", ended)
	}
	accepted := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-new-stale", Name: "new-stale", Harness: "other",
		Model: "gpt-6", Prompt: "try it",
	})
	if !accepted.OK || accepted.Link == nil || accepted.Link.Model != "gpt-6" {
		t.Fatalf("spawn against a stale catalog = %+v, want accepted", accepted)
	}

	// The operator's picker and the lead read the same catalog.
	view, err := f.service.ReportedModels(f.ctx, "project-coord", "other")
	if err != nil {
		t.Fatal(err)
	}
	if view.Current != "gpt-5" || view.ThreadID != string(otherThread.ID) {
		t.Fatalf("reported models = %+v", view)
	}
}

func TestGrantKeepsTheDefaultWorkerHarnessAllowed(t *testing.T) {
	f := newCoordFixture(t)
	leadThread, _ := leadWithModels(t, f, "mock", "other")
	coord, err := f.service.ThreadCoordinationOf(f.ctx, leadThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := f.service.GetCoordinationGrant(f.ctx, coord.GrantID)
	if err != nil {
		t.Fatal(err)
	}

	// Match spawns on the lead's harness, so it cannot be removed.
	onlyOther := []string{"other"}
	if _, err := f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{AllowedHarnesses: &onlyOther}, "drop-lead"); !errors.Is(err, domain.ErrInvalid) ||
		!strings.Contains(err.Error(), "lead's harness") {
		t.Fatalf("dropping the lead's harness under Match = %v, want invalid", err)
	}

	// Once new workers default to another harness, the lead's may go.
	custom := domain.WorkerModelSetting{Mode: domain.WorkerModelCustom, Harness: "other", Model: "gpt-5"}
	if grant, err = f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{WorkerModel: &custom}, "custom-other"); err != nil {
		t.Fatal(err)
	}
	if grant, err = f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{AllowedHarnesses: &onlyOther}, "drop-lead-custom"); err != nil {
		t.Fatalf("dropping the lead's harness under Custom other = %v", err)
	}

	// Switching back to Match would leave default spawns nowhere to go.
	match := domain.WorkerModelSetting{Mode: domain.WorkerModelMatch}
	if _, err := f.service.UpdateCoordinationGrant(f.ctx, grant.ID, grant.ResourceVersion,
		domain.CoordinationGrantPatch{WorkerModel: &match}, "back-to-match"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Match without the lead's harness = %v, want invalid", err)
	}

	// A grant from before Worker Models can still change its other limits.
	if err := f.store.Transact(f.ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCoordinationGrant(f.ctx, grant.ID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.WorkerModel = domain.WorkerModelSetting{}
		current.ResourceVersion++
		return tx.UpdateCoordinationGrant(f.ctx, current, previous)
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := f.service.GetCoordinationGrant(f.ctx, grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	operator := domain.SpawnOperator
	if _, err := f.service.UpdateCoordinationGrant(f.ctx, legacy.ID, legacy.ResourceVersion,
		domain.CoordinationGrantPatch{SpawnApproval: &operator}, "legacy-approval"); err != nil {
		t.Fatalf("legacy grant spawn approval update = %v", err)
	}

	// A new grant that excludes the lead's harness under Match is refused.
	if _, err := f.service.CreateProjectThread(f.ctx, app.CreateProjectThreadInput{
		ProjectID: "project-coord", Harness: "mock", Prompt: "lead", IdempotencyKey: "lead-elsewhere",
		Coordination: &domain.CoordinationPolicy{AllowedHarnesses: []string{"other"}},
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("lead grant without its harness = %v, want invalid", err)
	}
}

func TestRepeatedModelsReportsDoNotRepushTheLeadMailbox(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true
	leadThread, leadMailbox := leadWithModels(t, f, "mock")
	// A daemon round trip lets the background poller finish its first
	// configuration (whoami is answered inside the Capsule).
	if listed := tool(t, leadMailbox, coordproto.Request{Type: coordproto.KindList}); !listed.OK {
		t.Fatalf("list = %+v", listed)
	}
	set := adapterproto.ModelSet{
		Current: "opus",
		Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}, {ID: "sonnet", Label: "Sonnet 4.6"}},
	}
	before := f.runtime.configureCount()
	f.runtime.emitModels(leadThread.CurrentRunID, set)
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.runtime.configureCount(); after != before {
		t.Fatalf("unchanged models report pushed the mailbox %d times", after-before)
	}
	set.Current = "sonnet"
	f.runtime.emitModels(leadThread.CurrentRunID, set)
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}
	if after := f.runtime.configureCount(); after != before+1 {
		t.Fatalf("model switch pushed the mailbox %d times, want 1", after-before)
	}
}

func TestMatchSubagentFallsBackToTheHarnessDefault(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true
	_, leadMailbox := leadWithModels(t, f, "mock")

	spawnMatch := func(name string) (domain.ThreadLink, domain.Thread) {
		t.Helper()
		spawned := tool(t, leadMailbox, coordproto.Request{
			Type: coordproto.KindSpawn, Key: "spawn-" + name, Name: name, Prompt: "task for " + name,
		})
		if !spawned.OK || spawned.Link == nil || spawned.Link.Model != "opus" {
			t.Fatalf("match spawn = %+v, want the lead's model", spawned)
		}
		link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
		if err != nil {
			t.Fatal(err)
		}
		if !link.ModelFallback || link.RequestedModel != "opus" {
			t.Fatalf("match link = %+v, want fallback for opus", link)
		}
		f.ready(t, link.ChildCapsuleID)
		child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
		if err != nil {
			t.Fatal(err)
		}
		return link, child
	}
	linkModel := func(id domain.ThreadLinkID) string {
		t.Helper()
		listed := tool(t, leadMailbox, coordproto.Request{Type: coordproto.KindList})
		for _, link := range listed.Links {
			if link.ID == string(id) {
				return link.Model
			}
		}
		t.Fatalf("link %s not listed: %+v", id, listed)
		return ""
	}

	// The child's harness does not offer the lead's model: it starts on its
	// own default instead of failing.
	offered, offeredChild := spawnMatch("not-offered")
	f.runtime.emitModels(offeredChild.CurrentRunID, adapterproto.ModelSet{
		Current: "haiku", Options: []adapterproto.ModelOption{{ID: "haiku", Label: "Haiku 4.5"}},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	if prompts := userPrompts(f.runtime.sentFrames(offeredChild.CurrentRunID)); !slices.Equal(prompts, []string{"task for not-offered"}) {
		t.Fatalf("prompts = %q, want the task on the harness default", prompts)
	}
	for _, frame := range f.runtime.sentFrames(offeredChild.CurrentRunID) {
		if frame.Type == adapterproto.KindSetModel {
			t.Fatalf("sent set_model for a model the harness does not offer: %+v", frame)
		}
	}
	if offered, err := f.service.GetThreadLink(f.ctx, offered.ID); err != nil {
		t.Fatal(err)
	} else if offered.State != domain.LinkActive || offered.ModelState != domain.LinkModelDefault {
		t.Fatalf("fallen-back link = %+v, want active with model state default", offered)
	}
	if model := linkModel(offered.ID); model != "haiku" {
		t.Fatalf("fallen-back link model = %q, want the model it runs", model)
	}

	// The child's harness never reports models: after the wait it starts
	// anyway, and the lead is not told it runs the lead's model.
	silent, silentChild := spawnMatch("silent")
	f.advance(domain.CoordinationModelWait + time.Second)
	f.service.CoordinationTick(asSystem(f.ctx))
	if prompts := userPrompts(f.runtime.sentFrames(silentChild.CurrentRunID)); !slices.Equal(prompts, []string{"task for silent"}) {
		t.Fatalf("prompts = %q, want the task after the wait", prompts)
	}
	if model := linkModel(silent.ID); model != "" {
		t.Fatalf("silent fallen-back link model = %q, want none", model)
	}

	// The same failure on an explicitly requested model is strict.
	strict := tool(t, leadMailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-strict", Name: "strict", Model: "sonnet", Prompt: "strict task",
	})
	if !strict.OK || strict.Link == nil {
		t.Fatalf("strict spawn = %+v", strict)
	}
	strictLink, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(strict.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strictLink.ModelFallback {
		t.Fatalf("explicit model link = %+v, want strict", strictLink)
	}
	f.ready(t, strictLink.ChildCapsuleID)
	strictChild, err := f.service.GetThread(f.ctx, strictLink.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(strictChild.CurrentRunID, adapterproto.ModelSet{
		Current: "haiku", Options: []adapterproto.ModelOption{{ID: "haiku", Label: "Haiku 4.5"}},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	if strictLink, err = f.service.GetThreadLink(f.ctx, strictLink.ID); err != nil {
		t.Fatal(err)
	}
	if strictLink.State != domain.LinkClosed || strictLink.LastReportReason != domain.ReportModelUnavailable {
		t.Fatalf("strict link = %+v, want failed with model_unavailable", strictLink)
	}
	if prompts := userPrompts(f.runtime.sentFrames(strictChild.CurrentRunID)); len(prompts) != 0 {
		t.Fatalf("strict prompts = %q, want none", prompts)
	}
}

func TestMatchSubagentCarriesTheLeadsEffort(t *testing.T) {
	f := newCoordFixture(t)
	f.runtime.modelsSupported = true
	f.runtime.effortSupported = true
	efforts := []adapterproto.ModelOption{{ID: "low", Label: "Low"}, {ID: "high", Label: "High"}}
	lead, leadMailbox := f.spawnLead(t, domain.CoordinationPolicy{AllowedHarnesses: []string{"mock"}})
	leadThread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime.emitModels(leadThread.CurrentRunID, adapterproto.ModelSet{
		Current: "opus", Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}},
		Effort: &adapterproto.EffortSet{Current: "low", Options: efforts},
	})
	if err := f.service.SyncThread(f.ctx, leadThread.ID); err != nil {
		t.Fatal(err)
	}

	spawn := func(name string, request coordproto.Request) (domain.ThreadLink, domain.Thread) {
		t.Helper()
		request.Type, request.Key, request.Name, request.Prompt = coordproto.KindSpawn, "spawn-"+name, name, "task for "+name
		spawned := tool(t, leadMailbox, request)
		if !spawned.OK || spawned.Link == nil {
			t.Fatalf("spawn %s = %+v", name, spawned)
		}
		link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
		if err != nil {
			t.Fatal(err)
		}
		f.ready(t, link.ChildCapsuleID)
		child, err := f.service.GetThread(f.ctx, link.ChildThreadID)
		if err != nil {
			t.Fatal(err)
		}
		return link, child
	}
	setEfforts := func(run domain.RunID) []string {
		var sent []string
		for _, frame := range f.runtime.sentFrames(run) {
			if frame.Type == adapterproto.KindSetEffort {
				sent = append(sent, frame.Effort)
			}
		}
		return sent
	}

	// Match copies the lead's effort and applies it before the first prompt.
	matched, child := spawn("matched", coordproto.Request{})
	if matched.RequestedEffort != "low" {
		t.Fatalf("match link effort = %q, want the lead's", matched.RequestedEffort)
	}
	f.runtime.emitModels(child.CurrentRunID, adapterproto.ModelSet{
		Current: "opus", Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}},
		Effort: &adapterproto.EffortSet{Current: "high", Options: efforts},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	if sent := setEfforts(child.CurrentRunID); !slices.Equal(sent, []string{"low"}) {
		t.Fatalf("set_effort frames = %q, want low", sent)
	}
	if prompts := userPrompts(f.runtime.sentFrames(child.CurrentRunID)); len(prompts) != 0 {
		t.Fatalf("prompts = %q, want none before the effort applies", prompts)
	}
	f.service.CoordinationTick(asSystem(f.ctx))
	if sent := setEfforts(child.CurrentRunID); len(sent) != 1 {
		t.Fatalf("set_effort frames = %q, want it sent once", sent)
	}
	f.runtime.emitModels(child.CurrentRunID, adapterproto.ModelSet{
		Current: "opus", Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}},
		Effort: &adapterproto.EffortSet{Current: "low", Options: efforts},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	if prompts := userPrompts(f.runtime.sentFrames(child.CurrentRunID)); !slices.Equal(prompts, []string{"task for matched"}) {
		t.Fatalf("prompts = %q, want the task once the effort applies", prompts)
	}

	// A harness that never reports the level starts on its default after the wait.
	stuck, stuckChild := spawn("stuck", coordproto.Request{})
	f.runtime.emitModels(stuckChild.CurrentRunID, adapterproto.ModelSet{
		Current: "opus", Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}},
		Effort: &adapterproto.EffortSet{Current: "high", Options: efforts},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	f.advance(domain.CoordinationModelWait + time.Second)
	f.service.CoordinationTick(asSystem(f.ctx))
	if prompts := userPrompts(f.runtime.sentFrames(stuckChild.CurrentRunID)); !slices.Equal(prompts, []string{"task for stuck"}) {
		t.Fatalf("prompts = %q, want the task after the wait", prompts)
	}
	if stuck, err = f.service.GetThreadLink(f.ctx, stuck.ID); err != nil || stuck.State != domain.LinkActive {
		t.Fatalf("stuck link = %+v, %v, want active", stuck, err)
	}

	// A model without that level gets no set_effort and starts at once.
	unoffered, unofferedChild := spawn("unoffered", coordproto.Request{})
	f.runtime.emitModels(unofferedChild.CurrentRunID, adapterproto.ModelSet{
		Current: "opus", Options: []adapterproto.ModelOption{{ID: "opus", Label: "Opus 4.6"}},
		Effort: &adapterproto.EffortSet{Current: "high", Options: efforts[1:]},
	})
	f.service.CoordinationTick(asSystem(f.ctx))
	if sent := setEfforts(unofferedChild.CurrentRunID); len(sent) != 0 {
		t.Fatalf("set_effort frames = %q, want none", sent)
	}
	if prompts := userPrompts(f.runtime.sentFrames(unofferedChild.CurrentRunID)); !slices.Equal(prompts, []string{"task for unoffered"}) {
		t.Fatalf("prompts = %q, want the task at once", prompts)
	}
	_ = unoffered

	// An explicit model is not Match: no effort is copied.
	explicit, _ := spawn("explicit", coordproto.Request{Model: "opus"})
	if explicit.RequestedEffort != "" {
		t.Fatalf("explicit link effort = %q, want none", explicit.RequestedEffort)
	}
}
