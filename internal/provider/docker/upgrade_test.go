package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
)

const (
	oldUpgradeDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newUpgradeDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	upgradeTarget    = "ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0"
)

// upgradeSupervisor answers health and status for every container that
// carries the returned network settings.
func upgradeSupervisor(t *testing.T) *container.NetworkSettings {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(capsuleproto.VersionHeader, capsuleproto.Version)
		switch request.URL.Path {
		case capsuleproto.HealthPath:
			_ = json.NewEncoder(writer).Encode(capsuleproto.ProbeResponse{Status: "ok", Version: capsuleproto.Version})
		case capsuleproto.StatusPath:
			_ = json.NewEncoder(writer).Encode(capsuleproto.StatusResponse{Version: capsuleproto.Version, Preparation: capsuleproto.StateReady})
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &container.NetworkSettings{Ports: network.PortMap{
		network.MustParsePort(protocolPort): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: endpoint.Port()}},
	}}
}

// upgradeFixture installs an owned Capsule container on the old image with its
// volumes and network, and points created containers at a test supervisor.
func upgradeFixture(t *testing.T, state *container.State, withHome bool) (*fakeEngine, *Provider, domain.CapsuleID, resourceNames) {
	t.Helper()
	engine := newFakeEngine()
	engine.image = image.InspectResponse{ID: newUpgradeDigest}
	settings := upgradeSupervisor(t)
	engine.createNetwork = settings
	provider := testProvider(t, engine)
	capsuleID := domain.CapsuleID("capsule-upgrade")
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
	if withHome {
		engine.volumes[names.homeVolume] = volume.Volume{
			Name: names.homeVolume, Labels: provider.labels(capsuleID, "home", ""),
		}
	}
	engine.containers[names.container] = ownedContainer(provider, capsuleID, names.container, oldUpgradeDigest, state, settings)
	return engine, provider, capsuleID, names
}

func ownedContainer(provider *Provider, capsuleID domain.CapsuleID, name, digest string, state *container.State, settings *container.NetworkSettings) container.InspectResponse {
	return container.InspectResponse{
		ID:              name,
		Config:          &container.Config{Image: digest, Labels: provider.labels(capsuleID, "capsule", digest)},
		State:           state,
		NetworkSettings: settings,
	}
}

func upgradeRequest(capsuleID domain.CapsuleID, names resourceNames, paused bool) ports.UpgradeCapsuleRequest {
	return ports.UpgradeCapsuleRequest{
		CapsuleID: capsuleID, ResourceID: names.container, ImageReference: upgradeTarget, Paused: paused,
	}
}

func assertNoUpgradeLeftovers(t *testing.T, engine *fakeEngine, names resourceNames) {
	t.Helper()
	for _, leftover := range []string{names.container + upgradeSuffix, names.container + previousSuffix} {
		if _, ok := engine.containers[leftover]; ok {
			t.Fatalf("leftover container %s", leftover)
		}
	}
}

func TestUpgradeImagePausedBootTestsReplacementAndLeavesItStopped(t *testing.T) {
	engine, provider, capsuleID, names := upgradeFixture(t,
		&container.State{Running: true, Paused: true, Status: container.StatePaused}, false)

	res, err := provider.UpgradeImage(context.Background(), upgradeRequest(capsuleID, names, true))
	if err != nil {
		t.Fatalf("UpgradeImage: %v", err)
	}
	if res.State != ports.ProviderStopped || res.ImageDigest != newUpgradeDigest {
		t.Fatalf("resource = %#v, want stopped + %s", res, newUpgradeDigest)
	}
	if _, ok := engine.volumes[names.homeVolume]; !ok {
		t.Fatalf("expected legacy Capsule upgrade to create persistent home volume %s", names.homeVolume)
	}
	if engine.starts != 1 {
		t.Fatalf("replacement starts = %d, want one readiness boot", engine.starts)
	}
	assertNoUpgradeLeftovers(t, engine, names)
	updated, ok := engine.containers[names.container]
	if !ok || updated.Config.Image != newUpgradeDigest || updated.State.Running {
		t.Fatalf("updated container = %#v, want the new image, stopped", updated)
	}
}

func TestUpgradeImagePausedBootFailureKeepsOldContainer(t *testing.T) {
	engine, provider, capsuleID, names := upgradeFixture(t,
		&container.State{Running: true, Paused: true, Status: container.StatePaused}, true)
	engine.startExits = true

	if _, err := provider.UpgradeImage(context.Background(), upgradeRequest(capsuleID, names, true)); err == nil {
		t.Fatal("expected boot failure")
	}
	assertNoUpgradeLeftovers(t, engine, names)
	old, ok := engine.containers[names.container]
	if !ok || old.Config.Image != oldUpgradeDigest {
		t.Fatalf("old container was not kept: %#v", old)
	}
}

func TestUpgradeImagePullFailureLeavesOldContainerUntouched(t *testing.T) {
	engine, provider, capsuleID, names := upgradeFixture(t,
		&container.State{Running: true, Status: container.StateRunning}, true)
	engine.missing = true
	engine.pullErr = context.DeadlineExceeded

	if _, err := provider.UpgradeImage(context.Background(), upgradeRequest(capsuleID, names, false)); err == nil {
		t.Fatal("expected pull error")
	}
	current := engine.containers[names.container]
	if !current.State.Running || current.Config.Image != oldUpgradeDigest {
		t.Fatalf("old container was disturbed on pull failure: %#v", current)
	}
	if len(engine.containers) != 1 {
		t.Fatalf("containers = %d, want only the old one", len(engine.containers))
	}
}

