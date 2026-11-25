package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const (
	maintenanceUpgrading = "upgrading"
	upgradeScanBatchSize = 100
	// upgradeIdleGrace is how long a Ready Capsule must go without recorded
	// activity (a Run, a preview request, a file or terminal operation)
	// before a background pass upgrades it, so a dev server or background
	// process the operator is still using is not restarted (ADR 0064).
	upgradeIdleGrace = 15 * time.Minute
)

type capsuleUpgradeNotice struct {
	FromRef string
	ToRef   string
}

// threadRebase records the Thread an upgrade stopped and resumed, and its
// version before and after, so a send that waited on the upgrade with the
// version from before it still applies (ADR 0064).
type threadRebase struct {
	thread domain.ThreadID
	// from is the version before the upgrade stopped the session, paused the
	// version while it was stopped, and to the version once it resumed.
	from, paused, to domain.ResourceVersion
}

// upgradeStartedEvent records an upgrade claiming a Capsule, and the idle
// Thread session it stopped, so startup recovery can resume that session
// after a restart (ADR 0064).
const upgradeStartedEvent = "capsule.upgrade_started"

type upgradeStartedData struct {
	StoppedThreadID domain.ThreadID `json:"stoppedThreadId,omitempty"`
}

// upgradeResumeWait bounds how long startup recovery waits for a settled
// Capsule's runtime to answer before resuming the session the upgrade
// stopped; past it the Thread stays Paused for the operator.
const upgradeResumeWait = 3 * time.Minute

// stoppedSession is the idle Thread session an upgrade stopped.
type stoppedSession struct {
	thread  domain.ThreadID
	version domain.ResourceVersion
}

type upgradeOwnerKey struct{}

// withUpgradeOwner marks ctx as the upgrade of capsule id itself, so its own
// Thread resume does not wait on it.
func withUpgradeOwner(ctx context.Context, id domain.CapsuleID) context.Context {
	return context.WithValue(ctx, upgradeOwnerKey{}, id)
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

// RecoverUpgradingCapsules settles every Capsule a daemon restart left in the
// "upgrading" maintenance state (ADR 0064). The provider removes containers an
// interrupted swap left behind, so at most one runtime uses the Capsule's
// volumes, and reports which image the runtime now has. It runs before the
// reconciler recovers Capsules, which then restarts a stopped runtime.
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
			if capsule.Maintenance != maintenanceUpgrading {
				continue
			}
			stopped, err := s.upgradeStoppedThread(ctx, capsule.ID)
			if err != nil {
				return err
			}
			if err := s.settleUpgrade(ctx, capsule.ID); err != nil {
				return err
			}
			if stopped != "" {
				s.upgradeMu.Lock()
				if s.upgradeRecovered == nil {
					s.upgradeRecovered = make(map[domain.CapsuleID]domain.ThreadID)
				}
				s.upgradeRecovered[capsule.ID] = stopped
				s.upgradeMu.Unlock()
			}
		}
	}
	return nil
}

// upgradeStoppedThread returns the Thread session the Capsule's latest
// upgrade stopped, from its upgrade_started event.
func (s *Service) upgradeStoppedThread(ctx context.Context, id domain.CapsuleID) (domain.ThreadID, error) {
	var events []domain.Event
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		events, err = reader.ListEvents(ctx, "capsule", string(id))
		return err
	}); err != nil {
		return "", err
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != upgradeStartedEvent {
			continue
		}
		var data upgradeStartedData
		if json.Unmarshal(events[i].Data, &data) != nil {
			return "", nil
		}
		return data.StoppedThreadID, nil
	}
	return "", nil
}

