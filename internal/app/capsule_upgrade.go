package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const (
	maintenanceUpgrading = "upgrading"
	upgradeScanBatchSize = 100
)

type capsuleUpgradeNotice struct {
	FromRef string
	ToRef   string
}

// ConfigureImageUpgrade enables in-place official pack image upgrades
// (ADR 0064). prepuller may be nil (for providers like Agent Sandbox where
// node kubelets pull images).
func (s *Service) ConfigureImageUpgrade(
	upgrader ports.ImageUpgrader,
	prepuller ports.ImagePrepuller,
	stopGitHubRefresh func(string),
) {
	s.imageUpgrader = upgrader
	s.imagePrepuller = prepuller
	s.upgradeHook = stopGitHubRefresh
}

// RecoverUpgradingCapsules clears any stale "upgrading" maintenance flag left
// behind if meridiand restarted mid-upgrade (ADR 0064).
func (s *Service) RecoverUpgradingCapsules(ctx context.Context) error {
	if err := requireSystem(ctx); err != nil {
		return err
	}
	projects, err := s.listAllProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		capsules, err := s.listAllProjectCapsules(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, capsule := range capsules {
			if capsule.Maintenance == maintenanceUpgrading {
				_ = s.clearMaintenance(ctx, capsule.ID, maintenanceUpgrading)
			}
		}
	}
	return nil
}

// UpgradeCapsule performs an operator-requested in-place upgrade of an idle
// Capsule to the daemon's current official pack image (ADR 0064).
func (s *Service) UpgradeCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	key string,
) (domain.Capsule, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Capsule{}, err
	}
	if s.imageUpgrader == nil {
		return domain.Capsule{}, domain.ErrUnsupported
	}
	if expected <= 0 {
		return domain.Capsule{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Capsule{}, err
	}
	scope := "capsule:" + string(id) + ":upgrade"
	var replay domain.Capsule
	var replayed bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		record, ok, err := getReplay[domain.Capsule](ctx, reader, scope, key)
		if err != nil {
			return err
		}
		if ok {
			replay, replayed = record, true
		}
		return nil
	}); err != nil {
		return domain.Capsule{}, err
	}
	if replayed {
		return s.GetCapsule(ctx, replay.ID)
	}
	updated, err := s.upgradeCapsule(ctx, id, expected, true)
	if err != nil {
		return domain.Capsule{}, err
	}
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		return putReplay(ctx, tx, scope, key, updated, s.clock.Now().UTC())
	})
	return updated, nil
}

// ScanUpgradableCapsules upgrades Paused and strictly idle Ready Capsules that
// belong to an outdated official pack (ADR 0064). On Docker, a background pass
// upgrades only after the target image is already available locally.
func (s *Service) ScanUpgradableCapsules(ctx context.Context, logger *log.Logger) (int, error) {
	if err := requireSystem(ctx); err != nil {
		return 0, err
	}
	if s.imageUpgrader == nil || len(s.harnessPacks) == 0 {
		return 0, nil
	}
	projects, err := s.listAllProjects(ctx)
	if err != nil {
		return 0, err
	}
	upgraded := 0
	for _, project := range projects {
		if ctx.Err() != nil {
			return upgraded, ctx.Err()
		}
		capsules, err := s.listAllProjectCapsules(ctx, project.ID)
		if err != nil {
			return upgraded, err
		}
		for _, capsule := range capsules {
			if ctx.Err() != nil {
				return upgraded, ctx.Err()
			}
			if capsule.Maintenance != "" || capsule.ProviderResourceID == "" {
				continue
			}
			isPaused := capsule.State == domain.CapsulePaused && capsule.DesiredState == domain.IntentPaused
			isReady := capsule.State == domain.CapsuleReady && capsule.DesiredState == domain.IntentReady
			if !isPaused && !isReady {
				continue
			}
			target, ok := domain.OfficialPackUpgradeTarget(s.harnessPacks, project, capsule.ImageReference)
			if !ok {
				continue
			}
			if s.imagePrepuller != nil {
				available, availErr := s.imagePrepuller.ImageAvailable(ctx, target.ImageReference)
				if availErr != nil || !available {
					continue
				}
			}
			if _, err := s.upgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, false); err != nil {
				if !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrIllegalTransition) && logger != nil && ctx.Err() == nil {
					logger.Printf("background Capsule upgrade skipped for %s: %v", capsule.ID, err)
				}
				continue
			}
			upgraded++
		}
	}
	return upgraded, nil
}

