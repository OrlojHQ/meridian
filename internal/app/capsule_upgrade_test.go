package app_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

type fakeImageUpgrader struct {
	mu         sync.Mutex
	calls      []ports.UpgradeCapsuleRequest
	available  map[string]bool
	block      chan struct{}
	entered    chan struct{}
	err        error
	digest     string
	stoppedRes []string
	// RecoverUpgrade results and calls.
	recoverOnTarget bool
	recoverErr      error
	recovered       []ports.UpgradeCapsuleRequest
}

func (f *fakeImageUpgrader) RecoverUpgrade(_ context.Context, req ports.UpgradeCapsuleRequest) (ports.ProviderResource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovered = append(f.recovered, req)
	if f.recoverErr != nil {
		return ports.ProviderResource{}, false, f.recoverErr
	}
	return ports.ProviderResource{ID: req.ResourceID, ImageDigest: f.digest}, f.recoverOnTarget, nil
}

func (f *fakeImageUpgrader) callCount() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls), len(f.recovered)
}

func (f *fakeImageUpgrader) ImageAvailable(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.available == nil {
		return true, nil
	}
	return f.available[ref], nil
}

func (f *fakeImageUpgrader) PrepullImage(context.Context, string) error { return nil }

func (f *fakeImageUpgrader) UpgradeImage(ctx context.Context, req ports.UpgradeCapsuleRequest) (ports.ProviderResource, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	entered := f.entered
	block := f.block
	err := f.err
	digest := f.digest
	f.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ports.ProviderResource{}, ctx.Err()
		}
	}
	if err != nil {
		return ports.ProviderResource{}, err
	}
	state := ports.ProviderReady
	if req.Paused {
		state = ports.ProviderPaused
	}
	return ports.ProviderResource{ID: req.ResourceID, State: state, ImageDigest: digest}, nil
}

