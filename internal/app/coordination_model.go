package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// knownHarnessModels returns the latest known ModelSet for harness in the
// lead's Project and whether it came from a live session. When harness
// matches the lead's own harness, the lead's live transcript takes
// precedence. A catalog from an ended session is only advisory: the harness
// may have changed since, so spawns are not refused against it.
func (s *Service) knownHarnessModels(
	ctx context.Context, projectID domain.ProjectID, lead domain.Thread, harness string,
) (*adapterproto.ModelSet, bool) {
	if harness == lead.AdapterID {
		if set, err := s.latestModelSet(ctx, lead); err == nil && set != nil {
			return set, threadLive(lead)
		}
	}
	set, source, err := s.latestHarnessModelSet(ctx, projectID, harness)
	if err != nil || set == nil {
		return nil, false
	}
	return set, threadLive(source)
}

func threadLive(thread domain.Thread) bool {
	return thread.State == domain.ThreadActive && thread.CurrentRunID != ""
}

// runtimeNegotiated reports whether a structured runtime has finished its
// hello handshake, so its Models flag is final.
func runtimeNegotiated(run ports.RuntimeRun) bool {
	return run.Negotiated || run.Models
}

func modelSupportedInSet(set *adapterproto.ModelSet, model string) bool {
	if set == nil || model == "" {
		return false
	}
	return set.Current == model || hasModelOption(set.Options, model)
}

func formatModelChoices(options []adapterproto.ModelOption) string {
	if len(options) == 0 {
		return ""
	}
	limit := min(len(options), 16)
	ids := make([]string, 0, limit)
	for _, option := range options[:limit] {
		ids = append(ids, option.ID)
	}
	suffix := ""
	if len(options) > limit {
		suffix = fmt.Sprintf(" (+%d more)", len(options)-limit)
	}
	return strings.Join(ids, ", ") + suffix
}

// resolveSpawnTarget applies the precedence rules for a lead's spawn request:
//  1. Explicit harness/model override on the spawn request
//  2. The grant's Worker Model setting (when Custom)
//  3. Match (the lead's harness, current model, and current effort)
//
// Invalid harnesses or models unsupported by the target harness fail with a
// clear error rather than falling back silently. The returned fallback is
// true only for a model Match chose: the lead did not ask for it, so if the
// subagent's harness cannot run it the subagent starts on the harness
// default instead of failing. effort is set only by Match, from the lead's
// current effort, and is applied best effort (ADR 0033 amendment).
func (s *Service) resolveSpawnTarget(
	ctx context.Context,
	grant domain.CoordinationGrant,
	parent domain.Thread,
	capsule domain.Capsule,
	request coordproto.Request,
) (harness, model, effort string, fallback bool, failure *coordproto.Response) {
	worker := grant.EffectiveWorkerModel()

	// 1. Resolve target harness.
	harness = request.Harness
	if harness == "" {
		if worker.Mode == domain.WorkerModelCustom && worker.Harness != "" {
			harness = worker.Harness
		} else {
			harness = parent.AdapterID
		}
	}
	if !slices.Contains(grant.AllowedHarnesses, harness) {
		message := fmt.Sprintf("harness %q is not allowed by the coordination grant; allowed harnesses: %s",
			harness, strings.Join(grant.AllowedHarnesses, ", "))
		resp := coordproto.Fail(coordproto.CodeHarnessNotAllowed, message)
		return "", "", "", false, &resp
	}

	// 2. Resolve target model.
	explicitModel := request.Model != ""
	switch {
	case explicitModel:
		model = request.Model
	case worker.Mode == domain.WorkerModelCustom:
		customHarness := worker.Harness
		if customHarness == "" {
			customHarness = parent.AdapterID
		}
		if harness == customHarness {
			model = worker.Model
		}
	default: // WorkerModelMatch
		if harness == parent.AdapterID {
			if leadSet, err := s.latestModelSet(ctx, parent); err == nil && leadSet != nil && leadSet.Current != "" {
				model, fallback = leadSet.Current, true
				if leadSet.Effort != nil {
					effort = leadSet.Effort.Current
				}
			}
		}
	}

	// 3. Synchronously validate against a live session's catalog for the
	// target harness. Without one, the subagent checks its own catalog
	// before its first prompt.
	if model != "" {
		set, live := s.knownHarnessModels(ctx, grant.ProjectID, parent, harness)
		switch {
		case set != nil && live:
			if !modelSupportedInSet(set, model) {
				choices := formatModelChoices(set.Options)
				message := fmt.Sprintf("model %q is not available for harness %q", model, harness)
				if choices != "" {
					message += "; available models: " + choices
				}
				resp := coordproto.Fail(coordproto.CodeModelUnavailable, message)
				return "", "", "", false, &resp
			}
		case set == nil && explicitModel && harness == parent.AdapterID && parent.CurrentRunID != "" && s.structured != nil:
			if runtimeRun, err := s.structured.GetStructured(ctx, capsule.ProviderResourceID, parent.CurrentRunID); err == nil &&
				runtimeNegotiated(runtimeRun) && !runtimeRun.Models {
				resp := coordproto.Fail(coordproto.CodeModelUnavailable,
					fmt.Sprintf("harness %q does not support model selection", harness))
				return "", "", "", false, &resp
			}
		}
	}

	return harness, model, effort, fallback, nil
}

