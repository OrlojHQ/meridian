package chatgptauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAuth struct {
	t       *testing.T
	server  *httptest.Server
	key     *rsa.PrivateKey
	mu      sync.Mutex
	nonce   string
	aud     string
	forms   []url.Values
	revoked []string
	grant   string
}

func newFakeAuth(t *testing.T) *fakeAuth {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAuth{t: t, key: key, aud: "oaiapp_test"}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAuth) client() *Client {
	c := New()
	c.AuthBase = f.server.URL
	c.HTTP = f.server.Client()
	return c
}

func (f *fakeAuth) idToken(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k1"}`))
	body, _ := json.Marshal(claims)
	input := header + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (f *fakeAuth) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": f.server.URL, "jwks_uri": f.server.URL + "/jwks", "revocation_endpoint": f.server.URL + "/revoke",
		})
	case "/jwks":
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
		}}})
	case "/api/accounts/oauth/token":
		_ = r.ParseForm()
		f.mu.Lock()
		f.forms = append(f.forms, r.PostForm)
		grant := f.grant
		f.mu.Unlock()
		if grant == "invalid" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		response := map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600,
			"scope": "openid email offline_access " + PlanScope}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			response["id_token"] = f.idToken(map[string]any{"iss": f.server.URL, "aud": f.aud, "exp": time.Now().Add(time.Hour).Unix(),
				"nonce": f.nonce, "sub": "user-1", "email": "dev@example.com"})
		} else {
			response["access_token"], response["refresh_token"] = "access-2", "refresh-2"
		}
		_ = json.NewEncoder(w).Encode(response)
	case "/revoke":
		_ = r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.PostForm.Get("token"))
		f.mu.Unlock()
	default:
		http.NotFound(w, r)
	}
}