// ResumeThreadsAfterUpgradeRecovery resumes the Thread sessions that upgrades
// interrupted by a daemon restart had stopped (ADR 0064). Startup recovery
// settles those upgrades first; the reconciler then brings each runtime back,
// so this waits, up to upgradeResumeWait, for the Capsule's runtime to answer
// before resuming. A Thread the operator resumed meanwhile is left alone. It
// returns when every session is resumed or given up on, or ctx ends.
func (s *Service) ResumeThreadsAfterUpgradeRecovery(ctx context.Context) {
	if requireSystem(ctx) != nil || s.structured == nil {
		return
	}
	s.upgradeMu.Lock()
	pending := s.upgradeRecovered
	s.upgradeRecovered = nil
	s.upgradeMu.Unlock()
	deadline := s.clock.Now().Add(upgradeResumeWait)
	for len(pending) > 0 && ctx.Err() == nil && s.clock.Now().Before(deadline) {
		for id, thread := range pending {
			capsule, err := s.GetCapsule(ctx, id)
			if err != nil || capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
				delete(pending, id)
				continue
			}
			if capsule.Maintenance != "" || capsule.ProviderResourceID == "" {
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, probeErr := s.structured.HarnessProfiles(probeCtx, capsule.ProviderResourceID)
			cancel()
			if probeErr != nil {
				continue
			}
			s.resumeThreadAfterUpgrade(ctx, id, stoppedSession{thread: thread})
			delete(pending, id)
		}
		if len(pending) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// UpgradeCapsule performs an operator-requested in-place upgrade of an idle
// Capsule to the daemon's current official pack image (ADR 0064). The swap
// outlives the request: a client that disconnects mid-upgrade must not cancel
// the step that records the new image.
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
	ctx = context.WithoutCancel(ctx)
	updated, err := s.upgradeCapsule(ctx, id, expected, time.Time{})
	if err != nil {
		return domain.Capsule{}, err
	}
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		return putReplay(ctx, tx, scope, key, updated, s.clock.Now().UTC())
	})
	return updated, nil
}