// subagentEffortPending applies a Match link's RequestedEffort once its model
// is in effect, and reports whether the first prompt should wait for it.
// Effort is best effort: Match copied it from the lead, nobody asked for it,
// so a harness without effort, a level the model does not offer, a refused
// switch, or one not reported within CoordinationModelWait leaves the
// harness default and the prompt goes out anyway. It never fails the link.
func (s *Service) subagentEffortPending(
	ctx context.Context, link domain.ThreadLink, thread domain.Thread, runtimeRun ports.RuntimeRun,
	set *adapterproto.ModelSet, now time.Time,
) bool {
	effort := link.RequestedEffort
	if effort == "" || !runtimeRun.Effort || set.Effort == nil || set.Effort.Current == effort ||
		!hasModelOption(set.Effort.Options, effort) {
		return false
	}
	if link.EffortRequested {
		return now.Sub(link.ModelSelectingSince) <= domain.CoordinationModelWait
	}
	if _, err := s.SetThreadEffort(ctx, thread.ID, thread.ResourceVersion, effort,
		"coord-effort-"+string(link.ID)); err != nil {
		if errors.Is(err, domain.ErrUnsupported) || errors.Is(err, domain.ErrInvalid) {
			return false
		}
		// A transient failure retries on the next scan, within the same wait.
		since := link.ModelSelectingSince
		if since.IsZero() {
			since = link.UpdatedAt
		}
		return now.Sub(since) <= domain.CoordinationModelWait
	}
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThreadLink(ctx, link.ID)
		if err != nil {
			return err
		}
		if current.State != domain.LinkActive || current.EffortRequested {
			return nil
		}
		previous := current.ResourceVersion
		current.EffortRequested = true
		current.ModelSelectingSince = now
		current.UpdatedAt = now
		current.ResourceVersion++
		return tx.UpdateThreadLink(ctx, current, previous)
	})
	return true
}

// buildLeadWorkerInfo computes the current worker default and known models per
// allowed harness for the lead's mailbox and start brief.
func (s *Service) buildLeadWorkerInfo(
	ctx context.Context, grant domain.CoordinationGrant, lead domain.Thread,
) (*coordproto.WorkerDefaults, []coordproto.HarnessModels) {
	catalogs := make(map[string]*adapterproto.ModelSet, len(grant.AllowedHarnesses))
	available := make([]coordproto.HarnessModels, 0, len(grant.AllowedHarnesses))
	for _, harness := range grant.AllowedHarnesses {
		set, _ := s.knownHarnessModels(ctx, grant.ProjectID, lead, harness)
		catalogs[harness] = set
		entry := coordproto.HarnessModels{Harness: harness}
		if set != nil && len(set.Options) > 0 {
			limit := min(len(set.Options), coordproto.MaxHarnessModels)
			entry.Models = make([]coordproto.ModelOption, 0, limit)
			for _, option := range set.Options[:limit] {
				entry.Models = append(entry.Models, coordproto.ModelOption{
					ID:    option.ID,
					Label: boundedLabel(option.Label),
				})
			}
		}
		available = append(available, entry)
	}

	worker := grant.EffectiveWorkerModel()
	defaults := &coordproto.WorkerDefaults{Mode: string(worker.Mode)}
	if worker.Mode == domain.WorkerModelCustom {
		harness := worker.Harness
		if harness == "" {
			harness = lead.AdapterID
		}
		defaults.Harness = harness
		defaults.Model = worker.Model
		if set := catalogs[harness]; set != nil {
			defaults.ModelLabel = optionLabel(worker.Model, set.Options)
		} else {
			defaults.ModelLabel = boundedLabel(worker.Model)
		}
	} else {
		defaults.Harness = lead.AdapterID
		if set := catalogs[lead.AdapterID]; set != nil {
			defaults.Model = set.Current
			summary := summarizeThreadModels(set)
			defaults.ModelLabel = summary.Model
		}
	}
	return defaults, available
}

