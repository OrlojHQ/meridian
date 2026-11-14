package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/principal"
)

func TestAuthenticatedRequestsActAsTheBootstrapOwner(t *testing.T) {
	tokenValue := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	token, err := apiauth.ParseToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	server := New(nil, token)
	var seen []principal.Principal
	server.mux.HandleFunc("GET /principal-probe", func(writer http.ResponseWriter, request *http.Request) {
		caller, _ := principal.From(request.Context())
		seen = append(seen, caller)
		writer.WriteHeader(http.StatusNoContent)
	})
	// A listener whose base context carried the system principal must not
	// leak it into a request.
	system := principal.WithSystem(context.Background())
	tests := []struct {
		name   string
		header string
		cookie string
		status int
		caller principal.Principal
	}{
		{name: "bearer", header: "Bearer " + tokenValue, status: http.StatusNoContent, caller: principal.Owner()},
		{name: "cookie", cookie: server.browserValue, status: http.StatusNoContent, caller: principal.Owner()},
		{name: "wrong bearer", header: "Bearer not-the-token", status: http.StatusUnauthorized},
		{name: "wrong cookie", cookie: "not-the-cookie", status: http.StatusUnauthorized},
		{name: "none", status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seen = nil
			request := httptest.NewRequestWithContext(system, http.MethodGet, "/principal-probe", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: browserSessionCookie, Value: test.cookie})
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if test.status != http.StatusNoContent {
				if len(seen) != 0 {
					t.Fatalf("unauthenticated request reached the handler as %+v", seen)
				}
				return
			}
			if len(seen) != 1 || seen[0] != test.caller {
				t.Fatalf("handler saw %+v, want %+v", seen, test.caller)
			}
		})
	}
}

func TestTicketRedemptionActsOnlyAsTheMinter(t *testing.T) {
	// A ticket minted without a principal reaches the Service as nobody,
	// whatever the redeeming request's context carried, and fails closed.
	server := &Server{
		service: app.NewService(nil, nil, nil, nil),
		tickets: newAttachTicketRegistry(4),
		mux:     http.NewServeMux(),
	}
	server.routes()
	if err := server.tickets.mint("orphan", attachTicket{
		RunID: "run-1", ExpiresAt: time.Now().Add(time.Minute),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(
		principal.With(context.Background(), principal.Owner()),
		http.MethodGet, "/runs/run-1/attach?ticket=orphan", nil,
	)
	request.Header.Set("Origin", "http://localhost")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError ||
		!strings.Contains(recorder.Body.String(), "internal server error") ||
		strings.Contains(recorder.Body.String(), "principal") {
		t.Fatalf("orphan ticket = %d %s", recorder.Code, recorder.Body.String())
	}
}
