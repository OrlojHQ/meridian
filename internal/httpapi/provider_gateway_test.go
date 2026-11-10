package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
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