// ScanUpgradableCapsules upgrades Paused and strictly idle Ready Capsules that
// belong to an outdated official pack (ADR 0064). A Ready Capsule must also
// have had no recorded activity for upgradeIdleGrace. On Docker, a background
// pass upgrades only after the target image is already available locally.
// Passes do not overlap: one that starts while another runs returns at once.
func (s *Service) ScanUpgradableCapsules(ctx context.Context, logger *log.Logger) (int, error) {
	if err := requireSystem(ctx); err != nil {
		return 0, err
	}
	if s.imageUpgrader == nil || len(s.harnessPacks) == 0 {
		return 0, nil
	}
	if !s.upgradeScanMu.TryLock() {
		return 0, nil
	}
	defer s.upgradeScanMu.Unlock()
	projects, err := s.listAllProjects(ctx)
	if err != nil {
		return 0, err
	}
	upgraded := 0
	quietSince := s.clock.Now().UTC().Add(-upgradeIdleGrace)
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
			if capsule.Maintenance != "" || capsule.ProviderResourceID == "" || !upgradableState(capsule) ||
				recentlyActive(capsule, quietSince) {
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
			if _, err := s.upgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, quietSince); err != nil {
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

// upgradableState reports an idle Ready or Paused Capsule whose desired state
// matches.
func upgradableState(capsule domain.Capsule) bool {
	return capsule.State == domain.CapsulePaused && capsule.DesiredState == domain.IntentPaused ||
		capsule.State == domain.CapsuleReady && capsule.DesiredState == domain.IntentReady
}

// recentlyActive reports a Ready Capsule with activity after quietSince. A
// zero quietSince, for an operator's own upgrade, never counts.
func recentlyActive(capsule domain.Capsule, quietSince time.Time) bool {
	return !quietSince.IsZero() && capsule.State == domain.CapsuleReady && capsule.LastActivityAt.After(quietSince)
}

// upgradeTarget checks that capsule may upgrade and returns the catalog pack it
// upgrades to.
func (s *Service) upgradeTarget(
	ctx context.Context,
	reader ports.Reader,
	capsule domain.Capsule,
	expected domain.ResourceVersion,
	quietSince time.Time,
) (domain.HarnessPack, error) {
	if expected > 0 && capsule.ResourceVersion != expected {
		return domain.HarnessPack{}, domain.ErrConflict
	}
	if recentlyActive(capsule, quietSince) {
		return domain.HarnessPack{}, fmt.Errorf("%w: Capsule was used recently", domain.ErrConflict)
	}
	project, err := reader.GetProject(ctx, capsule.ProjectID)
	if err != nil {
		return domain.HarnessPack{}, err
	}
	target, ok := domain.OfficialPackUpgradeTarget(s.harnessPacks, project, capsule.ImageReference)
	if !ok {
		return domain.HarnessPack{}, fmt.Errorf("%w: Capsule is already current or does not use an official pack", domain.ErrIllegalTransition)
	}
	// Re-verify Project isolation class fail-closed (ADR 0050).
	if project.RequiredIsolation != "" && !s.isolation.Satisfies(project.RequiredIsolation) {
		return domain.HarnessPack{}, fmt.Errorf("%w: provider isolation %q cannot satisfy Project requirement %q",
			domain.ErrUnsupported, s.isolation, project.RequiredIsolation)
	}
	if !upgradableState(capsule) || capsule.Maintenance != "" || capsule.ProviderResourceID == "" {
		return domain.HarnessPack{}, fmt.Errorf("%w: Capsule must be idle Ready or Paused to upgrade", domain.ErrIllegalTransition)
	}
	return target, nil
}

func (s *Service) upgradeCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	quietSince time.Time,
) (domain.Capsule, error) {
	if s.imageUpgrader == nil {
		return domain.Capsule{}, domain.ErrUnsupported
	}
	// Check eligibility and every quiescence condition except the idle
	// Thread session itself before stopping that session, so a Capsule that
	// cannot upgrade never loses its session.
	var pack domain.HarnessPack
	precheck := func() error {
		return s.store.View(ctx, func(reader ports.Reader) error {
			current, err := reader.GetCapsule(ctx, id)
			if err != nil {
				return err
			}
			if pack, err = s.upgradeTarget(ctx, reader, current, expected, quietSince); err != nil {
				return err
			}
			_, err = checkUpgradeQuiescence(ctx, reader, current, true)
			return err
		})
	}
	if err := precheck(); err != nil {
		return domain.Capsule{}, err
	}
	// Pull the target image while the Capsule still runs as before, so a slow
	// or failed pull never keeps its session down or Thread sends waiting.
	if s.imagePrepuller != nil {
		if err := s.imagePrepuller.PrepullImage(ctx, pack.ImageReference); err != nil {
			return domain.Capsule{}, err
		}
	}
	// One upgrade per Capsule at a time. Thread sends and resumes wait on
	// this until the upgrade ends.
	doneCh, ok := s.beginUpgradeWait(id)
	if !ok {
		return domain.Capsule{}, fmt.Errorf("%w: Capsule upgrade is already in progress", domain.ErrConflict)
	}
	waitEnded := false
	finishWait := func() {
		if !waitEnded {
			waitEnded = true
			s.endUpgradeWait(id, doneCh)
		}
	}
	defer finishWait()
	if err := precheck(); err != nil {
		return domain.Capsule{}, err
	}

	// An open structured Thread session that is idle is stopped before the
	// swap and resumed on whatever runtime the Capsule has afterwards, on
	// success and on every failure, including a stop that did not finish
	// cleanly (ADR 0064). Waiting sends are released only once it is back.
	var stopped stoppedSession
	defer func() {
		if stopped.thread != "" {
			s.resumeThreadAfterUpgrade(ctx, id, stopped)
		}
		finishWait()
	}()
	var err error
	if stopped, err = s.stopIdleThreadForUpgrade(ctx, id); err != nil {
		return domain.Capsule{}, err
	}

	var capsule domain.Capsule
	var target domain.HarnessPack
	var leadThreadID domain.ThreadID
	if err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		capsule, err = tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		// The idle grace was checked before the session stopped; stopping it
		// records activity itself, so it is not checked again here.
		if target, err = s.upgradeTarget(ctx, tx, capsule, expected, time.Time{}); err != nil {
			return err
		}
		if leadThreadID, err = checkUpgradeQuiescence(ctx, tx, capsule, false); err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.Maintenance = maintenanceUpgrading
		capsule.UpdatedAt = s.clock.Now().UTC()
		capsule.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, capsule, previous); err != nil {
			return err
		}
		data, _ := json.Marshal(upgradeStartedData{StoppedThreadID: stopped.thread})
		return s.appendEvent(ctx, tx, "capsule", string(capsule.ID), upgradeStartedEvent, capsule.ResourceVersion, data)
	}); err != nil {
		return domain.Capsule{}, err
	}

	// From here on the runtime may be mid-swap. Unless the upgrade is
	// recorded, the provider settles it and the record follows what it
	// reports; a Capsule whose runtime cannot be settled fails instead of
	// claiming to be Ready. Either way the reconciler, which skips upgrading
	// Capsules, then checks the runtime and any request made meanwhile.
	recorded := false
	defer func() {
		cleanup := principal.WithSystem(context.WithoutCancel(ctx))
		if !recorded {
			_ = s.settleUpgrade(cleanup, id)
		}
		if s.queue != nil {
			_ = s.queue.Enqueue(cleanup, id)
		}
	}()

	if s.upgradeHook != nil {
		s.upgradeHook(capsule.ProviderResourceID)
	}
	previousRef := capsule.ImageReference
	res, err := s.imageUpgrader.UpgradeImage(ctx, ports.UpgradeCapsuleRequest{
		CapsuleID:              capsule.ID,
		ResourceID:             capsule.ProviderResourceID,
		ImageReference:         target.ImageReference,
		PreviousImageReference: previousRef,
		Paused:                 capsule.State == domain.CapsulePaused,
	})
	if err != nil {
		return domain.Capsule{}, err
	}

	var updated domain.Capsule
	recordCtx := context.WithoutCancel(ctx)
	if err := s.store.Transact(recordCtx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(recordCtx, id)
		if err != nil {
			return err
		}
		if current.Maintenance != maintenanceUpgrading {
			return domain.ErrConflict
		}
		updated, err = s.recordImageUpgrade(recordCtx, tx, current, previousRef, target.ImageReference, res.ImageDigest)
		return err
	}); err != nil {
		return domain.Capsule{}, err
	}
	recorded = true
	s.afterImageUpgrade(id, previousRef, target.ImageReference, leadThreadID)
	return updated, nil
}

