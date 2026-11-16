package githubci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadCommitCombinesChecksAndStatuses(t *testing.T) {
	var sawToken bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "token") || strings.Contains(r.Header.Get("Authorization"), "ghp_") == false && r.URL.Path != "" {
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer ghp_testtokenvalue" {
			t.Errorf("authorization = %q", auth)
		}
		sawToken = true
		switch {
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"check_runs": []map[string]any{{
					"id": 9, "name": "build", "status": "completed", "conclusion": "failure",
					"html_url": "https://github.com/orloj/meridian/runs/9",
					"output":   map[string]string{"title": "build", "summary": "summary line"},
				}},
			})
		case strings.Contains(r.URL.Path, "/annotations"):
			_ = json.NewEncoder(w).Encode([]map[string]string{{"message": "assertion failed"}})
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statuses": []map[string]any{{
					"context": "ci/lint", "state": "success", "description": "ok",
					"target_url": "https://example.com/lint",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(server.Client())
	client.APIBase = server.URL
	snapshot, err := client.ReadCommit(context.Background(), "ghp_testtokenvalue", "orloj", "meridian", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if !sawToken || len(snapshot.Checks) != 2 {
		t.Fatalf("checks = %#v", snapshot.Checks)
	}
	if snapshot.Checks[0].Conclusion != "failure" || !strings.Contains(snapshot.Checks[0].Detail, "assertion failed") {
		t.Fatalf("check = %#v", snapshot.Checks[0])
	}
	if snapshot.Checks[1].Name != "ci/lint" || snapshot.Checks[1].Conclusion != "success" {
		t.Fatalf("status = %#v", snapshot.Checks[1])
	}
}

func TestReadCommitKeeps403OffTheLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Resource protected by organization SAML enforcement"}`, http.StatusForbidden)
	}))
	defer server.Close()
	client := New(server.Client())
	client.APIBase = server.URL
	_, err := client.ReadCommit(context.Background(), "ghp_testtokenvalue", "orloj", "meridian", strings.Repeat("b", 40))
	status, ok := err.(*StatusError)
	if !ok || status.Status != http.StatusForbidden || status.Reason != "" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), "ghp_") || strings.Contains(err.Error(), "SAML") {
		t.Fatalf("error leaked detail: %v", err)
	}
}

func TestReadCommitMarks401AsTheLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer server.Close()
	client := New(server.Client())
	client.APIBase = server.URL
	_, err := client.ReadCommit(context.Background(), "ghp_testtokenvalue", "orloj", "meridian", strings.Repeat("c", 40))
	status, ok := err.(*StatusError)
	if !ok || status.Status != http.StatusUnauthorized || status.Reason != "invalid" {
		t.Fatalf("err = %#v", err)
	}
}

func TestWriteChecksSectionReplacesTheMarkerOnly(t *testing.T) {
	var patched string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.RawQuery, "token") || strings.Contains(string(body), "ghp_") {
			t.Errorf("token leaked into %s %s", r.URL, body)
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"body": "Please review.\n\n<!-- meridian-checks -->\nold\n<!-- /meridian-checks -->\n",
			})
			return
		}
		patched = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New(server.Client())
	client.APIBase = server.URL
	section := "<!-- meridian-checks -->\n## Meridian checks\n\n- failure `build`\n<!-- /meridian-checks -->\n"
	if err := client.WriteChecksSection(context.Background(), "ghp_testtokenvalue", "orloj", "meridian", 7, section); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patched, "Please review.") || !strings.Contains(patched, "failure `build`") || strings.Contains(patched, "old") {
		t.Fatalf("patched = %s", patched)
	}
}
