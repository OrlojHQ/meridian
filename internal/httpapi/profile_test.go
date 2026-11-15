package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestProfileEndpoints(t *testing.T) {
	ctx := principal.With(context.Background(), principal.Owner())
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
	service := app.NewService(db, clock{}, &ids{}, discardQueue{})
	service.ConfigureSecrets(key)
	const bearer = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(bearer)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(service, token)
	call := func(method, path, idempotency string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		if idempotency != "" {
			req.Header.Set("Idempotency-Key", idempotency)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	// Unauthenticated callers get nothing.
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated profile = %d", unauthenticated.Code)
	}

	empty := call(http.MethodGet, "/profile", "", nil)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"defaultConnections":{}`) ||
		!strings.Contains(empty.Body.String(), `"setups":[]`) || strings.Contains(empty.Body.String(), "updatedAt") {
		t.Fatalf("empty profile = %d %s", empty.Code, empty.Body.String())
	}

	imported := call(http.MethodPost, "/profile/imports", "import-1", map[string]any{
		"setups": []any{map[string]any{
			"bundle": map[string]any{"harness": "codex", "files": []any{map[string]any{"path": ".codex/AGENTS.md", "content": "review"}}},
			"report": map[string]any{"issues": []any{map[string]any{"path": ".codex/auth.json", "reason": "excluded"}}, "warnings": []any{}},
		}},
		"git":                     map[string]any{"name": "Ada Lovelace", "email": "ada@example.com"},
		"expectedResourceVersion": 0,
	})
	if imported.Code != http.StatusCreated {
		t.Fatalf("import = %d %s", imported.Code, imported.Body.String())
	}
	var result struct {
		Profile struct {
			GitName         string `json:"gitName"`
			ResourceVersion int64  `json:"resourceVersion"`
			Setups          []struct {
				Harness string `json:"harness"`
			} `json:"setups"`
		} `json:"profile"`
		Saved     []json.RawMessage `json:"saved"`
		Unchanged []string          `json:"unchanged"`
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Profile.GitName != "Ada Lovelace" || result.Profile.ResourceVersion != 1 || len(result.Saved) != 1 ||
		result.Unchanged == nil || len(result.Profile.Setups) != 1 || result.Profile.Setups[0].Harness != "codex" {
		t.Fatalf("import result = %s", imported.Body.String())
	}

	// Unknown fields are refused, and so is an unsafe identity.
	if response := call(http.MethodPut, "/profile", "update-unknown", map[string]any{
		"gitName": "", "gitEmail": "", "expectedResourceVersion": 1, "ownerUserId": "usr_other",
	}); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPut, "/profile", "update-unsafe", map[string]any{
		"gitName": "Ada\n[core]", "gitEmail": "a@x", "expectedResourceVersion": 1,
	}); response.Code == http.StatusOK {
		t.Fatalf("unsafe identity accepted: %s", response.Body.String())
	}
	updated := call(http.MethodPut, "/profile", "update-1", map[string]any{
		"gitName": "Ada", "gitEmail": "ada@example.com", "expectedResourceVersion": 1,
	})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"gitName":"Ada"`) {
		t.Fatalf("update = %d %s", updated.Code, updated.Body.String())
	}

	project, err := service.CreateProject(ctx, "p", "p")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "c", "c")
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := call(http.MethodGet, "/capsules/"+string(capsule.ID)+"/profile-diagnostic", "", nil)
	if diagnostic.Code != http.StatusOK || diagnostic.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("diagnostic = %d %s", diagnostic.Code, diagnostic.Body.String())
	}
	var body struct {
		MatchesProfile bool `json:"matchesProfile"`
		Harnesses      []struct {
			Harness    string `json:"harness"`
			Exclusions []struct {
				Path string `json:"path"`
			} `json:"exclusions"`
			Connection struct {
				Granted bool `json:"granted"`
			} `json:"connection"`
		} `json:"harnesses"`
		CommitAuthor struct {
			Source string `json:"source"`
			Name   string `json:"name"`
		} `json:"commitAuthor"`
	}
	if err := json.Unmarshal(diagnostic.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.MatchesProfile || len(body.Harnesses) != 1 || len(body.Harnesses[0].Exclusions) != 1 ||
		body.Harnesses[0].Connection.Granted || body.CommitAuthor.Source != "capsule" || body.CommitAuthor.Name != "Ada" {
		t.Fatalf("diagnostic body = %s", diagnostic.Body.String())
	}
	if response := call(http.MethodGet, "/capsules/missing/profile-diagnostic", "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("missing Capsule diagnostic = %d", response.Code)
	}
	repinned := call(http.MethodPost, "/capsules/"+string(capsule.ID)+"/profile-update", "repin-1", nil)
	if repinned.Code != http.StatusOK || repinned.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("profile update = %d %s", repinned.Code, repinned.Body.String())
	}
}
