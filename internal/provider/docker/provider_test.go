package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

type fakeEngine struct {
	image       image.InspectResponse
	containers  map[string]container.InspectResponse
	networks    map[string]network.Inspect
	volumes     map[string]volume.Volume
	created     *client.ContainerCreateOptions
	volumeFails int
	imageWait   <-chan struct{}
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{
		image:      image.InspectResponse{ID: "sha256:" + strings.Repeat("a", 64)},
		containers: make(map[string]container.InspectResponse),
		networks:   make(map[string]network.Inspect),
		volumes:    make(map[string]volume.Volume),
	}
}

func (e *fakeEngine) ImageInspect(ctx context.Context, _ string, _ ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	if e.imageWait != nil {
		select {
		case <-e.imageWait:
		case <-ctx.Done():
			return client.ImageInspectResult{}, ctx.Err()
		}
	}
	return client.ImageInspectResult{InspectResponse: e.image}, nil
}
func (e *fakeEngine) VolumeInspect(_ context.Context, id string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	value, ok := e.volumes[id]
	if !ok {
		return client.VolumeInspectResult{}, errdefs.ErrNotFound
	}
	return client.VolumeInspectResult{Volume: value}, nil
}
func (e *fakeEngine) VolumeCreate(_ context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	value := volume.Volume{Name: options.Name, Labels: options.Labels}
	e.volumes[options.Name] = value
	return client.VolumeCreateResult{Volume: value}, nil
}
func (e *fakeEngine) VolumeRemove(_ context.Context, id string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	if e.volumeFails > 0 {
		e.volumeFails--
		return client.VolumeRemoveResult{}, errors.New("injected volume removal failure")
	}
	if _, ok := e.volumes[id]; !ok {
		return client.VolumeRemoveResult{}, errdefs.ErrNotFound
	}
	delete(e.volumes, id)
	return client.VolumeRemoveResult{}, nil
}
func (e *fakeEngine) NetworkInspect(_ context.Context, id string, _ client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	value, ok := e.networks[id]
	if !ok {
		return client.NetworkInspectResult{}, errdefs.ErrNotFound
	}
	return client.NetworkInspectResult{Network: value}, nil
}
func (e *fakeEngine) NetworkCreate(_ context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	value := network.Inspect{Network: network.Network{Name: name, ID: name, Labels: options.Labels}}
	e.networks[name] = value
	return client.NetworkCreateResult{ID: name}, nil
}
func (e *fakeEngine) NetworkRemove(_ context.Context, id string, _ client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	if _, ok := e.networks[id]; !ok {
		return client.NetworkRemoveResult{}, errdefs.ErrNotFound
	}
	delete(e.networks, id)
	return client.NetworkRemoveResult{}, nil
}
func (e *fakeEngine) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	value, ok := e.containers[id]
	if !ok {
		return client.ContainerInspectResult{}, errdefs.ErrNotFound
	}
	return client.ContainerInspectResult{Container: value}, nil
}
func (e *fakeEngine) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	e.created = &options
	e.containers[options.Name] = container.InspectResponse{
		ID: options.Name, Config: options.Config, HostConfig: options.HostConfig,
		State: &container.State{},
	}
	return client.ContainerCreateResult{ID: options.Name}, nil
}
func (e *fakeEngine) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	value := e.containers[id]
	value.State.Running = true
	e.containers[id] = value
	return client.ContainerStartResult{}, nil
}
func (e *fakeEngine) ContainerPause(_ context.Context, id string, _ client.ContainerPauseOptions) (client.ContainerPauseResult, error) {
	value := e.containers[id]
	value.State.Paused = true
	e.containers[id] = value
	return client.ContainerPauseResult{}, nil
}
func (e *fakeEngine) ContainerUnpause(_ context.Context, id string, _ client.ContainerUnpauseOptions) (client.ContainerUnpauseResult, error) {
	value := e.containers[id]
	value.State.Paused = false
	e.containers[id] = value
	return client.ContainerUnpauseResult{}, nil
}
func (e *fakeEngine) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if _, ok := e.containers[id]; !ok {
		return client.ContainerRemoveResult{}, errdefs.ErrNotFound
	}
	delete(e.containers, id)
	return client.ContainerRemoveResult{}, nil
}

