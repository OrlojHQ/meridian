package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/OrlojHQ/meridian/internal/gatewaytls"
	dockerprovider "github.com/OrlojHQ/meridian/internal/provider/docker"
)

// LocalGatewayOff disables the local provider gateway.
const LocalGatewayOff = "off"

// ValidateProviderGatewayListen checks --provider-gateway-listen against the
// rest of the configuration. Empty means choose from the Docker engine.
func ValidateProviderGatewayListen(config Config) error {
	listen := config.ProviderGatewayListen
	if listen == "" || listen == LocalGatewayOff {
		return nil
	}
	if config.Provider != "docker" || config.ProviderGatewayURL != "" {
		return errors.New("--provider-gateway-listen applies only to --provider=docker without --provider-gateway-url")
	}
	_, err := parseGatewayListen(listen)
	return err
}

func parseGatewayListen(listen string) (netip.AddrPort, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("invalid provider gateway listen address: %w", err)
	}
	addr, err := netip.ParseAddr(host)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || addr.Zone() != "" {
		return netip.AddrPort{}, errors.New("provider gateway listen address must be a literal IP and port")
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(number)), nil
}

// gatewayHostEntry is what Capsules map the gateway name to for a listener
// on addr: host-gateway reaches host loopback (Docker Desktop and VM-backed
// engines) and the default bridge address (native Linux).
func gatewayHostEntry(addr netip.Addr) string {
	if addr.IsLoopback() || addr.IsUnspecified() {
		return dockerprovider.HostGateway
	}
	return addr.String()
}

// openLocalGateway opens the local gateway listener and names it in new
// Capsules. It returns the HTTPS origin Capsules use.
func openLocalGateway(ctx context.Context, listen string, provider *dockerprovider.Provider) (net.Listener, string, error) {
	var bind netip.AddrPort
	var entry string
	if listen != "" {
		var err error
		if bind, err = parseGatewayListen(listen); err != nil {
			return nil, "", err
		}
		entry = gatewayHostEntry(bind.Addr())
	} else {
		route, err := provider.LocalGatewayRoute(ctx)
		if err != nil {
			return nil, "", err
		}
		bind, entry = netip.AddrPortFrom(route.Listen, 0), route.HostEntry
	}
	listener, err := net.Listen("tcp", bind.String())
	if err != nil {
		return nil, "", fmt.Errorf("listen for the provider gateway: %w", err)
	}
	if err := provider.ConfigureGatewayHost(gatewaytls.Hostname, entry); err != nil {
		_ = listener.Close()
		return nil, "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return listener, "https://" + net.JoinHostPort(gatewaytls.Hostname, strconv.Itoa(port)), nil
}
