package agentsandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestIntegrationIsolation checks ADR 0050 RuntimeClass verification
// against a real API server. Kind runs no Kata or gVisor runtime, so it never
// creates a Sandbox on the isolating class: "refuse" proves a never-isolating
// handler is refused before any side effect, and "verify" proves the handler
// check and Capabilities report only.
func TestIntegrationIsolation(t *testing.T) {
	mode := os.Getenv("MERIDIAN_AGENTSANDBOX_ISOLATION_MODE")
	if os.Getenv("MERIDIAN_AGENTSANDBOX_TEST") != "1" || mode == "" {
		t.Skip("set MERIDIAN_AGENTSANDBOX_TEST=1 and MERIDIAN_AGENTSANDBOX_ISOLATION_MODE for the opt-in cluster test")
	}
	namespace := os.Getenv("MERIDIAN_AGENTSANDBOX_NAMESPACE")
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	runtimeClass := os.Getenv("MERIDIAN_AGENTSANDBOX_RUNTIME_CLASS")
	class := domain.IsolationClass(os.Getenv("MERIDIAN_AGENTSANDBOX_ISOLATION_CLASS"))
	if namespace == "" || image == "" || runtimeClass == "" || class == "" {
		t.Fatal("namespace, image, runtime class, and isolation class are required")
	}
	provider, err := New(Config{
		Namespace: namespace, Image: image, NamePrefix: "meridian-it",
		RuntimeClass: runtimeClass, IsolationClass: class,
		VolumeSize: "1Gi", TTL: time.Hour, OperationTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch mode {
	case "refuse":
		if _, err := provider.Create(ctx, ports.CreateCapsuleRequest{
			CapsuleID: domain.CapsuleID("integration-refuse-" + time.Now().UTC().Format("20060102-150405.000000000")),
		}); !errors.Is(err, domain.ErrUnsupported) {
			t.Fatalf("declined handler create error = %v", err)
		}
		sandboxes, err := provider.sandboxes.List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(sandboxes.Items) != 0 {
			t.Fatalf("refused create still produced %d Sandboxes", len(sandboxes.Items))
		}
	case "verify":
		capabilities, err := provider.Capabilities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if capabilities.Isolation != class {
			t.Fatalf("verified isolation = %q, want %q", capabilities.Isolation, class)
		}
	default:
		t.Fatalf("unknown MERIDIAN_AGENTSANDBOX_ISOLATION_MODE %q", mode)
	}
}

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
