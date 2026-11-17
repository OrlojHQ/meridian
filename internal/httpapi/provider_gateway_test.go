package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/gitgateway"
	"github.com/OrlojHQ/meridian/internal/httpapi"
)

// The local gateway has a listener of its own (ADR 0037), so the API
// listener stops answering gateway routes; an explicit URL keeps them.
func TestAPIListenerServesGatewayOnlyForExplicitURL(t *testing.T) {
	token, err := apiauth.ParseToken("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{false, true} {
		service := app.NewService(nil, clock{}, &ids{}, nil)
		if local {
			err = service.ConfigureLocalProviderGateway("https://provider-gateway.meridian.internal:4433", func() (string, error) { return "", nil }, false)
		} else {
			err = service.ConfigureProviderGateway("https://meridian.example", false)
		}
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/provider-gateway/openai/v1/responses", strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
		response := httptest.NewRecorder()
		httpapi.New(service, token).ServeHTTP(response, request)
		service.CloseProviderGateway()
		// The gateway rejects the unknown lease; the API rejects the bearer.
		want := `"code":"provider_gateway_error"`
		if local {
			want = `"code":"unauthorized"`
		}
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("local=%t: %d %s", local, response.Code, response.Body.String())
		}
	}
}

// The Git gateway follows the same split (ADR 0054): with an explicit URL the
// API listener serves /git-gateway/ before authentication, and the gateway
// asks Git for its capability; with the local listener the API does not.
func TestAPIListenerServesGitGatewayOnlyForExplicitURL(t *testing.T) {
	token, err := apiauth.ParseToken("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	for _, local := range []bool{false, true} {
		service := app.NewService(nil, clock{}, &ids{}, nil)
		var trust func() (string, error)
		if local {
			trust = func() (string, error) { return "", nil }
		}
		gateway := gitgateway.New(nil)
		if err := service.ConfigureGitGateway(gateway, "https://meridian.example", trust); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, gitgateway.RepositoryPath+"/info/refs?service=git-receive-pack", nil)
		response := httptest.NewRecorder()
		httpapi.New(service, token).ServeHTTP(response, request)
		gateway.Close()
		challenged := response.Code == http.StatusUnauthorized &&
			strings.Contains(response.Header().Get("WWW-Authenticate"), "Meridian")
		if challenged == local {
			t.Fatalf("local=%t: %d %q %s", local, response.Code, response.Header().Get("WWW-Authenticate"), response.Body.String())
		}
	}
}
