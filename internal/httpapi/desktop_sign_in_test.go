package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestDesktopSignInHandsOffToTheShellsLoopbackListener(t *testing.T) {
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

	do := func(method, target, origin, body string, cookies []*http.Cookie, bearer bool) *http.Response {
		t.Helper()
		request := httptest.NewRequest(method, "https://meridian.example"+target, strings.NewReader(body))
		request.RemoteAddr = "203.0.113.5:4000"
		if origin != "" {
			request.Header.Set("Origin", origin)
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
	verifier := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	start := "/auth/oidc/start?" + url.Values{
		"desktopPort": {"51234"}, "desktopState": {state}, "desktopChallenge": {challenge},
	}.Encode()

	// Partial or malformed parameters are refused before the issuer.
	for _, bad := range []string{
		"/auth/oidc/start?desktopPort=51234",
		"/auth/oidc/start?" + url.Values{"desktopPort": {"80"}, "desktopState": {state}, "desktopChallenge": {challenge}}.Encode(),
		start + "&desktopPort=51235",
	} {
		if response := do(http.MethodGet, bad, "", "", nil, false); response.StatusCode != http.StatusBadRequest {
			t.Fatalf("start %s = %d", bad, response.StatusCode)
		}
	}

	handoff := func() string {
		t.Helper()
		begun := do(http.MethodGet, start, "", "", nil, false)
		if begun.StatusCode != http.StatusFound || !strings.HasPrefix(begun.Header.Get("Location"), "https://issuer.example/") {
			t.Fatalf("desktop start = %d %s", begun.StatusCode, begun.Header.Get("Location"))
		}
		callback := do(http.MethodGet, "/auth/oidc/callback?state=the-state&code=code", "", "", nil, false)
		location, err := url.Parse(callback.Header.Get("Location"))
		if err != nil || callback.StatusCode != http.StatusSeeOther || location.Scheme != "http" ||
			location.Host != "127.0.0.1:51234" || location.Path != "/meridian-sign-in" ||
			location.Query().Get("state") != state || location.Query().Get("ticket") == "" {
			t.Fatalf("desktop callback = %d %s", callback.StatusCode, callback.Header.Get("Location"))
		}
		// The system browser gets no Meridian cookie.
		if len(callback.Cookies()) != 0 {
			t.Fatalf("desktop callback set cookies %v", callback.Cookies())
		}
		return location.Query().Get("ticket")
	}
	redeem := func(ticket, origin string) *http.Response {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"ticket": ticket, "verifier": verifier})
		return do(http.MethodPost, "/auth/desktop/redeem", origin, string(body), nil, false)
	}

	// Redemption must come from the gateway's own page.
	ticket := handoff()
	for _, origin := range []string{"", "https://evil.example"} {
		if response := redeem(ticket, origin); response.StatusCode != http.StatusForbidden {
			t.Fatalf("redeem from %q = %d", origin, response.StatusCode)
		}
	}
	redeemed := redeem(ticket, "https://meridian.example")
	var next desktopRedeemedJSON
	_ = json.NewDecoder(redeemed.Body).Decode(&next)
	claim := cookieNamed(redeemed, claimCookie)
	if redeemed.StatusCode != http.StatusOK || next.Next != "/ui/settings/account?claim=ready" || claim == nil || !claim.HttpOnly {
		t.Fatalf("pre-claim redeem = %d %+v %+v", redeemed.StatusCode, next, claim)
	}
	if response := redeem(ticket, "https://meridian.example"); response.StatusCode != http.StatusNotFound {
		t.Fatalf("second redeem = %d", response.StatusCode)
	}
	if response := do(http.MethodPost, "/auth/claim", "https://meridian.example", "", []*http.Cookie{claim}, true); response.StatusCode != http.StatusNoContent {
		t.Fatalf("claim from the desktop's claim cookie = %d", response.StatusCode)
	}

	// After the claim the handoff signs the webview in.
	redeemed = redeem(handoff(), "https://meridian.example")
	next = desktopRedeemedJSON{}
	_ = json.NewDecoder(redeemed.Body).Decode(&next)
	session := cookieNamed(redeemed, hostUserSessionCookie)
	if redeemed.StatusCode != http.StatusOK || next.Next != "/ui/" || session == nil || !session.Secure ||
		session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("team redeem = %d %+v %+v", redeemed.StatusCode, next, session)
	}
	if response := do(http.MethodGet, "/auth/me", "", "", []*http.Cookie{session}, false); response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("desktop session = %d %s", response.StatusCode, body)
	}
}
