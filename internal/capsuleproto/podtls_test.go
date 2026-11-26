package capsuleproto

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// podTLSServer serves the protocol handler the way capsuled's pod-network
// listener does (ADR 0065) and returns its origin and the material for it.
func podTLSServer(t *testing.T, name string) (string, map[string][]byte) {
	t.Helper()
	material, err := GeneratePodTLS(PodTLSServerName(name), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	config, err := ServerTLSConfig(
		material[PodTLSServerCert], material[PodTLSServerKey], material[PodTLSClientCert],
	)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: filepath.Join(t.TempDir(), "workspace")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	httpServer := httptest.NewUnstartedServer(server.Handler())
	httpServer.TLS = config
	httpServer.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	httpServer.StartTLS()
	t.Cleanup(httpServer.Close)
	return httpServer.URL, material
}

func TestPodTLSAcceptsOnlyThePinnedPair(t *testing.T) {
	origin, material := podTLSServer(t, "meridian-one")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	config, err := ClientTLSConfig(material, PodTLSServerName("meridian-one"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewTLSClient(origin, testToken, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if _, err := client.Health(ctx); err != nil {
		t.Fatalf("pinned client health: %v", err)
	}
	if _, err := client.Status(ctx); err != nil {
		t.Fatalf("pinned client authenticated call: %v", err)
	}

	// Another Capsule's material: its client certificate is refused, and
	// its server pin does not match this listener.
	other, err := GeneratePodTLS(PodTLSServerName("meridian-one"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	otherConfig, err := ClientTLSConfig(other, PodTLSServerName("meridian-one"))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewTLSClient(origin, testToken, otherConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Health(ctx); err == nil {
		t.Fatal("another Capsule's TLS material reached this listener")
	}

	// The right server pin but no client certificate fails the handshake
	// before any HTTP request is parsed.
	noClient := config.Clone()
	noClient.Certificates = nil
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: noClient}}
	if response, err := plain.Get(origin + HealthPath); err == nil {
		_ = response.Body.Close()
		t.Fatal("a client without the pinned certificate completed a request")
	}

	// A server name the certificate does not carry is refused.
	if _, err := ClientTLSConfig(material, PodTLSServerName("meridian-two")); err == nil {
		t.Fatal("client config built for another Capsule's name")
	}
	wrongName := config.Clone()
	wrongName.ServerName = PodTLSServerName("meridian-two")
	wrongClient, err := NewTLSClient(origin, testToken, wrongName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongClient.Health(ctx); err == nil {
		t.Fatal("server certificate accepted for another Capsule's name")
	}

	// A bad token is still refused over a valid channel.
	badToken, err := NewTLSClient(origin, strings.Repeat("x", 43), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badToken.Status(ctx); err == nil {
		t.Fatal("authenticated call accepted a wrong token over mutual TLS")
	}
}

func TestPodTLSCarriesWebSocketAttach(t *testing.T) {
	origin, material := podTLSServer(t, "meridian-shell")
	config, err := ClientTLSConfig(material, PodTLSServerName("meridian-shell"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewTLSClient(origin, testToken, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.StartShell(ctx, 80, 24, nil); err != nil {
		t.Fatal(err)
	}
	conn, err := client.AttachShell(ctx)
	if err != nil {
		t.Fatalf("attach over pod TLS: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("echo pod-tls-ok\n")); err != nil {
		t.Fatal(err)
	}
	var output string
	deadline := time.Now().Add(8 * time.Second)
	for !strings.Contains(output, "pod-tls-ok") && time.Now().Before(deadline) {
		readCtx, stop := context.WithTimeout(ctx, time.Second)
		kind, payload, err := conn.Read(readCtx)
		stop()
		if err == nil && kind == websocket.MessageBinary {
			output += string(payload)
		}
	}
	if !strings.Contains(output, "pod-tls-ok") {
		t.Fatalf("shell output over pod TLS = %q", output)
	}
}

func TestNewTLSClientRefusesUnpinnedConfigurations(t *testing.T) {
	material, err := GeneratePodTLS(PodTLSServerName("meridian-one"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := ClientTLSConfig(material, PodTLSServerName("meridian-one"))
	if err != nil {
		t.Fatal(err)
	}
	insecure := pinned.Clone()
	insecure.InsecureSkipVerify = true
	noRoots := pinned.Clone()
	noRoots.RootCAs = nil
	noName := pinned.Clone()
	noName.ServerName = ""
	noClient := pinned.Clone()
	noClient.Certificates = nil
	oldTLS := pinned.Clone()
	oldTLS.MinVersion = tls.VersionTLS12
	callback := pinned.Clone()
	callback.VerifyPeerCertificate = func([][]byte, [][]*x509.Certificate) error { return nil }
	for name, test := range map[string]struct {
		origin string
		config *tls.Config
	}{
		"plain http":    {"http://10.0.0.1:7778", pinned},
		"no port":       {"https://10.0.0.1", pinned},
		"path":          {"https://10.0.0.1:7778/v1", pinned},
		"nil config":    {"https://10.0.0.1:7778", nil},
		"skip verify":   {"https://10.0.0.1:7778", insecure},
		"no roots":      {"https://10.0.0.1:7778", noRoots},
		"no name":       {"https://10.0.0.1:7778", noName},
		"no client":     {"https://10.0.0.1:7778", noClient},
		"TLS 1.2":       {"https://10.0.0.1:7778", oldTLS},
		"peer callback": {"https://10.0.0.1:7778", callback},
	} {
		if _, err := NewTLSClient(test.origin, testToken, test.config); err == nil {
			t.Errorf("%s: unpinned pod client accepted", name)
		}
	}
	if _, err := NewClient("https://10.0.0.1:7778", testToken, nil); err == nil {
		t.Error("the loopback client accepted a pod-network origin")
	}
}

func TestGeneratePodTLSBoundsInputsAndValidates(t *testing.T) {
	if _, err := GeneratePodTLS("meridian-one.example.com", time.Now().Add(time.Hour)); err == nil {
		t.Error("server name outside .capsule.meridian.invalid accepted")
	}
	if _, err := GeneratePodTLS(PodTLSServerName("meridian-one"), time.Now().Add(-time.Minute)); err == nil {
		t.Error("expired pod TLS material generated")
	}
	material, err := GeneratePodTLS(PodTLSServerName("meridian-one"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePodTLS(material, PodTLSServerName("meridian-one")); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePodTLS(material, PodTLSServerName("meridian-two")); err == nil {
		t.Error("material validated for another Capsule's name")
	}
	swapped := map[string][]byte{}
	for key, value := range material {
		swapped[key] = value
	}
	swapped[PodTLSServerKey] = material[PodTLSClientKey]
	if err := ValidatePodTLS(swapped, PodTLSServerName("meridian-one")); err == nil {
		t.Error("mismatched server pair validated")
	}
	if _, err := ServerTLSConfig(material[PodTLSServerCert], material[PodTLSServerKey],
		append(append([]byte{}, material[PodTLSClientCert]...), material[PodTLSServerCert]...)); err == nil {
		t.Error("server accepted a client pool with more than one certificate")
	}
}
