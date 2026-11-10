package gatewaytls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func trustPool(t *testing.T, bundle string) (*x509.CertPool, []*x509.Certificate) {
	t.Helper()
	pool := x509.NewCertPool()
	var certs []*x509.Certificate
	rest := []byte(bundle)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		pool.AddCert(cert)
		certs = append(certs, cert)
	}
	return pool, certs
}

func leaf(t *testing.T, a *Authority) *x509.Certificate {
	t.Helper()
	cert, err := a.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return cert.Leaf
}

func verifies(c *x509.Certificate, pool *x509.CertPool, at time.Time) error {
	_, err := c.Verify(x509.VerifyOptions{Roots: pool, DNSName: Hostname, CurrentTime: at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func TestAuthorityConstrainsCA(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	a, err := NewAuthority(c.now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := a.Trust()
	if err != nil {
		t.Fatal(err)
	}
	pool, certs := trustPool(t, bundle)
	if len(certs) != 1 {
		t.Fatalf("trust bundle has %d certificates", len(certs))
	}
	ca := certs[0]
	if !ca.IsCA || !ca.MaxPathLenZero || !ca.PermittedDNSDomainsCritical || len(ca.PermittedDNSDomains) != 1 || ca.PermittedDNSDomains[0] != Hostname || len(ca.ExcludedIPRanges) != 2 {
		t.Fatalf("CA is not name constrained: %+v", ca)
	}
	if strings.Contains(bundle, "PRIVATE") {
		t.Fatal("trust bundle contains a private key")
	}
	l := leaf(t, a)
	if err := verifies(l, pool, c.t); err != nil {
		t.Fatal(err)
	}
	if len(l.DNSNames) != 1 || l.DNSNames[0] != Hostname || len(l.IPAddresses) != 0 || l.IsCA {
		t.Fatalf("leaf names: %v %v", l.DNSNames, l.IPAddresses)
	}
	if got := l.NotAfter.Sub(c.t); got != leafLifetime {
		t.Fatalf("leaf lifetime %v", got)
	}
	if _, err := l.Verify(x509.VerifyOptions{Roots: pool, DNSName: "api.openai.com", CurrentTime: c.t}); err == nil {
		t.Fatal("leaf verified for another name")
	}
}

func TestAuthorityRenewsLeaf(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	a, err := NewAuthority(c.now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	first := leaf(t, a)
	c.t = c.t.Add(23 * time.Hour)
	if leaf(t, a) != first {
		t.Fatal("leaf renewed early")
	}
	c.t = c.t.Add(2 * time.Hour)
	second := leaf(t, a)
	if second == first || second.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Fatal("leaf not renewed with less than a day left")
	}
	if second.NotAfter.Sub(c.t) != leafLifetime {
		t.Fatalf("renewed leaf lifetime %v", second.NotAfter.Sub(c.t))
	}
}

func TestAuthorityRotatesCAAfterLeasesExpire(t *testing.T) {
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	c := &clock{t: start}
	a, err := NewAuthority(c.now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Every lease is issued with the trust bundle of its moment, then must
	// verify the served leaf for its whole 24-hour life.
	type issued struct {
		pool    *x509.CertPool
		expires time.Time
	}
	var leases []issued
	sawOverlap, sawSwitch := false, false
	firstCA := a.current
	for c.t.Before(start.Add(70 * 24 * time.Hour)) {
		bundle, err := a.Trust()
		if err != nil {
			t.Fatal(err)
		}
		pool, certs := trustPool(t, bundle)
		if len(certs) == 2 {
			sawOverlap = true
		}
		if len(certs) < 1 || len(certs) > 2 {
			t.Fatalf("bundle has %d CAs", len(certs))
		}
		leases = append(leases, issued{pool: pool, expires: c.t.Add(24 * time.Hour)})
		served := leaf(t, a)
		if a.current != firstCA {
			sawSwitch = true
		}
		live := leases[:0]
		for _, item := range leases {
			if c.t.Before(item.expires) {
				if err := verifies(served, item.pool, c.t); err != nil {
					t.Fatalf("at %v a live lease does not trust the served leaf: %v", c.t.Sub(start), err)
				}
				live = append(live, item)
			}
		}
		leases = live
		c.t = c.t.Add(90 * time.Minute)
	}
	if !sawOverlap || !sawSwitch {
		t.Fatalf("rotation not exercised: overlap=%t switch=%t", sawOverlap, sawSwitch)
	}
}

func TestAuthorityReplacesExpiredCA(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	a, err := NewAuthority(c.now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old := leaf(t, a)
	c.t = c.t.Add(40 * 24 * time.Hour)
	bundle, err := a.Trust()
	if err != nil {
		t.Fatal(err)
	}
	pool, certs := trustPool(t, bundle)
	if len(certs) != 1 {
		t.Fatalf("bundle has %d CAs", len(certs))
	}
	fresh := leaf(t, a)
	if fresh == old {
		t.Fatal("leaf from an expired CA served")
	}
	if err := verifies(fresh, pool, c.t); err != nil {
		t.Fatal(err)
	}
}

func TestNewAuthorityRejectsLongLease(t *testing.T) {
	if _, err := NewAuthority(nil, caOverlap); err == nil {
		t.Fatal("lease longer than the overlap accepted")
	}
}

func TestServerServesOnlyGatewayRoutes(t *testing.T) {
	a, err := NewAuthority(nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	gateway := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := Server(a, gateway)
	go func() { _ = server.ServeTLS(listener, "", "") }()
	defer server.Close()
	bundle, err := a.Trust()
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := trustPool(t, bundle)
	address := listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: Hostname},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}}
	base := "https://" + Hostname
	for path, want := range map[string]int{
		"/provider-gateway/openai/v1/responses": http.StatusUnauthorized,
		"/":                                     http.StatusNotFound,
		"/ui/":                                  http.StatusNotFound,
		"/projects":                             http.StatusNotFound,
		"/healthz":                              http.StatusNotFound,
		"/provider-gateway":                     http.StatusNotFound,
	} {
		response, err := client.Post(base+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s: status %d, want %d", path, response.StatusCode, want)
		}
	}
	if len(seen) != 1 || seen[0] != "/provider-gateway/openai/v1/responses" {
		t.Fatalf("gateway saw %v", seen)
	}
	// Without the CA, the handshake fails.
	untrusted := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: Hostname},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}}
	if _, err := untrusted.Get(base + "/provider-gateway/openai/v1/responses"); err == nil {
		t.Fatal("untrusted client completed a handshake")
	}
}
