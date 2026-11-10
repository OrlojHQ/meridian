// Package providergateway exposes only bounded provider inference routes using
// revocable Capsule credentials. Upstream API keys never enter a Capsule.
package providergateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	OpenAI              = "openai"
	Anthropic           = "anthropic"
	OpenAICompatible    = "openai-compatible"
	AnthropicCompatible = "anthropic-compatible"
	// ChatGPT uses a user's ChatGPT plan through Sign in with ChatGPT
	// (ADR 0036). Its upstream credential is a refreshable OAuth token.
	ChatGPT = "chatgpt"
)

// ErrSignInRequired is returned by Authorize when a ChatGPT connection's
// sign-in is no longer valid. The gateway tells the harness to sign in again.
var ErrSignInRequired = errors.New("ChatGPT sign-in required")

// API returns the wire protocol family of a provider connection type, or ""
// for an unknown type.
func API(provider string) string {
	switch provider {
	case OpenAI, OpenAICompatible, ChatGPT:
		return OpenAI
	case Anthropic, AnthropicCompatible:
		return Anthropic
	}
	return ""
}

// Custom reports whether the provider type sends requests to an
// operator-configured base URL instead of a fixed official upstream.
func Custom(provider string) bool {
	return provider == OpenAICompatible || provider == AnthropicCompatible
}

// LeaseLifetime is how long a Capsule gateway lease is valid.
const LeaseLifetime = 24 * time.Hour

type Lease struct {
	Connection, Project, Capsule, Harness, Provider string
	Expires                                         time.Time
}

// Upstream is resolved on the control plane for every request. BaseURL and
// AllowPrivateHTTP apply only to custom provider types; Key may be empty when
// a custom endpoint needs no authentication.
type Upstream struct {
	Key              string
	BaseURL          string
	AllowPrivateHTTP bool
	// Refresh, when set, forces a new upstream credential. The gateway calls
	// it once after an upstream 401 and retries the request with the result.
	Refresh func(context.Context) (string, error)
}
type Authorize func(context.Context, Lease) (Upstream, error)

// Options are installation-level gateway policy set by the daemon operator.
type Options struct {
	// AllowPrivateHTTP permits custom connections that opted in to send
	// plaintext HTTP to loopback, RFC 1918, ULA, or CGNAT addresses.
	AllowPrivateHTTP bool
}
type entry struct {
	slots  chan struct{}
	lease  Lease
	cancel context.CancelFunc
	ctx    context.Context
}
type Gateway struct {
	slots            chan struct{}
	mu               sync.Mutex
	leases           map[[32]byte]entry
	authorize        Authorize
	client           *http.Client
	custom           *http.Client
	private          *http.Client
	allowPrivateHTTP bool
	upstream         map[string]string
}

var errBlockedAddress = errors.New("upstream address is not permitted")
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// dialable excludes addresses no inference endpoint should use, notably
// link-local cloud metadata services.
func dialable(addr netip.Addr) bool {
	return addr.IsValid() && !addr.IsUnspecified() && !addr.IsLinkLocalUnicast() && !addr.IsMulticast() && !addr.IsLinkLocalMulticast() && !addr.IsInterfaceLocalMulticast()
}

// PrivateNetwork reports whether plaintext HTTP may reach addr when the
// operator opted in: loopback, RFC 1918, IPv6 ULA, or RFC 6598 shared space.
func PrivateNetwork(addr netip.Addr) bool {
	addr = addr.Unmap()
	return dialable(addr) && (addr.IsLoopback() || addr.IsPrivate() || sharedAddressSpace.Contains(addr))
}

// NormalizeBaseURL validates an operator-supplied custom upstream and returns
// it without a trailing slash or /v1 suffix. Hostnames are checked again
// against the resolved address on every connection.
func NormalizeBaseURL(raw string, allowPrivateHTTP bool) (string, error) {
	invalid := errors.New("base URL must be an absolute http(s) URL without credentials, query, or fragment")
	if raw == "" || len(raw) > 2048 {
		return "", invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", invalid
	}
	switch {
	case u.Scheme == "https" && !allowPrivateHTTP:
	case u.Scheme == "http" && allowPrivateHTTP:
	case u.Scheme == "http":
		return "", errors.New("http:// base URLs require the explicit private-network HTTP opt-in")
	case u.Scheme == "https":
		return "", errors.New("the private-network HTTP opt-in applies only to http:// base URLs")
	default:
		return "", invalid
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", invalid
		}
	}
	if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
		addr = addr.Unmap()
		if !dialable(addr) || allowPrivateHTTP && !PrivateNetwork(addr) {
			return "", errors.New("base URL address is not permitted")
		}
	} else if strings.ContainsAny(u.Hostname(), "%[]") {
		return "", invalid
	}
	path := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if path == "" {
			break
		}
		if segment == "" || segment == "." || segment == ".." || strings.Trim(segment, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~") != "" {
			return "", invalid
		}
	}
	return u.Scheme + "://" + u.Host + path, nil
}

