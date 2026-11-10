package claudelogin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBeginKeepsTheVerifierOffTheURL(t *testing.T) {
	loop, err := Begin("http://localhost:8080/callback", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(loop.URL, loop.Verifier) || strings.Contains(loop.URL, "code_verifier") || strings.Contains(loop.URL, "code=true") {
		t.Fatalf("authorize URL exposes the verifier or forces the code page: %s", loop.URL)
	}
	for _, part := range []string{
		"client_id=" + ClientID,
		"code_challenge_method=S256",
		"redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fcallback",
		"state=" + loop.State,
		"user%3Asessions%3Aclaude_code",
	} {
		if !strings.Contains(loop.URL, part) {
			t.Fatalf("URL missing %s: %s", part, loop.URL)
		}
	}
	manual, err := Begin("http://evil.example/callback", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(manual.URL, "code=true") || !strings.Contains(manual.URL, "platform.claude.com%2Foauth%2Fcode%2Fcallback") {
		t.Fatalf("manual URL = %s", manual.URL)
	}
	if strings.Contains(manual.URL, manual.Verifier) {
		t.Fatal("verifier leaked into the authorize URL")
	}
	if _, err := Begin("https://localhost:8080/callback", false); err == nil {
		t.Fatal("accepted a non-loopback redirect")
	}
	if _, err := Begin("http://127.0.0.1:8080/callback", false); err == nil {
		t.Fatal("accepted a redirect other than localhost")
	}
}

func TestExchangeDoesNotFollowRedirectsOrEchoTheCode(t *testing.T) {
	const code = "auth-code-canary"
	const access = "sk-ant-oat01-access-canary"
	const refresh = "sk-ant-ort01-refresh-canary"
	leaked := false
	decoy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		leaked = true
	}))
	defer decoy.Close()
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), access) {
			t.Error("request echoed a token")
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing user agent")
		}
		http.Redirect(w, r, decoy.URL, http.StatusFound)
	}))
	defer token.Close()
	_, err := Exchange(context.Background(), token.URL, "http://localhost:8080/callback", strings.Repeat("a", 43), code, strings.Repeat("b", 43), time.Now())
	if err == nil || strings.Contains(err.Error(), code) || strings.Contains(err.Error(), access) {
		t.Fatalf("error = %v", err)
	}
	if leaked {
		t.Fatal("token client followed a redirect")
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"code":"`+code+`"`) || !strings.Contains(string(body), `"code_verifier":"`+strings.Repeat("a", 43)+`"`) {
			t.Errorf("exchange body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":28800,"token_type":"Bearer","scope":"user:inference"}`, access, refresh)
	}))
	defer ok.Close()
	cred, err := Exchange(context.Background(), ok.URL, "http://localhost:8080/callback", strings.Repeat("a", 43), code, strings.Repeat("b", 43), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken() != access || strings.Contains(fmt.Sprintf("%+v %#v %s", cred, cred, cred), access) {
		t.Fatalf("credential leaked: %s", cred.AccessToken())
	}
	stored, err := cred.Stored()
	if err != nil || !strings.Contains(string(stored), refresh) || !strings.Contains(string(stored), access) {
		t.Fatalf("stored = %s %v", stored, err)
	}
	opened, err := ParseStored(stored)
	if err != nil || opened.AccessToken() != access || opened.NeedsRefresh(time.Now()) {
		t.Fatalf("opened = %v %v", opened.AccessToken(), err)
	}
	if !opened.NeedsRefresh(time.Now().Add(9 * time.Hour)) {
		t.Fatal("token near expiry was treated as fresh")
	}
}

func TestRefreshKeepsAnOmittedRefreshTokenAndRefusesNull(t *testing.T) {
	const previous = "sk-ant-ort01-previous-canary"
	const access = "sk-ant-oat01-renewed-canary"
	current, err := ParseStored([]byte(`{"access_token":"sk-ant-oat01-old-canary","refresh_token":"` + previous + `","expires_at":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var sawBeta bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), previous) {
			t.Errorf("refresh body = %s", body)
		}
		sawBeta = r.Header.Get("Anthropic-Beta") == oauthBeta
		fmt.Fprintf(w, `{"access_token":%q,"expires_in":28800,"token_type":"bearer"}`, access)
	}))
	defer server.Close()
	renewed, err := Refresh(context.Background(), server.URL, current, time.Now())
	if err != nil || renewed.AccessToken() != access || !sawBeta {
		t.Fatalf("renewed = %s %v beta %v", renewed.AccessToken(), err, sawBeta)
	}
	stored, err := renewed.Stored()
	if err != nil || !strings.Contains(string(stored), previous) || strings.Count(string(stored), previous) != 1 {
		t.Fatalf("stored = %s %v", stored, err)
	}
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":null,"expires_in":28800}`, access)
	}))
	defer refused.Close()
	_, err = Refresh(context.Background(), refused.URL, current, time.Now())
	if err == nil || strings.Contains(err.Error(), previous) || strings.Contains(err.Error(), access) {
		t.Fatalf("null refresh error = %v", err)
	}
	if err := AllowedEndpoint("https://evil.example/v1/oauth/token"); err == nil {
		t.Fatal("accepted a non-official token host")
	}
	if err := AllowedEndpoint("http://8.8.8.8/token"); err == nil {
		t.Fatal("accepted a public http token host")
	}
}
