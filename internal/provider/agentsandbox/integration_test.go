package agentsandbox

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestIntegrationLifecycle(t *testing.T) {
	if os.Getenv("MERIDIAN_AGENTSANDBOX_TEST") != "1" {
		t.Skip("set MERIDIAN_AGENTSANDBOX_TEST=1 for the opt-in cluster test")
	}
	namespace := os.Getenv("MERIDIAN_AGENTSANDBOX_NAMESPACE")
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if namespace == "" || image == "" {
		t.Skip("MERIDIAN_AGENTSANDBOX_NAMESPACE and MERIDIAN_CAPSULE_IMAGE are required")
	}
	provider, err := New(Config{
		Namespace: namespace, Image: image, NamePrefix: "meridian-it",
		VolumeSize: "1Gi", TTL: time.Hour, OperationTimeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	request := ports.CreateCapsuleRequest{
		CapsuleID: domain.CapsuleID("integration-" + time.Now().UTC().Format("20060102-150405.000000000")),
	}
	resource, err := provider.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		_ = provider.Delete(cleanup, resource.ID)
	})
	replayed, err := provider.Create(ctx, request)
	if err != nil || replayed.ID != resource.ID {
		t.Fatalf("idempotent create = %#v, %v", replayed, err)
	}
	paused, err := provider.Pause(ctx, resource.ID)
	if err != nil || paused.State != ports.ProviderPaused {
		t.Fatalf("pause = %#v, %v", paused, err)
	}
	resumed, err := provider.Resume(ctx, resource.ID)
	if err != nil || resumed.State != ports.ProviderReady {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	if err := provider.Delete(ctx, resource.ID); err != nil {
		t.Fatal(err)
	}
	deleted, err := provider.Get(ctx, resource.ID)
	if err != nil || deleted.State != ports.ProviderDeleted {
		t.Fatalf("deleted get = %#v, %v", deleted, err)
	}
	if _, err := provider.CaptureWorkspace(ctx, resource.ID); err == nil {
		t.Fatal("unsupported CSI Moment capture unexpectedly succeeded")
	}
}
