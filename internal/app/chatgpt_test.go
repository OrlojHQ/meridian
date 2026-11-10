package app

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/chatgptauth"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

// fakeOpenAIAuth issues ID tokens for whatever nonce the last authorization
// URL carried, rotates tokens on refresh, and records revocations.
type fakeOpenAIAuth struct {
	t            *testing.T
	server       *httptest.Server
	key          *rsa.PrivateKey
	mu           sync.Mutex
	nonce        string
	refresh      int
	invalid      bool
	revoked      []string
	revokeStatus int
	failExchange string
}

func newFakeOpenAIAuth(t *testing.T) *fakeOpenAIAuth {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOpenAIAuth{t: t, key: key, revokeStatus: http.StatusOK}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenAIAuth) client() *chatgptauth.Client {
	c := chatgptauth.New()
	c.AuthBase, c.HTTP = f.server.URL, f.server.Client()
	return c
}

func (f *fakeOpenAIAuth) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.server.URL, "jwks_uri": f.server.URL + "/jwks", "revocation_endpoint": f.server.URL + "/revoke"})
	case "/jwks":
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
	case "/api/accounts/oauth/token":
		_ = r.ParseForm()
		if f.invalid {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		if f.failExchange != "" && r.PostForm.Get("grant_type") == "authorization_code" {
			w.Header().Set("X-Request-Id", "req-123")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": f.failExchange})
			f.failExchange = ""
			return
		}
		scope := "openid email offline_access " + chatgptauth.PlanScope
		if r.PostForm.Get("grant_type") == "refresh_token" {
			f.refresh++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-r" + itoa(f.refresh), "refresh_token": "refresh-r" + itoa(f.refresh), "expires_in": 3600, "scope": scope})
			return
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k"}`))
		claims, _ := json.Marshal(map[string]any{"iss": f.server.URL, "aud": r.PostForm.Get("client_id"), "exp": time.Now().Add(time.Hour).Unix(),
			"nonce": f.nonce, "sub": "user-1", "email": "dev@example.com"})
		input := header + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(input))
		signature, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-signin", "refresh_token": "refresh-signin", "expires_in": 3600, "scope": scope,
			"id_token": input + "." + base64.RawURLEncoding.EncodeToString(signature)})
	case "/revoke":
		_ = r.ParseForm()
		f.revoked = append(f.revoked, r.PostForm.Get("token"))
		w.WriteHeader(f.revokeStatus)
	default:
		http.NotFound(w, r)
	}
}

func itoa(n int) string { return string(rune('0' + n)) }

