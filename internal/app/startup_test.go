package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestStartupProgressIsThrottledAndPullFailuresAreSpecific(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	secret := "registry-token-SECRET"
	provider := fake.New(fake.Options{CreateHook: func(_ context.Context, request ports.CreateCapsuleRequest) error {
		if request.ReportStartup == nil {
			return errors.New("create did not receive a startup reporter")
		}
		request.ReportStartup(ports.StartupReport{
			Phase: domain.StartupPhase("rm -rf /"), Phases: []domain.StartupPhase{domain.StartupPhase("rm -rf /")},
		})
		for i := 1; i <= 25; i++ {
			request.ReportStartup(ports.StartupReport{
				Phase: domain.PhasePullingImage, Phases: domain.ImagePullPhases(),
				CurrentBytes: int64(i) * 1_000_000, TotalBytes: 540_000_000,
			})
		}
		request.ReportStartup(ports.StartupReport{
			Phase: domain.PhaseCreatingCapsule, Phases: domain.ImagePullPhases(),
		})
		return &domain.StartupFailure{Code: domain.PullAuthRequired}
	}})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	project, err := service.CreateProject(ctx, "project", "project-startup")
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateCapsule(ctx, project.ID, "capsule", "capsule-startup")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var capsule domain.Capsule
	for {
		capsule, err = service.GetCapsule(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == domain.CapsuleFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capsule did not fail: %#v", capsule)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if capsule.FailureCode != domain.PullAuthRequired ||
		capsule.Failure != domain.PullAuthRequired.Message() ||
		strings.Contains(capsule.Failure, secret) {
		t.Fatalf("failure = %q %q", capsule.FailureCode, capsule.Failure)
	}
	if capsule.Startup == nil || capsule.Startup.Phase != domain.PhaseCreatingCapsule {
		t.Fatalf("startup = %#v", capsule.Startup)
	}
	if capsule.Startup.Pull != nil {
		t.Fatal("non-pull phase retained byte progress")
	}
	var events []domain.Event
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		events, err = reader.ListEvents(ctx, "capsule", string(capsule.ID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	startupEvents := 0
	for _, event := range events {
		if event.Type != "capsule.startup" {
			continue
		}
		startupEvents++
		if strings.Contains(string(event.Data), secret) || strings.Contains(string(event.Data), "rm -rf") {
			t.Fatalf("startup event retained untrusted text: %s", event.Data)
		}
	}
	if startupEvents != 2 {
		t.Fatalf("startup events = %d, want the first pull sample and the phase change", startupEvents)
	}

	retried, err := service.RetryCapsule(ctx, capsule.ID, capsule.ResourceVersion, "retry-startup")
	if err != nil {
		t.Fatal(err)
	}
	if retried.State != domain.CapsuleCreating || retried.Failure != "" || retried.FailureCode != "" {
		t.Fatalf("retry = %#v", retried)
	}
}