// afterImageUpgrade prepares the upgraded runtime's next session once an
// upgrade is recorded: a harness that rejects its earlier session falls back
// to a fresh one, and a lead's mailbox is pushed again to the new capsuled.
func (s *Service) afterImageUpgrade(id domain.CapsuleID, previousRef, ref string, leadThreadID domain.ThreadID) {
	s.recordPendingUpgradeFallback(id, previousRef, ref)
	if leadThreadID != "" {
		s.coordMu.Lock()
		delete(s.coordPushed, leadThreadID)
		s.coordMu.Unlock()
	}
}

// recordImageUpgrade ends the upgrading maintenance with capsule on ref and
// digest, and records the event. It saves the Capsule.
func (s *Service) recordImageUpgrade(
	ctx context.Context,
	tx ports.Transaction,
	capsule domain.Capsule,
	previousRef, ref, digest string,
) (domain.Capsule, error) {
	previous := capsule.ResourceVersion
	capsule.ImageReference = ref
	capsule.Maintenance = ""
	capsule.UpdatedAt = s.clock.Now().UTC()
	capsule.ResourceVersion++
	if err := tx.UpdateCapsule(ctx, capsule, previous); err != nil {
		return domain.Capsule{}, err
	}
	if digest != "" {
		prep, err := tx.GetPreparation(ctx, capsule.ID)
		switch {
		case err == nil:
			prep.ImageDigest = digest
			if err := tx.PutPreparation(ctx, prep); err != nil {
				return domain.Capsule{}, err
			}
		case !errors.Is(err, domain.ErrNotFound):
			return domain.Capsule{}, err
		}
	}
	metadata, _ := json.Marshal(map[string]any{
		"previousImageReference": previousRef,
		"imageReference":         ref,
		"imageDigest":            digest,
	})
	if err := s.appendEvent(ctx, tx, "capsule", string(capsule.ID), "capsule.image_upgraded", capsule.ResourceVersion, metadata); err != nil {
		return domain.Capsule{}, err
	}
	return capsule, nil
}

