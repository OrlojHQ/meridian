package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	fake "github.com/OrlojHQ/meridian/internal/testutil/fakeprovider"
)

// newIsolationReconcileHarness runs a real reconciler over the fake provider
// and returns a service whose daemon wiring is not yet given a verified
// isolation class.
func newIsolationReconcileHarness(
	t *testing.T, options fake.Options,
) (context.Context, *app.Service, *fake.Provider, *app.Reconciler) {
	t.Helper()
	ctx, cancel := context.WithCancel(operatorContext())
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(options)
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	t.Cleanup(reconciler.Close)
	service := app.NewService(store, clock, ids, reconciler)
	return ctx, service, provider, reconciler
}

func awaitCapsuleState(
	t *testing.T, service *app.Service, id domain.CapsuleID, done func(domain.Capsule) bool,
) domain.Capsule {
	t.Helper()
	ctx := operatorContext()
	deadline := time.Now().Add(2 * time.Second)
	for {
		capsule, err := service.GetCapsule(ctx, id)
		if err == nil && done(capsule) {
			return capsule
		}
		if time.Now().After(deadline) {
			t.Fatalf("Capsule did not settle: %#v, %v", capsule, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReconcilerFailsCapsuleOnUnmetIsolation(t *testing.T) {
	// The provider refuses the create with ErrUnsupported, as the AgentSandbox
	// provider does when its RuntimeClass verification fails (ADR 0050). A
	// Capsule seeded by hand stands in for work the daemon accepted before
	// the provider's verified class changed under it.
	ctx, cancel := context.WithCancel(operatorContext())
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{Unsupported: map[string]int{"create": 1}})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	t.Cleanup(reconciler.Close)
	service := app.NewService(store, clock, ids, reconciler)
	reconciler.ConfigureIsolation(domain.IsolationContainer)
	project := domain.Project{
		OwnerUserID: domain.BootstrapOwnerID,
		ID:          "project-refuse", Name: "project",
		RequiredIsolation: domain.IsolationSandbox,
		CreatedAt:         clock.Now(), UpdatedAt: clock.Now(), ResourceVersion: 1,
	}
	capsule := domain.Capsule{
		ID: "capsule-refuse", ProjectID: project.ID, Name: "capsule",
		State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
		CreatedAt: clock.Now(), UpdatedAt: clock.Now(), ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, capsule)
	}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Enqueue(ctx, capsule.ID); err != nil {
		t.Fatal(err)
	}
	failed := awaitCapsuleState(t, service, capsule.ID, func(c domain.Capsule) bool {
		return c.State == domain.CapsuleFailed
	})
	if !strings.Contains(failed.Failure, "sandbox isolation is required") ||
		!strings.Contains(failed.Failure, "container") {
		t.Fatalf("failed Capsule failure = %q", failed.Failure)
	}
}

func TestReconcilerAllowsMetIsolation(t *testing.T) {
	ctx, service, _, reconciler := newIsolationReconcileHarness(t, fake.Options{})
	service.ConfigureIsolation(domain.IsolationSandbox)
	reconciler.ConfigureIsolation(domain.IsolationSandbox)
	project, err := service.CreateProjectConfigured(ctx, "sandboxed", app.ProjectConfiguration{
		RequiredIsolation: domain.IsolationSandbox,
	}, "project-reconcile-met")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-met")
	if err != nil {
		t.Fatal(err)
	}
	ready := awaitCapsuleState(t, service, capsule.ID, func(c domain.Capsule) bool {
		return c.State == domain.CapsuleReady
	})
	if ready.Failure != "" {
		t.Fatalf("ready Capsule failure = %q", ready.Failure)
	}
}

func TestReconcilerDoesNotRetryUnmetIsolation(t *testing.T) {
	// The reconciler stops on an unmet isolation requirement instead of
	// burning its retry budget against a provider that will refuse again.
	ctx, cancel := context.WithCancel(operatorContext())
	t.Cleanup(cancel)
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{Unsupported: map[string]int{"create": 1}})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	t.Cleanup(reconciler.Close)
	service := app.NewService(store, clock, ids, reconciler)
	reconciler.ConfigureIsolation(domain.IsolationContainer)
	project := domain.Project{
		OwnerUserID: domain.BootstrapOwnerID,
		ID:          "project-no-retry", Name: "project",
		RequiredIsolation: domain.IsolationSandbox,
		CreatedAt:         clock.Now(), UpdatedAt: clock.Now(), ResourceVersion: 1,
	}
	capsule := domain.Capsule{
		ID: "capsule-no-retry", ProjectID: project.ID, Name: "capsule",
		State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
		CreatedAt: clock.Now(), UpdatedAt: clock.Now(), ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, capsule)
	}); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Enqueue(ctx, capsule.ID); err != nil {
		t.Fatal(err)
	}
	failed := awaitCapsuleState(t, service, capsule.ID, func(c domain.Capsule) bool {
		return c.State == domain.CapsuleFailed
	})
	// The reconciler's own floor check refuses before the provider is asked:
	// the failure names the requirement, and the provider saw no create at
	// all, so no retry can follow.
	time.Sleep(50 * time.Millisecond)
	if !strings.Contains(failed.Failure, "isolation requirement unmet") {
		t.Fatalf("failed Capsule failure = %q", failed.Failure)
	}
	if got := provider.Calls("create"); got != 0 {
		t.Fatalf("provider create calls = %d, want 0 (the reconciler refuses before the provider)", got)
	}
}
