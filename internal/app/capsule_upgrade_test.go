package app_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
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
	pullErr         error
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

func (f *fakeImageUpgrader) PrepullImage(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pullErr
}

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

	// A Ready Capsule used within the idle grace (a preview request, a Run)
	// is left alone; a Paused one is not.
	var logs bytes.Buffer
	count, err := svc.ScanUpgradableCapsules(sysCtx, log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("ScanUpgradableCapsules: %v (logs: %s)", err, logs.String())
	}
	if count != 1 {
		t.Fatalf("recently used scan upgraded %d, want only the Paused Capsule (logs: %s)", count, logs.String())
	}
	if got, _ := svc.GetCapsule(userCtx, readyCap.ID); got.ImageReference != readyCap.ImageReference {
		t.Fatalf("recently used Ready Capsule upgraded: %#v", got)
	}

	clock.mu.Lock()
	clock.now = clock.now.Add(16 * time.Minute)
	clock.mu.Unlock()
	count, err = svc.ScanUpgradableCapsules(sysCtx, log.New(&logs, "", 0))
	if err != nil {
		t.Fatalf("ScanUpgradableCapsules: %v (logs: %s)", err, logs.String())
	}
	if count != 1 {
		t.Fatalf("quiet scan upgraded %d, want the Ready Capsule (logs: %s)", count, logs.String())
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
	before, err := svc.GetThread(operatorContext(), threadID)
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
	// A send the operator made with the version from before the upgrade,
	// which waited on it, still goes to the resumed session.
	if _, err := svc.SendThreadMessage(operatorContext(), threadID, before.ResourceVersion, "after upgrade", "send-after-upgrade", ""); err != nil {
		t.Fatalf("send with the pre-upgrade version: %v", err)
	}
}

// A session whose cancel fails but whose runtime Run ended is force-ended;
// the refused upgrade must still bring it back.
func TestUpgradeResumesThreadWhenSessionStopFails(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-thread-stuck", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	runtime.mu.Lock()
	runtime.cancelErr = errors.New("adapter exited before answering")
	runtime.cancelEnds = true
	runtime.mu.Unlock()
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "thread-stuck"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("upgrade with a stuck session = %v, want conflict", err)
	}
	if calls, _ := upgrader.callCount(); calls != 0 {
		t.Fatalf("UpgradeImage calls = %d, want none", calls)
	}
	runtime.mu.Lock()
	starts := len(runtime.starts)
	runtime.mu.Unlock()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadActive || starts != 2 {
		t.Fatalf("Thread = %#v (starts %d), %v; want it resumed", thread, starts, err)
	}
}

// A session the runtime still runs after a failed cancel must not be
// resumed beside itself; the Thread stays Paused for the operator.
func TestUpgradeLeavesThreadPausedWhileOldSessionMayRun(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-thread-wedged", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	runtime.mu.Lock()
	runtime.cancelErr = errors.New("adapter did not answer")
	runtime.mu.Unlock()
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "thread-wedged"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("upgrade with a wedged session = %v, want conflict", err)
	}
	runtime.mu.Lock()
	starts := len(runtime.starts)
	runtime.mu.Unlock()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadPaused || starts != 1 {
		t.Fatalf("Thread = %#v (starts %d), %v; want it Paused with no second session", thread, starts, err)
	}
}