func seedCapsuleForUpgrade(
	t *testing.T,
	ctx context.Context,
	store ports.Store,
	project domain.Project,
	id domain.CapsuleID,
	imageRef string,
	state domain.CapsuleState,
	intent domain.CapsuleIntent,
) domain.Capsule {
	t.Helper()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	timelineID := domain.TimelineID("tl-" + string(id))
	capsule := domain.Capsule{
		OwnerUserID:        project.OwnerUserID,
		ID:                 id,
		ProjectID:          project.ID,
		TimelineID:         timelineID,
		Name:               string(id),
		State:              state,
		DesiredState:       intent,
		ProviderResourceID: "meridian-" + string(id),
		ImageReference:     imageRef,
		LastActivityAt:     now,
		CreatedAt:          now,
		UpdatedAt:          now,
		ResourceVersion:    1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		if err := tx.InsertTimeline(ctx, domain.Timeline{
			ID: timelineID, ProjectID: project.ID, CapsuleID: id,
			Reason: domain.TimelineRoot, CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.PutPreparation(ctx, domain.Preparation{
			CapsuleID:      id,
			ImageReference: imageRef,
			ImageDigest:    "sha256:" + strings.Repeat("1", 64),
		})
	}); err != nil {
		t.Fatalf("seed capsule %s: %v", id, err)
	}
	return capsule
}

func TestScanUpgradableCapsulesUpgradesIdleOfficialAndSkipsCustomAndUnpulled(t *testing.T) {
	sysCtx := principal.WithSystem(context.Background())
	userCtx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(sysCtx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &testClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	svc := app.NewService(store, clock, ids, &recordingQueue{})
	svc.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", ""))
	svc.ConfigureIsolation(domain.IsolationContainer)

	newDigest := "sha256:" + strings.Repeat("9", 64)
	upgrader := &fakeImageUpgrader{
		digest: newDigest,
		available: map[string]bool{
			"ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0": true,
			"ghcr.io/orlojhq/meridian-capsule-codex:v0.18.0":  false, // not pre-pulled yet
		},
	}
	svc.ConfigureImageUpgrade(upgrader, upgrader, func(res string) {
		upgrader.mu.Lock()
		upgrader.stoppedRes = append(upgrader.stoppedRes, res)
		upgrader.mu.Unlock()
	})

	project, err := svc.CreateProjectConfigured(userCtx, "demo", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{
			{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"},
			{Name: "codex", ImageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.16.0"},
			{Name: "custom", ImageReference: "ghcr.io/example/custom:v0.16.0"},
		},
	}, "key-proj")
	if err != nil {
		t.Fatal(err)
	}

	readyCap := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-ready", "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0", domain.CapsuleReady, domain.IntentReady)
	pausedCap := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-paused", "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0", domain.CapsulePaused, domain.IntentPaused)
	unpulledCap := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-codex", "ghcr.io/orlojhq/meridian-capsule-codex:v0.17.0", domain.CapsuleReady, domain.IntentReady)
	customCap := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-custom", "ghcr.io/example/custom:v0.16.0", domain.CapsuleReady, domain.IntentReady)

	var logs bytes.Buffer
	count, err := svc.ScanUpgradableCapsules(sysCtx, log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("ScanUpgradableCapsules: %v (logs: %s)", err, logs.String())
	}
	if count != 2 {
		t.Fatalf("upgraded count = %d, want 2 (logs: %s)", count, logs.String())
	}

	gotReady, err := svc.GetCapsule(userCtx, readyCap.ID)
	if err != nil || gotReady.ImageReference != "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0" || gotReady.Maintenance != "" {
		t.Fatalf("readyCap after upgrade = %#v, %v", gotReady, err)
	}
	gotPaused, err := svc.GetCapsule(userCtx, pausedCap.ID)
	if err != nil || gotPaused.ImageReference != "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0" || gotPaused.State != domain.CapsulePaused {
		t.Fatalf("pausedCap after upgrade = %#v, %v", gotPaused, err)
	}
	gotUnpulled, _ := svc.GetCapsule(userCtx, unpulledCap.ID)
	if gotUnpulled.ImageReference != "ghcr.io/orlojhq/meridian-capsule-codex:v0.17.0" {
		t.Fatalf("unpulled capsule should not upgrade in background: %#v", gotUnpulled)
	}
	gotCustom, _ := svc.GetCapsule(userCtx, customCap.ID)
	if gotCustom.ImageReference != "ghcr.io/example/custom:v0.16.0" {
		t.Fatalf("custom capsule should never upgrade: %#v", gotCustom)
	}

	// Verify Preparation.ImageDigest was updated without rerunning setup.
	var prep domain.Preparation
	if err := store.View(sysCtx, func(r ports.Reader) error {
		var err error
		prep, err = r.GetPreparation(sysCtx, readyCap.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if prep.ImageDigest != newDigest {
		t.Fatalf("preparation digest = %q, want %q", prep.ImageDigest, newDigest)
	}

	// Manual UpgradeCapsule still works for codex even when not pre-pulled yet.
	upgradedCodex, err := svc.UpgradeCapsule(userCtx, unpulledCap.ID, unpulledCap.ResourceVersion, "manual-upgrade-1")
	if err != nil {
		t.Fatalf("manual UpgradeCapsule: %v", err)
	}
	if upgradedCodex.ImageReference != "ghcr.io/orlojhq/meridian-capsule-codex:v0.18.0" {
		t.Fatalf("manual upgrade image = %q", upgradedCodex.ImageReference)
	}
}

func TestUpgradeCapsuleRefusesLeadWithActiveSubagent(t *testing.T) {
	sysCtx := principal.WithSystem(context.Background())
	userCtx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(sysCtx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clock := &testClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	svc := app.NewService(store, clock, ids, &recordingQueue{})
	svc.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", ""))
	svc.ConfigureIsolation(domain.IsolationContainer)
	upgrader := &fakeImageUpgrader{digest: "sha256:" + strings.Repeat("8", 64)}
	svc.ConfigureImageUpgrade(upgrader, upgrader, nil)

	project, err := svc.CreateProjectConfigured(userCtx, "lead-proj", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{
			{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"},
		},
	}, "key-lead-proj")
	if err != nil {
		t.Fatal(err)
	}
	leadCap := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-lead", "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0", domain.CapsuleReady, domain.IntentReady)
	childCap := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-child", "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0", domain.CapsuleReady, domain.IntentReady)
	now := clock.Now()
	if err := store.Transact(sysCtx, func(tx ports.Transaction) error {
		thread := domain.Thread{
			ID: "th-lead", CapsuleID: leadCap.ID,
			AdapterID: "claude", State: domain.ThreadActive,
			KEKID: "kek-1", KEKVersion: 1, EnvelopeVersion: 1, WrappedDEK: make([]byte, 64),
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertThread(sysCtx, thread); err != nil {
			return err
		}
		if err := thread.Transition(domain.ThreadPaused, now); err != nil {
			return err
		}
		if err := tx.UpdateThread(sysCtx, thread, 1); err != nil {
			return err
		}
		pendingMsg, encErr := transcripts.EncryptMessage(make([]byte, 32), transcripts.MessageInput{
			ID: "msg-child-1", ThreadID: "th-child-1", Sequence: 1,
			Role: domain.ThreadRoleUser, Kind: domain.ThreadMessagePrompt, CreatedAt: now,
			Blocks: []transcripts.PlaintextBlock{{
				ID: "blk-child-1", Kind: domain.ThreadBlockText, Content: []byte(`{"content":"hi"}`),
			}},
		})
		if encErr != nil {
			return encErr
		}
		if err := tx.InsertProjectThreadIntent(sysCtx, domain.ProjectThreadIntent{
			ID: "intent-child-1", ProjectID: project.ID, IdempotencyKey: "child-key-1",
			Harness: "claude", CapsuleName: "cap-child", CapsuleID: childCap.ID, ThreadID: "th-child-1",
			RunID: "run-child-1", MessageID: "msg-child-1",
			KEKID: "kek-1", KEKVersion: 1, EnvelopeVersion: 1, WrappedDEK: make([]byte, 64),
			State:          domain.ProjectThreadProvisioning,
			PendingMessage: pendingMsg,
			CreatedAt:      now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		if err := tx.InsertCoordinationGrant(sysCtx, domain.CoordinationGrant{
			ID: "grant-1", ProjectID: project.ID,
			RootThreadID: "th-lead", RootCapsuleID: leadCap.ID, State: domain.GrantActive,
			SpawnApproval: domain.SpawnAuto, OnDeadline: domain.DeadlineCancelAndPause,
			MaxChildren: 4, MaxDescendants: 10, MaxDepth: 1, MaxLiveCapsules: 4, MaxMessagesPerLink: 16, UnattendedLimit: 24 * time.Hour,
			LastOperatorAt: now, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.InsertThreadLink(sysCtx, domain.ThreadLink{
			ID: "link-1", GrantID: "grant-1", ParentThreadID: "th-lead", ParentCapsuleID: leadCap.ID,
			ChildIntentID: "intent-child-1", ChildThreadID: "th-child-1", ChildCapsuleID: childCap.ID,
			Depth: 1, Name: "worker-1", Harness: "claude", State: domain.LinkActive,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(userCtx, leadCap.ID, leadCap.ResourceVersion, "lead-busy"); err == nil {
		t.Fatal("expected upgrade to refuse lead with active subagent")
	}
}

// upgradeTestService returns a Service with official v0.18.0 packs, the fake
// upgrader, and a Project allowlisting the official claude pack.
func upgradeTestService(t *testing.T, upgrader *fakeImageUpgrader) (*app.Service, ports.Store, *recordingQueue, domain.Project) {
	t.Helper()
	sysCtx := principal.WithSystem(context.Background())
	userCtx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(sysCtx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	queue := &recordingQueue{}
	svc := app.NewService(store, &testClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}, &testIDs{}, queue)
	svc.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", ""))
	svc.ConfigureIsolation(domain.IsolationContainer)
	svc.ConfigureImageUpgrade(upgrader, upgrader, nil)
	project, err := svc.CreateProjectConfigured(userCtx, "upgrade-proj", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{
			{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"},
		},
	}, "key-upgrade-proj")
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, queue, project
}

const (
	oldClaudePack = "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0"
	newClaudePack = "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0"
)

func TestUpgradeFailureSettlesRuntimeAndKeepsCapsuleReady(t *testing.T) {
	upgrader := &fakeImageUpgrader{err: errors.New("replacement failed readiness")}
	svc, store, queue, project := upgradeTestService(t, upgrader)
	ctx := principal.With(context.Background(), principal.Owner())
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-fail", oldClaudePack, domain.CapsuleReady, domain.IntentReady)

	if _, err := svc.UpgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, "fail-1"); err == nil {
		t.Fatal("expected upgrade error")
	}
	got, err := svc.GetCapsule(ctx, capsule.ID)
	if err != nil || got.Maintenance != "" || got.State != domain.CapsuleReady || got.ImageReference != oldClaudePack {
		t.Fatalf("capsule after failed upgrade = %#v, %v", got, err)
	}
	if _, recovered := upgrader.callCount(); recovered != 1 {
		t.Fatalf("RecoverUpgrade calls = %d, want the failure settled once", recovered)
	}
	if ids := queue.snapshot(); len(ids) == 0 || ids[len(ids)-1] != capsule.ID {
		t.Fatalf("reconcile queue = %v, want the Capsule checked after the failure", ids)
	}
}

func TestUpgradeFailureThatCannotSettleFailsCapsule(t *testing.T) {
	upgrader := &fakeImageUpgrader{
		err:        errors.New("replacement failed readiness"),
		recoverErr: errors.New("engine unreachable"),
	}
	svc, store, _, project := upgradeTestService(t, upgrader)
	ctx := principal.With(context.Background(), principal.Owner())
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-unsettled", oldClaudePack, domain.CapsuleReady, domain.IntentReady)

	if _, err := svc.UpgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, "unsettled-1"); err == nil {
		t.Fatal("expected upgrade error")
	}
	got, err := svc.GetCapsule(ctx, capsule.ID)
	if err != nil || got.State != domain.CapsuleFailed || got.Maintenance != "" ||
		!strings.Contains(got.Failure, "could not be settled") {
		t.Fatalf("capsule = %#v, %v; want Failed, not Ready on a runtime nobody settled", got, err)
	}
}

func TestRecoverUpgradingCapsulesFollowsProviderRuntime(t *testing.T) {
	upgrader := &fakeImageUpgrader{digest: "sha256:" + strings.Repeat("9", 64)}
	svc, store, _, project := upgradeTestService(t, upgrader)
	sysCtx := principal.WithSystem(context.Background())
	swapped := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-swapped", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	setMaintenance(t, store, swapped.ID, "upgrading")

	upgrader.recoverOnTarget = true
	if err := svc.RecoverUpgradingCapsules(sysCtx); err != nil {
		t.Fatalf("RecoverUpgradingCapsules: %v", err)
	}
	got, err := svc.GetCapsule(sysCtx, swapped.ID)
	if err != nil || got.Maintenance != "" || got.ImageReference != newClaudePack {
		t.Fatalf("swapped capsule = %#v, %v; want the new image recorded", got, err)
	}
	if prep := preparationOf(t, store, swapped.ID); prep.ImageDigest != upgrader.digest {
		t.Fatalf("preparation digest = %q, want %q", prep.ImageDigest, upgrader.digest)
	}

	kept := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-kept", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	setMaintenance(t, store, kept.ID, "upgrading")
	upgrader.recoverOnTarget = false
	if err := svc.RecoverUpgradingCapsules(sysCtx); err != nil {
		t.Fatalf("RecoverUpgradingCapsules: %v", err)
	}
	got, err = svc.GetCapsule(sysCtx, kept.ID)
	if err != nil || got.Maintenance != "" || got.ImageReference != oldClaudePack || got.State != domain.CapsuleReady {
		t.Fatalf("kept capsule = %#v, %v; want the old image kept", got, err)
	}
	if _, recovered := upgrader.callCount(); recovered != 2 {
		t.Fatalf("RecoverUpgrade calls = %d, want one per upgrading Capsule", recovered)
	}
}

func TestUpgradeCapsuleAllowsOneUpgradeAtATime(t *testing.T) {
	upgrader := &fakeImageUpgrader{block: make(chan struct{}), entered: make(chan struct{})}
	svc, store, _, project := upgradeTestService(t, upgrader)
	ctx := principal.With(context.Background(), principal.Owner())
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-once", oldClaudePack, domain.CapsuleReady, domain.IntentReady)

	done := make(chan error, 1)
	go func() {
		_, err := svc.UpgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, "once-1")
		done <- err
	}()
	<-upgrader.entered
	if _, err := svc.UpgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, "once-2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("concurrent upgrade = %v, want conflict", err)
	}
	if _, err := svc.ScanUpgradableCapsules(principal.WithSystem(context.Background()), nil); err != nil {
		t.Fatalf("scan during upgrade: %v", err)
	}
	close(upgrader.block)
	if err := <-done; err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	if calls, _ := upgrader.callCount(); calls != 1 {
		t.Fatalf("provider upgrades = %d, want 1", calls)
	}
}

func TestUpgradeCapsuleRecordsUpgradeAfterClientDisconnects(t *testing.T) {
	upgrader := &fakeImageUpgrader{block: make(chan struct{}), entered: make(chan struct{})}
	svc, store, _, project := upgradeTestService(t, upgrader)
	ctx, cancel := context.WithCancel(principal.With(context.Background(), principal.Owner()))
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-disconnect", oldClaudePack, domain.CapsuleReady, domain.IntentReady)

	done := make(chan error, 1)
	go func() {
		_, err := svc.UpgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, "disconnect-1")
		done <- err
	}()
	<-upgrader.entered
	cancel()
	close(upgrader.block)
	if err := <-done; err != nil {
		t.Fatalf("upgrade after disconnect: %v", err)
	}
	got, err := svc.GetCapsule(principal.WithSystem(context.Background()), capsule.ID)
	if err != nil || got.ImageReference != newClaudePack || got.Maintenance != "" {
		t.Fatalf("capsule = %#v, %v; want the upgrade recorded", got, err)
	}
}

func TestUpgradeCapsuleRefusesActiveRunBeforeTouchingAnything(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	ctx := principal.With(context.Background(), principal.Owner())
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-terminal", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(context.Background(), func(tx ports.Transaction) error {
		return tx.InsertRun(context.Background(), domain.Run{
			ID: "run-terminal", CapsuleID: capsule.ID, Harness: "shell", State: domain.RunRunning,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(ctx, capsule.ID, capsule.ResourceVersion, "terminal-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("upgrade with an active Run = %v, want conflict", err)
	}
	calls, recovered := upgrader.callCount()
	if calls != 0 || recovered != 0 {
		t.Fatalf("provider calls = %d upgrades, %d recoveries; want none", calls, recovered)
	}
	got, _ := svc.GetCapsule(ctx, capsule.ID)
	if got.Maintenance != "" || got.ResourceVersion != capsule.ResourceVersion {
		t.Fatalf("capsule changed although the upgrade was refused: %#v", got)
	}
}

func setMaintenance(t *testing.T, store ports.Store, id domain.CapsuleID, maintenance string) {
	t.Helper()
	ctx := principal.WithSystem(context.Background())
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.Maintenance = maintenance
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, previous)
	}); err != nil {
		t.Fatal(err)
	}
}

func preparationOf(t *testing.T, store ports.Store, id domain.CapsuleID) domain.Preparation {
	t.Helper()
	ctx := principal.WithSystem(context.Background())
	var prep domain.Preparation
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		prep, err = reader.GetPreparation(ctx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return prep
}

// startIdleThread opens a structured Thread session with no turn on capsule.
func startIdleThread(t *testing.T, svc *app.Service, capsuleID domain.CapsuleID) (*structuredRuntime, domain.ThreadID) {
	t.Helper()
	key, err := transcripts.NewInstallationKey("test-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	runtime := &structuredRuntime{}
	// The first turn runs and goes idle.
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{runningFrame(1), idleFrame(2)},
		NextCursor: 2,
	}
	svc.ConfigureThreads(runtime, key)
	created, err := svc.CreateThread(operatorContext(), app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "hello", Start: true, IdempotencyKey: "upgrade-thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncThread(operatorContext(), created.Thread.ID); err != nil {
		t.Fatal(err)
	}
	return runtime, created.Thread.ID
}

func TestUpgradeStopsIdleThreadAndResumesItAfterwards(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-thread", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "thread-upgrade"); err != nil {
		t.Fatalf("UpgradeCapsule: %v", err)
	}
	runtime.mu.Lock()
	cancels, starts := runtime.cancels, len(runtime.starts)
	runtime.mu.Unlock()
	if cancels != 1 || starts != 2 {
		t.Fatalf("session cancels = %d, starts = %d; want it stopped once and started again", cancels, starts)
	}
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadActive {
		t.Fatalf("Thread after upgrade = %#v, %v; want it resumed", thread, err)
	}
}

// Project checks queue when a Thread turn goes idle, so an idle session and a
// running check are common together. The upgrade must refuse before it stops
// the session, not after.
func TestUpgradeRefusedByRunningCheckLeavesIdleThreadRunning(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-thread-check", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(context.Background(), func(tx ports.Transaction) error {
		return tx.InsertCheckRun(context.Background(), domain.CheckRun{
			ID: "check-running", CapsuleID: capsule.ID, ProjectID: project.ID, OwnerUserID: project.OwnerUserID,
			ThreadID: threadID, Attempt: 1, Trigger: domain.CheckTriggerTurnIdle, State: domain.CheckRunning,
			StartedAt: now, NotBefore: now, CreatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "thread-check"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("upgrade with a running check = %v, want conflict", err)
	}
	runtime.mu.Lock()
	cancels := runtime.cancels
	runtime.mu.Unlock()
	if cancels != 0 {
		t.Fatalf("session cancels = %d; a refused upgrade must not stop the Thread", cancels)
	}
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadActive {
		t.Fatalf("Thread = %#v, %v; want it still running", thread, err)
	}
}