func transport(allow func(netip.Addr) bool, headerTimeout time.Duration) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.ResponseHeaderTimeout = headerTimeout
	t.MaxResponseHeaderBytes = 32 << 10
	if allow != nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			parsed, err := netip.ParseAddrPort(address)
			if err != nil || !allow(parsed.Addr().Unmap()) {
				return errBlockedAddress
			}
			return nil
		}}
		t.DialContext = dialer.DialContext
	}
	return t
}

func New(authorize Authorize, options Options) *Gateway {
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Self-hosted servers may load a model before sending response headers.
	customHeaderTimeout := 5 * time.Minute
	return &Gateway{
		slots: make(chan struct{}, 64), leases: map[[32]byte]entry{}, authorize: authorize, allowPrivateHTTP: options.AllowPrivateHTTP,
		client:   &http.Client{Transport: transport(nil, 30*time.Second), CheckRedirect: noRedirect},
		custom:   &http.Client{Transport: transport(dialable, customHeaderTimeout), CheckRedirect: noRedirect},
		private:  &http.Client{Transport: transport(PrivateNetwork, customHeaderTimeout), CheckRedirect: noRedirect},
		upstream: map[string]string{OpenAI: "https://api.openai.com", ChatGPT: "https://api.openai.com", Anthropic: "https://api.anthropic.com"},
	}
}
func (g *Gateway) Issue(lease Lease) (string, error) {
	if API(lease.Provider) == "" {
		return "", errors.New("unsupported provider")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for hash, item := range g.leases {
		if !item.lease.Expires.After(now) {
			item.cancel()
			delete(g.leases, hash)
		}
	}
	if len(g.leases) >= 4096 {
		return "", errors.New("provider session limit reached")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	lease.Expires = now.Add(LeaseLifetime)
	ctx, cancel := context.WithDeadline(context.Background(), lease.Expires)
	g.leases[sha256.Sum256([]byte(token))] = entry{lease: lease, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 4)}
	return token, nil
}
func (g *Gateway) Revoke(connection, project, harness string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for hash, item := range g.leases {
		if (connection == "" || item.lease.Connection == connection) && (project == "" || item.lease.Project == project) && (harness == "" || item.lease.Harness == harness) {
			item.cancel()
			delete(g.leases, hash)
		}
	}
}
func (g *Gateway) Close() { g.Revoke("", "", "") }
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	g.mu.Lock()
	item, ok := g.leases[sha256.Sum256([]byte(token))]
	g.mu.Unlock()
	if !ok || !item.lease.Expires.After(time.Now()) || item.ctx.Err() != nil {
		gatewayError(w, "provider connection expired or revoked", http.StatusUnauthorized)
		return
	}
	select {
	case item.slots <- struct{}{}:
		defer func() { <-item.slots }()
	default:
		gatewayError(w, "too many active provider requests", http.StatusTooManyRequests)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		gatewayError(w, "provider gateway busy", http.StatusServiceUnavailable)
		return
	}
	provider, route, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/provider-gateway/"), "/")
	route = "/" + route
	api := API(provider)
	allowed := api == OpenAI && (route == "/v1/responses" || route == "/v1/chat/completions") || api == Anthropic && (route == "/v1/messages" || route == "/v1/messages/count_tokens")
	if provider == ChatGPT {
		// Plan usage covers only the Responses API (ADR 0036).
		allowed = route == "/v1/responses"
	}
	// Anthropic SDK beta clients mark requests with ?beta=true. It selects
	// nothing upstream, so it is accepted and not forwarded.
	query := r.URL.RawQuery == "" || api == Anthropic && r.URL.RawQuery == "beta=true"
	if !ok || provider != item.lease.Provider || !allowed || r.Method != http.MethodPost || !query {
		gatewayError(w, "unsupported provider operation", http.StatusNotFound)
		return
	}
	if len(r.Header) > 64 {
		gatewayError(w, "too many headers", http.StatusRequestHeaderFieldsTooLarge)
		return
	}
	resolved, err := g.authorize(r.Context(), item.lease)
	if errors.Is(err, ErrSignInRequired) {
		gatewayError(w, "ChatGPT sign-in expired; sign in to ChatGPT again in Meridian's Harness settings", http.StatusUnauthorized)
		return
	}
	if err != nil {
		gatewayError(w, "provider connection unavailable", http.StatusForbidden)
		return
	}
	target, client := g.upstream[provider], g.client
	if Custom(provider) {
		target, err = NormalizeBaseURL(resolved.BaseURL, resolved.AllowPrivateHTTP)
		if err != nil || resolved.AllowPrivateHTTP && !g.allowPrivateHTTP {
			gatewayError(w, "provider connection unavailable", http.StatusForbidden)
			return
		}
		client = g.custom
		if resolved.AllowPrivateHTTP {
			client = g.private
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	defer r.Body.Close()
	// ChatGPT requests are buffered: their shape is checked, and they can be
	// retried once with a refreshed credential.
	var buffered []byte
	if provider == ChatGPT {
		buffered, err = io.ReadAll(r.Body)
		if err != nil {
			gatewayError(w, "provider request too large", http.StatusRequestEntityTooLarge)
			return
		}
		if reason := chatGPTRequestProblem(buffered); reason != "" {
			gatewayError(w, reason, http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(item.ctx, cancel)
	defer stop()
	newRequest := func() (*http.Request, error) {
		var body io.Reader = r.Body
		if buffered != nil {
			body = bytes.NewReader(buffered)
		}
		return http.NewRequestWithContext(ctx, http.MethodPost, target+route, body)
	}
	upstream, err := newRequest()
	if err != nil {
		gatewayError(w, "invalid provider request", 400)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if api == OpenAI {
		if resolved.Key != "" {
			upstream.Header.Set("Authorization", "Bearer "+resolved.Key)
		}
	} else {
		if resolved.Key != "" {
			upstream.Header.Set("X-Api-Key", resolved.Key)
			// Self-hosted servers such as vLLM authenticate only bearer tokens,
			// including on their Anthropic Messages routes.
			if provider == AnthropicCompatible {
				upstream.Header.Set("Authorization", "Bearer "+resolved.Key)
			}
		}
		upstream.Header.Set("Anthropic-Version", "2023-06-01")
		if beta := r.Header.Get("Anthropic-Beta"); beta != "" && len(beta) <= 2048 {
			upstream.Header.Set("Anthropic-Beta", beta)
		}
	}
	response, err := client.Do(upstream)
	if err == nil && response.StatusCode == http.StatusUnauthorized && resolved.Refresh != nil && buffered != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		key, refreshErr := resolved.Refresh(ctx)
		if errors.Is(refreshErr, ErrSignInRequired) {
			gatewayError(w, "ChatGPT sign-in expired; sign in to ChatGPT again in Meridian's Harness settings", http.StatusUnauthorized)
			return
		}
		if refreshErr != nil {
			gatewayError(w, "provider connection unavailable", http.StatusForbidden)
			return
		}
		resolved.Key = key
		retry, buildErr := newRequest()
		if buildErr != nil {
			gatewayError(w, "invalid provider request", 400)
			return
		}
		retry.Header.Set("Content-Type", "application/json")
		retry.Header.Set("Authorization", "Bearer "+key)
		upstream = retry
		response, err = client.Do(upstream)
	}
	if err != nil {
		gatewayError(w, "provider request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		gatewayError(w, "upstream provider redirected the request", http.StatusBadGateway)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("upstream provider rejected the request (HTTP %d)", response.StatusCode)
		if reason := upstreamReason(response, resolved.Key, token, upstream.URL.Hostname()); reason != "" {
			message += ": " + reason
		}
		status := response.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		writeGatewayError(w, message, status, response.StatusCode)
		return
	}
	resolved.Key = ""
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.WriteHeader(response.StatusCode)
	reader := io.LimitReader(response.Body, 64<<20)
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func gatewayError(w http.ResponseWriter, message string, status int) {
	writeGatewayError(w, message, status, 0)
}

func writeGatewayError(w http.ResponseWriter, message string, status, upstreamStatus int) {
	body := map[string]any{"code": "provider_gateway_error", "message": message}
	if upstreamStatus != 0 {
		body["upstreamStatus"] = upstreamStatus
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

// chatGPTRequestProblem enforces OpenAI's plan-usage request shape: streamed,
// not stored, and without server-side continuation or background runs. It
// returns "" for an acceptable body. Only these fields are read; the body is
// forwarded unmodified.
func chatGPTRequestProblem(body []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return "provider request must be a JSON object"
	}
	literal := func(name string) string { return string(bytes.TrimSpace(fields[name])) }
	if literal("stream") != "true" {
		return "ChatGPT plan requests must set stream to true"
	}
	if literal("store") != "false" {
		return "ChatGPT plan requests must set store to false"
	}
	if value := literal("previous_response_id"); value != "" && value != "null" {
		return "ChatGPT plan requests cannot use previous_response_id"
	}
	if value := literal("background"); value != "" && value != "null" && value != "false" {
		return "ChatGPT plan requests cannot run in the background"
	}
	return ""
}
