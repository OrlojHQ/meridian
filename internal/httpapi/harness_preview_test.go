package httpapi_test

import (
	"encoding/json"
	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHarnessPreviewRequiresAuthAndDoesNotNeedPersistence(t *testing.T) {
	const rawToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(rawToken)
	if err != nil {
		t.Fatal(err)
	}
	// A nil service proves the review endpoint does not read or write application
	// storage. Its only input is the authenticated, explicitly uploaded files.
	handler := httpapi.New(nil, token)
	body := `{"harness":"opencode","files":[{"path":".config/opencode/opencode.json","content":"{\"model\":\"test\",\"provider\":{\"apiKey\":\"secret-sentinel\"}}"}]}`
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodPost, "/harness-setups/preview", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if authenticated {
			request.Header.Set("Authorization", "Bearer "+rawToken)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if !authenticated {
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status=%d", response.Code)
			}
			continue
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "secret-sentinel") {
			t.Fatal("excluded content returned")
		}
		var preview struct {
			Bundle harnesssetup.Bundle  `json:"bundle"`
			Issues []harnesssetup.Issue `json:"issues"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
			t.Fatal(err)
		}
		if err := preview.Bundle.Validate(); err != nil || len(preview.Issues) != 1 {
			t.Fatalf("preview=%#v err=%v", preview, err)
		}
	}
}