// buildCoordinationConfig builds the full mailbox configuration for a lead or
// subagent Capsule, including live worker defaults, known models, and note
// proposals.
func (s *Service) buildCoordinationConfig(
	ctx context.Context, grant domain.CoordinationGrant, role domain.CapsuleRole, threadID domain.ThreadID,
) coordproto.Config {
	config := coordinationConfigFor(grant, role)
	if role == domain.CapsuleMain {
		var lead domain.Thread
		if err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			lead, err = reader.GetThread(ctx, grant.RootThreadID)
			return err
		}); err == nil {
			config.Self.WorkerDefaults, config.Self.AvailableModels = s.buildLeadWorkerInfo(ctx, grant, lead)
		}
	}
	if threadID != "" {
		config.Self.NoteProposals = s.noteProposalRefs(ctx, threadID)
	}
	return config
}

// leadWorkerBrief builds the bounded worker-options metadata for a lead
// Thread's Start or Resume frame.
func (s *Service) leadWorkerBrief(ctx context.Context, thread domain.Thread) *adapterproto.LeadWorkerBrief {
	var grant domain.CoordinationGrant
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrantByRootThread(ctx, thread.ID)
		return err
	}); err != nil || grant.State != domain.GrantActive {
		return nil
	}
	defaults, available := s.buildLeadWorkerInfo(ctx, grant, thread)
	brief := &adapterproto.LeadWorkerBrief{
		AllowedHarnesses: slices.Clone(grant.AllowedHarnesses),
	}
	if defaults != nil {
		brief.Mode = defaults.Mode
		brief.DefaultHarness = defaults.Harness
		brief.DefaultModel = defaults.Model
		brief.DefaultLabel = defaults.ModelLabel
	}
	for _, hm := range available {
		entry := adapterproto.WorkerHarnessModels{Harness: hm.Harness}
		limit := min(len(hm.Models), 64)
		for _, opt := range hm.Models[:limit] {
			entry.Models = append(entry.Models, opt.ID)
		}
		brief.AvailableModels = append(brief.AvailableModels, entry)
	}
	if !brief.Valid() {
		// Trim model lists if a very large multi-harness catalog exceeds MaxWorkerBriefBytes.
		brief.AvailableModels = nil
		if !brief.Valid() {
			return nil
		}
	}
	return brief
}

// refreshLeadMailboxForModels re-pushes the lead's mailbox configuration when
// the lead reports a new model set (so Match mode and availableModels stay
// current mid-session).
func (s *Service) refreshLeadMailboxForModels(ctx context.Context, threadID domain.ThreadID) {
	if s.coordination == nil {
		return
	}
	var grant domain.CoordinationGrant
	var capsule domain.Capsule
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrantByRootThread(ctx, threadID)
		if err != nil {
			return err
		}
		capsule, err = reader.GetCapsule(ctx, grant.RootCapsuleID)
		return err
	}); err != nil || grant.State != domain.GrantActive {
		return
	}
	if capsule.State != domain.CapsuleReady || capsule.ProviderResourceID == "" {
		return
	}
	config := s.buildCoordinationConfig(ctx, grant, domain.CapsuleMain, threadID)
	_ = s.coordination.ConfigureCoordination(ctx, capsule.ProviderResourceID, config)
}

