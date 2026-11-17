// Package gitgateway forwards one approved Git push from a Capsule to the
// source host (ADR 0054). The Capsule authenticates with a one-time
// capability bound to the approved ref update. The gateway checks the
// receive-pack command, adds the source-host credential, and streams the rest
// of the push upstream without unpacking it. The credential never leaves the
// Station.
package gitgateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// Prefix is the route the gateway serves. Any other path is not its.
	Prefix = "/git-gateway/"
	// RepositoryPath is the one repository path a capability pushes to. The
	// upstream repository comes from the grant, never from the path.
	RepositoryPath = Prefix + "push.git"
	// Username is the HTTP Basic user a Capsule presents with a capability.
	Username = "meridian"

	// CapabilityLifetime is how long an issued capability can start a push.
	CapabilityLifetime = 2 * time.Minute
	// RequestTimeout bounds one forwarded request, including the pack upload.
	RequestTimeout = 10 * time.Minute
	// MaxPushBytes caps the receive-pack request body.
	MaxPushBytes = 1 << 30
	// MaxConcurrentPushes caps receive-pack requests in flight.
	MaxConcurrentPushes = 4
	// PostBufferBytes is the http.postBuffer a Capsule pushes with. A push
	// that fits is sent as one request with a length. A larger one makes Git
	// probe its credentials and send the pack chunked, which some Git and
	// libcurl versions truncate after the first packet.
	PostBufferBytes = 64 << 20

	maxCommandSection = 64 << 10
	maxAdvertisement  = 32 << 20
	maxReport         = 1 << 20

	receivePackRequest       = "application/x-git-receive-pack-request"
	receivePackResult        = "application/x-git-receive-pack-result"
	receivePackAdvertisement = "application/x-git-receive-pack-advertisement"
)

// Credential is the source-host credential the gateway adds upstream.
type Credential struct {
	Username string
	Password string
}

// Grant is one approved ref update. ExpectedOld is empty for a new branch.
type Grant struct {
	Upstream       string
	Credential     Credential
	SourceCommit   string
	DestinationRef string
	ExpectedOld    string
}

type issued struct {
	grant   Grant
	expires time.Time
	used    bool
}

// Gateway holds issued capabilities in memory. A restarted Station forgets
// them, which fails any push in flight closed.
type Gateway struct {
	client *http.Client
	now    func() time.Time
	slots  chan struct{}

	mu     sync.Mutex
	grants map[[32]byte]*issued
	closed bool
}

// New returns a gateway that reaches source hosts with client. A nil client
// uses a default transport. Redirects are never followed: a redirect would
// carry the credential to a host the grant did not name.
func New(client *http.Client) *Gateway {
	var forward http.Client
	if client != nil {
		forward = *client
	} else {
		forward.Transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	forward.Timeout = 0
	forward.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{
		client: &forward,
		now:    time.Now,
		slots:  make(chan struct{}, MaxConcurrentPushes),
		grants: map[[32]byte]*issued{},
	}
}

// Issue records grant and returns the capability that authorizes it, and a
// function that revokes it. The capability is valid once, for at most
// CapabilityLifetime before the push starts.
func (g *Gateway) Issue(grant Grant) (string, func(), error) {
	if err := validateGrant(grant); err != nil {
		return "", func() {}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", func() {}, err
	}
	capability := hex.EncodeToString(raw)
	key := sha256.Sum256([]byte(capability))
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return "", func() {}, errors.New("git gateway is closed")
	}
	g.grants[key] = &issued{grant: grant, expires: g.now().Add(CapabilityLifetime)}
	return capability, func() {
		g.mu.Lock()
		if item := g.grants[key]; item != nil {
			item.grant.Credential = Credential{}
			delete(g.grants, key)
		}
		g.mu.Unlock()
	}, nil
}

// Close revokes every capability.
func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for key, item := range g.grants {
		item.grant.Credential = Credential{}
		delete(g.grants, key)
	}
}

