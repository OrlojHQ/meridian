package app_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestIdleScannerRequestsOnlyEligibleCapsules(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	clock := &testClock{now: base.Add(2 * time.Hour)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	service := app.NewService(store, clock, ids, reconciler)

	project := domain.Project{
		ID: "project", Name: "project", CreatedAt: base, UpdatedAt: base, ResourceVersion: 1,
	}
	names := []domain.CapsuleID{"idle", "recent", "run", "thread", "maintenance"}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		for _, id := range names {
			if err := tx.InsertCapsule(ctx, domain.Capsule{
				ID: id, ProjectID: project.ID, Name: string(id),
				State: domain.CapsuleReady, DesiredState: domain.IntentReady,
				ProviderResourceID: "resource-" + string(id),
				LastActivityAt:     base, CreatedAt: base, UpdatedAt: base, ResourceVersion: 1,
			}); err != nil {
				return err
			}
		}
		runTime := base.Add(time.Minute)
		if err := tx.InsertRun(ctx, domain.Run{
			ID: "run-1", CapsuleID: "run", Harness: "test", State: domain.RunRunning,
			CreatedAt: runTime, UpdatedAt: runTime, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		if err := tx.InsertThread(ctx, domain.Thread{
			ID: "thread-1", CapsuleID: "thread", State: domain.ThreadActive,
			AdapterID: "test", WrappedDEK: bytes.Repeat([]byte{1}, 64),
			KEKID: "key", KEKVersion: 1, EnvelopeVersion: 1,
			CreatedAt: runTime, UpdatedAt: runTime, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		maintenance, err := tx.GetCapsule(ctx, "maintenance")
		if err != nil {
			return err
		}
		maintenance.Maintenance = "capture"
		maintenance.ResourceVersion++
		return tx.UpdateCapsule(ctx, maintenance, maintenance.ResourceVersion-1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.TouchCapsuleActivity(ctx, "recent"); err != nil {
		t.Fatal(err)
	}

	count, err := reconciler.ScanIdleCapsules(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idle pause requests = %d, want 1", count)
	}
	runtimeService := app.NewService(store, clock, ids, reconciler, &runRuntime{})
	if _, err := runtimeService.StartRun(
		ctx, "idle", "test", "prompt", "pending-pause-run", 0, 0,
	); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("Run against pending idle pause = %v", err)
	}
	for _, id := range names {
		capsule, err := service.GetCapsule(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := domain.IntentReady
		if id == "idle" {
			want = domain.IntentPaused
		}
		if capsule.DesiredState != want {
			t.Fatalf("%s desired state = %s, want %s", id, capsule.DesiredState, want)
		}
	}
}

func TestIdleScannerIsDisabledAndBounded(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	reconciler := app.NewReconciler(store, fake.New(fake.Options{}), clock, &testIDs{})
	if err := reconciler.StartIdleScanner(ctx, 0, 0); err != nil {
		t.Fatalf("disabled scanner = %v", err)
	}
	if _, err := reconciler.ScanIdleCapsules(ctx, 0); err != nil {
		t.Fatalf("disabled scan = %v", err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		project := domain.Project{
			ID: "project", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		for index := 0; index < 101; index++ {
			id := domain.CapsuleID(string(rune(0x1000 + index)))
			if err := tx.InsertCapsule(ctx, domain.Capsule{
				ID: id, ProjectID: project.ID, Name: "idle",
				State: domain.CapsuleReady, DesiredState: domain.IntentReady,
				LastActivityAt: now.Add(-2 * time.Hour),
				CreatedAt:      now, UpdatedAt: now, ResourceVersion: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	count, err := reconciler.ScanIdleCapsules(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Fatalf("bounded scan count = %d, want 100", count)
	}
}