// settleUpgrade ends a Capsule's upgrading maintenance after an upgrade that
// failed or was interrupted (ADR 0064). The provider settles the runtime and
// reports whether it runs the upgrade target. A runtime the provider cannot
// settle fails the Capsule, so nothing restarts a second runtime on its
// volumes. It returns only store errors.
func (s *Service) settleUpgrade(ctx context.Context, id domain.CapsuleID) error {
	var capsule domain.Capsule
	var target domain.HarnessPack
	var hasTarget bool
	var leadThreadID domain.ThreadID
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsule, err = reader.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		project, err := reader.GetProject(ctx, capsule.ProjectID)
		if err != nil {
			return err
		}
		target, hasTarget = domain.OfficialPackUpgradeTarget(s.harnessPacks, project, capsule.ImageReference)
		grant, err := reader.GetCoordinationGrantByRootCapsule(ctx, id)
		switch {
		case err == nil:
			leadThreadID = grant.RootThreadID
		case !errors.Is(err, domain.ErrNotFound):
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if capsule.Maintenance != maintenanceUpgrading {
		return nil
	}
	var resource ports.ProviderResource
	var onTarget bool
	var settleErr error
	if s.imageUpgrader == nil {
		settleErr = errors.New("provider no longer supports image upgrades")
	} else if capsule.ProviderResourceID != "" {
		request := ports.UpgradeCapsuleRequest{CapsuleID: capsule.ID, ResourceID: capsule.ProviderResourceID}
		if hasTarget {
			request.ImageReference = target.ImageReference
		}
		resource, onTarget, settleErr = s.imageUpgrader.RecoverUpgrade(ctx, request)
	}
	var upgradedFrom string
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		upgradedFrom = ""
		current, err := tx.GetCapsule(ctx, id)
		if err != nil || current.Maintenance != maintenanceUpgrading {
			return err
		}
		if settleErr == nil && onTarget && hasTarget {
			if _, err := s.recordImageUpgrade(ctx, tx, current, current.ImageReference, target.ImageReference, resource.ImageDigest); err != nil {
				return err
			}
			upgradedFrom = current.ImageReference
			return nil
		}
		previous := current.ResourceVersion
		current.Maintenance = ""
		if settleErr == nil {
			current.UpdatedAt = s.clock.Now().UTC()
			current.ResourceVersion++
			return tx.UpdateCapsule(ctx, current, previous)
		}
		if err := current.Transition(domain.CapsuleFailed, s.clock.Now()); err != nil {
			return err
		}
		current.Failure = domain.RedactFailureText("Capsule image upgrade could not be settled: " + settleErr.Error())
		current.FailureCode = ""
		if err := tx.UpdateCapsule(ctx, current, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "capsule", string(id), "capsule.failed", current.ResourceVersion, nil)
	})
	if err == nil && upgradedFrom != "" {
		s.afterImageUpgrade(id, upgradedFrom, target.ImageReference, leadThreadID)
	}
	return err
}

// onlyThreadRunActive reports whether the active Run on the Capsule is the
// active Thread session's own. A Capsule has at most one active Run.
func onlyThreadRunActive(ctx context.Context, reader ports.Reader, thread domain.Thread) (bool, error) {
	if thread.CurrentRunID == "" {
		return false, nil
	}
	run, err := reader.GetRun(ctx, thread.CurrentRunID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !run.State.Terminal(), nil
}

// resumeThreadAfterUpgrade resumes the Thread session an upgrade stopped,
// when its Capsule is still Ready. It runs before the upgrade releases waiting
// sends, as the upgrade itself, and records the Thread's new version so a send
// made with the version from before the upgrade still applies.
func (s *Service) resumeThreadAfterUpgrade(ctx context.Context, id domain.CapsuleID, stopped stoppedSession) {
	sysCtx := withUpgradeOwner(principal.WithSystem(context.WithoutCancel(ctx)), id)
	capsule, err := s.GetCapsule(sysCtx, id)
	if err != nil || capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
		return
	}
	thread, err := s.GetThread(sysCtx, stopped.thread)
	if err != nil || thread.State != domain.ThreadPaused {
		return
	}
	result, err := s.ResumeThread(sysCtx, stopped.thread, thread.ResourceVersion,
		fmt.Sprintf("upgrade-resume-%s-%d", capsule.ID, capsule.ResourceVersion))
	if err != nil {
		return
	}
	s.upgradeMu.Lock()
	if s.upgradeRebase == nil {
		s.upgradeRebase = make(map[domain.CapsuleID]threadRebase)
	}
	s.upgradeRebase[id] = threadRebase{
		thread: stopped.thread, from: stopped.version, paused: thread.ResourceVersion, to: result.Thread.ResourceVersion,
	}
	s.upgradeMu.Unlock()
}

