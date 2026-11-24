package app_test

import (
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
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
