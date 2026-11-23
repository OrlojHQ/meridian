// Package githubapp talks to the GitHub App API: manifest conversion,
// installation lookup, and short-lived installation tokens. The App private
// key is used only to sign a JWT. It is never sent to GitHub again and never
// leaves the Station.
package githubapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// TokenPermissions are the permissions a Capsule token asks for. They are a
// subset of the App's install permissions: contents to read and push, pull
// requests and issues to review and comment, checks and statuses to read CI.
// Metadata is required on every App and is always read.
func TokenPermissions() map[string]string {
	return map[string]string{
		"contents":      "write",
		"pull_requests": "write",
		"issues":        "write",
		"checks":        "read",
		"statuses":      "read",
		"metadata":      "read",
	}
}

// ManifestPermissions are what the App asks for when it is created. They
// match TokenPermissions so a token can request the full set.
func ManifestPermissions() map[string]string {
	return TokenPermissions()
}

// ErrNotInstalled means the App is not installed on that repository.
var ErrNotInstalled = errors.New("github app is not installed on the repository")

// APIError is a GitHub HTTP failure. The message is the status only, so a
// response body cannot carry a token into a log.
type APIError struct {
	Status int
}

func (e *APIError) Error() string {
	if e == nil {
		return "GitHub API request failed"
	}
	return fmt.Sprintf("GitHub API request failed with status %d", e.Status)
}

// ParsePrivateKey parses a GitHub App private key. GitHub returns PKCS#1.
func ParsePrivateKey(pemBytes string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemBytes))
	if block == nil {
		return nil, errors.New("github app private key is not PEM")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github app private key is not RSA")
		}
		return key, nil
	default:
		return nil, errors.New("github app private key type is unsupported")
	}
}

// SignJWT signs a GitHub App JWT (RS256). exp is at most 10 minutes, which
// is GitHub's maximum. iat is one minute in the past for clock skew.
func SignJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	if appID <= 0 || key == nil {
		return "", errors.New("github app jwt needs an app id and a key")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}{
		IAT: now.Add(-time.Minute).Unix(),
		EXP: now.Add(8 * time.Minute).Unix(),
		ISS: fmt.Sprintf("%d", appID),
	})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	signing := header + "." + payload
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
