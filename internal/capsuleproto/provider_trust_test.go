package capsuleproto

import (
	"context"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/gatewaytls"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

func gatewayTrust(t *testing.T) (string, string) {
	t.Helper()
	authority, err := gatewaytls.NewAuthority(nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := authority.Trust()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := authority.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}))
}

func TestLocalGatewayTrustForEveryHarness(t *testing.T) {
	ca, _ := gatewayTrust(t)
	system := filepath.Join(t.TempDir(), "ca-certificates.crt")
	if err := os.WriteFile(system, []byte("PUBLIC ROOTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := systemCABundles
	systemCABundles = []string{filepath.Join(t.TempDir(), "missing.crt"), system}
	t.Cleanup(func() { systemCABundles = saved })
	t.Setenv("SSL_CERT_FILE", "")
	token := strings.Repeat("c", 64)
	for _, harness := range []string{"codex", "claude", "opencode", "pi", "dsh", "codex-structured"} {
		t.Run(harness, func(t *testing.T) {
			s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
			provider, model := "openai", ""
			switch harness {
			case "claude":
				provider = "anthropic"
			case "dsh":
				provider, model = "openai-compatible", "m"
			}
			url := "https://provider-gateway.meridian.internal:4433/provider-gateway/" + provider
			env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: url, Token: token, Model: model, CA: ca}, nil, harness, nil)
			if err != nil {
				t.Fatal(err)
			}
			home := env["HOME"]
			if env["SSL_CERT_FILE"] != filepath.Join(home, ".meridian", "provider-ca-bundle.pem") || env["NODE_EXTRA_CA_CERTS"] != filepath.Join(home, ".meridian", "provider-ca.pem") {
				t.Fatalf("trust environment: %v", env)
			}
			raw, err := os.ReadFile(env["NODE_EXTRA_CA_CERTS"])
			if err != nil || string(raw) != ca {
				t.Fatalf("CA file: %v %q", err, raw)
			}
			raw, err = os.ReadFile(env["SSL_CERT_FILE"])
			if err != nil || string(raw) != "PUBLIC ROOTS\n"+ca {
				t.Fatalf("bundle: %v %q", err, raw)
			}
			// A second launch replaces the files in place.
			if _, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: provider, URL: url, Token: token, Model: model, CA: ca}, nil, harness, nil); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(filepath.Join(home, ".meridian"))
			if err != nil || len(entries) != 2 {
				t.Fatalf("trust directory: %v %v", err, entries)
			}
		})
	}
}

func TestLocalGatewayTrustStartsFromInheritedBundle(t *testing.T) {
	ca, _ := gatewayTrust(t)
	inherited := filepath.Join(t.TempDir(), "corporate.pem")
	if err := os.WriteFile(inherited, []byte("CORPORATE ROOTS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", inherited)
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: "openai", URL: "https://provider-gateway.meridian.internal:4433/provider-gateway/openai", Token: strings.Repeat("d", 64), CA: ca}, nil, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(env["SSL_CERT_FILE"])
	if err != nil || string(raw) != "CORPORATE ROOTS\n"+ca {
		t.Fatalf("bundle: %v %q", err, raw)
	}
}

func TestExplicitGatewayLeavesTrustAlone(t *testing.T) {
	s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
	env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: "openai", URL: "https://meridian.example/provider-gateway/openai", Token: strings.Repeat("e", 64)}, nil, "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, set := env["SSL_CERT_FILE"]; set {
		t.Fatal("SSL_CERT_FILE set without a CA")
	}
	if _, set := env["NODE_EXTRA_CA_CERTS"]; set {
		t.Fatal("NODE_EXTRA_CA_CERTS set without a CA")
	}
	if _, err := os.Stat(filepath.Join(env["HOME"], ".meridian")); !os.IsNotExist(err) {
		t.Fatalf("trust directory written: %v", err)
	}
}

func TestLocalGatewayTrustRejectsInvalidCA(t *testing.T) {
	ca, leaf := gatewayTrust(t)
	key := "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"
	for name, value := range map[string]string{
		"garbage":     "not a certificate",
		"leaf":        leaf,
		"private key": ca + key,
		"trailing":    ca + "junk",
		"five CAs":    strings.Repeat(ca, 5),
		"oversized":   ca + strings.Repeat(" ", 16<<10),
		"headers":     strings.Replace(ca, "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\nProc-Type: 4,ENCRYPTED\n\n", 1),
		"empty block": "-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := &Server{config: ServerConfig{HarnessHome: t.TempDir()}}
			if _, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: "openai", URL: "https://provider-gateway.meridian.internal:4433/provider-gateway/openai", Token: strings.Repeat("f", 64), CA: value}, nil, "codex", nil); err == nil {
				t.Fatal("invalid CA accepted")
			}
		})
	}
	if !validGatewayCA(strings.Repeat(ca, 2)) {
		t.Fatal("rotation bundle of two CAs rejected")
	}
}
