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
	"github.com/moby/moby/api/types/network"
)

// stoppedCapsule installs an owned Capsule container that an engine restart
// left exited, with a supervisor that answers once the container runs again.
func stoppedCapsule(t *testing.T, engine *fakeEngine, provider *Provider) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(capsuleproto.VersionHeader, capsuleproto.Version)
		switch request.URL.Path {
		case capsuleproto.HealthPath:
			_ = json.NewEncoder(writer).Encode(capsuleproto.ProbeResponse{
				Status: "ok", Version: capsuleproto.Version,
			})
		case capsuleproto.StatusPath:
			_ = json.NewEncoder(writer).Encode(capsuleproto.StatusResponse{
				Version: capsuleproto.Version, Preparation: capsuleproto.StateReady,
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	capsuleID := domain.CapsuleID("capsule-restart")
	names := provider.resourceNames(capsuleID)
	engine.containers[names.container] = container.InspectResponse{
		Config: &container.Config{Labels: provider.labels(capsuleID, "capsule", engine.image.ID)},
		State:  &container.State{Status: container.StateExited, ExitCode: 0},
		NetworkSettings: &container.NetworkSettings{Ports: network.PortMap{
			network.MustParsePort(protocolPort): {{
				HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: endpoint.Port(),
			}},
		}},
	}
	if _, err := provider.tokens.loadOrCreate(capsuleID); err != nil {
		t.Fatal(err)
	}
	return names.container
}

func TestStoppedContainerIsReportedAndRestartedByResume(t *testing.T) {
	ctx := context.Background()
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	id := stoppedCapsule(t, engine, provider)

	resource, err := provider.Get(ctx, id)
	if err != nil || resource.State != ports.ProviderStopped || resource.Reason != "exit code 0" {
		t.Fatalf("stopped Get = %#v, %v", resource, err)
	}
	if _, err := provider.runtimeClient(ctx, id); !errors.Is(err, domain.ErrRuntimeStopped) ||
		!errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("stopped runtime error = %v", err)
	}

	resource, err = provider.Resume(ctx, id)
	if err != nil || resource.State != ports.ProviderReady {
		t.Fatalf("restart = %#v, %v", resource, err)
	}
	if engine.starts != 1 {
		t.Fatalf("container starts = %d, want 1", engine.starts)
	}
	if engine.created != nil {
		t.Fatal("restart recreated the container")
	}
	if _, err := provider.runtimeClient(ctx, id); err != nil {
		t.Fatalf("restarted runtime = %v", err)
	}
}

func TestRestartFailsFastWhenTheContainerExitsAgain(t *testing.T) {
	ctx := context.Background()
	engine := newFakeEngine()
	engine.startExits = true
	provider := testProvider(t, engine)
	id := stoppedCapsule(t, engine, provider)

	_, err := provider.Resume(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "stopped before its supervisor was ready (exit code 1)") {
		t.Fatalf("crashing restart = %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("crashing restart waited for the timeout: %v", err)
	}
}

func TestPausedAndDeadContainersAreNotStopped(t *testing.T) {
	ctx := context.Background()
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	id := stoppedCapsule(t, engine, provider)

	value := engine.containers[id]
	value.State = &container.State{Status: container.StatePaused, Paused: true, Running: true}
	engine.containers[id] = value
	if resource, err := provider.Get(ctx, id); err != nil || resource.State != ports.ProviderPaused {
		t.Fatalf("paused Get = %#v, %v", resource, err)
	}
	if _, err := provider.runtimeClient(ctx, id); errors.Is(err, domain.ErrRuntimeStopped) ||
		!errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("paused runtime error = %v", err)
	}

	value.State = &container.State{Status: container.StateDead, Dead: true}
	engine.containers[id] = value
	if resource, err := provider.Get(ctx, id); err == nil {
		t.Fatalf("dead Get = %#v, want an error", resource)
	}
	if engine.starts != 0 {
		t.Fatalf("container starts = %d, want 0", engine.starts)
	}
}

// Meridian, not the engine, decides when a Capsule runs again (ADR 0044).
func TestCapsuleContainerHasNoRestartPolicy(t *testing.T) {
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	names := provider.resourceNames("capsule-policy")
	options := provider.ContainerCreateOptions(
		names, engine.image.ID, "secret-token", provider.labels("capsule-policy", "capsule", engine.image.ID),
	)
	if policy := options.HostConfig.RestartPolicy.Name; policy != "" && policy != container.RestartPolicyDisabled {
		t.Fatalf("restart policy = %q", policy)
	}
}
