// Package oidcauth is Meridian's OpenID Connect relying party for one issuer
// (ADR 0046). It runs the authorization code flow with PKCE and checks the
// ID token's issuer, audience, signature, expiry, and nonce. It knows
// nothing about users or sessions: it turns a callback into a verified
// identity.
package oidcauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ErrDenied reports an identity the issuer authenticated that Meridian
// refuses, such as one without the required group.
var ErrDenied = errors.New("identity is not allowed to sign in")

// Config is the operator's issuer configuration.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is the daemon's callback, chosen by the daemon, never by
	// a request.
	RedirectURL string
	// RequiredGroup, when set, must appear in the GroupsClaim of the ID
	// token, or the sign-in is refused.
	RequiredGroup string
	GroupsClaim   string
	// HTTPClient reaches the issuer; nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Validate reports a configuration that cannot work.
func (c Config) Validate() error {
	if !secureURL(c.Issuer) {
		return errors.New("OIDC issuer must be an https URL, or http on loopback")
	}
	if c.ClientID == "" {
		return errors.New("OIDC client id is required")
	}
	if !secureURL(c.RedirectURL) {
		return errors.New("OIDC redirect URL must be https, or http on loopback")
	}
	return nil
}

// secureURL accepts https, or plain http only to a loopback host.
func secureURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Identity is a verified ID token's subject. Email and Name are display
// only; (Issuer, Subject) is the identity.
type Identity struct {
	Issuer  string
	Subject string
	Email   string
	Name    string
}

// Attempt is one sign-in's single-use secrets. They stay in daemon memory.
type Attempt struct {
	State    string
	Nonce    string
	Verifier string
	URL      string
}

// RelyingParty signs users in against one issuer.
type RelyingParty struct {
	config Config

	mu       sync.Mutex
	provider *oidc.Provider
}

// New returns a relying party. Discovery runs on first use and is retried
// on failure, so an unreachable issuer does not stop the daemon starting.
func New(config Config) (*RelyingParty, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.GroupsClaim == "" {
		config.GroupsClaim = "groups"
	}
	return &RelyingParty{config: config}, nil
}

// Issuer is the configured issuer URL.
func (r *RelyingParty) Issuer() string { return r.config.Issuer }

func (r *RelyingParty) context(ctx context.Context) context.Context {
	if r.config.HTTPClient != nil {
		return oidc.ClientContext(ctx, r.config.HTTPClient)
	}
	return ctx
}

func (r *RelyingParty) discover(ctx context.Context) (*oidc.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.provider != nil {
		return r.provider, nil
	}
	provider, err := oidc.NewProvider(r.context(ctx), r.config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC issuer: %w", err)
	}
	r.provider = provider
	return provider, nil
}

func (r *RelyingParty) oauth(provider *oidc.Provider) oauth2.Config {
	return oauth2.Config{
		ClientID: r.config.ClientID, ClientSecret: r.config.ClientSecret,
		Endpoint: provider.Endpoint(), RedirectURL: r.config.RedirectURL,
		Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

// Begin starts a sign-in: fresh state, nonce, and PKCE verifier, and the
// issuer URL to send the browser to.
func (r *RelyingParty) Begin(ctx context.Context) (Attempt, error) {
	provider, err := r.discover(ctx)
	if err != nil {
		return Attempt{}, err
	}
	state, err := random()
	if err != nil {
		return Attempt{}, err
	}
	nonce, err := random()
	if err != nil {
		return Attempt{}, err
	}
	verifier := oauth2.GenerateVerifier()
	config := r.oauth(provider)
	return Attempt{
		State: state, Nonce: nonce, Verifier: verifier,
		URL: config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)),
	}, nil
}

// Complete exchanges the callback's code for an ID token and verifies it
// against attempt. The caller has already matched the callback's state to
// attempt and removed it, so a state is used once.
func (r *RelyingParty) Complete(ctx context.Context, attempt Attempt, code string) (Identity, error) {
	if code == "" || len(code) > 4096 {
		return Identity{}, errors.New("OIDC callback carries no code")
	}
	provider, err := r.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = r.context(ctx)
	config := r.oauth(provider)
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchange OIDC code: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, errors.New("OIDC token response has no ID token")
	}
	return r.identity(ctx, provider, raw, attempt.Nonce)
}

// identity verifies an ID token's issuer, audience, signature, and expiry,
// its nonce when the flow sent one, and the required group.
func (r *RelyingParty) identity(ctx context.Context, provider *oidc.Provider, raw, nonce string) (Identity, error) {
	verified, err := provider.Verifier(&oidc.Config{ClientID: r.config.ClientID}).Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("verify ID token: %w", err)
	}
	if nonce != "" && verified.Nonce != nonce {
		return Identity{}, errors.New("ID token nonce does not match the sign-in")
	}
	var claims map[string]any
	if err := verified.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("decode ID token claims: %w", err)
	}
	if r.config.RequiredGroup != "" && !hasGroup(claims[r.config.GroupsClaim], r.config.RequiredGroup) {
		return Identity{}, ErrDenied
	}
	identity := Identity{Issuer: verified.Issuer, Subject: verified.Subject}
	identity.Email, _ = claims["email"].(string)
	identity.Name, _ = claims["name"].(string)
	identity.Email, identity.Name = bounded(identity.Email), bounded(identity.Name)
	if identity.Subject == "" || len(identity.Subject) > 255 {
		return Identity{}, errors.New("ID token subject is invalid")
	}
	return identity, nil
}

func hasGroup(value any, group string) bool {
	switch groups := value.(type) {
	case []any:
		for _, item := range groups {
			if name, ok := item.(string); ok && name == group {
				return true
			}
		}
	case string:
		return groups == group
	}
	return false
}

func bounded(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func random() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