func testProvider(t *testing.T, engine *fakeEngine) *Provider {
	t.Helper()
	provider, err := NewWithEngine(Config{
		StateDir: t.TempDir(), Image: "fixture:tag",
		OperationTimeout: time.Second, SetupTimeout: time.Second,
	}, engine)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestContainerRequestIsHardenedAndLoopbackOnly(t *testing.T) {
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	names := provider.resourceNames("capsule-1")
	labels := provider.labels("capsule-1", "capsule", engine.image.ID)
	options := provider.ContainerCreateOptions(names, engine.image.ID, "secret-token", labels)

	if options.Config.User == "" || options.Config.User == "0" {
		t.Fatalf("container user = %q", options.Config.User)
	}
	if !options.HostConfig.ReadonlyRootfs || options.HostConfig.Privileged {
		t.Fatal("root filesystem or privilege hardening missing")
	}
	if len(options.HostConfig.CapDrop) != 1 || options.HostConfig.CapDrop[0] != "ALL" {
		t.Fatalf("capability drop = %#v", options.HostConfig.CapDrop)
	}
	if len(options.HostConfig.SecurityOpt) != 1 ||
		options.HostConfig.SecurityOpt[0] != "no-new-privileges:true" {
		t.Fatalf("security options = %#v", options.HostConfig.SecurityOpt)
	}
	if options.HostConfig.Memory == 0 || options.HostConfig.NanoCPUs == 0 ||
		options.HostConfig.PidsLimit == nil {
		t.Fatal("resource limits are missing")
	}
	if len(options.HostConfig.Binds) != 0 || len(options.HostConfig.Mounts) != 1 {
		t.Fatalf("mounts = %#v, binds = %#v", options.HostConfig.Mounts, options.HostConfig.Binds)
	}
	if tmp := options.HostConfig.Tmpfs["/tmp"]; !strings.Contains(tmp, "noexec") {
		t.Fatalf("/tmp tmpfs is not noexec: %q", tmp)
	}
	if home := options.HostConfig.Tmpfs["/home/capsule"]; !strings.Contains(home, "exec") ||
		strings.Contains(home, "noexec") {
		t.Fatalf("private home tmpfs cannot load harness-native libraries: %q", home)
	}
	mounted := options.HostConfig.Mounts[0]
	if mounted.Source != names.volume || mounted.Target != workspace ||
		strings.HasPrefix(mounted.Source, "/") {
		t.Fatalf("workspace mount = %#v", mounted)
	}
	port := network.MustParsePort(protocolPort)
	bindings := options.HostConfig.PortBindings[port]
	if len(bindings) != 1 || bindings[0].HostIP != netip.MustParseAddr("127.0.0.1") ||
		bindings[0].HostPort != "" {
		t.Fatalf("port bindings = %#v", bindings)
	}
	if options.HostConfig.PidMode != "" || options.HostConfig.IpcMode != "" ||
		options.HostConfig.UTSMode != "" || len(options.HostConfig.Devices) != 0 {
		t.Fatal("host namespaces or devices were enabled")
	}
	for key, value := range options.Config.Labels {
		if strings.Contains(key, "token") || strings.Contains(value, "secret-token") {
			t.Fatalf("token leaked into label %q", key)
		}
	}
}

func TestImageResolutionRecordsImmutableIdentity(t *testing.T) {
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	resolved, err := provider.resolveImage(context.Background(), "mutable:tag")
	if err != nil || resolved != engine.image.ID {
		t.Fatalf("resolve = %q, %v", resolved, err)
	}
	engine.image.ID = "mutable:tag"
	if _, err := provider.resolveImage(context.Background(), "mutable:tag"); err == nil {
		t.Fatal("expected unresolved mutable image rejection")
	}
}

func TestProviderLossIsBoundedByOperationTimeout(t *testing.T) {
	t.Parallel()
	engine := newFakeEngine()
	engine.imageWait = make(chan struct{})
	provider, err := NewWithEngine(Config{
		StateDir: t.TempDir(), Image: "fixture:tag",
		OperationTimeout: 20 * time.Millisecond, SetupTimeout: time.Second,
	}, engine)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = provider.Create(context.Background(), ports.CreateCapsuleRequest{CapsuleID: "capsule-timeout"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Create error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("provider timeout was not bounded: %s", elapsed)
	}
}

func TestTokenFilePermissionsAndRestartRecovery(t *testing.T) {
	directory := t.TempDir()
	first, err := newTokenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	token, err := first.loadOrCreate("capsule-1")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(first.path("capsule-1"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token permissions = %v, %v", info, err)
	}
	second, err := newTokenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := second.loadOrCreate("capsule-1")
	if err != nil || recovered != token {
		t.Fatalf("recovered token differs: %v", err)
	}
	if err := os.Remove(second.path("capsule-1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "elsewhere"), second.path("capsule-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.load("capsule-1"); err == nil {
		t.Fatal("expected token symlink rejection")
	}
}

func TestAdoptionOwnershipAndRetryablePartialCleanup(t *testing.T) {
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	capsuleID := domain.CapsuleID("capsule-1")
	names := provider.resourceNames(capsuleID)
	containerLabels := provider.labels(capsuleID, "capsule", engine.image.ID)
	engine.containers[names.container] = container.InspectResponse{
		Config: &container.Config{Labels: containerLabels},
		State:  &container.State{Running: true},
	}
	engine.networks[names.network] = network.Inspect{
		Network: network.Network{Labels: provider.labels(capsuleID, "network", "")},
	}
	engine.volumes[names.volume] = volume.Volume{
		Name: names.volume, Labels: provider.labels(capsuleID, "workspace", ""),
	}
	engine.volumes["unrelated"] = volume.Volume{Name: "unrelated", Labels: map[string]string{"other": "true"}}
	if _, err := provider.tokens.loadOrCreate(capsuleID); err != nil {
		t.Fatal(err)
	}

	if err := provider.ensureVolume(context.Background(), names.volume, capsuleID); err != nil {
		t.Fatalf("owned volume adoption: %v", err)
	}
	engine.volumes[names.volume].Labels[provider.label(labelCapsule)] = "other-capsule"
	if err := provider.ensureVolume(context.Background(), names.volume, capsuleID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mismatched adoption error = %v", err)
	}
	engine.volumes[names.volume].Labels[provider.label(labelCapsule)] = string(capsuleID)

	engine.volumeFails = 1
	if err := provider.Delete(context.Background(), names.container); err == nil {
		t.Fatal("expected partial cleanup failure")
	}
	if _, ok := engine.containers[names.container]; ok {
		t.Fatal("container survived partial cleanup")
	}
	if _, ok := engine.networks[names.network]; ok {
		t.Fatal("network survived partial cleanup")
	}
	if _, ok := engine.volumes[names.volume]; !ok {
		t.Fatal("failed volume cleanup was not retryable")
	}
	if err := provider.Delete(context.Background(), names.container); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.volumes[names.volume]; ok {
		t.Fatal("owned volume survived retry")
	}
	if _, ok := engine.volumes["unrelated"]; !ok {
		t.Fatal("unrelated resource was removed")
	}
	if _, err := provider.tokens.load(capsuleID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token survived completed cleanup: %v", err)
	}
}

func TestStructuredRuntimeUsesProtectedLoopbackTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(capsuleproto.VersionHeader, capsuleproto.Version)
		switch request.URL.Path {
		case "/v1/structured/sessions":
			_, _ = writer.Write([]byte(`{"runId":"structured-run","state":"Running","startedAt":"2026-01-01T00:00:00Z","pty":false,"cursor":0,"protocol":"meridian.adapter.v1","restartCount":0}`))
		case "/v1/structured/sessions/structured-run/events":
			_, _ = writer.Write([]byte(`{"events":[{"sequence":1,"frame":{"protocol":"meridian.adapter.v1","type":"status","status":"running","cursor":1}}],"nextCursor":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	capsuleID := domain.CapsuleID("capsule-structured")
	names := provider.resourceNames(capsuleID)
	engine.containers[names.container] = container.InspectResponse{
		Config: &container.Config{Labels: provider.labels(capsuleID, "capsule", engine.image.ID)},
		State:  &container.State{Running: true},
		NetworkSettings: &container.NetworkSettings{Ports: network.PortMap{
			network.MustParsePort(protocolPort): {{
				HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: endpoint.Port(),
			}},
		}},
	}
	if _, err := provider.tokens.loadOrCreate(capsuleID); err != nil {
		t.Fatal(err)
	}
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil || !capabilities.Structured || !capabilities.Browse || !capabilities.Delivery {
		t.Fatalf("capabilities = %#v, %v", capabilities, err)
	}
	run, err := provider.StartStructured(context.Background(), ports.RuntimeStructuredStartRequest{
		ResourceID: names.container, RunID: "structured-run", Harness: "structured",
		Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStart,
			ID: "start-1", SessionID: "session-1",
		},
	})
	if err != nil || !run.Structured || run.PTY {
		t.Fatalf("structured start = %#v, %v", run, err)
	}
	events, err := provider.StructuredEvents(
		context.Background(), names.container, "structured-run", 0,
	)
	if err != nil || len(events.Items) != 1 ||
		events.Items[0].Frame.Status != adapterproto.StatusRunning {
		t.Fatalf("structured events = %#v, %v", events, err)
	}
}