// signIn starts an attempt and returns the callback URL the browser would be
// redirected to.
func (f *fakeOpenAIAuth) signIn(t *testing.T, s *Service, connection string) (ChatGPTSignIn, string) {
	t.Helper()
	view, err := s.StartChatGPTSignIn(context.Background(), connection, false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(view.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	f.mu.Lock()
	f.nonce = query.Get("nonce")
	f.mu.Unlock()
	callback := url.Values{"code": {"code"}, "state": {query.Get("state")}, "scope": {chatgptauth.PlanScope}}
	if query.Get("client_id") == chatgptauth.DynamicClient {
		callback.Set("client_id", "oaiapp_meridian")
	}
	return view, chatgptauth.RedirectURI + "?" + callback.Encode()
}

func chatGPTService(t *testing.T) (*Service, ports.Store, *fakeOpenAIAuth, string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	s := NewService(db, connectionClock{}, &connectionIDs{}, connectionQueue{})
	s.ConfigureSecrets(key)
	if err := s.ConfigureProviderGateway("https://meridian.example", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseProviderGateway)
	f := newFakeOpenAIAuth(t)
	s.ConfigureChatGPT(dataDir, f.client())
	t.Cleanup(s.CloseChatGPT)
	return s, db, f, dataDir
}

func TestChatGPTSignInStoresSealedTokens(t *testing.T) {
	ctx := context.Background()
	s, db, f, dataDir := chatGPTService(t)
	if !s.ChatGPTAvailable() {
		t.Fatal("ChatGPT sign-in unavailable")
	}
	plain, _ := f.signIn(t, s, "")
	if strings.Contains(plain.AuthorizationURL, "ext_agent_host_id") {
		t.Fatalf("host ID sent by default: %s", plain.AuthorizationURL)
	}
	if _, err := os.Stat(filepath.Join(dataDir, chatGPTHostIDFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host ID file created without use: %v", err)
	}
	s.chatgpt.sendHostID = true
	view, callback := f.signIn(t, s, "")
	if view.State != ChatGPTSignInPending || view.RedirectURI != chatgptauth.RedirectURI || view.Listening {
		t.Fatalf("start = %+v", view)
	}
	host, err := os.ReadFile(filepath.Join(dataDir, chatGPTHostIDFile))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dataDir, chatGPTHostIDFile))
	if !hostIDPattern.MatchString(strings.TrimSpace(string(host))) || info.Mode().Perm() != 0o600 ||
		!strings.Contains(view.AuthorizationURL, url.QueryEscape(strings.TrimSpace(string(host)))) {
		t.Fatalf("host ID %q mode %v", host, info.Mode())
	}
	if _, err := s.CompleteChatGPTSignIn(ctx, view.ID, strings.Replace(callback, "state=", "state=x", 1)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("mismatched state: %v", err)
	}
	done, err := s.CompleteChatGPTSignIn(ctx, view.ID, callback)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != ChatGPTSignInCompleted || done.ConnectionID == "" {
		t.Fatalf("complete = %+v", done)
	}
	if _, err := s.CompleteChatGPTSignIn(ctx, view.ID, callback); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second completion: %v", err)
	}
	var stored domain.ProviderConnection
	_ = db.View(ctx, func(r ports.Reader) error { stored, err = r.GetProviderConnection(ctx, done.ConnectionID); return err })
	if stored.Provider != providergateway.ChatGPT || stored.Account != "dev@example.com" || stored.CredentialGeneration != 1 ||
		bytes.Contains(stored.Ciphertext, []byte("refresh-signin")) || bytes.Contains(stored.Ciphertext, []byte("access-signin")) {
		t.Fatalf("stored = %+v", stored)
	}
	again, err := s.ChatGPTSignInStatus(view.ID)
	if err != nil || again.State != ChatGPTSignInCompleted {
		t.Fatalf("status = %+v, %v", again, err)
	}
	// The host ID is stable across attempts.
	next, _ := f.signIn(t, s, "")
	if !strings.Contains(next.AuthorizationURL, url.QueryEscape(strings.TrimSpace(string(host)))) {
		t.Fatal("host ID changed")
	}
}

