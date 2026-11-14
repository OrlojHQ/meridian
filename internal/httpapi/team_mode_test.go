package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type teamClock struct{ now time.Time }

func (c *teamClock) Now() time.Time { c.now = c.now.Add(time.Millisecond); return c.now }

type teamIDs struct{ next int }

func (i *teamIDs) NewID() string {
	i.next++
	return "id" + string(rune('a'+i.next%26)) + time.Now().Format("150405.000000000")
}

type teamIssuer struct{ subject string }

func (teamIssuer) Issuer() string { return "https://issuer.example" }

func (teamIssuer) Begin(context.Context) (oidcauth.Attempt, error) {
	return oidcauth.Attempt{State: "the-state", URL: "https://issuer.example/authorize?state=the-state"}, nil
}

func (f teamIssuer) Complete(_ context.Context, _ oidcauth.Attempt, code string) (oidcauth.Identity, error) {
	if code != "code" {
		return oidcauth.Identity{}, oidcauth.ErrDenied
	}
	return oidcauth.Identity{Issuer: "https://issuer.example", Subject: f.subject}, nil
}

func cookieNamed(response *http.Response, name string) *http.Cookie {
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func TestClaimMovesTheInstallationToTeamMode(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &teamClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
	service := app.NewService(store, clock, &teamIDs{}, nil)
	identity, err := app.NewIdentity(ctx, store, clock, &teamIDs{}, teamIssuer{subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	tokenValue := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	token, err := apiauth.ParseToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	server := New(service, token)
	server.SetIdentity(identity)
	browserCookie := &http.Cookie{Name: browserSessionCookie, Value: server.browserValue}

	do := func(method, path string, cookies []*http.Cookie, bearer bool) *http.Response {
		t.Helper()
		request := httptest.NewRequest(method, "http://localhost"+path, nil)
		request.RemoteAddr = "127.0.0.1:4000"
		if method != http.MethodGet {
			request.Header.Set("Origin", "http://localhost")
		}
		if bearer {
			request.Header.Set("Authorization", "Bearer "+tokenValue)
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder.Result()
	}
	mode := func() authenticationModeJSON {
		t.Helper()
		var body authenticationModeJSON
		if err := json.NewDecoder(do(http.MethodGet, "/auth/mode", nil, false).Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if got := mode(); got.Mode != "single_user" || !got.SignIn || !got.Claimable {
		t.Fatalf("mode before claim = %+v", got)
	}
	// Single-user mode is unchanged: the installation cookie works.
	if response := do(http.MethodGet, "/projects", []*http.Cookie{browserCookie}, false); response.StatusCode != http.StatusOK {
		t.Fatalf("single-user cookie = %d", response.StatusCode)
	}

	// Sign in with the issuer, then claim with the installation token.
	start := do(http.MethodGet, "/auth/oidc/start", nil, false)
	if start.StatusCode != http.StatusFound || !strings.Contains(start.Header.Get("Location"), "state=the-state") {
		t.Fatalf("start = %d %s", start.StatusCode, start.Header.Get("Location"))
	}
	callback := do(http.MethodGet, "/auth/oidc/callback?state=the-state&code=code", nil, false)
	claim := cookieNamed(callback, claimCookie)
	if callback.StatusCode != http.StatusSeeOther || callback.Header.Get("Location") != "/ui/settings/account?claim=ready" ||
		claim == nil || !claim.HttpOnly || claim.SameSite != http.SameSiteStrictMode {
		t.Fatalf("callback = %d %s %+v", callback.StatusCode, callback.Header.Get("Location"), claim)
	}
	// The browser cookie alone cannot claim: the installation token is
	// required.
	if response := do(http.MethodPost, "/auth/claim", []*http.Cookie{browserCookie, claim}, false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie-only claim = %d", response.StatusCode)
	}
	claimed := do(http.MethodPost, "/auth/claim", []*http.Cookie{claim}, true)
	user := cookieNamed(claimed, userSessionCookie)
	if claimed.StatusCode != http.StatusNoContent || user == nil || !user.HttpOnly || user.SameSite != http.SameSiteStrictMode {
		t.Fatalf("claim = %d %+v", claimed.StatusCode, user)
	}
	if got := mode(); got.Mode != "team" || got.Claimable {
		t.Fatalf("mode after claim = %+v", got)
	}
	if response := do(http.MethodPost, "/auth/claim", []*http.Cookie{claim}, true); response.StatusCode == http.StatusNoContent {
		t.Fatal("claimed twice")
	}

	// Team mode: the member's session works; the installation cookie does
	// not; the installation token reaches only the audit log.
	if response := do(http.MethodGet, "/projects", []*http.Cookie{user}, false); response.StatusCode != http.StatusOK {
		t.Fatalf("member session = %d", response.StatusCode)
	}
	var member memberJSON
	if err := json.NewDecoder(do(http.MethodGet, "/auth/me", []*http.Cookie{user}, false).Body).Decode(&member); err != nil ||
		member.Mode != "team" || member.Role != "admin" {
		t.Fatalf("me = %+v, %v", member, err)
	}
	if response := do(http.MethodGet, "/projects", []*http.Cookie{browserCookie}, false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("installation cookie in team mode = %d", response.StatusCode)
	}
	if response := do(http.MethodGet, "/projects", nil, true); response.StatusCode != http.StatusForbidden {
		t.Fatalf("break-glass on an object route = %d", response.StatusCode)
	}
	audit := do(http.MethodGet, "/audit-records?limit=100", nil, true)
	var records auditRecordPageJSON
	if err := json.NewDecoder(audit.Body).Decode(&records); err != nil || audit.StatusCode != http.StatusOK {
		t.Fatalf("break-glass audit read = %d, %v", audit.StatusCode, err)
	}
	actions := map[string]bool{}
	for _, record := range records.Items {
		actions[record.Action] = true
	}
	if !actions["installation.claim"] || !actions["auth.sign_in"] {
		t.Fatalf("audit actions = %v", actions)
	}
	if response := do(http.MethodPost, "/auth/browser-session", nil, true); response.StatusCode != http.StatusForbidden {
		t.Fatalf("browser session in team mode = %d", response.StatusCode)
	}
	if ui := do(http.MethodGet, "/ui/", nil, false); cookieNamed(ui, browserSessionCookie) != nil {
		t.Fatal("a loopback page load minted the installation cookie in team mode")
	}

	// A second break-glass use is audited too.
	audit = do(http.MethodGet, "/audit-records?limit=100", nil, true)
	records = auditRecordPageJSON{}
	if err := json.NewDecoder(audit.Body).Decode(&records); err != nil {
		t.Fatal(err)
	}
	uses := 0
	for _, record := range records.Items {
		if record.Action == "break_glass.use" && record.Principal == "break_glass" {
			uses++
		}
	}
	if uses != 2 {
		t.Fatalf("break-glass uses = %d", uses)
	}

	// A member mints personal access tokens from the browser; the CLI then
	// uses one as a bearer.
	mint := func(body string, cookies []*http.Cookie, bearer string) *http.Response {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "http://localhost/auth/tokens", strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:4000"
		request.Header.Set("Origin", "http://localhost")
		request.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder.Result()
	}
	withToken := func(method, path, value string) int {
		t.Helper()
		request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader("{}"))
		request.RemoteAddr = "127.0.0.1:4000"
		request.Header.Set("Authorization", "Bearer "+value)
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder.Code
	}
	type mintedJSON struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	decodeMinted := func(response *http.Response) mintedJSON {
		t.Helper()
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("mint = %d", response.StatusCode)
		}
		var minted mintedJSON
		if err := json.NewDecoder(response.Body).Decode(&minted); err != nil || !strings.HasPrefix(minted.Token, app.TokenPrefix) {
			t.Fatalf("minted = %+v, %v", minted, err)
		}
		return minted
	}
	full := decodeMinted(mint(`{"name":"laptop"}`, []*http.Cookie{user}, ""))
	readOnly := decodeMinted(mint(`{"name":"viewer","readOnly":true,"expiresInDays":30}`, []*http.Cookie{user}, ""))
	if code := withToken(http.MethodGet, "/projects", full.Token); code != http.StatusOK {
		t.Fatalf("token read = %d", code)
	}
	if code := withToken(http.MethodGet, "/projects", readOnly.Token); code != http.StatusOK {
		t.Fatalf("read-only token read = %d", code)
	}
	if code := withToken(http.MethodPost, "/projects", readOnly.Token); code != http.StatusForbidden {
		t.Fatalf("read-only token write = %d", code)
	}
	// A token cannot mint another token.
	if response := mint(`{"name":"chained"}`, nil, full.Token); response.StatusCode != http.StatusForbidden {
		t.Fatalf("token minted a token: %d", response.StatusCode)
	}
	revokeRequest := httptest.NewRequest(http.MethodPost, "http://localhost/auth/tokens/"+full.ID+"/revoke", nil)
	revokeRequest.RemoteAddr = "127.0.0.1:4000"
	revokeRequest.Header.Set("Origin", "http://localhost")
	revokeRequest.AddCookie(user)
	revoked := httptest.NewRecorder()
	server.ServeHTTP(revoked, revokeRequest)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", revoked.Code)
	}
	if code := withToken(http.MethodGet, "/projects", full.Token); code != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d", code)
	}

	// Signing out ends the session.
	if response := do(http.MethodPost, "/auth/sign-out", []*http.Cookie{user}, false); response.StatusCode != http.StatusNoContent {
		t.Fatalf("sign-out = %d", response.StatusCode)
	}
	if response := do(http.MethodGet, "/projects", []*http.Cookie{user}, false); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed-out session = %d", response.StatusCode)
	}
	if denied := do(http.MethodGet, "/auth/oidc/callback?state=the-state&code=wrong", nil, false); denied.StatusCode == http.StatusSeeOther {
		t.Fatal("a failed sign-in redirected as signed in")
	}
}
