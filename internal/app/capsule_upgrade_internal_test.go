package app

import (
	"context"
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
