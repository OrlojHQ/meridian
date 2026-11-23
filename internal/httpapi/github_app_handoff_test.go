package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestGitHubAppHandoffPostsOnlyToGitHub(t *testing.T) {
	ctx := principal.With(context.Background(), principal.Owner())
	dir := t.TempDir()
	db, err := sqlite.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service := app.NewService(db, clock{}, &ids{}, discardQueue{})
	service.ConfigureSecrets(key)
	setup, err := service.BeginGitHubAppSetup(ctx, "", "http://127.0.0.1:7777", "127.0.0.1:7777")
	if err != nil {
		t.Fatal(err)
	}
	const bearer = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(bearer)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(service, token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, setup.HandoffURL, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action https://github.com") || strings.Contains(csp, "form-action 'self'") || strings.Contains(csp, "form-action *") {
		t.Fatalf("csp = %q", csp)
	}
	body := response.Body.String()
	if !strings.Contains(body, `action="https://github.com/settings/apps/new"`) || strings.Contains(body, "PRIVATE KEY") {
		t.Fatalf("body = %s", body)
	}
	large := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/github/app/webhook", strings.NewReader(strings.Repeat("x", 1<<20+1)))
	handler.ServeHTTP(large, request)
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized webhook = %d", large.Code)
	}
}
