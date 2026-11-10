package docker

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

func TestLocalGatewayRoute(t *testing.T) {
	bridge := netip.MustParseAddr("172.17.0.1")
	loopback := GatewayRoute{Listen: netip.MustParseAddr("127.0.0.1"), HostEntry: HostGateway}
	for _, test := range []struct {
		name, goos, host string
		info             system.Info
		bridge           netip.Addr
		want             GatewayRoute
		fails            bool
	}{
		{name: "docker desktop on macOS", goos: "darwin", host: "unix:///Users/me/.docker/run/docker.sock", info: system.Info{OperatingSystem: "Docker Desktop"}, want: loopback},
		{name: "orbstack", goos: "darwin", host: "unix:///Users/me/.orbstack/run/docker.sock", info: system.Info{OperatingSystem: "OrbStack"}, want: loopback},
		{name: "docker desktop on Windows", goos: "windows", host: "npipe:////./pipe/docker_engine", info: system.Info{OperatingSystem: "Docker Desktop"}, want: loopback},
		{name: "docker desktop on Linux", goos: "linux", host: "unix:///home/me/.docker/desktop/docker.sock", info: system.Info{OperatingSystem: "Docker Desktop"}, want: loopback},
		{name: "native Linux", goos: "linux", host: "unix:///var/run/docker.sock", info: system.Info{OperatingSystem: "Ubuntu 24.04"}, bridge: bridge, want: GatewayRoute{Listen: bridge, HostEntry: "172.17.0.1"}},
		{name: "native Linux without bridge", goos: "linux", host: "unix:///var/run/docker.sock", info: system.Info{OperatingSystem: "Debian"}, fails: true},
		{name: "public bridge address", goos: "linux", host: "unix:///var/run/docker.sock", bridge: netip.MustParseAddr("203.0.113.1"), fails: true},
		{name: "remote engine", goos: "darwin", host: "tcp://10.0.0.5:2376", info: system.Info{OperatingSystem: "Docker Desktop"}, fails: true},
		{name: "ssh engine", goos: "linux", host: "ssh://me@builder", bridge: bridge, fails: true},
		{name: "rootless", goos: "linux", host: "unix:///run/user/1000/docker.sock", info: system.Info{SecurityOptions: []string{"name=seccomp,profile=builtin", "name=rootless"}}, bridge: bridge, fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := localGatewayRoute(test.goos, test.host, test.info, test.bridge)
			if test.fails {
				if err == nil {
					t.Fatalf("route %+v, want an error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("route %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
}

type routingEngine struct {
	*fakeEngine
	info   system.Info
	host   string
	bridge netip.Addr
}

func (e routingEngine) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return client.SystemInfoResult{Info: e.info}, nil
}
func (e routingEngine) NetworkInspect(ctx context.Context, id string, options client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	if id != "bridge" {
		return e.fakeEngine.NetworkInspect(ctx, id, options)
	}
	var result client.NetworkInspectResult
	result.Network.IPAM.Config = []network.IPAMConfig{{Gateway: netip.MustParseAddr("fd00::1")}, {Gateway: e.bridge}}
	return result, nil
}
func (e routingEngine) DaemonHost() string { return e.host }

func TestProviderLocalGatewayRoute(t *testing.T) {
	engine := routingEngine{fakeEngine: newFakeEngine(), info: system.Info{OperatingSystem: "Docker Desktop"}, host: "unix:///var/run/docker.sock", bridge: netip.MustParseAddr("172.17.0.1")}
	provider, err := NewWithEngine(Config{StateDir: t.TempDir(), Image: "fixture:tag"}, engine)
	if err != nil {
		t.Fatal(err)
	}
	route, err := provider.LocalGatewayRoute(context.Background())
	if err != nil || route.HostEntry != HostGateway || route.Listen.String() != "127.0.0.1" {
		t.Fatalf("route %+v, %v", route, err)
	}
	// An engine that cannot report routing has no default.
	if _, err := testProvider(t, newFakeEngine()).LocalGatewayRoute(context.Background()); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("plain engine: %v", err)
	}
}

func TestContainerRequestNamesLocalGatewayOnlyWhenConfigured(t *testing.T) {
	engine := newFakeEngine()
	provider := testProvider(t, engine)
	names := provider.resourceNames("capsule-1")
	labels := provider.labels("capsule-1", "capsule", engine.image.ID)
	if hosts := provider.ContainerCreateOptions(names, engine.image.ID, "token", labels).HostConfig.ExtraHosts; len(hosts) != 0 {
		t.Fatalf("extra hosts without a local gateway: %v", hosts)
	}
	for _, entry := range []string{"", "0.0.0.0", "::", "fe80::1%eth0", "not-an-ip", "224.0.0.1"} {
		if err := provider.ConfigureGatewayHost("provider-gateway.meridian.internal", entry); err == nil {
			t.Fatalf("entry %q accepted", entry)
		}
	}
	for _, name := range []string{"", "a:b", "a b"} {
		if err := provider.ConfigureGatewayHost(name, HostGateway); err == nil {
			t.Fatalf("name %q accepted", name)
		}
	}
	for entry, want := range map[string]string{
		HostGateway:  "provider-gateway.meridian.internal:host-gateway",
		"172.17.0.1": "provider-gateway.meridian.internal:172.17.0.1",
	} {
		if err := provider.ConfigureGatewayHost("provider-gateway.meridian.internal", entry); err != nil {
			t.Fatal(err)
		}
		hosts := provider.ContainerCreateOptions(names, engine.image.ID, "token", labels).HostConfig.ExtraHosts
		if len(hosts) != 1 || hosts[0] != want {
			t.Fatalf("extra hosts %v, want %s", hosts, want)
		}
	}
}