// RemoteRef returns the commit the source host has for ref, or "" when the
// ref does not exist. It reads the receive-pack advertisement, the same view
// a push would get.
func (g *Gateway) RemoteRef(ctx context.Context, upstream string, credential Credential, ref string) (string, error) {
	if err := validateUpstream(upstream); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	response, err := g.advertisement(ctx, upstream, credential, "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	return findRef(io.LimitReader(response.Body, maxAdvertisement), ref)
}

// ServeHTTP serves the two smart HTTP requests a push makes. Everything else
// is 404 before authentication.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == RepositoryPath+"/info/refs" &&
		r.URL.Query().Get("service") == "git-receive-pack":
		g.serveAdvertisement(w, r)
	case r.Method == http.MethodPost && r.URL.Path == RepositoryPath+"/git-receive-pack":
		g.serveReceivePack(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (g *Gateway) authenticate(w http.ResponseWriter, r *http.Request) (Grant, [32]byte, bool) {
	user, password, ok := r.BasicAuth()
	if !ok {
		// Git sends credentials only after a challenge.
		w.Header().Set("WWW-Authenticate", `Basic realm="Meridian"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return Grant{}, [32]byte{}, false
	}
	key := sha256.Sum256([]byte(password))
	g.mu.Lock()
	item := g.grants[key]
	valid := item != nil && subtle.ConstantTimeCompare([]byte(user), []byte(Username)) == 1 &&
		!item.used && g.now().Before(item.expires)
	var grant Grant
	if valid {
		grant = item.grant
	}
	g.mu.Unlock()
	if !valid {
		http.Error(w, "push capability expired, used, or unknown", http.StatusForbidden)
		return Grant{}, key, false
	}
	return grant, key, true
}

// consume marks a capability used. Only the first receive-pack request with
// a ref update gets it.
func (g *Gateway) consume(key [32]byte) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	item := g.grants[key]
	if item == nil || item.used || !g.now().Before(item.expires) {
		return false
	}
	item.used = true
	return true
}

func (g *Gateway) serveAdvertisement(w http.ResponseWriter, r *http.Request) {
	grant, _, ok := g.authenticate(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	response, err := g.advertisement(ctx, grant.Upstream, grant.Credential, r.UserAgent())
	if err != nil {
		upstreamError(w, err)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", receivePackAdvertisement)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxAdvertisement))
}

func (g *Gateway) serveReceivePack(w http.ResponseWriter, r *http.Request) {
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "git gateway busy", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Content-Type") != receivePackRequest {
		http.Error(w, "unexpected content type", http.StatusUnsupportedMediaType)
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		// An encoded body would be checked in one form and forwarded in another.
		http.Error(w, "encoded push bodies are not accepted", http.StatusUnsupportedMediaType)
		return
	}
	grant, key, ok := g.authenticate(w, r)
	if !ok {
		return
	}
	// The listener's read and write deadlines are short; a pack upload is not.
	controller := http.NewResponseController(w)
	deadline := g.now().Add(RequestTimeout)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	section, err := readCommandSection(r.Body)
	if err != nil {
		http.Error(w, "invalid push request", http.StatusBadRequest)
		return
	}
	if len(section.commands) == 0 && len(section.shallow) == 0 {
		// Before a large push Git sends a lone flush to check that its
		// credentials still work. Nothing is pushed, so the capability is
		// not used up.
		var probe [1]byte
		if n, _ := r.Body.Read(probe[:]); n == 0 {
			w.Header().Set("Content-Type", receivePackResult)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "invalid push request", http.StatusBadRequest)
		return
	}
	if err := checkCommand(section.commands, section.capabilities, grant); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if !g.consume(key) {
		http.Error(w, "push capability expired, used, or unknown", http.StatusForbidden)
		return
	}
	body := io.MultiReader(
		bytes.NewReader(section.raw),
		&limitedReader{reader: r.Body, remaining: MaxPushBytes - int64(len(section.raw))},
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, grant.Upstream+"/git-receive-pack", body)
	if err != nil {
		upstreamError(w, err)
		return
	}
	request.Header.Set("Content-Type", receivePackRequest)
	request.Header.Set("Accept", receivePackResult)
	request.Header.Set("User-Agent", gitUserAgent(r.UserAgent()))
	request.SetBasicAuth(grant.Credential.Username, grant.Credential.Password)
	response, err := g.client.Do(request)
	if err != nil {
		upstreamError(w, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != receivePackResult {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		upstreamError(w, fmt.Errorf("source host answered %d", response.StatusCode))
		return
	}
	w.Header().Set("Content-Type", receivePackResult)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxReport))
}

func (g *Gateway) advertisement(ctx context.Context, upstream string, credential Credential, agent string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		upstream+"/info/refs?service=git-receive-pack", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", gitUserAgent(agent))
	request.SetBasicAuth(credential.Username, credential.Password)
	response, err := g.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != receivePackAdvertisement {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		return nil, &StatusError{Status: response.StatusCode}
	}
	return response, nil
}

// StatusError is a source host's refusal, without its body.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("source host answered %d", e.Status)
}

func upstreamError(w http.ResponseWriter, err error) {
	var status *StatusError
	if errors.As(err, &status) && (status.Status == http.StatusUnauthorized ||
		status.Status == http.StatusForbidden || status.Status == http.StatusNotFound) {
		http.Error(w, "the source host refused the push", http.StatusForbidden)
		return
	}
	http.Error(w, "the source host could not be reached", http.StatusBadGateway)
}

var agentPattern = regexp.MustCompile(`^git/[0-9A-Za-z.\-_]{1,64}$`)

// gitUserAgent forwards the Capsule's Git version so the source host speaks
// the smart protocol, and nothing else from that header.
func gitUserAgent(value string) string {
	if agent, _, _ := strings.Cut(value, " "); agentPattern.MatchString(agent) {
		return agent
	}
	return "git/2.0"
}

func validateGrant(grant Grant) error {
	if err := validateUpstream(grant.Upstream); err != nil {
		return err
	}
	if grant.Credential.Password == "" || strings.ContainsAny(grant.Credential.Username+grant.Credential.Password, "\x00\r\n") {
		return errors.New("git gateway grant needs a credential")
	}
	if !validObjectID(grant.SourceCommit) {
		return errors.New("git gateway grant needs an exact source commit")
	}
	if grant.ExpectedOld != "" && (!validObjectID(grant.ExpectedOld) || len(grant.ExpectedOld) != len(grant.SourceCommit)) {
		return errors.New("git gateway grant has an invalid expected old commit")
	}
	if !strings.HasPrefix(grant.DestinationRef, "refs/heads/") || len(grant.DestinationRef) > 1024 ||
		strings.ContainsAny(grant.DestinationRef, " \x00\r\n\t") {
		return errors.New("git gateway grant needs a branch ref")
	}
	return nil
}

func validateUpstream(upstream string) error {
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.HasSuffix(upstream, "/") {
		return errors.New("git gateway upstream must be a credential-free HTTPS repository URL")
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

type limitedReader struct {
	reader    io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		var probe [1]byte
		if n, _ := l.reader.Read(probe[:]); n > 0 {
			return 0, errors.New("push exceeds the size limit")
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.reader.Read(p)
	l.remaining -= int64(n)
	return n, err
}
