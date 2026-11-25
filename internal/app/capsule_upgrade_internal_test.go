package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	fake "github.com/OrlojHQ/meridian/internal/testutil/fakeprovider"
)

type countingGetProvider struct {
	*fake.Provider
	gets atomic.Int32
}

func (p *countingGetProvider) Get(ctx context.Context, id string) (ports.ProviderResource, error) {
	p.gets.Add(1)
	return p.Provider.Get(ctx, id)
}

type sequenceIDs struct{ next atomic.Int64 }

func (s *sequenceIDs) NewID() string { return fmt.Sprintf("id-%d", s.next.Add(1)) }

// An upgrade owns the runtime: the reconciler must not restart a container the
// swap stopped, or a second capsuled would run on the Capsule's volumes.
func TestReconcilerLeavesUpgradingCapsuleAlone(t *testing.T) {
	ctx := principal.WithSystem(context.Background())
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &fixedClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	ids := &sequenceIDs{}
	provider := &countingGetProvider{Provider: fake.New(fake.Options{})}
	reconciler := NewReconciler(store, provider, clock, ids)
	service := NewService(store, clock, ids, reconciler)
	project, err := service.CreateProject(principal.With(context.Background(), principal.Owner()), "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule := domain.Capsule{
		ID: "cap-upgrading", OwnerUserID: project.OwnerUserID, ProjectID: project.ID, TimelineID: "tl-upgrading",
		Name: "upgrading", State: domain.CapsuleReady, DesiredState: domain.IntentReady,
		ProviderResourceID: "resource-upgrading", Maintenance: "upgrading",
		LastActivityAt: clock.now, CreatedAt: clock.now, UpdatedAt: clock.now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		return tx.InsertTimeline(ctx, domain.Timeline{
			ID: capsule.TimelineID, ProjectID: project.ID, CapsuleID: capsule.ID,
			Reason: domain.TimelineRoot, CreatedAt: clock.now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if err := reconciler.reconcile(ctx, capsule.ID); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gets := provider.gets.Load(); gets != 0 {
		t.Fatalf("provider Get calls = %d, want none while upgrading", gets)
	}
}

// An older pack's adapter rejects resume state a newer one wrote, so the
// invalid-state codes must fall back to a fresh session too.
func TestHarnessResumeFailureCodesIncludeRejectedResumeState(t *testing.T) {
	for _, code := range []string{
		"claude_resume_failed", "claude_resume_invalid",
		"codex_resume_failed", "codex_resume_invalid",
		"acp_resume_failed", "acp_resume_invalid", "acp_resume_unsupported",
		"pi_resume_failed", "pi_resume_invalid",
	} {
		if !isHarnessResumeFailureCode(code) {
			t.Errorf("%s does not fall back to a fresh session", code)
		}
	}
	for _, code := range []string{"", "acp_auth_required", "pi_command_failed", "claude_turn_failed"} {
		if isHarnessResumeFailureCode(code) {
			t.Errorf("%s falls back to a fresh session", code)
		}
	}
}

type settlingUpgrader struct{}

func (settlingUpgrader) UpgradeImage(context.Context, ports.UpgradeCapsuleRequest) (ports.ProviderResource, error) {
	return ports.ProviderResource{}, nil
}

func (settlingUpgrader) RecoverUpgrade(_ context.Context, request ports.UpgradeCapsuleRequest) (ports.ProviderResource, bool, error) {
	return ports.ProviderResource{ID: request.ResourceID}, true, nil
}

// An upgrade that startup recovery finds finished is recorded like one that
// completed in place, so a session saved before it may fall back to a fresh one.
func TestSettledUpgradeArmsFreshSessionFallback(t *testing.T) {
	ctx := principal.WithSystem(context.Background())
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &fixedClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	ids := &sequenceIDs{}
	service := NewService(store, clock, ids, nil)
	service.ConfigureHarnessPacks(domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", ""))
	service.ConfigureImageUpgrade(settlingUpgrader{}, nil, nil)
	project, err := service.CreateProjectConfigured(principal.With(context.Background(), principal.Owner()), "project", ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"}},
	}, "project")
	if err != nil {
		t.Fatal(err)
	}
	const fromRef = "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0"
	capsule := domain.Capsule{
		ID: "cap-settled", OwnerUserID: project.OwnerUserID, ProjectID: project.ID, TimelineID: "tl-settled",
		Name: "settled", State: domain.CapsuleReady, DesiredState: domain.IntentReady,
		ProviderResourceID: "resource-settled", Maintenance: maintenanceUpgrading, ImageReference: fromRef,
		LastActivityAt: clock.now, CreatedAt: clock.now, UpdatedAt: clock.now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		return tx.InsertTimeline(ctx, domain.Timeline{
			ID: capsule.TimelineID, ProjectID: project.ID, CapsuleID: capsule.ID,
			Reason: domain.TimelineRoot, CreatedAt: clock.now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.RecoverUpgradingCapsules(ctx); err != nil {
		t.Fatalf("RecoverUpgradingCapsules: %v", err)
	}
	got, err := service.GetCapsule(ctx, capsule.ID)
	if err != nil || got.Maintenance != "" || got.ImageReference == fromRef {
		t.Fatalf("Capsule = %#v, %v; want the upgrade recorded", got, err)
	}
	// The fresh-session fallback reads the upgrade from this event.
	var events []domain.Event
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		events, err = reader.ListEvents(ctx, "capsule", string(capsule.ID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != upgradedEvent {
			continue
		}
		var data struct {
			PreviousImageReference string `json:"previousImageReference"`
			ImageReference         string `json:"imageReference"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil ||
			data.PreviousImageReference != fromRef || data.ImageReference != got.ImageReference {
			t.Fatalf("upgrade event = %s, %v; want %s to %s", event.Data, err, fromRef, got.ImageReference)
		}
		return
	}
	t.Fatalf("events = %#v; want the upgrade recorded", events)
}
