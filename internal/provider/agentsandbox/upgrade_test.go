package agentsandbox

import (
	"context"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
)

func TestUpgradeImageUpdatesPausedSandboxInPlaceWithoutPodChurn(t *testing.T) {
	provider, sandboxes, _ := testProvider(t)
	capsuleID := domain.CapsuleID("capsule-k8s-paused-upgrade")
	name := provider.resourceName(capsuleID)
	sb := provider.sandbox(capsuleID, name, "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0")
	sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	sbClient := sandboxes.AgentsV1beta1().Sandboxes(provider.config.Namespace)
	if _, err := sbClient.Create(context.Background(), sb, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	res, err := provider.UpgradeImage(context.Background(), ports.UpgradeCapsuleRequest{
		CapsuleID:      capsuleID,
		ResourceID:     name,
		ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0",
		Paused:         true,
	})
	if err != nil {
		t.Fatalf("UpgradeImage: %v", err)
	}
	if res.State != ports.ProviderPaused {
		t.Fatalf("state = %s, want paused", res.State)
	}
	updated, err := sbClient.Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Spec.PodTemplate.Spec.Containers[0].Image; got != "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0" {
		t.Fatalf("sandbox image = %q", got)
	}
	if updated.Spec.OperatingMode != sandboxv1beta1.SandboxOperatingModeSuspended {
		t.Fatalf("operating mode = %s, want Suspended", updated.Spec.OperatingMode)
	}
}