// The image is pulled while the Capsule still runs, so a pull that fails
// leaves the idle session untouched.
func TestUpgradePullFailureLeavesIdleThreadRunning(t *testing.T) {
	upgrader := &fakeImageUpgrader{pullErr: errors.New("registry unavailable")}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-thread-pull", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "thread-pull"); err == nil {
		t.Fatal("expected pull error")
	}
	runtime.mu.Lock()
	cancels := runtime.cancels
	runtime.mu.Unlock()
	if calls, _ := upgrader.callCount(); cancels != 0 || calls != 0 {
		t.Fatalf("session cancels = %d, UpgradeImage calls = %d; want neither before the pull", cancels, calls)
	}
	got, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil || got.Maintenance != "" {
		t.Fatalf("Capsule = %#v, %v; want no maintenance", got, err)
	}
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadActive {
		t.Fatalf("Thread = %#v, %v; want it still running", thread, err)
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

// Stopping the idle session records activity itself, which must not count
// against the background idle grace: a quiet Capsule with an open Thread
// upgrades once, and its session comes back.
func TestBackgroundUpgradeUpgradesQuietCapsuleWithIdleThread(t *testing.T) {
	sysCtx := principal.WithSystem(context.Background())
	userCtx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(sysCtx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	upgrader := &fakeImageUpgrader{}
	svc := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	svc.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", ""))
	svc.ConfigureIsolation(domain.IsolationContainer)
	svc.ConfigureImageUpgrade(upgrader, upgrader, nil)
	project, err := svc.CreateProjectConfigured(userCtx, "bg-thread", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"}},
	}, "key-bg-thread")
	if err != nil {
		t.Fatal(err)
	}
	capsule := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-bg-thread", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	clock.mu.Lock()
	clock.now = clock.now.Add(16 * time.Minute)
	clock.mu.Unlock()

	count, err := svc.ScanUpgradableCapsules(sysCtx, nil)
	if err != nil || count != 1 {
		t.Fatalf("ScanUpgradableCapsules = %d, %v; want the quiet Capsule upgraded", count, err)
	}
	got, err := svc.GetCapsule(userCtx, capsule.ID)
	if err != nil || got.ImageReference != newClaudePack {
		t.Fatalf("Capsule = %#v, %v; want %s", got, err, newClaudePack)
	}
	runtime.mu.Lock()
	cancels, starts := runtime.cancels, len(runtime.starts)
	runtime.mu.Unlock()
	thread, err := svc.GetThread(userCtx, threadID)
	if err != nil || thread.State != domain.ThreadActive || cancels != 1 || starts != 2 {
		t.Fatalf("Thread = %#v (cancels %d, starts %d), %v; want it stopped once and resumed", thread, cancels, starts, err)
	}
}

// A daemon restart mid-upgrade loses the in-process resume. Startup recovery
// finds the stopped session in the upgrade's event and resumes it once the
// runtime answers.
func TestRestartMidUpgradeResumesStoppedThread(t *testing.T) {
	sysCtx := principal.WithSystem(context.Background())
	store, err := sqlite.Open(sysCtx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	key, err := transcripts.NewInstallationKey("test-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	runtime := &structuredRuntime{events: ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{runningFrame(1), idleFrame(2)}, NextCursor: 2,
	}}
	newService := func(upgrader *fakeImageUpgrader) *app.Service {
		svc := app.NewService(store, clock, ids, &recordingQueue{})
		svc.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", ""))
		svc.ConfigureIsolation(domain.IsolationContainer)
		svc.ConfigureImageUpgrade(upgrader, upgrader, nil)
		svc.ConfigureThreads(runtime, key)
		return svc
	}
	blocked := &fakeImageUpgrader{block: make(chan struct{}), entered: make(chan struct{})}
	before := newService(blocked)
	project, err := before.CreateProjectConfigured(operatorContext(), "restart", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"}},
	}, "key-restart")
	if err != nil {
		t.Fatal(err)
	}
	capsule := seedCapsuleForUpgrade(t, sysCtx, store, project, "cap-restart", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	created, err := before.CreateThread(operatorContext(), app.CreateThreadInput{
		CapsuleID: capsule.ID, Harness: "mock", FirstMessage: "hello", Start: true, IdempotencyKey: "restart-thread",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := before.SyncThread(operatorContext(), created.Thread.ID); err != nil {
		t.Fatal(err)
	}
	current, err := before.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = before.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "restart-upgrade")
	}()
	<-blocked.entered

	// The daemon restarts: a new Service on the same store finds the swap
	// finished on the new image.
	after := newService(&fakeImageUpgrader{recoverOnTarget: true})
	if err := after.RecoverUpgradingCapsules(sysCtx); err != nil {
		t.Fatalf("RecoverUpgradingCapsules: %v", err)
	}
	after.ResumeThreadsAfterUpgradeRecovery(sysCtx)
	thread, err := after.GetThread(operatorContext(), created.Thread.ID)
	if err != nil || thread.State != domain.ThreadActive {
		t.Fatalf("Thread after restart recovery = %#v, %v; want it resumed", thread, err)
	}
	got, err := after.GetCapsule(operatorContext(), capsule.ID)
	if err != nil || got.ImageReference != newClaudePack || got.Maintenance != "" {
		t.Fatalf("Capsule = %#v, %v; want the upgrade recorded", got, err)
	}

	close(blocked.block)
	<-done
}

// The UI shows a Thread Paused while an upgrade has it stopped. A Resume the
// operator sends then waits, and must succeed once the upgrade has resumed
// the session itself.
func TestResumeDuringUpgradeReturnsTheResumedSession(t *testing.T) {
	upgrader := &fakeImageUpgrader{block: make(chan struct{}), entered: make(chan struct{})}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-resume-wait", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	_, threadID := startIdleThread(t, svc, capsule.ID)
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	upgraded := make(chan error, 1)
	go func() {
		_, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "resume-wait-upgrade")
		upgraded <- err
	}()
	<-upgrader.entered
	paused, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || paused.State != domain.ThreadPaused {
		t.Fatalf("Thread during upgrade = %#v, %v; want Paused", paused, err)
	}

	resumed := make(chan error, 1)
	go func() {
		result, err := svc.ResumeThread(operatorContext(), threadID, paused.ResourceVersion, "operator-resume")
		if err == nil && result.Thread.State != domain.ThreadActive {
			err = errors.New("resume returned a Thread that is not active")
		}
		resumed <- err
	}()
	close(upgrader.block)
	if err := <-upgraded; err != nil {
		t.Fatalf("UpgradeCapsule: %v", err)
	}
	if err := <-resumed; err != nil {
		t.Fatalf("operator Resume during the upgrade: %v", err)
	}
}

