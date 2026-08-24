package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesSPAFallbackWithSecurityHeaders(t *testing.T) {
	handler := Handler()
	for _, requestPath := range []string{"/ui/", "/ui/capsules/capsule-1", "/ui/runs/run-1"} {
		request := httptest.NewRequest(http.MethodGet, requestPath, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", requestPath, response.Code)
		}
		if !strings.Contains(response.Body.String(), "<!doctype html>") {
			t.Fatalf("%s did not receive SPA index", requestPath)
		}
		for _, name := range []string{
			"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options",
			"X-Frame-Options", "Cross-Origin-Opener-Policy", "Permissions-Policy",
		} {
			if response.Header().Get(name) == "" {
				t.Errorf("%s missing %s", requestPath, name)
			}
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s cache control = %q", requestPath, response.Header().Get("Cache-Control"))
		}
	}
}

func TestHandlerIsIsolatedToUIRoutes(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/capsules/capsule-1", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("API-shaped route status = %d", response.Code)
	}
}