// rebaseAfterUpgrade returns the version a send to thread should expect: the
// resumed session's version when expected is one the Thread had before or
// while an upgrade stopped it, otherwise expected.
func (s *Service) rebaseAfterUpgrade(id domain.CapsuleID, thread domain.ThreadID, expected domain.ResourceVersion) domain.ResourceVersion {
	if expected == 0 {
		return expected
	}
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	rebase, ok := s.upgradeRebase[id]
	if !ok || rebase.thread != thread || expected != rebase.from && expected != rebase.paused {
		return expected
	}
	delete(s.upgradeRebase, id)
	return rebase.to
}

// resumedByUpgrade reports a resume request that waited on an upgrade which
// already resumed the same session itself, and returns that session, so the
// operator's Resume succeeds instead of failing on the changed version.
func (s *Service) resumedByUpgrade(
	ctx context.Context, id domain.CapsuleID, threadID domain.ThreadID, expected domain.ResourceVersion,
) (ThreadMutationResult, bool) {
	s.upgradeMu.Lock()
	rebase, ok := s.upgradeRebase[id]
	s.upgradeMu.Unlock()
	if !ok || rebase.thread != threadID || expected != rebase.from && expected != rebase.paused {
		return ThreadMutationResult{}, false
	}
	thread, err := s.GetThread(ctx, threadID)
	if err != nil || thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return ThreadMutationResult{}, false
	}
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	if err != nil || run.State.Terminal() {
		return ThreadMutationResult{}, false
	}
	return ThreadMutationResult{Thread: thread, Run: &run}, true
}

// stopIdleThreadForUpgrade stops an open structured Thread session on capsule
// id when it is genuinely idle: no active turn and no pending permission or
// input request. The caller already checked every other quiescence condition.
// It returns the stopped session so the caller can resume it, also with an
// error once stopping has begun, but not while the runtime may still run the
// old session.
func (s *Service) stopIdleThreadForUpgrade(ctx context.Context, id domain.CapsuleID) (stoppedSession, error) {
	if s.structured == nil {
		return stoppedSession{}, nil
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
		return stoppedSession{}, err
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" {
		return stoppedSession{}, nil
	}
	if thread.CurrentRunID == "" {
		return stoppedSession{}, nil
	}

	sysCtx := principal.WithSystem(ctx)
	if err := s.SyncThread(sysCtx, thread.ID); err != nil {
		return stoppedSession{}, err
	}
	unlock := s.lockThreadSync(thread.ID)
	defer unlock()
	thread, err := s.GetThread(sysCtx, thread.ID)
	if err != nil || thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return stoppedSession{}, err
	}
	attention, err := s.ThreadAttention(sysCtx, thread)
	if err != nil {
		return stoppedSession{}, err
	}
	if attention.Turn == ThreadTurnWorking || attention.Awaiting != nil {
		return stoppedSession{}, fmt.Errorf("%w: Capsule has an active Thread turn or pending prompt", domain.ErrConflict)
	}
	run, err := s.GetRunStored(sysCtx, thread.CurrentRunID)
	if err != nil || run.State.Terminal() {
		return stoppedSession{}, err
	}
	if _, err := s.transitionRun(sysCtx, run.ID, domain.RunCancelling, nil); err != nil {
		return stoppedSession{}, err
	}
	// From here the session is going away, so every return hands it back
	// to the caller to resume.
	stopped := stoppedSession{thread: thread.ID, version: thread.ResourceVersion}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(sysCtx), harnessCancelWait)
	runtimeRun, cancelErr := s.structured.CancelStructured(cancelCtx, capsule.ProviderResourceID, run.ID)
	cancel()
	if cancelErr != nil || !runtimeRun.State.Terminal() {
		_, _ = s.forceEndRun(sysCtx, run.ID, thread.ID)
		stopErr := fmt.Errorf("%w: could not stop idle Thread session cleanly before upgrade", domain.ErrConflict)
		// forceEndRun ends the Run only in the store. Resume only once the
		// runtime confirms the old session is gone, so a second harness
		// process never runs beside it on the same session; otherwise the
		// Thread stays Paused for the operator to resume.
		checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(sysCtx), harnessCancelWait)
		current, getErr := s.structured.GetStructured(checkCtx, capsule.ProviderResourceID, run.ID)
		checkCancel()
		if errors.Is(getErr, domain.ErrNotFound) || getErr == nil && current.State.Terminal() {
			return stopped, stopErr
		}
		return stoppedSession{}, stopErr
	}
	if _, err := s.transitionRun(sysCtx, run.ID, domain.RunCancelled, nil); err != nil {
		return stopped, err
	}
	if err := s.pauseCompletedThread(sysCtx, thread.ID, run.ID); err != nil {
		return stopped, err
	}
	return stopped, nil
}

