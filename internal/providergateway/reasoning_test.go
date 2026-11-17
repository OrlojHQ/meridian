package providergateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestApplyReasoningDefaultsAndStrips(t *testing.T) {
	body, sent := applyReasoning([]byte(`{"model":"alpha"}`), "/v1/chat/completions", "", false)
	if !sent || !strings.Contains(string(body), `"reasoning_effort":"medium"`) {
		t.Fatalf("unknown chat = %s sent %v", body, sent)
	}
	body, sent = applyReasoning([]byte(`{"model":"alpha","reasoning_effort":"high"}`), "/v1/chat/completions", "yes", false)
	if !sent || !strings.Contains(string(body), `"reasoning_effort":"high"`) {
		t.Fatalf("declared effort = %s", body)
	}
	body, sent = applyReasoning([]byte(`{"model":"alpha","reasoning_effort":"high"}`), "/v1/chat/completions", "no", true)
	if sent || strings.Contains(string(body), "reasoning") {
		t.Fatalf("stripped = %s sent %v", body, sent)
	}
	body, sent = applyReasoning([]byte(`{"model":"alpha"}`), "/v1/responses", "", false)
	if !sent || !strings.Contains(string(body), `"effort":"medium"`) {
		t.Fatalf("unknown responses = %s", body)
	}
	if reasoningRejected("the model does not support reasoning_effort") != true || reasoningRejected("the prompt was empty") != false {
		t.Fatal("rejection classification")
	}
}

func TestEndpointReasoningReadsModelInfo(t *testing.T) {
	const secret = "endpoint-private-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"alpha","supports_reasoning":true},{"id":"beta","supports_reasoning":false}]}`)
		case "/model/info":
			_, _ = io.WriteString(w, `{"data":[{"model_name":"gamma","model_info":{"supports_reasoning":true}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	flags := EndpointReasoning(context.Background(), Upstream{BaseURL: server.URL, Key: secret, AllowPrivateHTTP: true})
	if flags["alpha"] != "yes" || flags["beta"] != "no" || flags["gamma"] != "yes" {
		t.Fatalf("flags = %v", flags)
	}
}

func TestGatewayRetriesARejectedReasoningEffortOnce(t *testing.T) {
	const secret = "upstream-private-key"
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if strings.Contains(string(raw), "reasoning_effort") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"unknown parameter reasoning_effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	g := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{Key: secret, BaseURL: server.URL, AllowPrivateHTTP: true}, nil
	}, Options{AllowPrivateHTTP: true})
	defer g.Close()
	token, err := g.Issue(Lease{Provider: OpenAICompatible, Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/provider-gateway/openai-compatible/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	g.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(bodies) != 2 || !strings.Contains(bodies[0], `"reasoning_effort":"medium"`) || strings.Contains(bodies[1], "reasoning") {
		t.Fatalf("status %d bodies %v", recorder.Code, bodies)
	}
	if !g.ReasoningBlocked("connection", "alpha") {
		t.Fatal("rejection was not remembered")
	}
	bodies = nil
	request = httptest.NewRequest(http.MethodPost, "/provider-gateway/openai-compatible/v1/chat/completions", strings.NewReader(`{"model":"alpha","reasoning_effort":"high"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	g.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || len(bodies) != 1 || strings.Contains(bodies[0], "reasoning") || strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("remembered status %d bodies %v response %s", recorder.Code, bodies, recorder.Body.String())
	}
}