func TestAuthorizationURLRegistersAndReauthorizes(t *testing.T) {
	c := New()
	attempt, err := NewAttempt("")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(c.AuthorizationURL(attempt, "urn:uuid:host", ""))
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Host != "auth.openai.com" || parsed.Path != "/api/accounts/authorize" ||
		query.Get("client_id") != DynamicClient || query.Get("agent_name_hint") != AgentName ||
		query.Get("redirect_uri") != RedirectURI || query.Get("code_challenge_method") != "S256" ||
		query.Get("ext_agent_host_id") != "urn:uuid:host" || !strings.Contains(query.Get("scope"), PlanScope) ||
		query.Get("resource") != Resource || query.Get("code_challenge") == "" ||
		strings.Contains(parsed.String(), attempt.Verifier) {
		t.Fatalf("authorization URL = %s", parsed)
	}
	again, _ := NewAttempt("oaiapp_issued")
	query = mustQuery(t, c.AuthorizationURL(again, "urn:uuid:host", "dev@example.com"))
	if query.Get("client_id") != "oaiapp_issued" || query.Has("agent_name_hint") || query.Get("login_hint") != "dev@example.com" {
		t.Fatalf("reauthorization query = %v", query)
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func TestParseCallback(t *testing.T) {
	callback, err := ParseCallback(" http://127.0.0.1:1455/auth/callback?code=c&state=s&scope=a+b&client_id=oaiapp_x ")
	if err != nil || callback.Code != "c" || callback.State != "s" || callback.ClientID != "oaiapp_x" || callback.Scope != "a b" {
		t.Fatalf("callback = %+v, %v", callback, err)
	}
	if _, err := ParseCallback("code=c&state=s"); err != nil {
		t.Fatalf("query only: %v", err)
	}
	for _, bad := range []string{"", "https://example.com/other?code=c&state=s", "http://127.0.0.1:1455/auth/callback?code=c"} {
		if _, err := ParseCallback(bad); err == nil {
			t.Errorf("ParseCallback(%q) succeeded", bad)
		}
	}
}

func TestCompleteVerifiesAndExchanges(t *testing.T) {
	f := newFakeAuth(t)
	c := f.client()
	attempt, _ := NewAttempt("")
	f.nonce = attempt.Nonce
	set, err := c.Complete(context.Background(), attempt, Callback{Code: "code", State: attempt.State, ClientID: "oaiapp_test", Scope: PlanScope})
	if err != nil {
		t.Fatal(err)
	}
	if set.ClientID != "oaiapp_test" || set.AccessToken != "access-1" || set.RefreshToken != "refresh-1" ||
		set.Subject != "user-1" || set.Email != "dev@example.com" || !set.ExpiresAt.After(time.Now()) {
		t.Fatalf("token set = %+v", set)
	}
	form := f.forms[0]
	if form.Get("client_id") != "oaiapp_test" || form.Get("code_verifier") != attempt.Verifier || form.Get("redirect_uri") != RedirectURI || form.Get("resource") != Resource {
		t.Fatalf("exchange form = %v", form)
	}
}

func TestCompleteRejectsBadResults(t *testing.T) {
	f := newFakeAuth(t)
	c := f.client()
	attempt, _ := NewAttempt("")
	f.nonce = attempt.Nonce
	cases := map[string]Callback{
		"state":    {Code: "code", State: "other", ClientID: "oaiapp_test"},
		"denied":   {Error: "access_denied", State: attempt.State},
		"noClient": {Code: "code", State: attempt.State},
		"scope":    {Code: "code", State: attempt.State, ClientID: "oaiapp_test", Scope: "openid email"},
	}
	for name, callback := range cases {
		if _, err := c.Complete(context.Background(), attempt, callback); err == nil {
			t.Errorf("%s: Complete succeeded", name)
		}
	}
	if _, err := c.Complete(context.Background(), attempt, Callback{Error: "access_denied", State: attempt.State}); !errors.Is(err, ErrDenied) {
		t.Errorf("denied error = %v", err)
	}
	// A token issued for another client or nonce fails ID token checks.
	f.aud = "oaiapp_other"
	if _, err := c.Complete(context.Background(), attempt, Callback{Code: "code", State: attempt.State, ClientID: "oaiapp_test"}); err == nil {
		t.Error("audience mismatch accepted")
	}
	f.aud, f.nonce = "oaiapp_test", "wrong"
	if _, err := c.Complete(context.Background(), attempt, Callback{Code: "code", State: attempt.State, ClientID: "oaiapp_test"}); err == nil {
		t.Error("nonce mismatch accepted")
	}
	reauth, _ := NewAttempt("oaiapp_test")
	if _, err := c.Complete(context.Background(), reauth, Callback{Code: "code", State: reauth.State, ClientID: "oaiapp_changed"}); err == nil {
		t.Error("changed client ID accepted")
	}
}

func TestVerifyIDTokenRejectsTampering(t *testing.T) {
	f := newFakeAuth(t)
	c := f.client()
	token := f.idToken(map[string]any{"iss": f.server.URL, "aud": []string{"oaiapp_test"}, "exp": time.Now().Add(time.Hour).Unix(), "nonce": "n", "sub": "u"})
	if _, err := c.VerifyIDToken(context.Background(), token, "oaiapp_test", "n"); err != nil {
		t.Fatalf("valid token: %v", err)
	}
	parts := strings.Split(token, ".")
	forged, _ := json.Marshal(map[string]any{"iss": f.server.URL, "aud": "oaiapp_test", "exp": time.Now().Add(time.Hour).Unix(), "nonce": "n", "sub": "attacker"})
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(forged) + "." + parts[2]
	if _, err := c.VerifyIDToken(context.Background(), tampered, "oaiapp_test", "n"); err == nil {
		t.Error("tampered token accepted")
	}
	expired := f.idToken(map[string]any{"iss": f.server.URL, "aud": "oaiapp_test", "exp": time.Now().Add(-time.Hour).Unix(), "nonce": "n", "sub": "u"})
	if _, err := c.VerifyIDToken(context.Background(), expired, "oaiapp_test", "n"); err == nil {
		t.Error("expired token accepted")
	}
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + "."
	if _, err := c.VerifyIDToken(context.Background(), none, "oaiapp_test", "n"); err == nil {
		t.Error("unsigned token accepted")
	}
}

func TestRefreshAndRevoke(t *testing.T) {
	f := newFakeAuth(t)
	c := f.client()
	current := TokenSet{ClientID: "oaiapp_test", AccessToken: "access-1", RefreshToken: "refresh-1", Subject: "user-1", Email: "dev@example.com"}
	next, err := c.Refresh(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken != "access-2" || next.RefreshToken != "refresh-2" || next.Subject != "user-1" || next.ClientID != "oaiapp_test" {
		t.Fatalf("refreshed = %+v", next)
	}
	form := f.forms[len(f.forms)-1]
	if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "oaiapp_test" || form.Get("resource") != Resource || form.Has("scope") {
		t.Fatalf("refresh form = %v", form)
	}
	f.grant = "invalid"
	if _, err := c.Refresh(context.Background(), current); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("invalid grant error = %v", err)
	}
	if err := c.Revoke(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "refresh-2" {
		t.Fatalf("revoked = %v", f.revoked)
	}
}
