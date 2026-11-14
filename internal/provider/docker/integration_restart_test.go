package docker

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/system"
	"github.com/moby/moby/client"
)

// TestDockerProviderIntegrationStoppedContainerIsRestarted stops a Ready
// Capsule's container behind Meridian's back, as an engine restart does, and
// checks that the live refresh fails the active Run and the reconciler starts
// the same container again with its workspace.
func TestDockerProviderIntegrationStoppedContainerIsRestarted(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(principal.With(context.Background(), principal.Owner()))
	defer cancel()
	store, err := sqlite.Open(ctx, path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, err := New(Config{
		Image: image, StateDir: path.Join(root, "provider"),
		OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	reconciler := app.NewReconciler(store, provider, system.Clock{}, system.IDs{})
	service := app.NewService(store, system.Clock{}, system.IDs{}, reconciler, provider)
	reconciler.ConfigureRuntimeRecovery(service.FailStoppedRuntimeRuns)
	reconciler.Start(ctx)
	defer reconciler.Close()
	project, err := service.CreateProjectConfigured(ctx, "stopped", app.ProjectConfiguration{
		RepositoryURL: "file:///fixture", ImageReference: image,
	}, "stopped-project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "stopped", "stopped-capsule")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for capsule.State != domain.CapsuleReady {
		capsule, err = service.GetCapsule(ctx, capsule.ID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("Capsule readiness = %#v, %v", capsule, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = provider.Delete(context.Background(), capsule.ProviderResourceID) })
	run, err := service.StartRun(ctx, capsule.ID, "mock-long", "long", "stopped-run", 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	stopper, ok := provider.engine.(interface {
		ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error)
	})
	if !ok {
		t.Fatal("Docker engine cannot stop containers")
	}
	if _, err := stopper.ContainerStop(ctx, capsule.ProviderResourceID, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if resource, err := provider.Get(ctx, capsule.ProviderResourceID); err != nil ||
		resource.State != ports.ProviderStopped {
		t.Fatalf("stopped container = %#v, %v", resource, err)
	}

	if err := service.RefreshLiveRuntime(principal.WithSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	failed, err := service.GetRunStored(ctx, run.ID)
	if err != nil || failed.State != domain.RunFailed ||
		failed.Failure != "Capsule runtime stopped while this Run was active" {
		t.Fatalf("Run after the container stopped = %#v, %v", failed, err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		resource, err := provider.Get(ctx, capsule.ProviderResourceID)
		if err == nil && resource.State == ports.ProviderReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted container = %#v, %v", resource, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	current, err := service.GetCapsule(ctx, capsule.ID)
	if err != nil || current.State != domain.CapsuleReady {
		t.Fatalf("Capsule after restart = %#v, %v", current, err)
	}
	next, err := service.StartRun(ctx, capsule.ID, "mock-long", "again", "stopped-run-again", 0, 0)
	if err != nil || next.State.Terminal() {
		t.Fatalf("Run on the restarted Capsule = %#v, %v", next, err)
	}
}
