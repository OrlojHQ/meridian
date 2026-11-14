package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type claudeQueue struct{}

func (claudeQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

type claudeIDs struct{ next int }

func (i *claudeIDs) NewID() string { i.next++; return fmt.Sprintf("id-%d", i.next) }

func TestClaudeLoginCallbackExchangesWithoutEchoingTheToken(t *testing.T) {
	const rawToken = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	const access = "sk-ant-oat01-callback-access"
	const refresh = "sk-ant-ort01-callback-refresh"
	const code = "callback-code-canary"
	token, err := apiauth.ParseToken(rawToken)
	if err != nil {
		t.Fatal(err)
	}
	ctx := principal.With(context.Background(), principal.Owner())
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
	service := app.NewService(db, clock{}, &claudeIDs{}, claudeQueue{})
	service.ConfigureSecrets(key)
	if err := service.ConfigureProviderGateway("https://meridian.example", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.CloseProviderGateway)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":28800,"token_type":"Bearer"}`, access, refresh)
	}))
	t.Cleanup(upstream.Close)
	if err := service.SetClaudeTokenURL(upstream.URL); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(service, token)

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/claude-login", strings.NewReader(`{}`)))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", unauthenticated.Code)
	}

	planted := httptest.NewRecorder()
	plantedRequest := httptest.NewRequest(http.MethodGet, "/callback?code="+code+"&state=not-a-real-state", nil)
	handler.ServeHTTP(planted, plantedRequest)
	if planted.Code != http.StatusOK || strings.Contains(planted.Body.String(), code) || strings.Contains(planted.Body.String(), "not-a-real-state") {
		t.Fatalf("callback echoed the query: %d %s", planted.Code, planted.Body.String())
	}

	begin := httptest.NewRecorder()
	beginRequest := httptest.NewRequest(http.MethodPost, "/claude-login", strings.NewReader(`{}`))
	beginRequest.Host = "127.0.0.1:8080"
	beginRequest.Header.Set("Authorization", "Bearer "+rawToken)
	handler.ServeHTTP(begin, beginRequest)
	if begin.Code != http.StatusOK || begin.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("begin %d %s", begin.Code, begin.Body.String())
	}
	var started struct {
		LoginID      string `json:"loginId"`
		AuthorizeURL string `json:"authorizeUrl"`
		Manual       bool   `json:"manual"`
	}
	if err := json.Unmarshal(begin.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if started.Manual || started.LoginID == "" || parsed.Query().Get("redirect_uri") != "http://localhost:8080/callback" || strings.Contains(begin.Body.String(), "code_verifier") {
		t.Fatalf("begin body = %s", begin.Body.String())
	}

	callback := httptest.NewRecorder()
	callbackURL := "/callback?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(parsed.Query().Get("state"))
	handler.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, callbackURL, nil))
	if callback.Code != http.StatusOK || !strings.Contains(callback.Body.String(), "result=ok") {
		t.Fatalf("callback %d %s", callback.Code, callback.Body.String())
	}
	if strings.Contains(callback.Body.String(), access) || strings.Contains(callback.Body.String(), refresh) || strings.Contains(callback.Body.String(), code) {
		t.Fatal("callback page contains a secret")
	}
	finished := httptest.NewRecorder()
	handler.ServeHTTP(finished, httptest.NewRequest(http.MethodGet, "/claude-login/finished?result=ok&access_token="+access, nil))
	if finished.Code != http.StatusOK || strings.Contains(finished.Body.String(), access) || !strings.Contains(finished.Body.String(), "Signed in") {
		t.Fatalf("finished page = %s", finished.Body.String())
	}

	status := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(http.MethodGet, "/claude-login/"+started.LoginID, nil)
	statusRequest.Header.Set("Authorization", "Bearer "+rawToken)
	handler.ServeHTTP(status, statusRequest)
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), access) || strings.Contains(status.Body.String(), refresh) || !strings.Contains(status.Body.String(), `"status":"complete"`) {
		t.Fatalf("status %d %s", status.Code, status.Body.String())
	}
	list := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/provider-connections", nil)
	listRequest.Header.Set("Authorization", "Bearer "+rawToken)
	handler.ServeHTTP(list, listRequest)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), access) || strings.Contains(list.Body.String(), refresh) {
		t.Fatal("connection list contains a token")
	}
	if err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(access)) || bytes.Contains(raw, []byte(refresh)) || bytes.Contains(raw, []byte(code)) {
			t.Errorf("plaintext secret in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
