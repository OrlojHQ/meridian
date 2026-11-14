package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type pinClock struct{ now time.Time }

func (c pinClock) Now() time.Time { return c.now }

type pinIDs struct{ n int }

func (s *pinIDs) NewID() string {
	s.n++
	return "id-" + strconv.Itoa(s.n)
}

type discardQueue struct{}

func (discardQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

func TestCapsuleReportsPinnedHarnessSetup(t *testing.T) {
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
	clock := &pinClock{now: time.Date(2026, 11, 9, 14, 5, 0, 0, time.UTC)}
	service := app.NewService(db, clock, &pinIDs{}, discardQueue{})
	service.ConfigureSecrets(key)
	codexBundle := harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "version one"}}}
	codex, err := service.ImportHarnessSetup(ctx, app.SetupImport{Name: "Codex home", Default: true, Bundle: codexBundle}, "import-codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportHarnessSetup(ctx, app.SetupImport{
		Name: "Claude home", Default: true,
		Bundle: harnesssetup.Bundle{Harness: "claude", Files: []harnesssetup.File{{Path: ".claude/CLAUDE.md", Content: "notes"}}},
	}, "import-claude"); err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(ctx, "Project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "Workspace", "capsule")
	if err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(time.Hour)
	codexBundle.Files[0].Content = "version two"
	updated, err := service.ImportHarnessSetup(ctx, app.SetupImport{
		ID: codex.ID, Name: "Codex home", Default: true, Bundle: codexBundle, ExpectedResourceVersion: codex.ResourceVersion,
	}, "import-codex-2")
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := service.MutateHarnessSetup(ctx, codex.ID, app.SetupMutation{
		Name: "Codex home renamed", ExpectedResourceVersion: updated.ResourceVersion,
	}, "rename")
	if err != nil {
		t.Fatal(err)
	}
	clean, err := service.CreateCapsuleForHarness(ctx, project.ID, "Clean", "", "capsule-clean", "clean")
	if err != nil {
		t.Fatal(err)
	}

	const bearer = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(bearer)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(service, token)
	body := getJSON(t, handler, bearer, "/capsules/"+string(capsule.ID))
	var pinned struct {
		PinnedHarnessSetups []struct {
			Harness   string    `json:"harness"`
			SetupID   string    `json:"setupId"`
			Name      string    `json:"name"`
			Revision  string    `json:"revision"`
			RevisedAt time.Time `json:"revisedAt"`
		} `json:"pinnedHarnessSetups"`
	}
	if err := json.Unmarshal(body, &pinned); err != nil {
		t.Fatal(err)
	}
	if len(pinned.PinnedHarnessSetups) != 2 {
		t.Fatalf("pins = %#v", pinned.PinnedHarnessSetups)
	}
	if pinned.PinnedHarnessSetups[0].Harness != "claude" || pinned.PinnedHarnessSetups[0].Name != "Claude home" {
		t.Fatalf("claude pin = %#v", pinned.PinnedHarnessSetups[0])
	}
	codexPin := pinned.PinnedHarnessSetups[1]
	if codexPin.Harness != "codex" || codexPin.SetupID != codex.ID || codexPin.Name != renamed.Name || codexPin.Revision != codex.Revision || !codexPin.RevisedAt.Equal(time.Date(2026, 11, 9, 14, 5, 0, 0, time.UTC)) {
		t.Fatalf("codex pin = %#v, want revision %s at 14:05", codexPin, codex.Revision)
	}
	cleanBody := getJSON(t, handler, bearer, "/capsules/"+string(clean.ID))
	if strings.Contains(string(cleanBody), "pinnedHarnessSetups") {
		t.Fatalf("clean Capsule included a pin: %s", cleanBody)
	}
	list := getJSON(t, handler, bearer, "/projects/"+string(project.ID)+"/capsules")
	if !strings.Contains(string(list), `"name":"Codex home renamed"`) || strings.Count(string(list), "pinnedHarnessSetups") != 1 {
		t.Fatalf("list = %s", list)
	}
}

func getJSON(t *testing.T, handler http.Handler, bearer, path string) []byte {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
	}
	return response.Body.Bytes()
}