func resumeFailedFrame(sequence uint64) ports.RuntimeStructuredEvent {
	return ports.RuntimeStructuredEvent{Sequence: sequence, Frame: adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindError,
		Code: "claude_resume_failed", Summary: "session not found",
	}}
}

func failedEndFrame(sequence uint64) ports.RuntimeStructuredEvent {
	return ports.RuntimeStructuredEvent{Sequence: sequence, Frame: adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindEnd, Status: adapterproto.StatusFailed,
	}}
}

// upgradeIdleThread upgrades a Capsule whose Thread is idle, which stops the
// session and resumes it on the new image.
func upgradeIdleThread(t *testing.T, name string) (*app.Service, *structuredRuntime, domain.ThreadID) {
	t.Helper()
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, domain.CapsuleID(name), oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, name); err != nil {
		t.Fatalf("UpgradeCapsule: %v", err)
	}
	runtime.mu.Lock()
	starts := runtime.startFrames
	runtime.mu.Unlock()
	if len(starts) != 2 || starts[1].Type != adapterproto.KindResume {
		t.Fatalf("start frames = %#v; want the session resumed after the upgrade", starts)
	}
	return svc, runtime, threadID
}

// assertFreshSessionAfterUpgrade checks that a harness that could not resume
// after the upgrade got a fresh session and a transcript divider.
func assertFreshSessionAfterUpgrade(t *testing.T, svc *app.Service, runtime *structuredRuntime, threadID domain.ThreadID) {
	t.Helper()
	runtime.mu.Lock()
	starts := runtime.startFrames
	// The fake replays its frames to every session; the fresh one has none
	// yet, and reading the transcript below syncs it.
	runtime.events = ports.RuntimeStructuredEvents{}
	runtime.mu.Unlock()
	if len(starts) != 3 || starts[2].Type != adapterproto.KindStart {
		t.Fatalf("start frames = %#v; want a fresh session after the failed resume", starts)
	}
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadActive {
		t.Fatalf("Thread = %#v, %v; want it active on the fresh session", thread, err)
	}
	page, err := svc.ThreadBlocks(operatorContext(), threadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range page.Items {
		if block.Event != nil && strings.Contains(block.Event.Summary, "harness started a fresh session") {
			return
		}
	}
	t.Fatalf("transcript = %#v; want the fresh-session divider", page.Items)
}

func TestHarnessThatCannotResumeAfterUpgradeStartsFreshSession(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh")
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{resumeFailedFrame(1), failedEndFrame(2)},
		NextCursor: 2,
	}
	runtime.mu.Unlock()
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread: %v", err)
	}
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
}