func (s *Service) upgradeCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	manual bool,
) (domain.Capsule, error) {
	if s.imageUpgrader == nil {
		return domain.Capsule{}, domain.ErrUnsupported
	}
	var capsule domain.Capsule
	var project domain.Project
	var target domain.HarnessPack
	var leadGrantID domain.CoordinationGrantID
	var leadThreadID domain.ThreadID

	// Pre-check eligibility and expected version before stopping any idle
	// structured Thread session, so an ineligible or stale request never
	// disturbs a running session.
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		current, err := reader.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if expected > 0 && current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		proj, err := reader.GetProject(ctx, current.ProjectID)
		if err != nil {
			return err
		}
		if _, ok := domain.OfficialPackUpgradeTarget(s.harnessPacks, proj, current.ImageReference); !ok {
			return fmt.Errorf("%w: Capsule is already current or does not use an official pack", domain.ErrIllegalTransition)
		}
		if proj.RequiredIsolation != "" && !s.isolation.Satisfies(proj.RequiredIsolation) {
			return fmt.Errorf("%w: provider isolation %q cannot satisfy Project requirement %q",
				domain.ErrUnsupported, s.isolation, proj.RequiredIsolation)
		}
		isPaused := current.State == domain.CapsulePaused && current.DesiredState == domain.IntentPaused
		isReady := current.State == domain.CapsuleReady && current.DesiredState == domain.IntentReady
		if (!isPaused && !isReady) || current.Maintenance != "" || current.ProviderResourceID == "" {
			return fmt.Errorf("%w: Capsule must be idle Ready or Paused to upgrade", domain.ErrIllegalTransition)
		}
		return nil
	}); err != nil {
		return domain.Capsule{}, err
	}

	doneCh := s.beginUpgradeWait(id)
	waitEnded := false
	finishWait := func() {
		if !waitEnded {
			waitEnded = true
			s.endUpgradeWait(id, doneCh)
		}
	}
	defer finishWait()

	// If the Capsule has an open structured Thread session that is currently
	// idle (no active turn, no pending operator prompt, no active subagents),
	// stop it cleanly before swapping the container/Pod and resume it on the
	// upgraded image afterwards (ADR 0064).
	resumedThreadID, stopErr := s.stopIdleThreadForUpgrade(ctx, id)
	if stopErr != nil {
		return domain.Capsule{}, stopErr
	}

	acquired := false
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		capsule, err = tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if expected > 0 && capsule.ResourceVersion != expected {
			return domain.ErrConflict
		}
		project, err = tx.GetProject(ctx, capsule.ProjectID)
		if err != nil {
			return err
		}
		var ok bool
		target, ok = domain.OfficialPackUpgradeTarget(s.harnessPacks, project, capsule.ImageReference)
		if !ok {
			return fmt.Errorf("%w: Capsule is already current or does not use an official pack", domain.ErrIllegalTransition)
		}
		// Re-verify Project isolation class fail-closed (ADR 0050).
		if project.RequiredIsolation != "" && !s.isolation.Satisfies(project.RequiredIsolation) {
			return fmt.Errorf("%w: provider isolation %q cannot satisfy Project requirement %q",
				domain.ErrUnsupported, s.isolation, project.RequiredIsolation)
		}
		if capsule.ProviderResourceID == "" {
			return fmt.Errorf("%w: Capsule runtime is not provisioned", domain.ErrIllegalTransition)
		}
		isPaused := capsule.State == domain.CapsulePaused && capsule.DesiredState == domain.IntentPaused
		isReady := capsule.State == domain.CapsuleReady && capsule.DesiredState == domain.IntentReady
		if (!isPaused && !isReady) || capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule must be idle Ready or Paused to upgrade", domain.ErrIllegalTransition)
		}
		if err := s.checkUpgradeQuiescenceTx(ctx, tx, capsule, &leadGrantID, &leadThreadID); err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.Maintenance = maintenanceUpgrading
		capsule.UpdatedAt = s.clock.Now().UTC()
		capsule.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, capsule, previous); err != nil {
			return err
		}
		acquired = true
		return nil
	})
	if err != nil {
		return domain.Capsule{}, err
	}
	if !acquired {
		return domain.Capsule{}, domain.ErrConflict
	}

	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			_ = s.clearMaintenance(principal.WithSystem(context.WithoutCancel(ctx)), id, maintenanceUpgrading)
		}
	}()

	if s.upgradeHook != nil {
		s.upgradeHook(capsule.ProviderResourceID)
	}

	previousRef := capsule.ImageReference
	res, err := s.imageUpgrader.UpgradeImage(ctx, ports.UpgradeCapsuleRequest{
		CapsuleID:      capsule.ID,
		ResourceID:     capsule.ProviderResourceID,
		ImageReference: target.ImageReference,
		Paused:         capsule.State == domain.CapsulePaused,
	})
	if err != nil {
		if !manual {
			return domain.Capsule{}, err
		}
		return domain.Capsule{}, err
	}

	var updated domain.Capsule
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if current.Maintenance != maintenanceUpgrading {
			return domain.ErrConflict
		}
		previous := current.ResourceVersion
		now := s.clock.Now().UTC()
		current.ImageReference = target.ImageReference
		current.Maintenance = ""
		current.UpdatedAt = now
		current.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, current, previous); err != nil {
			return err
		}
		if prep, prepErr := tx.GetPreparation(ctx, id); prepErr == nil && res.ImageDigest != "" {
			prep.ImageDigest = res.ImageDigest
			_ = tx.PutPreparation(ctx, prep)
		}
		metadata, _ := json.Marshal(map[string]any{
			"previousImageReference": previousRef,
			"imageReference":         target.ImageReference,
			"imageDigest":            res.ImageDigest,
		})
		if err := s.appendEvent(ctx, tx, "capsule", string(id), "capsule.image_upgraded", current.ResourceVersion, metadata); err != nil {
			return err
		}
		updated = current
		return nil
	})
	if err != nil {
		return domain.Capsule{}, err
	}
	cleanupNeeded = false
	s.recordPendingUpgradeFallback(id, previousRef, target.ImageReference)
	// Force a fresh mailbox push to the upgraded capsuled instance for leads.
	if leadThreadID != "" {
		s.coordMu.Lock()
		delete(s.coordPushed, leadThreadID)
		s.coordMu.Unlock()
	}
	// Release upgrade waiters before resuming the Thread so ResumeThread does
	// not block on waitForCapsuleUpgrade.
	finishWait()
	if resumedThreadID != "" && updated.State == domain.CapsuleReady {
		if freshThread, getErr := s.GetThread(principal.WithSystem(ctx), resumedThreadID); getErr == nil && freshThread.State == domain.ThreadPaused {
			_, _ = s.ResumeThread(
				principal.WithSystem(ctx),
				resumedThreadID,
				freshThread.ResourceVersion,
				"upgrade-resume-"+string(updated.ID)+"-"+fmt.Sprintf("%d", updated.ResourceVersion),
			)
		}
	}
	_ = leadGrantID
	return updated, nil
}

