package agentsandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
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
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !capabilities.Snapshot || !capabilities.Clone || !capabilities.Preview {
		t.Fatalf("capsuled transport capabilities = %#v", capabilities)
	}
	capture, err := provider.CaptureWorkspace(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	archive, readErr := io.ReadAll(capture.Archive)
	closeErr := capture.Archive.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read captured workspace = %v; close = %v", readErr, closeErr)
	}
	digest := sha256.Sum256(archive)
	if err := provider.RestoreWorkspace(
		ctx, resource.ID, hex.EncodeToString(digest[:]), int64(len(archive)), bytes.NewReader(archive),
	); err != nil {
		t.Fatalf("restore captured workspace: %v", err)
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
		t.Fatal("capture of deleted Sandbox unexpectedly succeeded")
	}
}