// The adapter's error and its session end are separate frames; a sync that
// lands between them must not lose the resume failure.
func TestFreshSessionFallbackSurvivesSplitSyncs(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-split")
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{resumeFailedFrame(1)},
		NextCursor: 1,
	}
	runtime.mu.Unlock()
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread before the end: %v", err)
	}
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{resumeFailedFrame(1), failedEndFrame(2)},
		NextCursor: 2,
	}
	runtime.mu.Unlock()
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread at the end: %v", err)
	}
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
}

// A session that fails for another reason after an upgrade is not retried.
func TestUnrelatedFailureAfterUpgradeKeepsThreadPaused(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-other")
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{failedEndFrame(1)},
		NextCursor: 1,
	}
	runtime.mu.Unlock()
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread: %v", err)
	}
	runtime.mu.Lock()
	starts := len(runtime.startFrames)
	runtime.mu.Unlock()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadPaused || starts != 2 {
		t.Fatalf("Thread = %#v (starts %d), %v; want it paused with no retry", thread, starts, err)
	}
}

// failResume makes the Thread's current session report that it could not
// resume, and syncs it.
func failResume(t *testing.T, svc *app.Service, runtime *structuredRuntime, threadID domain.ThreadID) error {
	t.Helper()
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{resumeFailedFrame(1), failedEndFrame(2)},
		NextCursor: 2,
	}
	runtime.mu.Unlock()
	return svc.SyncThread(operatorContext(), threadID)
}

// sendPrompt sends content to the Thread's current session.
func sendPrompt(t *testing.T, svc *app.Service, threadID domain.ThreadID, content, key string) domain.ThreadMessageID {
	t.Helper()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := svc.SendThreadMessage(operatorContext(), threadID, thread.ResourceVersion, content, key, "")
	if err != nil {
		t.Fatal(err)
	}
	return sent.MessageID
}

// freshPromptSends returns how often each prompt with content was sent other
// than as original, the message first sent to the failed session.
func freshPromptSends(runtime *structuredRuntime, original domain.ThreadMessageID, content string) []int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var sends []int
	for id, frame := range runtime.frames {
		if id != string(original) && frame.Type == adapterproto.KindUserMessage && frame.Content == content {
			sends = append(sends, runtime.sends[id])
		}
	}
	return sends
}