// advanceSubagentModel drives strict pre-prompt model selection for a newly
// started subagent link. It returns true while model selection is still in
// progress so scanLinkReports does not treat the unprompted subagent as idle.
// Nothing ever falls back silently to a different model.
//
// When the harness cannot run the requested model, a Match link
// (ModelFallback) starts on the harness default; any other link fails
// without its prompt.
//
// The wait for the harness to report and switch models starts once its
// runtime has finished the hello handshake. Every retry path is bounded by
// CoordinationModelStall, so a persistent error fails the link instead of
// holding the subagent in provisioning forever.
func (s *Service) advanceSubagentModel(ctx context.Context, link domain.ThreadLink) bool {
	if link.State != domain.LinkActive ||
		(link.ModelState != domain.LinkModelPending && link.ModelState != domain.LinkModelRequested) {
		return false
	}
	if link.RequestedModel == "" {
		return false
	}
	now := s.clock.Now().UTC()
	since := link.ModelSelectingSince
	if since.IsZero() {
		since = link.UpdatedAt
	}
	retry := func() bool {
		if now.Sub(since) > domain.CoordinationModelStall {
			s.failSubagentModel(ctx, link)
			return false
		}
		return true
	}
	var intent domain.ProjectThreadIntent
	var thread domain.Thread
	var capsule domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if intent, err = reader.GetProjectThreadIntent(ctx, link.ChildIntentID); err != nil {
			return err
		}
		if intent.State != domain.ProjectThreadReady {
			return nil
		}
		if thread, err = reader.GetThread(ctx, link.ChildThreadID); err != nil {
			return err
		}
		capsule, err = reader.GetCapsule(ctx, link.ChildCapsuleID)
		return err
	})
	// Provisioning has its own bounds; the selection clock starts at
	// promotion.
	if err == nil && intent.State == domain.ProjectThreadProvisioning {
		return true
	}
	if err != nil {
		return retry()
	}
	if intent.State == domain.ProjectThreadFailed {
		return false
	}
	if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return false
	}
	if run, err := s.GetRunStored(ctx, thread.CurrentRunID); err == nil && run.State.Terminal() {
		return false
	}
	if capsule.State != domain.CapsuleReady || capsule.ProviderResourceID == "" {
		return retry()
	}
	_ = s.SyncThread(ctx, thread.ID)
	if fresh, err := s.GetThread(ctx, thread.ID); err == nil {
		thread = fresh
	}
	if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return false
	}
	unavailable := func() bool {
		if !link.ModelFallback {
			s.failSubagentModel(ctx, link)
			return false
		}
		if s.deliverSubagentInitialPrompt(ctx, link, intent, thread, domain.LinkModelDefault) {
			return false
		}
		return retry()
	}
	runtimeRun, err := s.structured.GetStructured(ctx, capsule.ProviderResourceID, thread.CurrentRunID)
	if err != nil {
		return retry()
	}
	if runtimeRun.State.Terminal() {
		return false
	}
	if !runtimeNegotiated(runtimeRun) {
		// The adapter is still starting; its Models flag reads false until
		// the handshake finishes, and the capsuled handshake timeout bounds
		// this wait. Restart the model clock so a slow start does not use up
		// the time the harness has to report models.
		s.restartSubagentModelClock(ctx, link, now)
		return true
	}
	if !runtimeRun.Models {
		return unavailable()
	}
	timedOut := now.Sub(since) > domain.CoordinationModelWait

	set, err := s.latestModelSet(ctx, thread)
	if err != nil {
		return retry()
	}
	if set == nil {
		if timedOut {
			return unavailable()
		}
		return true
	}
	if set.Current == link.RequestedModel {
		if s.subagentEffortPending(ctx, link, thread, runtimeRun, set, now) {
			return true
		}
		if s.deliverSubagentInitialPrompt(ctx, link, intent, thread, domain.LinkModelApplied) {
			return false
		}
		return retry()
	}
	if link.ModelState == domain.LinkModelRequested {
		if s.subagentModelRefused(ctx, thread) || timedOut {
			return unavailable()
		}
		return true
	}
	if !hasModelOption(set.Options, link.RequestedModel) {
		return unavailable()
	}
	if _, err := s.SetThreadModel(ctx, thread.ID, thread.ResourceVersion, link.RequestedModel,
		"coord-model-"+string(link.ID)); err != nil {
		if errors.Is(err, domain.ErrUnsupported) || errors.Is(err, domain.ErrInvalid) {
			return unavailable()
		}
		return retry()
	}
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThreadLink(ctx, link.ID)
		if err != nil {
			return err
		}
		if current.State != domain.LinkActive || current.ModelState != domain.LinkModelPending {
			return nil
		}
		previous := current.ResourceVersion
		current.ModelState = domain.LinkModelRequested
		current.ModelSelectingSince = now
		current.UpdatedAt = now
		current.ResourceVersion++
		return tx.UpdateThreadLink(ctx, current, previous)
	})
	return true
}

