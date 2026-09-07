package providergateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatewayLimitsRoutesAndReplacesCredentials(t *testing.T) {
	seen := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer upstream-key" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe upstream request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: response\n\n")
	}))
	defer upstream.Close()
	g := New(func(_ context.Context, l Lease) (string, error) {
		if l.Project != "project" {
			t.Error("wrong scope")
		}
		return "upstream-key", nil
	})
	defer g.Close()
	g.client = upstream.Client()
	g.upstream["openai"] = upstream.URL
	token, err := g.Issue(Lease{Provider: "openai", Connection: "connection", Project: "project", Capsule: "capsule"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method, token string
		status              int
	}{
		{"/provider-gateway/openai/v1/responses", "POST", token, 200},
		{"/provider-gateway/openai/v1/files", "POST", token, 404},
		{"/provider-gateway/anthropic/v1/messages", "POST", token, 404},
		{"/provider-gateway/openai/v1/responses?upstream=https://other.example", "POST", token, 404},
		{"/provider-gateway/openai/v1/responses", "GET", token, 404},
		{"/provider-gateway/openai/v1/responses", "POST", "wrong", 401},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Cookie", "do-not-forward")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d want %d", tc.path, w.Code, tc.status)
		}
	}
	if seen != 1 {
		t.Fatalf("unexpected upstream calls %d", seen)
	}
	g.Revoke("connection", "", "")
	r := httptest.NewRequest("POST", "/provider-gateway/openai/v1/responses", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked credential accepted")
	}
}
func TestRevocationCancelsActiveStream(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	g := New(func(context.Context, Lease) (string, error) { return "key", nil })
	defer g.Close()
	g.client = upstream.Client()
	g.upstream["openai"] = upstream.URL
	token, err := g.Issue(Lease{Provider: "openai", Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest("POST", "/provider-gateway/openai/v1/responses", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+token)
		g.ServeHTTP(httptest.NewRecorder(), r)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never started")
	}
	g.Revoke("connection", "", "")
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream request was not cancelled")
	}
	<-done
}
