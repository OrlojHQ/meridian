package httpapi

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
)

func browserTestToken(t *testing.T, fill byte) (*apiauth.Token, string) {
	t.Helper()
	value := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	token, err := apiauth.ParseToken(value)
	if err != nil {
		t.Fatal(err)
	}
	return token, value
}

func exchangeBrowserSession(t *testing.T, server *Server, bearer string) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://station.example/auth/browser-session", nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Origin", "https://station.example")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("exchange status = %d", recorder.Code)
	}
	cookie := cookieNamed(recorder.Result(), browserSessionCookie)
	if cookie == nil {
		t.Fatal("exchange set no session cookie")
	}
	return cookie
}

func probeWithCookie(server *Server, value string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "https://station.example/browser-probe", nil)
	request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: value})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func newProbeServer(token *apiauth.Token, now func() time.Time) *Server {
	server := New(nil, token)
	server.browser.now = now
	server.mux.HandleFunc("GET /browser-probe", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	return server
}

func TestBrowserSessionOutlivesTheAppAndTheDaemon(t *testing.T) {
	token, bearer := browserTestToken(t, 7)
	start := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	clock := start
	now := func() time.Time { return clock }

	cookie := exchangeBrowserSession(t, newProbeServer(token, now), bearer)
	if cookie.MaxAge != int(browserSessionLifetime.Seconds()) {
		t.Fatalf("MaxAge = %d, want a lasting cookie", cookie.MaxAge)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie attributes = %+v", cookie)
	}
	if strings.Contains(cookie.Value, bearer) {
		t.Fatal("session value carries the installation token")
	}

	// A restarted daemon with the same installation token accepts it.
	restarted := newProbeServer(token, now)
	if code := probeWithCookie(restarted, cookie.Value).Code; code != http.StatusNoContent {
		t.Fatalf("restarted daemon status = %d", code)
	}

	// A rotated installation token ends it.
	rotated, _ := browserTestToken(t, 9)
	if code := probeWithCookie(newProbeServer(rotated, now), cookie.Value).Code; code != http.StatusUnauthorized {
		t.Fatalf("rotated token status = %d", code)
	}

	// It expires after the lifetime without use.
	clock = start.Add(browserSessionLifetime)
	if code := probeWithCookie(restarted, cookie.Value).Code; code != http.StatusUnauthorized {
		t.Fatalf("expired session status = %d", code)
	}
}

func TestBrowserSessionRenewsWhileInUse(t *testing.T) {
	token, bearer := browserTestToken(t, 7)
	start := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	clock := start
	server := newProbeServer(token, func() time.Time { return clock })
	cookie := exchangeBrowserSession(t, server, bearer)

	fresh := probeWithCookie(server, cookie.Value)
	if fresh.Code != http.StatusNoContent || cookieNamed(fresh.Result(), browserSessionCookie) != nil {
		t.Fatalf("a new session was renewed: status %d", fresh.Code)
	}

	clock = start.Add(browserSessionRenewAfter + time.Minute)
	aged := probeWithCookie(server, cookie.Value)
	renewed := cookieNamed(aged.Result(), browserSessionCookie)
	if aged.Code != http.StatusNoContent || renewed == nil {
		t.Fatalf("a day-old session was not renewed: status %d", aged.Code)
	}

	// The renewed value outlives the original.
	clock = start.Add(browserSessionLifetime + time.Hour)
	if code := probeWithCookie(server, cookie.Value).Code; code != http.StatusUnauthorized {
		t.Fatalf("original session status = %d", code)
	}
	if code := probeWithCookie(server, renewed.Value).Code; code != http.StatusNoContent {
		t.Fatalf("renewed session status = %d", code)
	}
}

func TestBrowserSessionRejectsForgedValues(t *testing.T) {
	token, _ := browserTestToken(t, 7)
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	server := newProbeServer(token, func() time.Time { return now })
	valid := server.browser.mint()
	expires, signature, _ := strings.Cut(valid, ".")
	later := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	far := server.browser
	far.now = func() time.Time { return now.Add(365 * 24 * time.Hour) }
	farValue := far.mint()

	for name, value := range map[string]string{
		"other expiry":     later + "." + signature,
		"bad signature":    expires + "." + strings.Repeat("A", len(signature)),
		"no signature":     expires,
		"signed far ahead": farValue,
		"signed negative":  "-1." + signature,
		"oversized":        strings.Repeat("1", maxBrowserSessionValue+1),
		"old random value": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)),
	} {
		t.Run(name, func(t *testing.T) {
			if code := probeWithCookie(server, value).Code; code != http.StatusUnauthorized {
				t.Fatalf("status = %d", code)
			}
		})
	}
}