func TestUpgradeImageBootFailureRestartsOldContainer(t *testing.T) {
	engine, provider, capsuleID, names := upgradeFixture(t,
		&container.State{Running: true, Status: container.StateRunning}, true)
	engine.startExits = true

	_, err := provider.UpgradeImage(context.Background(), upgradeRequest(capsuleID, names, false))
	if err == nil {
		t.Fatal("expected boot failure")
	}
	assertNoUpgradeLeftovers(t, engine, names)
	old, ok := engine.containers[names.container]
	if !ok || old.Config.Image != oldUpgradeDigest {
		t.Fatalf("old container was not preserved on rollback: %#v", old)
	}
	// The old container exits too here, so the rollback error says the old
	// runtime did not come back instead of hiding it.
	if !strings.Contains(err.Error(), "restore previous Capsule container") {
		t.Fatalf("error = %v, want the failed restore reported", err)
	}
}

func TestUpgradeImageRunningSwapsNameAndRemovesPrevious(t *testing.T) {
	engine, provider, capsuleID, names := upgradeFixture(t,
		&container.State{Running: true, Status: container.StateRunning}, true)

	res, err := provider.UpgradeImage(context.Background(), upgradeRequest(capsuleID, names, false))
	if err != nil {
		t.Fatalf("UpgradeImage: %v", err)
	}
	if res.State != ports.ProviderReady || res.ImageDigest != newUpgradeDigest {
		t.Fatalf("resource = %#v", res)
	}
	assertNoUpgradeLeftovers(t, engine, names)
	updated := engine.containers[names.container]
	if updated.Config.Image != newUpgradeDigest || !updated.State.Running {
		t.Fatalf("updated container = %#v", updated)
	}
}

func TestUpgradeImageRenameFailureRestoresOldContainerName(t *testing.T) {
	engine, provider, capsuleID, names := upgradeFixture(t,
		&container.State{Running: true, Status: container.StateRunning}, true)
	engine.renameErr = map[string]error{names.container + upgradeSuffix: errors.New("injected rename failure")}

	if _, err := provider.UpgradeImage(context.Background(), upgradeRequest(capsuleID, names, false)); err == nil {
		t.Fatal("expected rename failure")
	}
	assertNoUpgradeLeftovers(t, engine, names)
	old, ok := engine.containers[names.container]
	if !ok || old.Config.Image != oldUpgradeDigest || !old.State.Running {
		t.Fatalf("old container = %#v, want it back under its name and running", old)
	}
}

func TestRecoverUpgradeSettlesInterruptedSwaps(t *testing.T) {
	running := func() *container.State { return &container.State{Running: true, Status: container.StateRunning} }
	stopped := func() *container.State { return &container.State{Status: container.StateExited} }
	for _, tc := range []struct {
		name        string
		containers  map[string]string // suffix → image digest
		wantImage   string
		wantRunning bool
	}{
		{
			// Interrupted after the replacement started: the old container
			// still holds the name, so the replacement must go.
			name:       "replacement booting",
			containers: map[string]string{"": oldUpgradeDigest, upgradeSuffix: newUpgradeDigest},
			wantImage:  oldUpgradeDigest,
		},
		{
			// Interrupted between the two renames.
			name:        "between renames",
			containers:  map[string]string{previousSuffix: oldUpgradeDigest, upgradeSuffix: newUpgradeDigest},
			wantImage:   newUpgradeDigest,
			wantRunning: true,
		},
		{
			name:       "old container moved aside",
			containers: map[string]string{previousSuffix: oldUpgradeDigest},
			wantImage:  oldUpgradeDigest,
		},
		{
			// The swap finished; only removing the previous container was left.
			name:        "previous not removed",
			containers:  map[string]string{"": newUpgradeDigest, previousSuffix: oldUpgradeDigest},
			wantImage:   newUpgradeDigest,
			wantRunning: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, provider, capsuleID, names := upgradeFixture(t, stopped(), true)
			delete(engine.containers, names.container)
			for suffix, digest := range tc.containers {
				state := stopped()
				if suffix == upgradeSuffix {
					state = running()
				}
				name := names.container + suffix
				engine.containers[name] = ownedContainer(provider, capsuleID, name, digest, state, nil)
			}

			res, onTarget, err := provider.RecoverUpgrade(context.Background(), upgradeRequest(capsuleID, names, false))
			if err != nil {
				t.Fatalf("RecoverUpgrade: %v", err)
			}
			assertNoUpgradeLeftovers(t, engine, names)
			current, ok := engine.containers[names.container]
			if !ok || current.Config.Image != tc.wantImage || res.ImageDigest != tc.wantImage {
				t.Fatalf("container = %#v, resource = %#v, want image %s", current, res, tc.wantImage)
			}
			if onTarget != tc.wantRunning {
				t.Fatalf("running target = %v, want %v", onTarget, tc.wantRunning)
			}
		})
	}
}
