package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	fake "github.com/OrlojHQ/meridian/internal/testutil/fakeprovider"
)

// setupFailingProvider allocates the Capsule's resources and then fails, as
// a provider does when the workspace checkout or setup command fails.
type setupFailingProvider struct {
	*fake.Provider
}

func (p setupFailingProvider) Create(
	ctx context.Context,
	request ports.CreateCapsuleRequest,
) (ports.ProviderResource, error) {
	if _, err := p.Provider.Create(ctx, request); err != nil {
		return ports.ProviderResource{}, err
	}
	return ports.ProviderResource{}, errors.New("Capsule setup failed: preparation command failed")
}

func TestDeletingACapsuleWhoseSetupFailedRemovesItsResources(t *testing.T) {
	ctx, cancel := context.WithCancel(operatorContext())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := setupFailingProvider{fake.New(fake.Options{})}
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	project, err := service.CreateProject(ctx, "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule")
	if err != nil {
		t.Fatal(err)
	}
	capsule = waitForCapsuleState(t, service, capsule.ID, domain.CapsuleFailed)
	if capsule.ProviderResourceID != "" {
		t.Fatalf("failed setup recorded resource %q", capsule.ProviderResourceID)
	}
	resourceID := provider.ResourceID(capsule.ID)
	if state := provider.State(resourceID); state != ports.ProviderReady {
		t.Fatalf("failed setup left resource %s, want it still running", state)
	}

	if _, err := service.DeleteCapsule(ctx, capsule.ID, capsule.ResourceVersion, "delete"); err != nil {
		t.Fatal(err)
	}
	waitForCapsuleState(t, service, capsule.ID, domain.CapsuleDeleted)
	if state := provider.State(resourceID); state != ports.ProviderDeleted {
		t.Fatalf("deleted Capsule left its provider resource %s", state)
	}
}

func waitForCapsuleState(
	t *testing.T,
	service *app.Service,
	id domain.CapsuleID,
	want domain.CapsuleState,
) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		capsule, err := service.GetCapsule(operatorContext(), id)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == want {
			return capsule
		}
		if time.Now().After(deadline) {
			t.Fatalf("Capsule state = %s (%s), want %s", capsule.State, capsule.Failure, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
