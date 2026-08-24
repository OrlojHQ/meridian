package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestUICoexistsWithAPIAndPreviewFailsClosed(t *testing.T) {
	server := httpapi.NewWithCapabilities(nil, ports.ProviderCapabilities{
		Version: "test/v1",
		Attach:  true,
		Preview: true,
	})

	capabilityResponse := httptest.NewRecorder()
	server.ServeHTTP(
		capabilityResponse,
		httptest.NewRequest(http.MethodGet, "/capabilities", nil),
	)
	if capabilityResponse.Code != http.StatusOK {
		t.Fatalf("capability status = %d", capabilityResponse.Code)
	}
	var capabilities struct {
		ProviderVersion string `json:"providerVersion"`
		Attach          bool   `json:"attach"`
		Preview         bool   `json:"preview"`
	}
	if err := json.Unmarshal(capabilityResponse.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.ProviderVersion != "test/v1" || !capabilities.Attach {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if capabilities.Preview {
		t.Fatal("preview was advertised without a public bounded proxy implementation")
	}

	uiResponse := httptest.NewRecorder()
	server.ServeHTTP(
		uiResponse,
		httptest.NewRequest(http.MethodGet, "/ui/capsules/capsule-1", nil),
	)
	if uiResponse.Code != http.StatusOK {
		t.Fatalf("SPA fallback status = %d", uiResponse.Code)
	}

	unknownAPI := httptest.NewRecorder()
	server.ServeHTTP(
		unknownAPI,
		httptest.NewRequest(http.MethodGet, "/unknown-api", nil),
	)
	if unknownAPI.Code != http.StatusNotFound {
		t.Fatalf("unknown API status = %d", unknownAPI.Code)
	}
}
