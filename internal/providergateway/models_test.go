package providergateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListModelsReadsIDsAndHidesTheKey(t *testing.T) {
	const secret = "endpoint-private-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"alpha"},{"id":"beta"}]}`))
	}))
	defer server.Close()
	ids, err := ListModels(context.Background(), Upstream{BaseURL: server.URL, Key: secret, AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "alpha,beta" {
		t.Fatalf("ids = %v", ids)
	}
	_, err = ListModels(context.Background(), Upstream{BaseURL: server.URL, Key: "wrong", AllowPrivateHTTP: true})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error = %v", err)
	}
}