// assertPromptAfterDivider checks that the transcript shows content again
// after the fresh-session divider, for the session that received it.
func assertPromptAfterDivider(t *testing.T, svc *app.Service, threadID domain.ThreadID, content string) {
	t.Helper()
	page, err := svc.ThreadBlocks(operatorContext(), threadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	divider := false
	for _, block := range page.Items {
		if block.Event != nil && strings.Contains(block.Event.Summary, "harness started a fresh session") {
			divider = true
		}
		if divider && block.Role == domain.ThreadRoleUser && block.Content == content {
			return
		}
	}
	t.Fatalf("transcript = %#v; want %q after the divider", page.Items, content)
}

// A prompt sent to the session that then could not resume never ran, so the
// fresh session must be given it.
func TestFreshSessionAfterUpgradeGetsThePromptTheFailedSessionReceived(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-prompt")
	original := sendPrompt(t, svc, threadID, "do the task", "fresh-prompt")
	if err := failResume(t, svc, runtime, threadID); err != nil {
		t.Fatalf("SyncThread: %v", err)
	}
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
	if sends := freshPromptSends(runtime, original, "do the task"); len(sends) != 1 || sends[0] != 1 {
		t.Fatalf("fresh session prompt sends = %v; want the prompt sent to it once", sends)
	}
	assertPromptAfterDivider(t, svc, threadID, "do the task")
}

// A prompt whose send to the fresh session fails is delivered by a later
// sync, not lost.
func TestFreshSessionPromptIsRetriedAfterAFailedSend(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-retry")
	original := sendPrompt(t, svc, threadID, "do the task", "fresh-retry")
	runtime.mu.Lock()
	runtime.sendFailures = 1
	runtime.mu.Unlock()
	// A send that fails is left for a later sync; neither the sync nor a
	// transcript read fails on it.
	if err := failResume(t, svc, runtime, threadID); err != nil {
		t.Fatalf("SyncThread with a failed send: %v", err)
	}
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
	if sends := freshPromptSends(runtime, original, "do the task"); len(sends) != 1 || sends[0] != 2 {
		t.Fatalf("fresh session prompt sends = %v; want the failed send retried once", sends)
	}
	if sends := freshPromptSends(runtime, original, "do the task"); len(sends) != 1 || sends[0] != 2 {
		t.Fatalf("fresh session prompt sends = %v; want the failed send retried once", sends)
	}
	// Once delivered, later syncs do not send it again.
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatal(err)
	}
	if sends := freshPromptSends(runtime, original, "do the task"); sends[0] != 2 {
		t.Fatalf("fresh session prompt sends = %v; want no more sends", sends)
	}
}

// A transcript read during a send outage still works.
func TestTranscriptReadsWhileFreshSessionPromptCannotBeSent(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-outage")
	sendPrompt(t, svc, threadID, "do the task", "fresh-outage")
	runtime.mu.Lock()
	runtime.sendFailures = 100
	runtime.mu.Unlock()
	if err := failResume(t, svc, runtime, threadID); err != nil {
		t.Fatalf("SyncThread during the outage: %v", err)
	}
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
	if _, err := svc.ThreadBlocks(operatorContext(), threadID, 0, 100); err != nil {
		t.Fatalf("ThreadBlocks during the outage: %v", err)
	}
}

// A copy whose send failed, on a fresh session that then ended, is given to
// the Thread's next session.
func TestStrandedFreshSessionPromptReachesTheNextSession(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-stranded")
	original := sendPrompt(t, svc, threadID, "do the task", "fresh-stranded")
	runtime.mu.Lock()
	runtime.sendFailures = 1
	runtime.mu.Unlock()
	if err := failResume(t, svc, runtime, threadID); err != nil {
		t.Fatalf("SyncThread: %v", err)
	}
	// The fresh session fails for another reason before a sync retries it.
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{failedEndFrame(1)}, NextCursor: 1,
	}
	runtime.mu.Unlock()
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread at the fresh session's end: %v", err)
	}
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadPaused {
		t.Fatalf("Thread = %#v, %v; want it paused", thread, err)
	}
	if sends := freshPromptSends(runtime, original, "do the task"); len(sends) != 1 || sends[0] != 1 {
		t.Fatalf("fresh session prompt sends = %v; want only the failed send", sends)
	}

	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{}
	runtime.mu.Unlock()
	if _, err := svc.ResumeThread(operatorContext(), threadID, thread.ResourceVersion, "fresh-stranded-resume"); err != nil {
		t.Fatalf("ResumeThread: %v", err)
	}
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread on the next session: %v", err)
	}
	sends := freshPromptSends(runtime, original, "do the task")
	if len(sends) != 2 {
		t.Fatalf("prompt copies sent = %v; want the stranded one copied again for the next session", sends)
	}
	// Later syncs neither copy nor send it again.
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatal(err)
	}
	if again := freshPromptSends(runtime, original, "do the task"); len(again) != 2 {
		t.Fatalf("prompt copies sent = %v; want no more", again)
	}
}