func readyCapsule(t *testing.T, s *Service, db ports.Store) (domain.Project, domain.Capsule) {
	t.Helper()
	ctx := context.Background()
	project, err := s.CreateProject(ctx, "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(ctx, project.ID, "capsule", "capsule")
	if err != nil {
		t.Fatal(err)
	}
	err = db.Transact(ctx, func(tx ports.Transaction) error {
		capsule.State = domain.CapsuleReady
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	return project, capsule
}

func TestChatGPTLeaseRefreshReauthAndRevoke(t *testing.T) {
	ctx := context.Background()
	s, db, f, _ := chatGPTService(t)
	view, callback := f.signIn(t, s, "")
	done, err := s.CompleteChatGPTSignIn(ctx, view.ID, callback)
	if err != nil {
		t.Fatal(err)
	}
	id := done.ConnectionID
	project, capsule := readyCapsule(t, s, db)
	if err := s.GrantProviderConnection(ctx, project.ID, "claude", id); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("ChatGPT granted to Claude Code")
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "codex", id); err != nil {
		t.Fatal(err)
	}
	runtime, err := s.runtimeProviderConnection(ctx, capsule, "codex-structured")
	if err != nil || runtime.Provider != providergateway.ChatGPT || runtime.URL != "https://meridian.example/provider-gateway/chatgpt" ||
		len(runtime.Token) != 64 || strings.Contains(runtime.Token, "access") {
		t.Fatalf("runtime connection = %+v, %v", runtime, err)
	}
	lease := providergateway.Lease{Connection: id, Project: string(project.ID), Capsule: string(capsule.ID), Harness: "codex", Provider: providergateway.ChatGPT}
	upstream, err := s.authorizeProviderLease(ctx, lease)
	if err != nil || upstream.Key != "access-signin" || upstream.Refresh == nil {
		t.Fatalf("upstream = %+v, %v", upstream, err)
	}
	before := connectionByID(t, db, id)
	key, err := upstream.Refresh(ctx)
	if err != nil || key != "access-r1" {
		t.Fatalf("forced refresh = %q, %v", key, err)
	}
	after := connectionByID(t, db, id)
	if after.CredentialGeneration != before.CredentialGeneration+1 || after.ResourceVersion != before.ResourceVersion {
		t.Fatalf("rotation changed generation %d->%d version %d->%d", before.CredentialGeneration, after.CredentialGeneration, before.ResourceVersion, after.ResourceVersion)
	}
	if upstream, err = s.authorizeProviderLease(ctx, lease); err != nil || upstream.Key != "access-r1" {
		t.Fatalf("rotated token not stored: %+v, %v", upstream, err)
	}

	// Rename keeps the token set; key-style edits and creation are refused.
	renamed, err := s.PutProviderConnection(ctx, id, ConnectionInput{Name: "Work ChatGPT", Provider: providergateway.ChatGPT, ExpectedResourceVersion: after.ResourceVersion})
	if err != nil || renamed.Name != "Work ChatGPT" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if upstream, err = s.authorizeProviderLease(ctx, lease); err != nil || upstream.Key != "access-r1" {
		t.Fatalf("rename lost tokens: %v", err)
	}
	if _, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "x", Provider: providergateway.ChatGPT}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("API creation: %v", err)
	}
	if _, err := s.PutProviderConnection(ctx, id, ConnectionInput{Name: "x", Provider: providergateway.ChatGPT, APIKey: "sk-something", ExpectedResourceVersion: renamed.ResourceVersion}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("key replacement: %v", err)
	}

	// A dead refresh token asks for sign-in again, and reauthorization fixes it.
	f.mu.Lock()
	f.invalid = true
	f.mu.Unlock()
	if _, err := upstream.Refresh(ctx); !errors.Is(err, providergateway.ErrSignInRequired) {
		t.Fatalf("invalid grant: %v", err)
	}
	if !connectionByID(t, db, id).NeedsSignIn {
		t.Fatal("connection not marked for sign-in")
	}
	if _, err := s.authorizeProviderLease(ctx, lease); !errors.Is(err, providergateway.ErrSignInRequired) {
		t.Fatalf("lease after invalid grant: %v", err)
	}
	f.mu.Lock()
	f.invalid = false
	f.mu.Unlock()
	reauth, callback := f.signIn(t, s, id)
	if !strings.Contains(reauth.AuthorizationURL, "client_id=oaiapp_meridian") || strings.Contains(reauth.AuthorizationURL, "agent_name_hint") ||
		!strings.Contains(reauth.AuthorizationURL, "login_hint=dev%40example.com") {
		t.Fatalf("reauthorization URL = %s", reauth.AuthorizationURL)
	}
	if _, err := s.CompleteChatGPTSignIn(ctx, reauth.ID, callback); err != nil {
		t.Fatal(err)
	}
	current := connectionByID(t, db, id)
	if current.NeedsSignIn {
		t.Fatal("reauthorization left the connection needing sign-in")
	}
	if upstream, err = s.authorizeProviderLease(ctx, lease); err != nil || upstream.Key != "access-signin" {
		t.Fatalf("lease after reauthorization: %+v, %v", upstream, err)
	}

	// Revocation clears local tokens and revokes the refresh token upstream.
	revoked, err := s.PutProviderConnection(ctx, id, ConnectionInput{Name: current.Name, Provider: providergateway.ChatGPT, ExpectedResourceVersion: current.ResourceVersion, Revoked: true})
	if err != nil || !revoked.Revoked || revoked.RevocationUnconfirmed {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if len(f.revoked) != 1 || f.revoked[0] != "refresh-signin" {
		t.Fatalf("upstream revocations = %v", f.revoked)
	}
	if _, err := s.authorizeProviderLease(ctx, lease); err == nil {
		t.Fatal("revoked connection authorized")
	}
	if _, err := s.StartChatGPTSignIn(ctx, id, false); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reauthorizing a revoked connection: %v", err)
	}
}

