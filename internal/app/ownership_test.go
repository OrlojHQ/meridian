package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestCreatedObjectsBelongToTheCaller(t *testing.T) {
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)},
		&testIDs{}, &recordingQueue{})

	project, err := service.CreateProject(ctx, "project", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	if project.OwnerUserID != principal.BootstrapOwner {
		t.Fatalf("created Project owner = %q", project.OwnerUserID)
	}
	if replay, err := service.CreateProject(ctx, "project", "project-key"); err != nil || replay.OwnerUserID != principal.BootstrapOwner {
		t.Fatalf("replayed Project owner = %q, %v", replay.OwnerUserID, err)
	}
	stored, err := service.GetProject(ctx, project.ID)
	if err != nil || stored.OwnerUserID != principal.BootstrapOwner {
		t.Fatalf("stored Project owner = %q, %v", stored.OwnerUserID, err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}
	if capsule, err = service.GetCapsule(ctx, capsule.ID); err != nil || capsule.OwnerUserID != principal.BootstrapOwner {
		t.Fatalf("Capsule owner = %q, %v", capsule.OwnerUserID, err)
	}

	// Only a user creates a root object. The system, the break-glass admin,
	// and a Capsule acting for its owner cannot.
	asCapsule, ok := principal.Capsule(principal.BootstrapOwner, capsule.ID)
	if !ok {
		t.Fatal("Capsule principal")
	}
	for name, caller := range map[string]context.Context{
		"system":      principal.WithSystem(context.Background()),
		"break-glass": principal.With(context.Background(), principal.BreakGlass()),
		"capsule":     principal.With(context.Background(), asCapsule),
	} {
		if _, err := service.CreateProject(caller, "orphan", "orphan-"+name); !errors.Is(err, principal.ErrMissing) {
			t.Fatalf("%s created a Project: %v", name, err)
		}
	}
	page, err := service.ListProjects(ctx, 0, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("Projects = %+v, %v", page.Items, err)
	}
}
