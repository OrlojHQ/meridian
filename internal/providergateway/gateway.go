// Package providergateway exposes only bounded provider inference routes using
// revocable Capsule credentials. Upstream API keys never enter a Capsule.
package providergateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Lease struct {
	Connection, Project, Capsule, Harness, Provider string
	Expires                                         time.Time
}
type Authorize func(context.Context, Lease) (string, error)
type entry struct {
	slots  chan struct{}
	lease  Lease
	cancel context.CancelFunc
	ctx    context.Context
}
type Gateway struct {
	slots     chan struct{}
	mu        sync.Mutex
	leases    map[[32]byte]entry
	authorize Authorize
	client    *http.Client
	upstream  map[string]string
}

func New(authorize Authorize) *Gateway {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.MaxResponseHeaderBytes = 32 << 10
	return &Gateway{slots: make(chan struct{}, 64), leases: map[[32]byte]entry{}, authorize: authorize, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, upstream: map[string]string{"openai": "https://api.openai.com", "anthropic": "https://api.anthropic.com"}}
}
func (g *Gateway) Issue(lease Lease) (string, error) {
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
	lease.Expires = now.Add(24 * time.Hour)
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
	allowed := provider == "openai" && (route == "/v1/responses" || route == "/v1/chat/completions") || provider == "anthropic" && (route == "/v1/messages" || route == "/v1/messages/count_tokens")
	if !ok || provider != item.lease.Provider || !allowed || r.Method != http.MethodPost || r.URL.RawQuery != "" {
		gatewayError(w, "unsupported provider operation", http.StatusNotFound)
		return
	}
	if len(r.Header) > 64 {
		gatewayError(w, "too many headers", http.StatusRequestHeaderFieldsTooLarge)
		return
	}
	key, err := g.authorize(r.Context(), item.lease)
	if err != nil {
		gatewayError(w, "provider connection unavailable", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	defer r.Body.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	stop := context.AfterFunc(item.ctx, cancel)
	defer stop()
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, g.upstream[provider]+route, r.Body)
	if err != nil {
		gatewayError(w, "invalid provider request", 400)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if provider == "openai" {
		upstream.Header.Set("Authorization", "Bearer "+key)
	} else {
		upstream.Header.Set("X-Api-Key", key)
		upstream.Header.Set("Anthropic-Version", "2023-06-01")
		if beta := r.Header.Get("Anthropic-Beta"); len(beta) <= 2048 {
			upstream.Header.Set("Anthropic-Beta", beta)
		}
	}
	response, err := g.client.Do(upstream)
	key = ""
	if err != nil {
		gatewayError(w, "provider request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		gatewayError(w, "upstream provider rejected the request", response.StatusCode)
		return
	}
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "provider_gateway_error", "message": message}})
}