func TestChatGPTRevocationUnconfirmed(t *testing.T) {
	ctx := context.Background()
	s, db, f, _ := chatGPTService(t)
	view, callback := f.signIn(t, s, "")
	done, err := s.CompleteChatGPTSignIn(ctx, view.ID, callback)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.revokeStatus = http.StatusServiceUnavailable
	f.mu.Unlock()
	current := connectionByID(t, db, done.ConnectionID)
	revoked, err := s.PutProviderConnection(ctx, current.ID, ConnectionInput{Name: current.Name, Provider: providergateway.ChatGPT, ExpectedResourceVersion: current.ResourceVersion, Revoked: true})
	if err != nil || !revoked.Revoked || !revoked.RevocationUnconfirmed {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if len(f.revoked) != chatGPTRevokeAttempts {
		t.Fatalf("revocation attempts = %d", len(f.revoked))
	}
	stored := connectionByID(t, db, current.ID)
	if _, err := s.openChatGPT(stored); err == nil {
		t.Fatal("revoked connection still holds tokens")
	}
}

func TestChatGPTLoopbackCallback(t *testing.T) {
	ctx := context.Background()
	s, _, f, _ := chatGPTService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	s.chatgpt.listen = address
	view, err := s.StartChatGPTSignIn(ctx, "", true)
	if err != nil || !view.Listening {
		t.Fatalf("start = %+v, %v", view, err)
	}
	parsed, _ := url.Parse(view.AuthorizationURL)
	f.mu.Lock()
	f.nonce = parsed.Query().Get("nonce")
	f.mu.Unlock()
	forged, err := http.Get("http://" + address + chatgptauth.CallbackPath + "?code=c&state=forged&client_id=oaiapp_x")
	if err != nil {
		t.Fatal(err)
	}
	_ = forged.Body.Close()
	if forged.StatusCode != http.StatusNotFound {
		t.Fatalf("forged callback status %d", forged.StatusCode)
	}
	query := url.Values{"code": {"c"}, "state": {parsed.Query().Get("state")}, "client_id": {"oaiapp_meridian"}, "scope": {chatgptauth.PlanScope}}
	response, err := http.Get("http://" + address + chatgptauth.CallbackPath + "?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || strings.Contains(string(body), "access-signin") || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("callback: %d %s", response.StatusCode, body)
	}
	status, err := s.ChatGPTSignInStatus(view.ID)
	if err != nil || status.State != ChatGPTSignInCompleted || status.ConnectionID == "" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	// The listener closes once nothing is waiting on it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("loopback listener stayed open")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func connectionByID(t *testing.T, db ports.Store, id string) domain.ProviderConnection {
	t.Helper()
	var item domain.ProviderConnection
	err := db.View(context.Background(), func(r ports.Reader) error {
		var err error
		item, err = r.GetProviderConnection(context.Background(), id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestChatGPTFailedExchangeReportsReasonAndKeepsIssuedClient(t *testing.T) {
	ctx := context.Background()
	s, _, f, _ := chatGPTService(t)
	f.mu.Lock()
	f.failExchange = "Authorization code was issued to another client\x1b[31m sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	f.mu.Unlock()
	view, callback := f.signIn(t, s, "")
	failed, err := s.CompleteChatGPTSignIn(ctx, view.ID, callback)
	if err == nil || failed.State != ChatGPTSignInFailed ||
		!strings.Contains(failed.Failure, "OpenAI rejected the sign-in code (Authorization code was issued to another client") ||
		!strings.Contains(failed.Failure, "request req-123") ||
		strings.Contains(failed.Failure, "sk-proj") || strings.ContainsRune(failed.Failure, 0x1b) {
		t.Fatalf("failure = %q, %v", failed.Failure, err)
	}
	retry, callback := f.signIn(t, s, "")
	if !strings.Contains(retry.AuthorizationURL, "client_id=oaiapp_meridian") || strings.Contains(retry.AuthorizationURL, "agent_name_hint") {
		t.Fatalf("retry did not reuse the issued client: %s", retry.AuthorizationURL)
	}
	done, err := s.CompleteChatGPTSignIn(ctx, retry.ID, callback)
	if err != nil || done.State != ChatGPTSignInCompleted {
		t.Fatalf("retry = %+v, %v", done, err)
	}
	next, _ := f.signIn(t, s, "")
	if !strings.Contains(next.AuthorizationURL, "client_id=dynamic_agent_client") {
		t.Fatalf("issued client kept after success: %s", next.AuthorizationURL)
	}
}
