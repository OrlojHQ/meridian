// Package gatewaytls serves the local provider gateway (ADR 0037): an
// in-memory, name-constrained CA, the short-lived leaf it signs, and a TLS
// server that answers only provider gateway routes.
package gatewaytls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Hostname is the only name the CA may sign and the name Capsules use to
// reach the gateway. .internal is reserved for private use.
const Hostname = "provider-gateway.meridian.internal"

const (
	caLifetime   = 30 * 24 * time.Hour
	caOverlap    = 72 * time.Hour
	leafLifetime = 48 * time.Hour
	leafRenew    = 24 * time.Hour
	backdate     = 5 * time.Minute
)

type authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	created time.Time
}

// Authority holds the gateway CA and leaf. Private keys never leave it.
type Authority struct {
	mu    sync.Mutex
	now   func() time.Time
	lease time.Duration
	// current signs the leaf; next, while set, is already trusted by new
	// Capsules and takes over once every older lease has expired.
	current, next *authority
	leaf          *tls.Certificate
	leafExpires   time.Time
	leafCA        *authority
}

// NewAuthority creates a CA. lease is the longest gateway lease, which
// bounds how long a Capsule may rely on a trust bundle it was given.
func NewAuthority(now func() time.Time, lease time.Duration) (*Authority, error) {
	if now == nil {
		now = time.Now
	}
	if lease <= 0 || lease+time.Hour >= caOverlap {
		return nil, errors.New("gateway lease must be shorter than the CA overlap")
	}
	a := &Authority{now: now, lease: lease}
	var err error
	a.current, err = newAuthority(now())
	if err != nil {
		return nil, err
	}
	return a, nil
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func newAuthority(now time.Time) (*authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	number, err := serial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:                number,
		Subject:                     pkix.Name{CommonName: "Meridian local provider gateway CA"},
		NotBefore:                   now.Add(-backdate),
		NotAfter:                    now.Add(caLifetime),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{Hostname},
		ExcludedIPRanges: []*net.IPNet{
			{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &authority{cert: cert, key: key, created: now}, nil
}

// maintain rotates the CA and leaf; the caller holds mu.
func (a *Authority) maintain() error {
	now := a.now()
	switch {
	case !now.Before(a.current.cert.NotAfter):
		// The clock passed the CA's expiry, so every lease it covered has
		// expired too.
		fresh, err := newAuthority(now)
		if err != nil {
			return err
		}
		a.current, a.next = fresh, nil
	case a.next != nil && !now.Before(a.next.created.Add(a.lease+time.Hour)):
		a.current, a.next = a.next, nil
	case a.next == nil && a.current.cert.NotAfter.Sub(now) <= caOverlap:
		next, err := newAuthority(now)
		if err != nil {
			return err
		}
		a.next = next
	}
	if a.leaf != nil && a.leafCA == a.current && a.leafExpires.Sub(now) > leafRenew {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	number, err := serial()
	if err != nil {
		return err
	}
	expires := now.Add(leafLifetime)
	if expires.After(a.current.cert.NotAfter) {
		expires = a.current.cert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: number,
		Subject:      pkix.Name{CommonName: Hostname},
		DNSNames:     []string{Hostname},
		NotBefore:    now.Add(-backdate),
		NotAfter:     expires,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.current.cert, &key.PublicKey, a.current.key)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	a.leaf = &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	a.leafExpires, a.leafCA = expires, a.current
	return nil
}

// Trust returns the PEM certificates a Capsule given a lease now must
// trust: the current CA and, during rotation, the next one.
func (a *Authority) Trust() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.maintain(); err != nil {
		return "", err
	}
	var out strings.Builder
	for _, item := range []*authority{a.current, a.next} {
		if item != nil {
			_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: item.cert.Raw})
		}
	}
	return out.String(), nil
}

// GetCertificate serves the current leaf.
func (a *Authority) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.maintain(); err != nil {
		return nil, err
	}
	return a.leaf, nil
}

// Prefix is the only path prefix the listener serves.
const Prefix = "/provider-gateway/"

// Handler passes only provider gateway routes to gateway and answers
// everything else with 404, before any other handler sees the request.
func Handler(gateway http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, Prefix) {
			http.NotFound(w, r)
			return
		}
		gateway.ServeHTTP(w, r)
	})
}

// Server returns the TLS server for the local gateway listener. Handshake
// and protocol errors are discarded: any container on the engine can cause
// them.
func Server(authority *Authority, gateway http.Handler) *http.Server {
	return &http.Server{
		Handler: Handler(gateway),
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: authority.GetCertificate,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Streaming responses outlive ordinary deadlines; the gateway
		// bounds them itself.
		WriteTimeout:   0,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       log.New(io.Discard, "", 0),
	}
}