// Only prompts are given to the fresh session again: a model change sent to
// the failed session is not a prompt and must not block the ones after it.
func TestFreshSessionResendsOnlyPrompts(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-model")
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	resumed := runtime.runs[thread.CurrentRunID]
	resumed.Models = true
	runtime.runs[thread.CurrentRunID] = resumed
	runtime.mu.Unlock()
	if _, err := svc.SetThreadModel(operatorContext(), threadID, thread.ResourceVersion, "other-model", "fresh-model-switch"); err != nil {
		t.Fatalf("SetThreadModel: %v", err)
	}
	original := sendPrompt(t, svc, threadID, "after the switch", "fresh-model-prompt")
	if err := failResume(t, svc, runtime, threadID); err != nil {
		t.Fatalf("SyncThread: %v", err)
	}
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
	if sends := freshPromptSends(runtime, original, "after the switch"); len(sends) != 1 || sends[0] != 1 {
		t.Fatalf("fresh session prompt sends = %v; want the prompt sent once", sends)
	}
}

// pauseThread ends the Thread's idle session.
func pauseThread(t *testing.T, svc *app.Service, threadID domain.ThreadID, key string) {
	t.Helper()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelThread(operatorContext(), threadID, thread.ResourceVersion, key); err != nil {
		t.Fatalf("CancelThread: %v", err)
	}
	thread, err = svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadPaused {
		t.Fatalf("Thread = %#v, %v; want it paused", thread, err)
	}
}

// resumeThreadThatCannotResume resumes a Paused Thread and syncs a session
// that cannot resume.
func resumeThreadThatCannotResume(t *testing.T, svc *app.Service, runtime *structuredRuntime, threadID domain.ThreadID, key string) {
	t.Helper()
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items:      []ports.RuntimeStructuredEvent{resumeFailedFrame(1), failedEndFrame(2)},
		NextCursor: 2,
	}
	runtime.mu.Unlock()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeThread(operatorContext(), threadID, thread.ResourceVersion, key); err != nil {
		t.Fatalf("ResumeThread: %v", err)
	}
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatalf("SyncThread: %v", err)
	}
}

// A Thread paused before the upgrade, which the upgrade did not stop, gets a
// fresh session when it is resumed later.
func TestThreadPausedBeforeUpgradeFallsBackWhenResumedLater(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-fresh-paused", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	pauseThread(t, svc, threadID, "fresh-paused-stop")
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "fresh-paused"); err != nil {
		t.Fatalf("UpgradeCapsule: %v", err)
	}

	resumeThreadThatCannotResume(t, svc, runtime, threadID, "fresh-paused-resume")
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
}

// A session saved after the upgrade was not written by the previous image;
// its resume failure is not an upgrade's and is left to the operator.
func TestResumeFailureOfSessionSavedAfterUpgradeIsNotRetried(t *testing.T) {
	svc, runtime, threadID := upgradeIdleThread(t, "cap-fresh-later")
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{idleFrame(1)}, NextCursor: 1,
	}
	runtime.mu.Unlock()
	if err := svc.SyncThread(operatorContext(), threadID); err != nil {
		t.Fatal(err)
	}
	pauseThread(t, svc, threadID, "fresh-later-stop")

	resumeThreadThatCannotResume(t, svc, runtime, threadID, "fresh-later-resume")
	runtime.mu.Lock()
	starts := runtime.startFrames
	runtime.mu.Unlock()
	thread, err := svc.GetThread(operatorContext(), threadID)
	if err != nil || thread.State != domain.ThreadPaused || len(starts) != 3 || starts[2].Type != adapterproto.KindResume {
		t.Fatalf("Thread = %#v (starts %#v), %v; want it paused with no fresh session", thread, starts, err)
	}
}