// checkUpgradeQuiescence reports why capsule is not idle enough to upgrade: an
// active Run, an active Thread session (unless allowThread, for the check made
// before an idle session is stopped), an active Project check, or for a
// standing lead, subagents that are running, starting, or waiting to wake it
// with unread reports or operator messages (ADR 0064). It returns the lead's
// root Thread when capsule leads one. The session's own Run counts as active
// until it is stopped, so allowThread skips the Run check too.
func checkUpgradeQuiescence(
	ctx context.Context,
	reader ports.Reader,
	capsule domain.Capsule,
	allowThread bool,
) (domain.ThreadID, error) {
	thread, err := reader.GetActiveThread(ctx, capsule.ID)
	switch {
	case err == nil && !allowThread:
		return "", fmt.Errorf("%w: Capsule has an active Thread session", domain.ErrConflict)
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return "", err
	}
	activeRun, err := reader.HasActiveRun(ctx, capsule.ID)
	if err != nil {
		return "", err
	}
	if activeRun {
		sessionOnly, err := onlyThreadRunActive(ctx, reader, thread)
		if err != nil {
			return "", err
		}
		if !allowThread || !sessionOnly {
			return "", fmt.Errorf("%w: Capsule has an active Run", domain.ErrConflict)
		}
	}
	if _, err := reader.ActiveCheckRun(ctx, capsule.ID); err == nil {
		return "", fmt.Errorf("%w: Capsule has an active Project check run", domain.ErrConflict)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	grant, err := reader.GetCoordinationGrantByRootCapsule(ctx, capsule.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	links, err := reader.ListThreadLinksByParent(ctx, grant.RootThreadID)
	if err != nil {
		return "", err
	}
	for _, link := range links {
		if link.State == domain.LinkActive || link.State == domain.LinkPendingApproval {
			return "", fmt.Errorf("%w: lead has active or pending subagents", domain.ErrConflict)
		}
		if link.ReportSeq > link.ReportHeadlinedSeq || link.OperatorMessages > link.OperatorNoticed {
			return "", fmt.Errorf("%w: lead has pending subagent reports", domain.ErrConflict)
		}
	}
	return grant.RootThreadID, nil
}

func (s *Service) beginUpgradeWait(id domain.CapsuleID) (chan struct{}, bool) {
	s.upgradeMu.Lock()
	defer s.upgradeMu.Unlock()
	if s.upgrading[id] != nil {
		return nil, false
	}
	if s.upgrading == nil {
		s.upgrading = make(map[domain.CapsuleID]chan struct{})
	}
	ch := make(chan struct{})
	s.upgrading[id] = ch
	return ch, true
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
	if owner, ok := ctx.Value(upgradeOwnerKey{}).(domain.CapsuleID); ok && owner == id {
		return nil
	}
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

// isHarnessResumeFailureCode reports an adapter failure that means the
// harness could not continue its earlier session: the harness rejected it, the
// adapter could not read its own resume state, or the agent cannot load
// sessions. An adapter from an older pack rejects resume state written by a
// newer one (ACP state decodes strictly), so after an upgrade or a Meridian
// downgrade each of these falls back to a fresh session (ADR 0064).
func isHarnessResumeFailureCode(code string) bool {
	switch code {
	case "claude_resume_failed", "claude_resume_invalid",
		"codex_resume_failed", "codex_resume_invalid",
		"acp_resume_failed", "acp_resume_invalid", "acp_resume_unsupported",
		"pi_resume_failed", "pi_resume_invalid":
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
