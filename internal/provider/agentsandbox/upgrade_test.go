package agentsandbox

import (
	"context"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	corev1 "k8s.io/api/core/v1"
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

// legacySandbox is a Sandbox as Meridian created it before ADR 0064: an
// EmptyDir home, a single workspace claim, and no session mount or flag.
func legacySandbox(provider *Provider, capsuleID domain.CapsuleID, name, image string) *sandboxv1beta1.Sandbox {
	sb := provider.sandbox(capsuleID, name, image)
	spec := &sb.Spec.PodTemplate.Spec
	var volumes []corev1.Volume
	for _, volume := range spec.Volumes {
		if volume.Name != "session" {
			volumes = append(volumes, volume)
		}
	}
	spec.Volumes = append(volumes, corev1.Volume{Name: "home", VolumeSource: corev1.VolumeSource{
		EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: quantityPointer("512Mi")},
	}})
	container := &spec.Containers[0]
	var mounts []corev1.VolumeMount
	for _, mount := range container.VolumeMounts {
		if mount.Name != "session" {
			mounts = append(mounts, mount)
		}
	}
	container.VolumeMounts = mounts
	var args []string
	for _, arg := range container.Args {
		if !strings.HasPrefix(arg, "--session-dir=") {
			args = append(args, arg)
		}
	}
	container.Args = args
	sb.Spec.VolumeClaimTemplates = sb.Spec.VolumeClaimTemplates[:1]
	return sb
}

func TestUpgradeImageGivesLegacySandboxTheSessionMount(t *testing.T) {
	provider, sandboxes, _ := testProvider(t)
	capsuleID := domain.CapsuleID("capsule-k8s-legacy-upgrade")
	name := provider.resourceName(capsuleID)
	sb := legacySandbox(provider, capsuleID, name, "ghcr.io/orlojhq/meridian-capsule-claude:v0.17.0")
	sb.Spec.OperatingMode = sandboxv1beta1.SandboxOperatingModeSuspended
	sbClient := sandboxes.AgentsV1beta1().Sandboxes(provider.config.Namespace)
	if _, err := sbClient.Create(context.Background(), sb, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	request := ports.UpgradeCapsuleRequest{
		CapsuleID: capsuleID, ResourceID: name,
		ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0", Paused: true,
	}
	// Upgrading twice must not duplicate the mount, volume, or flag.
	for range 2 {
		if _, err := provider.UpgradeImage(context.Background(), request); err != nil {
			t.Fatalf("UpgradeImage: %v", err)
		}
	}
	updated, err := sbClient.Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := updated.Spec.PodTemplate.Spec
	count := func(match func(string) bool, values []string) int {
		n := 0
		for _, value := range values {
			if match(value) {
				n++
			}
		}
		return n
	}
	var volumes, mounts []string
	for _, volume := range spec.Volumes {
		volumes = append(volumes, volume.Name)
	}
	for _, mount := range spec.Containers[0].VolumeMounts {
		mounts = append(mounts, mount.MountPath)
	}
	if count(func(v string) bool { return v == "session" }, volumes) != 1 ||
		count(func(v string) bool { return v == "home" }, volumes) != 1 {
		t.Fatalf("volumes = %v, want one session and the legacy EmptyDir home", volumes)
	}
	if count(func(v string) bool { return v == sessionDir }, mounts) != 1 {
		t.Fatalf("mounts = %v, want one %s", mounts, sessionDir)
	}
	if count(func(v string) bool { return v == "--session-dir="+sessionDir }, spec.Containers[0].Args) != 1 {
		t.Fatalf("args = %v, want one --session-dir", spec.Containers[0].Args)
	}
	if len(updated.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("immutable claim templates changed: %#v", updated.Spec.VolumeClaimTemplates)
	}

	_, onTarget, err := provider.RecoverUpgrade(context.Background(), request)
	if err != nil || !onTarget {
		t.Fatalf("RecoverUpgrade = %v, %v; want the upgraded image reported", onTarget, err)
	}
	request.ImageReference = "ghcr.io/orlojhq/meridian-capsule-claude:v0.19.0"
	if _, onTarget, err := provider.RecoverUpgrade(context.Background(), request); err != nil || onTarget {
		t.Fatalf("RecoverUpgrade for another image = %v, %v", onTarget, err)
	}
}
