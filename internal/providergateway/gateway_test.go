package providergateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
	g := New(func(_ context.Context, l Lease) (Upstream, error) {
		if l.Project != "project" {
			t.Error("wrong scope")
		}
		return Upstream{Key: "upstream-key"}, nil
	}, Options{})
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
		{"/provider-gateway/openai-compatible/v1/responses", "POST", token, 404},
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
func TestGatewayAcceptsOnlyTheAnthropicBetaMarkerAndDropsIt(t *testing.T) {
	seen := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		if r.URL.Path != "/v1/messages" || r.URL.RawQuery != "" || r.Header.Get("X-Api-Key") != "upstream-key" {
			t.Errorf("upstream request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()
	g := New(func(context.Context, Lease) (Upstream, error) { return Upstream{Key: "upstream-key"}, nil }, Options{})
	defer g.Close()
	g.client = upstream.Client()
	g.upstream["anthropic"] = upstream.URL
	g.upstream["openai"] = upstream.URL
	anthropic, err := g.Issue(Lease{Provider: "anthropic", Connection: "a"})
	if err != nil {
		t.Fatal(err)
	}
	openai, err := g.Issue(Lease{Provider: "openai", Connection: "o"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/provider-gateway/anthropic/v1/messages?beta=true", anthropic, 200},
		{"/provider-gateway/anthropic/v1/messages?beta=false", anthropic, 404},
		{"/provider-gateway/anthropic/v1/messages?beta=true&x=1", anthropic, 404},
		{"/provider-gateway/openai/v1/chat/completions?beta=true", openai, 404},
	} {
		w := send(g, tc.path, tc.token, nil)
		if w.Code != tc.status {
			t.Fatalf("%s: %d want %d", tc.path, w.Code, tc.status)
		}
	}
	if seen != 1 {
		t.Fatalf("upstream calls %d", seen)
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
	g := New(func(context.Context, Lease) (Upstream, error) { return Upstream{Key: "key"}, nil }, Options{})
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

func trustTestServer(g *Gateway, server *httptest.Server) {
	config := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	g.custom.Transport.(*http.Transport).TLSClientConfig = config
}

func send(g *Gateway, path, token string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(`{"model":"qwen"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	for name, values := range header {
		r.Header[name] = values
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

func TestCustomUpstreamIsFixedByTheConnection(t *testing.T) {
	var paths []string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer local-key" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("unsafe upstream request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("request reached a Capsule-chosen upstream") }))
	defer other.Close()
	g := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{Key: "local-key", BaseURL: upstream.URL + "/llm/v1"}, nil
	}, Options{})
	defer g.Close()
	trustTestServer(g, upstream)
	token, err := g.Issue(Lease{Provider: OpenAICompatible, Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	overrides := http.Header{"Cookie": {"x"}, "X-Forwarded-Host": {other.Listener.Addr().String()}, "Host": {other.Listener.Addr().String()}}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/provider-gateway/openai-compatible/v1/chat/completions", 200},
		{"/provider-gateway/openai-compatible/v1/responses", 200},
		{"/provider-gateway/openai-compatible/v1/chat/completions?base_url=" + other.URL, 404},
		{"/provider-gateway/openai-compatible/v1/models", 404},
		{"/provider-gateway/openai-compatible/v1/messages", 404},
		{"/provider-gateway/openai/v1/responses", 404},
		{"/provider-gateway/openai-compatible/../../v1/chat/completions", 404},
	} {
		if w := send(g, tc.path, token, overrides); w.Code != tc.status {
			t.Fatalf("%s: %d want %d", tc.path, w.Code, tc.status)
		}
	}
	if strings.Join(paths, ",") != "/llm/v1/chat/completions,/llm/v1/responses" {
		t.Fatalf("unexpected upstream paths %v", paths)
	}
}

func TestCustomUpstreamWithoutKeySendsNoCredential(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Anthropic-Version") == "" {
			t.Error("lease token or empty key forwarded upstream")
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	g := New(func(context.Context, Lease) (Upstream, error) { return Upstream{BaseURL: upstream.URL}, nil }, Options{})
	defer g.Close()
	trustTestServer(g, upstream)
	token, err := g.Issue(Lease{Provider: AnthropicCompatible, Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/provider-gateway/anthropic-compatible/v1/messages", strings.NewReader(`{}`))
	r.Header.Set("X-Api-Key", token)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestAnthropicCompatibleKeyUsesBothHeaders(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "vllm-key" || r.Header.Get("Authorization") != "Bearer vllm-key" {
			t.Error("upstream key not sent in both accepted headers")
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	g := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{Key: "vllm-key", BaseURL: upstream.URL}, nil
	}, Options{})
	defer g.Close()
	trustTestServer(g, upstream)
	token, err := g.Issue(Lease{Provider: AnthropicCompatible, Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/provider-gateway/anthropic-compatible/v1/messages/count_tokens", strings.NewReader(`{}`))
	r.Header.Set("X-Api-Key", token)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestCustomUpstreamRedirectIsNotFollowed(t *testing.T) {
	followed := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	g := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{Key: "local-key", BaseURL: upstream.URL}, nil
	}, Options{})
	defer g.Close()
	trustTestServer(g, upstream)
	token, err := g.Issue(Lease{Provider: OpenAICompatible, Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	w := send(g, "/provider-gateway/openai-compatible/v1/chat/completions", token, nil)
	if w.Code != http.StatusBadGateway || w.Header().Get("Location") != "" || followed {
		t.Fatalf("redirect followed or exposed: %d %q %t", w.Code, w.Header().Get("Location"), followed)
	}
}

func TestPrivateHTTPUpstreamRequiresBothOptIns(t *testing.T) {
	seen := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	for _, tc := range []struct {
		name             string
		installation     bool
		connection       bool
		status, upstream int
	}{
		{"installation disabled", false, true, http.StatusForbidden, 0},
		{"connection did not opt in", true, false, http.StatusForbidden, 0},
		{"both opted in", true, true, http.StatusOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = 0
			g := New(func(context.Context, Lease) (Upstream, error) {
				return Upstream{BaseURL: upstream.URL, AllowPrivateHTTP: tc.connection}, nil
			}, Options{AllowPrivateHTTP: tc.installation})
			defer g.Close()
			token, err := g.Issue(Lease{Provider: OpenAICompatible, Connection: "connection"})
			if err != nil {
				t.Fatal(err)
			}
			if w := send(g, "/provider-gateway/openai-compatible/v1/chat/completions", token, nil); w.Code != tc.status || seen != tc.upstream {
				t.Fatalf("status %d upstream calls %d", w.Code, seen)
			}
		})
	}
}

func TestUpstreamDialPolicy(t *testing.T) {
	for _, tc := range []struct {
		addr            string
		custom, private bool
	}{
		{"127.0.0.1", true, true},
		{"10.0.0.8", true, true},
		{"192.168.1.20", true, true},
		{"100.101.102.103", true, true},
		{"fd00::1", true, true},
		{"::ffff:192.168.1.20", true, true},
		{"8.8.8.8", true, false},
		{"169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"0.0.0.0", false, false},
		{"224.0.0.1", false, false},
	} {
		addr := netip.MustParseAddr(tc.addr).Unmap()
		if dialable(addr) != tc.custom || PrivateNetwork(addr) != tc.private {
			t.Errorf("%s: dialable %t private %t", tc.addr, dialable(addr), PrivateNetwork(addr))
		}
	}
	g := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{BaseURL: "http://localhost.metadata.invalid", AllowPrivateHTTP: true}, nil
	}, Options{AllowPrivateHTTP: true})
	defer g.Close()
	dial := g.private.Transport.(*http.Transport).DialContext
	if _, err := dial(context.Background(), "tcp", "8.8.8.8:80"); !errors.Is(err, errBlockedAddress) {
		t.Fatalf("plaintext dial to a public address was not blocked: %v", err)
	}
	if _, err := g.custom.Transport.(*http.Transport).DialContext(context.Background(), "tcp", "169.254.169.254:80"); !errors.Is(err, errBlockedAddress) {
		t.Fatalf("metadata address was not blocked: %v", err)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		http      bool
	}{
		{"https://models.example/v1", "https://models.example", false},
		{"https://models.example/llm/v1/", "https://models.example/llm", false},
		{"http://dgx.lan:8000", "http://dgx.lan:8000", true},
		{"http://192.168.1.20:11434/v1", "http://192.168.1.20:11434", true},
		{"http://[fd00::1]:8000", "http://[fd00::1]:8000", true},
		{"http://dgx.lan:8000", "", false},
		{"https://models.example", "", true},
		{"http://8.8.8.8:8000", "", true},
		{"https://169.254.169.254", "", false},
		{"https://user:pass@models.example", "", false},
		{"https://models.example/v1?key=x", "", false},
		{"https://models.example/#x", "", false},
		{"https://models.example/a/../b", "", false},
		{"https://models.example/%2e%2e", "", false},
		{"ftp://models.example", "", false},
		{"https://models.example:99999", "", false},
		{"/v1", "", false},
	} {
		got, err := NormalizeBaseURL(tc.raw, tc.http)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Errorf("%s (http %t): %q %v", tc.raw, tc.http, got, err)
		}
	}
}

func TestUpstreamRejectionSurfacesStatusAndSanitizedReason(t *testing.T) {
	var upstream *httptest.Server
	g := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{Key: "local-key-1234", BaseURL: upstream.URL + "/v1"}, nil
	}, Options{})
	defer g.Close()
	token, err := g.Issue(Lease{Provider: OpenAICompatible, Connection: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	upstream = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=upstream-cookie")
		w.Header().Set("X-Upstream-Secret", "upstream-header")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"message": "Unsupported tool type \"web_search\"\u202e\u0007 (key LOCAL-KEY-1234, lease " + token + ", host 127.0.0.1, see https://internal.example/docs?k=v, sk-abcdefgh12345678)",
			"trace":   "Traceback: local-key-1234",
		}})
	}))
	defer upstream.Close()
	trustTestServer(g, upstream)
	w := send(g, "/provider-gateway/openai-compatible/v1/responses", token, nil)
	if w.Code != http.StatusBadRequest || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Upstream-Secret") != "" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	var body struct {
		Error struct {
			Code           string `json:"code"`
			Message        string `json:"message"`
			UpstreamStatus int    `json:"upstreamStatus"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	message := body.Error.Message
	if body.Error.Code != "provider_gateway_error" || body.Error.UpstreamStatus != 400 || !strings.HasPrefix(message, `upstream provider rejected the request (HTTP 400): Unsupported tool type "web_search" (key [redacted], lease [redacted], host [redacted]`) {
		t.Fatalf("unexpected error %+v", body.Error)
	}
	for _, leaked := range []string{"local-key", "LOCAL-KEY", token, "127.0.0.1", "internal.example", "sk-", "Traceback", "upstream-cookie", "upstream-header", "\u202e", "\u0007"} {
		if strings.Contains(message, leaked) {
			t.Fatalf("error message leaked %q: %s", leaked, message)
		}
	}
}

func TestUpstreamReason(t *testing.T) {
	long := strings.Repeat("é", 400)
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"openai", "application/json", `{"error":{"message":"The model ` + "`glm-5.3-flash`" + ` does not exist","type":"invalid_request_error"}}`, "The model `glm-5.3-flash` does not exist"},
		{"string error", "application/json", `{"error":"rate limited","code":429}`, "rate limited"},
		{"fastapi validation", "application/json", `{"detail":[{"loc":["body","tools",0,"type"],"msg":"Input should be 'function'","type":"literal_error"}]}`, "Input should be 'function'"},
		{"plain text", "text/plain; charset=utf-8", "model\r\nnot\tloaded\x1b[31m\xff", "model not loaded [31m"},
		{"html ignored", "text/html", "<html><body>Bad Gateway</body></html>", ""},
		{"unknown json ignored", "application/json", `{"trace":"Traceback ..."}`, ""},
		{"bearer", "text/plain", "Authorization: Bearer abc.def.ghi rejected; bearer xyz", "[redacted] rejected; [redacted]"},
		{"token assignment", "text/plain", "invalid api_key=xyz and password: hunter22", "invalid [redacted] and [redacted]"},
		{"long token", "text/plain", "key 0123456789abcdef0123456789abcdef0123 invalid for Qwen3-Coder-30B-A3B-Instruct", "key [redacted] invalid for Qwen3-Coder-30B-A3B-Instruct"},
		{"truncated", "text/plain", long, strings.Repeat("é", reasonRuneLimit) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			if got := upstreamReason(response); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	oversized := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"` + strings.Repeat("a", reasonBodyLimit) + `"}`))}
	if got := upstreamReason(oversized); got != "" {
		t.Fatalf("truncated JSON produced %q", got)
	}
}

func TestGatewayAttachesClaudeLoginAndLeavesCodexAlone(t *testing.T) {
	const login = "sk-ant-oat-claude-login-canary"
	const codexKey = "codex-upstream-key-canary"
	var sawClaude, sawCodex bool
	var claudeLease, codexLease string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			sawClaude = true
			if r.Header.Get("Authorization") != "Bearer "+login || r.Header.Get("X-Api-Key") != "" {
				t.Errorf("claude login headers: authorization %q x-api-key %q", r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"))
			}
			joined := r.Header.Get("Authorization") + r.Header.Get("X-Api-Key") + r.Header.Get("Cookie")
			if claudeLease != "" && strings.Contains(joined, claudeLease) {
				t.Errorf("lease forwarded: %v", r.Header)
			}
			beta := r.Header.Get("Anthropic-Beta")
			if !strings.Contains(beta, oauthBeta) || !strings.Contains(beta, "prompt-caching-2024-07-31") {
				t.Errorf("beta = %q", beta)
			}
			if r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Cookie") != "" {
				t.Errorf("unexpected headers %v", r.Header)
			}
		case "/v1/responses":
			sawCodex = true
			if r.Header.Get("Authorization") != "Bearer "+codexKey || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Anthropic-Beta") != "" {
				t.Errorf("codex headers changed: %v", r.Header)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	g := New(func(_ context.Context, lease Lease) (Upstream, error) {
		if lease.Provider == OpenAI {
			return Upstream{Key: codexKey}, nil
		}
		return Upstream{Key: login, CredentialKind: CredentialOAuth}, nil
	}, Options{})
	defer g.Close()
	g.client = upstream.Client()
	g.upstream[Anthropic] = upstream.URL
	g.upstream[OpenAI] = upstream.URL
	var err error
	claudeLease, err = g.Issue(Lease{Provider: Anthropic, Connection: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	codexLease, err = g.Issue(Lease{Provider: OpenAI, Connection: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	claude := send(g, "/provider-gateway/anthropic/v1/messages", claudeLease, http.Header{"Anthropic-Beta": {"prompt-caching-2024-07-31"}, "X-Api-Key": {"lease-" + claudeLease}, "Cookie": {"session=lease"}})
	if claude.Code != 200 {
		t.Fatalf("claude gateway %d %s", claude.Code, claude.Body.String())
	}
	codex := send(g, "/provider-gateway/openai/v1/responses", codexLease, nil)
	if codex.Code != 200 {
		t.Fatalf("codex gateway %d %s", codex.Code, codex.Body.String())
	}
	if !sawClaude || !sawCodex {
		t.Fatalf("upstream calls claude=%v codex=%v", sawClaude, sawCodex)
	}
	refused := New(func(context.Context, Lease) (Upstream, error) {
		return Upstream{Key: login, CredentialKind: CredentialOAuth}, nil
	}, Options{})
	defer refused.Close()
	refused.upstream[AnthropicCompatible] = upstream.URL
	token, err := refused.Issue(Lease{Provider: AnthropicCompatible, Connection: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	if got := send(refused, "/provider-gateway/anthropic-compatible/v1/messages", token, nil); got.Code != http.StatusForbidden {
		t.Fatalf("oauth kind on a custom endpoint: %d", got.Code)
	}
}