// stopIdleThreadForUpgrade stops an open structured Thread session on capsule
// id when it is genuinely idle (no active turn, no pending permission/input
// request, and for a lead, no active subagents or unread reports). Returns the
// stopped Thread's ID so upgradeCapsule can resume it on the upgraded runtime.
func (s *Service) stopIdleThreadForUpgrade(ctx context.Context, id domain.CapsuleID) (domain.ThreadID, error) {
	if s.structured == nil {
		return "", nil
	}
	var thread domain.Thread
	var capsule domain.Capsule
	var found bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsule, err = reader.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		thread, err = reader.GetActiveThread(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	}); err != nil || !found {
		return "", err
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" {
		return "", nil
	}
	if thread.CurrentRunID == "" {
		return "", nil
	}
	// Check lead subagent quiescence before stopping the session.
	var busyErr error
	_ = s.store.View(ctx, func(reader ports.Reader) error {
		if grant, err := reader.GetCoordinationGrantByRootCapsule(ctx, id); err == nil {
			links, listErr := reader.ListThreadLinksByParent(ctx, grant.RootThreadID)
			if listErr != nil {
				busyErr = listErr
				return nil
			}
			for _, link := range links {
				if link.State == domain.LinkActive || link.State == domain.LinkPendingApproval {
					busyErr = fmt.Errorf("%w: lead has active or pending subagents", domain.ErrConflict)
					return nil
				}
				if link.ReportSeq > link.ReportHeadlinedSeq || link.OperatorMessages > link.OperatorNoticed {
					busyErr = fmt.Errorf("%w: lead has pending subagent reports", domain.ErrConflict)
					return nil
				}
			}
		}
		return nil
	})
	if busyErr != nil {
		return "", busyErr
	}

	sysCtx := principal.WithSystem(ctx)
	if err := s.SyncThread(sysCtx, thread.ID); err != nil {
		return "", err
	}
	unlock := s.lockThreadSync(thread.ID)
	defer unlock()
	thread, err := s.GetThread(sysCtx, thread.ID)
	if err != nil || thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return "", err
	}
	attention, err := s.ThreadAttention(sysCtx, thread)
	if err != nil {
		return "", err
	}
	if attention.Turn == ThreadTurnWorking || attention.Awaiting != nil {
		return "", fmt.Errorf("%w: Capsule has an active Thread turn or pending prompt", domain.ErrConflict)
	}
	run, err := s.GetRunStored(sysCtx, thread.CurrentRunID)
	if err != nil || run.State.Terminal() {
		return "", err
	}
	if _, err := s.transitionRun(sysCtx, run.ID, domain.RunCancelling, nil); err != nil {
		return "", err
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(sysCtx), harnessCancelWait)
	runtimeRun, cancelErr := s.structured.CancelStructured(cancelCtx, capsule.ProviderResourceID, run.ID)
	cancel()
	if cancelErr != nil || !runtimeRun.State.Terminal() {
		_, _ = s.forceEndRun(sysCtx, run.ID, thread.ID)
		return "", fmt.Errorf("%w: could not stop idle Thread session cleanly before upgrade", domain.ErrConflict)
	}
	if _, err := s.transitionRun(sysCtx, run.ID, domain.RunCancelled, nil); err != nil {
		return "", err
	}
	if err := s.pauseCompletedThread(sysCtx, thread.ID, run.ID); err != nil {
		return "", err
	}
	return thread.ID, nil
}