// restartSubagentModelClock moves a selecting link's model clock to now.
func (s *Service) restartSubagentModelClock(ctx context.Context, link domain.ThreadLink, now time.Time) {
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThreadLink(ctx, link.ID)
		if err != nil {
			return err
		}
		if current.State != domain.LinkActive ||
			(current.ModelState != domain.LinkModelPending && current.ModelState != domain.LinkModelRequested) ||
			!current.ModelSelectingSince.Before(now) {
			return nil
		}
		previous := current.ResourceVersion
		current.ModelSelectingSince = now
		current.UpdatedAt = now
		current.ResourceVersion++
		return tx.UpdateThreadLink(ctx, current, previous)
	})
}

// deliverSubagentInitialPrompt sends a selecting subagent its held first
// prompt and records how selection ended: applied, or default when a Match
// link fell back to the harness default.
func (s *Service) deliverSubagentInitialPrompt(
	ctx context.Context, link domain.ThreadLink, intent domain.ProjectThreadIntent, thread domain.Thread,
	outcome domain.LinkModelState,
) bool {
	var payload persistedThreadBlock
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		payload, err = s.pendingBlock(ctx, reader, intent)
		return err
	}); err != nil {
		return false
	}
	if payload.Content == "" {
		_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
			current, err := tx.GetThreadLink(ctx, link.ID)
			if err != nil || current.State != domain.LinkActive {
				return err
			}
			previous := current.ResourceVersion
			current.ModelState = outcome
			current.UpdatedAt = s.clock.Now().UTC()
			current.ResourceVersion++
			return tx.UpdateThreadLink(ctx, current, previous)
		})
		s.invalidateCoordinationPush(link.ParentThreadID)
		return true
	}
	frame := func(messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock) adapterproto.Frame {
		return adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: string(messageID), SessionID: string(thread.ID), Role: adapterproto.RoleUser,
			Content: saved.Content,
		}
	}
	key := "coord-init-" + string(link.ID)
	_, err := s.appendAndSendHook(ctx, thread.ID, 0, key, "coordination",
		domain.ThreadRoleUser, domain.ThreadMessagePrompt, domain.ThreadBlockText, payload, frame,
		func(tx ports.Transaction, _ domain.Thread, _ domain.ThreadMessageID, now time.Time) error {
			current, err := tx.GetThreadLink(ctx, link.ID)
			if err != nil {
				return err
			}
			if current.State != domain.LinkActive {
				return errCoordinationRejected
			}
			previous := current.ResourceVersion
			current.ModelState = outcome
			current.UpdatedAt = now
			current.ResourceVersion++
			return tx.UpdateThreadLink(ctx, current, previous)
		})
	if err != nil {
		return false
	}
	s.invalidateCoordinationPush(link.ParentThreadID)
	return true
}

func (s *Service) failSubagentModel(ctx context.Context, link domain.ThreadLink) {
	s.recordLinkReport(ctx, link, true, domain.LinkFailed, domain.ReportFailed, domain.ReportModelUnavailable, 0)
	if fresh, err := s.GetThreadLink(ctx, link.ID); err == nil {
		s.stopClosedChild(ctx, fresh)
	}
}

// subagentModelRefused reports whether the harness emitted a model_unavailable
// error after the last set_model request.
func (s *Service) subagentModelRefused(ctx context.Context, thread domain.Thread) bool {
	if s.transcriptKey == nil || thread.MessageCount == 0 {
		return false
	}
	dek, err := s.unwrapThread(thread)
	if err != nil {
		return false
	}
	defer wipe(dek)
	after := max(thread.MessageCount-threadAttentionWindow, 0)
	var messages []domain.ThreadMessage
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, thread.ID, after, threadAttentionWindow)
		return err
	}); err != nil {
		return false
	}
	refused := false
	for _, message := range messages {
		plaintext, err := transcripts.DecryptMessage(dek, message)
		if err != nil {
			return false
		}
		for _, raw := range plaintext {
			var payload persistedThreadBlock
			if json.Unmarshal(raw, &payload) != nil || payload.Event == nil {
				continue
			}
			switch payload.Event.Type {
			case adapterproto.KindSetModel:
				refused = false
			case adapterproto.KindError:
				if payload.Event.Code == "model_unavailable" {
					refused = true
				}
			}
		}
		transcripts.ZeroPlaintextBlocks(plaintext)
	}
	return refused
}
