package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestListInstallationsReadsEveryPage(t *testing.T) {
	var seen []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		seen = append(seen, page)
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %s", r.URL.Query().Get("per_page"))
		}
		count := 100
		if page >= 2 {
			count = 1
		}
		items := make([]installationJSON, count)
		for i := range items {
			items[i].ID = int64(page*1000 + i + 1)
			items[i].RepositorySelection = "all"
			items[i].Account.Login = "octocat"
			items[i].Account.Type = "User"
		}
		if page == 1 {
			empty := ""
			items[0].SuspendedAt = &empty
		}
		if page == 2 {
			when := "2026-11-23T00:00:00Z"
			items[0].SuspendedAt = &when
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	t.Cleanup(server.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	items, err := NewClient(server.Client()).WithBase(server.URL).ListInstallations(context.Background(), 7, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Fatalf("pages = %v", seen)
	}
	if len(items) != 101 || items[0].Suspended || !items[100].Suspended {
		t.Fatalf("items = %d first=%v last=%v", len(items), items[0].Suspended, items[100].Suspended)
	}
}

func TestAllowedActionURLIsOnlyTheManifestForm(t *testing.T) {
	if !AllowedActionURL("https://github.com/settings/apps/new") {
		t.Fatal("user form")
	}
	if !AllowedActionURL("https://github.com/organizations/acme/settings/apps/new") {
		t.Fatal("organization form")
	}
	for _, raw := range []string{
		"https://example.com/settings/apps/new",
		"http://github.com/settings/apps/new",
		"https://github.com/settings/apps/new?x=1",
		"https://github.com/organizations/acme/settings/apps/new/extra",
	} {
		if AllowedActionURL(raw) {
			t.Fatalf("allowed %s", raw)
		}
	}
}
