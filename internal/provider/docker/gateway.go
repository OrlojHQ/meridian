package docker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// HostGateway is Docker's alias for the host address a container reaches,
// which Docker Desktop and VM-backed engines forward to host loopback.
const HostGateway = "host-gateway"

// GatewayRoute is where meridiand listens for the local provider gateway
// (ADR 0037) and the address Capsules map the gateway name to.
type GatewayRoute struct {
	Listen    netip.Addr
	HostEntry string
}

type gatewayEngine interface {
	Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error)
	NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	DaemonHost() string
}

// LocalGatewayRoute asks the engine where Capsules can reach the host.
func (p *Provider) LocalGatewayRoute(ctx context.Context) (GatewayRoute, error) {
	engine, ok := p.engine.(gatewayEngine)
	if !ok {
		return GatewayRoute{}, fmt.Errorf("%w: Docker engine does not report host routing", domain.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	info, err := engine.Info(ctx, client.InfoOptions{})
	if err != nil {
		return GatewayRoute{}, fmt.Errorf("inspect Docker engine: %w", err)
	}
	var bridge netip.Addr
	if runtime.GOOS == "linux" && !dockerInVM(info.Info) {
		inspection, err := engine.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
		if err != nil {
			return GatewayRoute{}, fmt.Errorf("inspect Docker bridge network: %w", err)
		}
		for _, item := range inspection.Network.IPAM.Config {
			if item.Gateway.Is4() {
				bridge = item.Gateway
				break
			}
		}
	}
	return localGatewayRoute(runtime.GOOS, engine.DaemonHost(), info.Info, bridge)
}

func dockerInVM(info system.Info) bool {
	return strings.Contains(info.OperatingSystem, "Docker Desktop")
}

func localGatewayRoute(goos, daemonHost string, info system.Info, bridge netip.Addr) (GatewayRoute, error) {
	if !strings.HasPrefix(daemonHost, "unix://") && !strings.HasPrefix(daemonHost, "npipe://") {
		return GatewayRoute{}, errors.New("the Docker engine is remote, so Capsules cannot reach this host's listener")
	}
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "name=rootless") {
			return GatewayRoute{}, errors.New("rootless Docker has no default route from Capsules to the host")
		}
	}
	if goos != "linux" || dockerInVM(info) {
		// The engine runs in a VM that forwards host-gateway to loopback.
		return GatewayRoute{Listen: netip.MustParseAddr("127.0.0.1"), HostEntry: HostGateway}, nil
	}
	if !bridge.Is4() || !bridge.IsPrivate() {
		return GatewayRoute{}, errors.New("the Docker bridge network has no private IPv4 gateway")
	}
	return GatewayRoute{Listen: bridge, HostEntry: bridge.String()}, nil
}

// ConfigureGatewayHost maps the local gateway name to entry in Capsules
// created from now on; call it before any Capsule is created.
func (p *Provider) ConfigureGatewayHost(name, entry string) error {
	if entry != HostGateway {
		addr, err := netip.ParseAddr(entry)
		if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() {
			return fmt.Errorf("%w: invalid gateway host address", domain.ErrInvalid)
		}
	}
	if name == "" || strings.ContainsAny(name, ": \t\r\n\x00") {
		return fmt.Errorf("%w: invalid gateway host name", domain.ErrInvalid)
	}
	p.gatewayHost = name + ":" + entry
	return nil
}
