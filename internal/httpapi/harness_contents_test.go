package httpapi_test

import (
	"context"
	"encoding/json"
	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHarnessContentsAuthenticatedNoStore(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service := app.NewService(db, clock{}, &ids{}, nil)
	service.ConfigureSecrets(key)
	input := app.SetupImport{Name: "Personal", Default: true, Bundle: harnesssetup.Bundle{Harness: "opencode", Files: []harnesssetup.File{{Path: ".config/opencode/AGENTS.md", Content: "review tests"}}}}
	setup, err := service.ImportHarnessSetup(ctx, input, "create")
	if err != nil {
		t.Fatal(err)
	}
	const bearer = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(bearer)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(service, token)
	for _, authenticated := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/harness-setups/"+setup.ID+"/contents", nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if !authenticated {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized: %d", response.Code)
			}
			continue
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response: %d %s", response.Code, response.Body.String())
		}
		var contents app.SetupImport
		if err := json.Unmarshal(response.Body.Bytes(), &contents); err != nil {
			t.Fatal(err)
		}
		if contents.ID != setup.ID || contents.ExpectedResourceVersion != setup.ResourceVersion || contents.Bundle.Digest() != input.Bundle.Digest() {
			t.Fatal("inconsistent editor response")
		}
	}
}
