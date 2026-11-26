package capsuleproto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Keys of the per-Capsule pod TLS material (ADR 0065). The Capsule receives
// only PodTLSServerCert, PodTLSServerKey, and PodTLSClientCert; the client key
// stays with the control plane.
const (
	PodTLSServerCert = "tls.crt"
	PodTLSServerKey  = "tls.key"
	PodTLSClientCert = "client.crt"
	PodTLSClientKey  = "client.key"

	// PodTLSServerNameSuffix makes each Capsule's server name unresolvable
	// (RFC 6761 .invalid), so it can only ever match a pinned certificate.
	PodTLSServerNameSuffix = ".capsule.meridian.invalid"

	maxPodTLSFileBytes = 16 << 10
)

// PodTLSServerName is the only DNS name in a Capsule's server certificate.
func PodTLSServerName(resourceName string) string {
	return resourceName + PodTLSServerNameSuffix
}

// GeneratePodTLS creates the two self-signed ECDSA P-256 pairs one Capsule's
// pod-network listener uses: a server pair naming serverName and a client
// pair. Both expire at notAfter.
func GeneratePodTLS(serverName string, notAfter time.Time) (map[string][]byte, error) {
	if !strings.HasSuffix(serverName, PodTLSServerNameSuffix) || len(serverName) > 253 ||
		strings.ContainsAny(serverName, " /\\\x00\r\n") {
		return nil, errors.New("pod TLS server name is invalid")
	}
	if !notAfter.After(time.Now()) {
		return nil, errors.New("pod TLS expiry must be in the future")
	}
	serverCert, serverKey, err := selfSigned(serverName, notAfter, x509.ExtKeyUsageServerAuth, []string{serverName})
	if err != nil {
		return nil, fmt.Errorf("generate pod TLS server certificate: %w", err)
	}
	clientCert, clientKey, err := selfSigned("meridiand", notAfter, x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		return nil, fmt.Errorf("generate pod TLS client certificate: %w", err)
	}
	return map[string][]byte{
		PodTLSServerCert: serverCert, PodTLSServerKey: serverKey,
		PodTLSClientCert: clientCert, PodTLSClientKey: clientKey,
	}, nil
}

func selfSigned(
	commonName string,
	notAfter time.Time,
	usage x509.ExtKeyUsage,
	dnsNames []string,
) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

// ValidatePodTLS checks that material holds every pair, that each pair
// matches, and that the server certificate names serverName.
func ValidatePodTLS(material map[string][]byte, serverName string) error {
	for _, key := range []string{PodTLSServerCert, PodTLSServerKey, PodTLSClientCert, PodTLSClientKey} {
		if len(material[key]) == 0 || len(material[key]) > maxPodTLSFileBytes {
			return fmt.Errorf("pod TLS %s is missing or oversized", key)
		}
	}
	server, err := tls.X509KeyPair(material[PodTLSServerCert], material[PodTLSServerKey])
	if err != nil {
		return fmt.Errorf("pod TLS server pair is invalid: %w", err)
	}
	if _, err := tls.X509KeyPair(material[PodTLSClientCert], material[PodTLSClientKey]); err != nil {
		return fmt.Errorf("pod TLS client pair is invalid: %w", err)
	}
	leaf, err := x509.ParseCertificate(server.Certificate[0])
	if err != nil || leaf.VerifyHostname(serverName) != nil {
		return errors.New("pod TLS server certificate does not name this Capsule")
	}
	return nil
}

// ServerTLSConfig is capsuled's pod-network listener config: TLS 1.3 only,
// HTTP/1.1 only (PTY and preview attach upgrade to WebSocket), and a required
// client certificate verified against a pool holding exactly clientCertPEM.
func ServerTLSConfig(certPEM, keyPEM, clientCertPEM []byte) (*tls.Config, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load pod TLS server pair: %w", err)
	}
	clients, err := singleCertificatePool(clientCertPEM)
	if err != nil {
		return nil, fmt.Errorf("load pod TLS client certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clients,
		NextProtos:   []string{"http/1.1"},
	}, nil
}

// ClientTLSConfig pins one Capsule: its server certificate is the only root,
// serverName must match it, and the control plane presents the client pair.
func ClientTLSConfig(material map[string][]byte, serverName string) (*tls.Config, error) {
	if err := ValidatePodTLS(material, serverName); err != nil {
		return nil, err
	}
	roots, err := singleCertificatePool(material[PodTLSServerCert])
	if err != nil {
		return nil, err
	}
	client, err := tls.X509KeyPair(material[PodTLSClientCert], material[PodTLSClientKey])
	if err != nil {
		return nil, fmt.Errorf("load pod TLS client pair: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		ServerName:   serverName,
		Certificates: []tls.Certificate{client},
		NextProtos:   []string{"http/1.1"},
	}, nil
}

func singleCertificatePool(value []byte) (*x509.CertPool, error) {
	block, rest := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("expected exactly one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	return pool, nil
}