// Each Thread's session is judged on its own: another Thread on the Capsule
// resuming and going idle after the upgrade does not use up the fallback.
func TestFreshSessionFallbackIsPerThread(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-fresh-two", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, first := startIdleThread(t, svc, capsule.ID)
	pauseThread(t, svc, first, "fresh-two-stop-first")
	second, err := svc.CreateThread(operatorContext(), app.CreateThreadInput{
		CapsuleID: capsule.ID, Harness: "mock", FirstMessage: "second", Start: true, IdempotencyKey: "fresh-two-second",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncThread(operatorContext(), second.Thread.ID); err != nil {
		t.Fatal(err)
	}
	pauseThread(t, svc, second.Thread.ID, "fresh-two-stop-second")
	current, err := svc.GetCapsule(operatorContext(), capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, "fresh-two"); err != nil {
		t.Fatalf("UpgradeCapsule: %v", err)
	}

	// The first Thread resumes on the new image and goes idle.
	runtime.mu.Lock()
	runtime.events = ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{idleFrame(1)}, NextCursor: 1,
	}
	runtime.mu.Unlock()
	thread, err := svc.GetThread(operatorContext(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeThread(operatorContext(), first, thread.ResourceVersion, "fresh-two-resume-first"); err != nil {
		t.Fatalf("ResumeThread: %v", err)
	}
	if err := svc.SyncThread(operatorContext(), first); err != nil {
		t.Fatal(err)
	}
	pauseThread(t, svc, first, "fresh-two-stop-first-again")

	runtime.mu.Lock()
	before := len(runtime.startFrames)
	runtime.mu.Unlock()
	resumeThreadThatCannotResume(t, svc, runtime, second.Thread.ID, "fresh-two-resume-second")
	runtime.mu.Lock()
	starts := runtime.startFrames
	runtime.mu.Unlock()
	if len(starts) != before+2 || starts[len(starts)-1].Type != adapterproto.KindStart {
		t.Fatalf("start frames = %#v; want the second Thread resumed, then started fresh", starts[before:])
	}
	got, err := svc.GetThread(operatorContext(), second.Thread.ID)
	if err != nil || got.State != domain.ThreadActive {
		t.Fatalf("second Thread = %#v, %v; want it active on a fresh session", got, err)
	}
}

// The divider names the image the session was saved under, though more than
// one upgrade came after it.
func TestFreshSessionDividerNamesTheSessionsImage(t *testing.T) {
	upgrader := &fakeImageUpgrader{}
	svc, store, _, project := upgradeTestService(t, upgrader)
	capsule := seedCapsuleForUpgrade(t, principal.WithSystem(context.Background()), store, project, "cap-fresh-twice", oldClaudePack, domain.CapsuleReady, domain.IntentReady)
	runtime, threadID := startIdleThread(t, svc, capsule.ID)
	pauseThread(t, svc, threadID, "fresh-twice-stop")
	for i, release := range []string{"v0.18.0", "v0.19.0"} {
		svc.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:"+release, ""))
		current, err := svc.GetCapsule(operatorContext(), capsule.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.UpgradeCapsule(operatorContext(), capsule.ID, current.ResourceVersion, fmt.Sprintf("fresh-twice-%d", i)); err != nil {
			t.Fatalf("UpgradeCapsule to %s: %v", release, err)
		}
	}

	resumeThreadThatCannotResume(t, svc, runtime, threadID, "fresh-twice-resume")
	assertFreshSessionAfterUpgrade(t, svc, runtime, threadID)
	page, err := svc.ThreadBlocks(operatorContext(), threadID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range page.Items {
		if block.Event != nil && strings.Contains(block.Event.Summary, "harness started a fresh session") {
			if !strings.Contains(block.Event.Summary, "from v0.17.0 to v0.19.0") {
				t.Fatalf("divider = %q; want it from v0.17.0 to v0.19.0", block.Event.Summary)
			}
			return
		}
	}
	t.Fatal("no fresh-session divider")
}
