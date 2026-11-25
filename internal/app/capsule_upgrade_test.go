package app_test

import (
	"bytes"
	"context"
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