func (s *Service) checkUpgradeQuiescenceTx(
	ctx context.Context,
	tx ports.Transaction,
	capsule domain.Capsule,
	leadGrantID *domain.CoordinationGrantID,
	leadThreadID *domain.ThreadID,
) error {
	activeRun, err := tx.HasActiveRun(ctx, capsule.ID)
	if err != nil {
		return err
	}
	if activeRun {
		return fmt.Errorf("%w: Capsule has an active Run", domain.ErrConflict)
	}
	if _, err := tx.GetActiveThread(ctx, capsule.ID); err == nil {
		return fmt.Errorf("%w: Capsule has an active Thread session", domain.ErrConflict)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if _, err := tx.ActiveCheckRun(ctx, capsule.ID); err == nil {
		return fmt.Errorf("%w: Capsule has an active Project check run", domain.ErrConflict)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	// Standing leads are only idle when no subagents are running/starting or
	// waiting to wake the lead with unread reports or operator messages (ADR 0064).
	if grant, err := tx.GetCoordinationGrantByRootCapsule(ctx, capsule.ID); err == nil {
		*leadGrantID = grant.ID
		*leadThreadID = grant.RootThreadID
		links, err := tx.ListThreadLinksByParent(ctx, grant.RootThreadID)
		if err != nil {
			return err
		}
		for _, link := range links {
			if link.State == domain.LinkActive || link.State == domain.LinkPendingApproval {
				return fmt.Errorf("%w: lead has active or pending subagents", domain.ErrConflict)
			}
			if link.ReportSeq > link.ReportHeadlinedSeq || link.OperatorMessages > link.OperatorNoticed {
				return fmt.Errorf("%w: lead has pending subagent reports", domain.ErrConflict)
			}
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return nil
}

func (s *Service) beginUpgradeWait(id domain.CapsuleID) chan struct{} {
	ch := make(chan struct{})
	s.upgradeMu.Lock()
	if s.upgrading == nil {
		s.upgrading = make(map[domain.CapsuleID]chan struct{})
	}
	s.upgrading[id] = ch
	s.upgradeMu.Unlock()
	return ch
}

func (s *Service) endUpgradeWait(id domain.CapsuleID, ch chan struct{}) {
	s.upgradeMu.Lock()
	if s.upgrading[id] == ch {
		delete(s.upgrading, id)
	}
	s.upgradeMu.Unlock()
	close(ch)
}

// waitForCapsuleUpgrade pauses callers (such as Thread sends or resumes) while
// a background image upgrade is in flight so they proceed seamlessly instead of
// receiving a 409 Conflict (ADR 0064).
func (s *Service) waitForCapsuleUpgrade(ctx context.Context, id domain.CapsuleID) error {
	for {
		s.upgradeMu.Lock()
		ch := s.upgrading[id]
		s.upgradeMu.Unlock()
		if ch == nil {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Service) recordPendingUpgradeFallback(id domain.CapsuleID, fromRef, toRef string) {
	s.upgradeMu.Lock()
	if s.upgradeNotice == nil {
		s.upgradeNotice = make(map[domain.CapsuleID]capsuleUpgradeNotice)
	}
	s.upgradeNotice[id] = capsuleUpgradeNotice{FromRef: fromRef, ToRef: toRef}
	s.upgradeMu.Unlock()
}

func (s *Service) clearPendingUpgradeFallback(id domain.CapsuleID) {
	s.upgradeMu.Lock()
	delete(s.upgradeNotice, id)
	s.upgradeMu.Unlock()
}

func (s *Service) consumePendingUpgradeFallback(id domain.CapsuleID) (capsuleUpgradeNotice, bool) {
	s.upgradeMu.Lock()
	notice, ok := s.upgradeNotice[id]
	if ok {
		delete(s.upgradeNotice, id)
	}
	s.upgradeMu.Unlock()
	return notice, ok
}

func isHarnessResumeFailureCode(code string) bool {
	switch code {
	case "claude_resume_failed", "codex_resume_failed", "acp_resume_failed", "pi_resume_failed", "pi_resume_invalid":
		return true
	default:
		return false
	}
}

// retryFreshSessionAfterUpgrade recovers from the rare case where a harness
// refuses to resume its on-disk session after an image upgrade (or on a legacy
// Capsule's first migration off tmpfs/EmptyDir). It appends a quiet transcript
// divider and starts a fresh KindStart Run on the same Thread, keeping the
// standing-lead grant, subagents, working notes, and transcript intact (ADR 0064).
func (s *Service) retryFreshSessionAfterUpgrade(
	ctx context.Context,
	threadID domain.ThreadID,
	notice capsuleUpgradeNotice,
) error {
	fromLabel := domain.ImageReleaseTag(notice.FromRef)
	toLabel := domain.ImageReleaseTag(notice.ToRef)
	summary := fmt.Sprintf("Upgraded Capsule from %s to %s — harness started a fresh session", fromLabel, toLabel)

	var thread domain.Thread
	var run domain.Run
	var undelivered domain.ThreadMessageID
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		thread, err = tx.GetThread(ctx, threadID)
		if err != nil {
			return err
		}
		if thread.State != domain.ThreadPaused {
			return nil
		}
		dek, err := s.unwrapThread(thread)
		if err != nil {
			return err
		}
		defer wipe(dek)
		now := s.clock.Now().UTC()
		divider := persistedThreadBlock{
			Event: &ThreadAdapterEvent{
				Type:    adapterproto.KindStatus,
				Summary: summary,
			},
		}
		dividerMsg, err := s.encryptThreadMessage(
			dek, thread, domain.ThreadMessageID(s.ids.NewID()), thread.MessageCount+1,
			domain.ThreadRoleSystem, domain.ThreadMessageStatus, domain.ThreadBlockJSON, divider, now,
		)
		if err != nil {
			return err
		}
		if err := tx.AppendThreadMessage(ctx, dividerMsg); err != nil {
			return err
		}
		thread, err = tx.GetThread(ctx, threadID)
		if err != nil {
			return err
		}
		run = domain.Run{
			ID: domain.RunID(s.ids.NewID()), CapsuleID: thread.CapsuleID, Harness: thread.AdapterID,
			State: domain.RunQueued, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertRun(ctx, run); err != nil {
			return err
		}
		previous := thread.ResourceVersion
		thread.CurrentRunID = run.ID
		if err := thread.Transition(domain.ThreadActive, now); err != nil {
			return err
		}
		if err := tx.UpdateThread(ctx, thread, previous); err != nil {
			return err
		}
		if message, msgErr := tx.FindUndeliveredUserMessage(ctx, threadID); msgErr == nil {
			undelivered = message.ID
			_ = tx.InsertThreadDelivery(ctx, domain.ThreadDelivery{
				ThreadID: threadID, MessageID: message.ID, RunID: run.ID,
				ControllerID: string(message.ID), CreatedAt: now,
			})
		}
		return nil
	})
	if err != nil || run.ID == "" {
		return err
	}
	startedRun, err := s.startStructuredRuntime(ctx, thread, run, false)
	if err != nil {
		return err
	}
	if undelivered != "" {
		if msg, findErr := s.findThreadMessage(ctx, threadID, undelivered); findErr == nil {
			_ = s.deliverThreadMessage(ctx, thread, startedRun, msg, func(saved persistedThreadBlock) adapterproto.Frame {
				return userMessageFrame(msg.ID, thread.ID, saved)
			})
		}
	}
	return nil
}

func (s *Service) listAllProjects(ctx context.Context) ([]domain.Project, error) {
	var all []domain.Project
	offset := 0
	for {
		var batch []domain.Project
		var more bool
		err := s.store.View(ctx, func(reader ports.Reader) error {
			var listErr error
			batch, more, listErr = reader.ListProjects(ctx, "", ports.Page{Offset: offset, Limit: upgradeScanBatchSize})
			return listErr
		})
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !more || len(batch) == 0 {
			break
		}
		offset += len(batch)
	}
	return all, nil
}

func (s *Service) listAllProjectCapsules(ctx context.Context, projectID domain.ProjectID) ([]domain.Capsule, error) {
	var all []domain.Capsule
	offset := 0
	for {
		var batch []domain.Capsule
		var more bool
		err := s.store.View(ctx, func(reader ports.Reader) error {
			var listErr error
			batch, more, listErr = reader.ListCapsules(ctx, projectID, "", ports.Page{Offset: offset, Limit: upgradeScanBatchSize})
			return listErr
		})
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !more || len(batch) == 0 {
			break
		}
		offset += len(batch)
	}
	return all, nil
}
