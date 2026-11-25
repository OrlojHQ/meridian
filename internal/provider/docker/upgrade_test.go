package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
)

func TestUpgradeImagePausedCreatesHomeVolumeAndSwapsContainer(t *testing.T) {
	engine := newFakeEngine()
	oldDigest := "sha256:" + strings.Repeat("1", 64)
	newDigest := "sha256:" + strings.Repeat("2", 64)
	engine.image = image.InspectResponse{ID: newDigest}

	provider := testProvider(t, engine)
	capsuleID := domain.CapsuleID("capsule-upgrade-paused")
	names := provider.resourceNames(capsuleID)
	if _, err := provider.tokens.loadOrCreate(capsuleID); err != nil {
		t.Fatal(err)
	}
	// Seed a legacy container without a home volume.
	engine.networks[names.network] = network.Inspect{
		Network: network.Network{Labels: provider.labels(capsuleID, "network", "")},
	}
	engine.volumes[names.volume] = volume.Volume{
		Name: names.volume, Labels: provider.labels(capsuleID, "workspace", ""),
	}
	engine.containers[names.container] = container.InspectResponse{
		ID: names.container,
		Config: &container.Config{
			Image:  oldDigest,
			Labels: provider.labels(capsuleID, "capsule", oldDigest),
		},
		State: &container.State{Running: true, Paused: true, Status: container.StatePaused},
	}

	res, err := provider.UpgradeImage(context.Background(), ports.UpgradeCapsuleRequest{
		CapsuleID:      capsuleID,
		ResourceID:     names.container,
		ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0",
		Paused:         true,
	})
	if err != nil {
		t.Fatalf("UpgradeImage: %v", err)
	}
	if res.State != ports.ProviderPaused || res.ImageDigest != newDigest {
		t.Fatalf("resource = %#v, want paused + %s", res, newDigest)
	}
	if _, ok := engine.volumes[names.homeVolume]; !ok {
		t.Fatalf("expected legacy Capsule upgrade to create persistent home volume %s", names.homeVolume)
	}
	if _, ok := engine.containers[names.container+"-upgrade"]; ok {
		t.Fatal("temporary upgrade container was not renamed")
	}
	updated, ok := engine.containers[names.container]
	if !ok || updated.Config.Image != newDigest {
		t.Fatalf("updated container = %#v", updated)
	}
}

func TestUpgradeImagePullFailureLeavesOldContainerUntouched(t *testing.T) {
	engine := newFakeEngine()
	oldDigest := "sha256:" + strings.Repeat("1", 64)
	engine.missing = true
	engine.pullErr = context.DeadlineExceeded

	provider := testProvider(t, engine)
	capsuleID := domain.CapsuleID("capsule-upgrade-fail-pull")
	names := provider.resourceNames(capsuleID)
	engine.containers[names.container] = container.InspectResponse{
		ID: names.container,
		Config: &container.Config{
			Image:  oldDigest,
			Labels: provider.labels(capsuleID, "capsule", oldDigest),
		},
		State: &container.State{Running: true, Status: container.StateRunning},
	}

	_, err := provider.UpgradeImage(context.Background(), ports.UpgradeCapsuleRequest{
		CapsuleID:      capsuleID,
		ResourceID:     names.container,
		ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0",
	})
	if err == nil {
		t.Fatal("expected pull error")
	}
	current := engine.containers[names.container]
	if !current.State.Running || current.Config.Image != oldDigest {
		t.Fatalf("old container was disturbed on pull failure: %#v", current)
	}
}

func TestUpgradeImageBootFailureRollsBackToOldContainer(t *testing.T) {
	engine := newFakeEngine()
	oldDigest := "sha256:" + strings.Repeat("1", 64)
	newDigest := "sha256:" + strings.Repeat("2", 64)
	engine.image = image.InspectResponse{ID: newDigest}
	engine.startExits = true

	provider := testProvider(t, engine)
	capsuleID := domain.CapsuleID("capsule-upgrade-rollback")
	names := provider.resourceNames(capsuleID)
	if _, err := provider.tokens.loadOrCreate(capsuleID); err != nil {
		t.Fatal(err)
	}
	engine.networks[names.network] = network.Inspect{
		Network: network.Network{Labels: provider.labels(capsuleID, "network", "")},
	}
	engine.volumes[names.volume] = volume.Volume{
		Name: names.volume, Labels: provider.labels(capsuleID, "workspace", ""),
	}
	engine.volumes[names.homeVolume] = volume.Volume{
		Name: names.homeVolume, Labels: provider.labels(capsuleID, "home", ""),
	}
	engine.containers[names.container] = container.InspectResponse{
		ID: names.container,
		Config: &container.Config{
			Image:  oldDigest,
			Labels: provider.labels(capsuleID, "capsule", oldDigest),
		},
		State: &container.State{Running: true, Status: container.StateRunning},
	}

	_, err := provider.UpgradeImage(context.Background(), ports.UpgradeCapsuleRequest{
		CapsuleID:      capsuleID,
		ResourceID:     names.container,
		ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0",
	})
	if err == nil {
		t.Fatal("expected boot failure")
	}
	if _, ok := engine.containers[names.container+"-upgrade"]; ok {
		t.Fatal("failed upgrade container was not removed during rollback")
	}
	old, ok := engine.containers[names.container]
	if !ok || old.Config.Image != oldDigest {
		t.Fatalf("old container was not preserved on rollback: %#v", old)
	}
	if engine.starts < 2 {
		t.Fatalf("expected rollback to attempt restarting old container, starts=%d", engine.starts)
	}
}
