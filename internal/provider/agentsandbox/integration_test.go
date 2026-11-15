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

// mergeConfig overlays the isolation fields onto the base integration config.
func mergeConfig(base, isolation Config) Config {
	base.RuntimeClass = isolation.RuntimeClass
	base.IsolationClass = isolation.IsolationClass
	base.IsolationHandlers = isolation.IsolationHandlers
	return base
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
	// The ADR 0050 negative run sets an isolation class whose RuntimeClass
	// exists in the cluster but uses a never-isolating handler; Create must
	// refuse it before any Sandbox side effect.
	if class := os.Getenv("AGENTSANDBOX_TEST_REFUSE_ISOLATION"); class != "" {
		runtimeClass := os.Getenv("AGENTSANDBOX_TEST_RUNTIME_CLASS")
		if runtimeClass == "" {
			t.Fatal("AGENTSANDBOX_TEST_RUNTIME_CLASS is required with AGENTSANDBOX_TEST_REFUSE_ISOLATION")
		}
		provider, err := New(Config{
			Namespace: namespace, Image: image, NamePrefix: "meridian-it",
			RuntimeClass: runtimeClass, IsolationClass: domain.IsolationClass(class),
			VolumeSize: "1Gi", TTL: time.Hour, OperationTimeout: 3 * time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
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
		return
	}
	// The positive run verifies the configured RuntimeClass handler and
	// reports its class through Capabilities.
	var isolationConfig Config
	if runtimeClass := os.Getenv("MERIDIAN_AGENTSANDBOX_RUNTIME_CLASS"); runtimeClass != "" {
		isolationClass := os.Getenv("MERIDIAN_AGENTSANDBOX_ISOLATION_CLASS")
		if isolationClass == "" {
			t.Fatal("MERIDIAN_AGENTSANDBOX_ISOLATION_CLASS is required with MERIDIAN_AGENTSANDBOX_RUNTIME_CLASS")
		}
		isolationConfig = Config{
			RuntimeClass: runtimeClass, IsolationClass: domain.IsolationClass(isolationClass),
		}
	}
	provider, err := New(mergeConfig(Config{
		Namespace: namespace, Image: image, NamePrefix: "meridian-it",
		VolumeSize: "1Gi", TTL: time.Hour, OperationTimeout: 3 * time.Minute,
	}, isolationConfig))
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
	if isolationConfig.IsolationClass != "" && capabilities.Isolation != isolationConfig.IsolationClass {
		t.Fatalf(
			"verified isolation = %q, want %q",
			capabilities.Isolation, isolationConfig.IsolationClass,
		)
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
