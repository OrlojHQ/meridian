package daemon

import (
	"net/netip"
	"testing"

	dockerprovider "github.com/OrlojHQ/meridian/internal/provider/docker"
)

func TestValidateProviderGatewayListen(t *testing.T) {
	for _, test := range []struct {
		config Config
		ok     bool
	}{
		{Config{Provider: "docker"}, true},
		{Config{Provider: "docker", ProviderGatewayListen: "off"}, true},
		{Config{Provider: "agentsandbox", ProviderGatewayURL: "https://meridian.example"}, true},
		{Config{Provider: "docker", ProviderGatewayListen: "172.17.0.1:7443"}, true},
		{Config{Provider: "docker", ProviderGatewayListen: "[::1]:0"}, true},
		{Config{Provider: "docker", ProviderGatewayListen: "localhost:7443"}, false},
		{Config{Provider: "docker", ProviderGatewayListen: "172.17.0.1"}, false},
		{Config{Provider: "docker", ProviderGatewayListen: "172.17.0.1:99999"}, false},
		{Config{Provider: "docker", ProviderGatewayListen: "[fe80::1%eth0]:7443"}, false},
		{Config{Provider: "docker", ProviderGatewayListen: "127.0.0.1:7443", ProviderGatewayURL: "https://meridian.example"}, false},
		{Config{Provider: "agentsandbox", ProviderGatewayListen: "127.0.0.1:7443"}, false},
		{Config{Provider: "fake", ProviderGatewayListen: "127.0.0.1:7443"}, false},
	} {
		if err := ValidateProviderGatewayListen(test.config); (err == nil) != test.ok {
			t.Fatalf("%+v: %v", test.config, err)
		}
	}
}

func TestGatewayHostEntry(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1":  dockerprovider.HostGateway,
		"::1":        dockerprovider.HostGateway,
		"0.0.0.0":    dockerprovider.HostGateway,
		"172.17.0.1": "172.17.0.1",
	} {
		if got := gatewayHostEntry(netip.MustParseAddr(addr)); got != want {
			t.Fatalf("%s: %s, want %s", addr, got, want)
		}
	}
}
