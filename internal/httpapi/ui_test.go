package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestUICoexistsWithAPIAndPreviewFailsClosed(t *testing.T) {
	tokenValue := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	server := httpapi.NewWithCapabilities(nil, ports.ProviderCapabilities{
		Version: "test/v1",
		Attach:  true,
		Preview: true,
	}, token)

	capabilityResponse := httptest.NewRecorder()
	capabilityRequest := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
	capabilityRequest.Header.Set("Authorization", "Bearer "+tokenValue)
	server.ServeHTTP(
		capabilityResponse,
		capabilityRequest,
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
	if unknownAPI.Code != http.StatusUnauthorized {
		t.Fatalf("unknown unauthenticated API status = %d", unknownAPI.Code)
	}
}

func TestAuthenticationBoundariesAndBrowserSession(t *testing.T) {
	tokenValue := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	server := httpapi.NewWithCapabilities(nil, ports.ProviderCapabilities{Version: "test/v1"}, token)
	server.SetReady(true)

	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
	for name, authorization := range map[string]string{
		"missing": "",
		"wrong":   "Bearer BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
			request.Header.Set("Authorization", authorization)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized ||
				strings.Contains(response.Body.String(), tokenValue) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}

	bootstrap := httptest.NewRequest(http.MethodPost, "/auth/browser-session", nil)
	bootstrap.Header.Set("Authorization", "Bearer "+tokenValue)
	bootstrapResponse := httptest.NewRecorder()
	server.ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusNoContent {
		t.Fatalf("bootstrap status = %d", bootstrapResponse.Code)
	}
	cookies := bootstrapResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure ||
		cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Value == tokenValue {
		t.Fatalf("browser session cookie = %#v", cookies)
	}
	cookieRequest := httptest.NewRequest(http.MethodGet, "/capabilities", nil)
	cookieRequest.AddCookie(cookies[0])
	cookieResponse := httptest.NewRecorder()
	server.ServeHTTP(cookieResponse, cookieRequest)
	if cookieResponse.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated status = %d", cookieResponse.Code)
	}
	crossOriginMutation := httptest.NewRequest(http.MethodPost, "/auth/browser-session", nil)
	crossOriginMutation.AddCookie(cookies[0])
	crossOriginMutation.Header.Set("Origin", "https://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	server.ServeHTTP(crossOriginResponse, crossOriginMutation)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin cookie mutation status = %d", crossOriginResponse.Code)
	}

	loopbackUI := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/ui/", nil)
	loopbackUI.Host = "127.0.0.1:8080"
	loopbackUI.RemoteAddr = "127.0.0.1:54321"
	loopbackResponse := httptest.NewRecorder()
	server.ServeHTTP(loopbackResponse, loopbackUI)
	loopbackCookies := loopbackResponse.Result().Cookies()
	if loopbackResponse.Code != http.StatusOK || len(loopbackCookies) != 1 ||
		!loopbackCookies[0].HttpOnly || loopbackCookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("loopback UI bootstrap = %d %#v", loopbackResponse.Code, loopbackCookies)
	}
}
