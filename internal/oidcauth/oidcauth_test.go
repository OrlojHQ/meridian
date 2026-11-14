package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// testIssuer is a minimal OIDC issuer: discovery, JWKS, and a token
// endpoint that checks the PKCE verifier and signs an ID token.
type testIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu        sync.Mutex
	challenge string
	nonce     string
	audience  string
	groups    []string
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &testIssuer{key: key, audience: "meridian", groups: []string{"engineering"}}
	mux := http.NewServeMux()
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer.server.URL, "authorization_endpoint": issuer.server.URL + "/authorize",
			"token_endpoint": issuer.server.URL + "/token", "jwks_uri": issuer.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &key.PublicKey, KeyID: "key", Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("code") != "code" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		issuer.mu.Lock()
		challenge, nonce, audience, groups := issuer.challenge, issuer.nonce, issuer.audience, issuer.groups
		issuer.mu.Unlock()
		if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
			(&jose.SignerOptions{}).WithHeader("kid", "key"))
		if err != nil {
			t.Error(err)
			return
		}
		claims, _ := json.Marshal(map[string]any{
			"iss": issuer.server.URL, "sub": "subject-1", "aud": audience, "nonce": nonce,
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"email": "alice@example.com", "name": "Alice", "groups": groups,
		})
		signed, err := signer.Sign(claims)
		if err != nil {
			t.Error(err)
			return
		}
		token, _ := signed.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access", "token_type": "Bearer", "expires_in": 3600, "id_token": token,
		})
	})
	return issuer
}

func (i *testIssuer) authorize(t *testing.T, attempt Attempt) {
	t.Helper()
	parsed, err := url.Parse(attempt.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("state") != attempt.State || query.Get("nonce") != attempt.Nonce ||
		query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" ||
		query.Get("redirect_uri") != "https://meridian.example/auth/oidc/callback" {
		t.Fatalf("authorize URL = %s", attempt.URL)
	}
	i.mu.Lock()
	i.challenge, i.nonce = query.Get("code_challenge"), query.Get("nonce")
	i.mu.Unlock()
}

func TestSignInVerifiesTokenNonceAndGroup(t *testing.T) {
	issuer := newTestIssuer(t)
	party, err := New(Config{
		Issuer: issuer.server.URL, ClientID: "meridian", ClientSecret: "secret",
		RedirectURL: "https://meridian.example/auth/oidc/callback", RequiredGroup: "engineering",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	attempt, err := party.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issuer.authorize(t, attempt)
	identity, err := party.Complete(ctx, attempt, "code")
	if err != nil {
		t.Fatal(err)
	}
	if identity != (Identity{Issuer: issuer.server.URL, Subject: "subject-1", Email: "alice@example.com", Name: "Alice"}) {
		t.Fatalf("identity = %+v", identity)
	}

	// A token for another sign-in's nonce is refused.
	other, err := party.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issuer.authorize(t, other)
	stale := other
	stale.Nonce = "another-nonce"
	if _, err := party.Complete(ctx, stale, "code"); err == nil {
		t.Fatal("nonce mismatch accepted")
	}
	// A verifier that does not match the challenge fails the exchange.
	third, err := party.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issuer.authorize(t, third)
	third.Verifier = "wrong-verifier-wrong-verifier-wrong-verifier-123"
	if _, err := party.Complete(ctx, third, "code"); err == nil {
		t.Fatal("PKCE verifier mismatch accepted")
	}
	// A token for another client is refused.
	issuer.mu.Lock()
	issuer.audience = "someone-else"
	issuer.mu.Unlock()
	fourth, err := party.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issuer.authorize(t, fourth)
	if _, err := party.Complete(ctx, fourth, "code"); err == nil {
		t.Fatal("audience mismatch accepted")
	}
	// Without the required group the sign-in is denied.
	issuer.mu.Lock()
	issuer.audience, issuer.groups = "meridian", []string{"sales"}
	issuer.mu.Unlock()
	fifth, err := party.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	issuer.authorize(t, fifth)
	if _, err := party.Complete(ctx, fifth, "code"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing group = %v", err)
	}
}

func TestConfigRefusesInsecureURLs(t *testing.T) {
	for _, config := range []Config{
		{Issuer: "http://issuer.example", ClientID: "c", RedirectURL: "https://m.example/cb"},
		{Issuer: "https://issuer.example", ClientID: "c", RedirectURL: "http://m.example/cb"},
		{Issuer: "https://issuer.example", RedirectURL: "https://m.example/cb"},
		{Issuer: "https://user@issuer.example", ClientID: "c", RedirectURL: "https://m.example/cb"},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
	if _, err := New(Config{Issuer: "http://127.0.0.1:5556", ClientID: "c", RedirectURL: "http://localhost:8080/auth/oidc/callback"}); err != nil {
		t.Fatalf("loopback development issuer refused: %v", err)
	}
}
