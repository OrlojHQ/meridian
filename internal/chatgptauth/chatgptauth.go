// Package chatgptauth implements the client side of OpenAI's Sign in with
// ChatGPT plan usage flow for open-source and self-hosted apps: per-host
// dynamic registration, authorization code with PKCE, ID token checks,
// refresh, and revocation (ADR 0036).
//
// Tokens returned by this package are credentials. Callers keep them on the
// control plane, seal them at rest, and never log them or put them in URLs.
package chatgptauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultAuthBase is OpenAI's authorization server.
	DefaultAuthBase = "https://auth.openai.com"
	// Resource is the API audience requested for plan inference.
	Resource = "https://api.openai.com/v1"
	// RedirectURI is the loopback callback OpenAI requires. Only the port may
	// change on reauthorization; Meridian keeps it fixed.
	RedirectURI = "http://127.0.0.1:1455/auth/callback"
	// CallbackAddress is the loopback listener address for RedirectURI.
	CallbackAddress = "127.0.0.1:1455"
	// CallbackPath is RedirectURI's path.
	CallbackPath = "/auth/callback"
	// DynamicClient is the client ID for first registration on a host.
	DynamicClient = "dynamic_agent_client"
	// PlanScope must be granted before any plan inference.
	PlanScope = "chatgpt.tokens.use.direct"
	// AgentName is shown to the user on OpenAI's consent screen.
	AgentName = "Meridian"

	scopes       = "openid profile email offline_access resource.invoke " + PlanScope
	maxBody      = 64 << 10
	maxToken     = 16 << 10
	clockSkew    = time.Minute
	jwksLifetime = time.Hour
)

var (
	// ErrInvalidGrant means the code or refresh token is no longer valid and
	// the user must sign in again.
	ErrInvalidGrant = errors.New("ChatGPT sign-in is no longer valid")
	// ErrDenied means the user declined consent.
	ErrDenied = errors.New("ChatGPT sign-in was declined")
)

// Client talks to OpenAI's authorization server.
type Client struct {
	AuthBase string
	HTTP     *http.Client
	Now      func() time.Time

	mu        sync.Mutex
	discovery *discovery
	keys      map[string]*rsa.PublicKey
	keysAt    time.Time
}

// New returns a Client for OpenAI's production authorization server.
func New() *Client {
	return &Client{
		AuthBase: DefaultAuthBase,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		Now: time.Now,
	}
}

// Attempt holds the per-attempt secrets. Verifier never leaves the daemon.
type Attempt struct {
	State    string
	Nonce    string
	Verifier string
	ClientID string
}

// NewAttempt creates fresh state, nonce, and PKCE verifier values. clientID
// is the issued client ID for reauthorization, or empty for registration.
func NewAttempt(clientID string) (Attempt, error) {
	values := make([]string, 3)
	for index := range values {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return Attempt{}, err
		}
		values[index] = base64.RawURLEncoding.EncodeToString(raw)
	}
	if clientID == "" {
		clientID = DynamicClient
	}
	return Attempt{State: values[0], Nonce: values[1], Verifier: values[2], ClientID: clientID}, nil
}

// AuthorizationURL is opened in the user's browser. hostID is the
// installation's stable ext_agent_host_id, or empty to omit it: OpenAI's
// reference client sends it only where the authorization server supports
// it. loginHint is optional.
func (c *Client) AuthorizationURL(attempt Attempt, hostID, loginHint string) string {
	challenge := sha256.Sum256([]byte(attempt.Verifier))
	query := url.Values{
		"client_id":             {attempt.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {scopes},
		"resource":              {Resource},
		"state":                 {attempt.State},
		"nonce":                 {attempt.Nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	if hostID != "" {
		query.Set("ext_agent_host_id", hostID)
	}
	if attempt.ClientID == DynamicClient {
		query.Set("agent_name_hint", AgentName)
	}
	if loginHint != "" {
		query.Set("login_hint", loginHint)
	}
	return strings.TrimRight(c.AuthBase, "/") + "/api/accounts/authorize?" + query.Encode()
}

// Callback is the parsed loopback redirect.
type Callback struct {
	Code, State, Scope, ClientID, Error string
}

// ParseCallback accepts the full redirected URL (as the browser shows it) or
// only its query string, and checks that it targets RedirectURI's path.
func ParseCallback(raw string) (Callback, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > 8192 {
		return Callback{}, errors.New("callback address is empty or too long")
	}
	var query url.Values
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Path != CallbackPath {
			return Callback{}, errors.New("paste the full address of the page ChatGPT redirected to")
		}
		query = parsed.Query()
	} else {
		parsed, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err != nil {
			return Callback{}, errors.New("paste the full address of the page ChatGPT redirected to")
		}
		query = parsed
	}
	callback := Callback{
		Code: query.Get("code"), State: query.Get("state"), Scope: query.Get("scope"),
		ClientID: query.Get("client_id"), Error: query.Get("error"),
	}
	if callback.State == "" || callback.Code == "" && callback.Error == "" {
		return Callback{}, errors.New("the address has no sign-in result")
	}
	return callback, nil
}

// TokenSet is everything Meridian keeps for one ChatGPT connection.
type TokenSet struct {
	ClientID     string    `json:"clientId"`
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Scopes       []string  `json:"scopes,omitempty"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email,omitempty"`
}

// Identity is the validated ID token's account.
type Identity struct {
	Subject string
	Email   string
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
	Message      string `json:"message"`
	Detail       any    `json:"detail"`
}

// Complete validates a callback against its attempt, exchanges the code, and
// verifies the ID token. It returns a complete TokenSet.
func (c *Client) Complete(ctx context.Context, attempt Attempt, callback Callback) (TokenSet, error) {
	if !constantEqual(callback.State, attempt.State) {
		return TokenSet{}, errors.New("sign-in result does not match this attempt")
	}
	if callback.Error == "access_denied" {
		return TokenSet{}, ErrDenied
	}
	if callback.Error != "" {
		return TokenSet{}, fmt.Errorf("ChatGPT sign-in failed: %s", boundedCode(callback.Error))
	}
	clientID := attempt.ClientID
	if clientID == DynamicClient {
		if !strings.HasPrefix(callback.ClientID, "oaiapp_") || len(callback.ClientID) > 256 {
			return TokenSet{}, errors.New("ChatGPT registration is incomplete; sign in again")
		}
		clientID = callback.ClientID
	} else if callback.ClientID != "" && callback.ClientID != clientID {
		return TokenSet{}, errors.New("ChatGPT returned a different app registration; sign in again")
	}
	if callback.Scope != "" && !slices.Contains(strings.Fields(callback.Scope), PlanScope) {
		return TokenSet{}, errors.New("ChatGPT plan usage was not granted")
	}
	response, err := c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {callback.Code},
		"code_verifier": {attempt.Verifier},
		"redirect_uri":  {RedirectURI},
		"resource":      {Resource},
	})
	if err != nil {
		return TokenSet{}, err
	}
	if response.IDToken == "" || response.RefreshToken == "" {
		return TokenSet{}, errors.New("ChatGPT did not return a complete sign-in")
	}
	identity, err := c.VerifyIDToken(ctx, response.IDToken, clientID, attempt.Nonce)
	if err != nil {
		return TokenSet{}, err
	}
	set, err := c.tokenSet(clientID, response, callback.Scope)
	if err != nil {
		return TokenSet{}, err
	}
	set.Subject, set.Email = identity.Subject, identity.Email
	return set, nil
}

// Refresh returns a replacement token set. The caller must store the rotated
// refresh token and serialize refreshes for one connection.
func (c *Client) Refresh(ctx context.Context, current TokenSet) (TokenSet, error) {
	response, err := c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {current.ClientID},
		"refresh_token": {current.RefreshToken},
		"resource":      {Resource},
	})
	if err != nil {
		return TokenSet{}, err
	}
	if response.RefreshToken == "" {
		response.RefreshToken = current.RefreshToken
	}
	set, err := c.tokenSet(current.ClientID, response, "")
	if err != nil {
		return TokenSet{}, err
	}
	if len(set.Scopes) == 0 {
		set.Scopes = current.Scopes
	}
	set.Subject, set.Email = current.Subject, current.Email
	return set, nil
}

// Revoke revokes a refresh token. It returns nil only when OpenAI confirmed
// revocation, including for an already-invalid token.
func (c *Client) Revoke(ctx context.Context, set TokenSet) error {
	meta, err := c.metadata(ctx)
	if err != nil {
		return err
	}
	body := url.Values{"token": {set.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {set.ClientID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RevocationEndpoint, strings.NewReader(body.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return errors.New("ChatGPT revocation request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBody))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ChatGPT revocation failed (HTTP %d)", response.StatusCode)
	}
	return nil
}

func (c *Client) tokenSet(clientID string, response tokenResponse, grantedScope string) (TokenSet, error) {
	if response.AccessToken == "" || len(response.AccessToken) > maxToken || len(response.RefreshToken) > maxToken ||
		strings.ContainsAny(response.AccessToken+response.RefreshToken, "\r\n\x00 ") {
		return TokenSet{}, errors.New("ChatGPT returned an invalid token")
	}
	expires := response.ExpiresIn
	if expires <= 0 || expires > 30*24*3600 {
		expires = 3600
	}
	scope := response.Scope
	if scope == "" {
		scope = grantedScope
	}
	fields := strings.Fields(scope)
	if scope != "" && !slices.Contains(fields, PlanScope) {
		return TokenSet{}, errors.New("ChatGPT plan usage was not granted")
	}
	return TokenSet{
		ClientID: clientID, AccessToken: response.AccessToken, RefreshToken: response.RefreshToken,
		ExpiresAt: c.Now().Add(time.Duration(expires) * time.Second).UTC(), Scopes: fields,
	}, nil
}

func (c *Client) token(ctx context.Context, form url.Values) (tokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.AuthBase, "/")+"/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return tokenResponse{}, errors.New("ChatGPT token request failed")
	}
	defer response.Body.Close()
	var decoded tokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxBody)).Decode(&decoded); err != nil {
		return tokenResponse{}, fmt.Errorf("ChatGPT token request failed (HTTP %d)", response.StatusCode)
	}
	text := decoded.Description
	if text == "" {
		text = decoded.Message
	}
	if value, ok := decoded.Detail.(string); ok && text == "" {
		text = value
	}
	detail := boundedDescription(text)
	// OpenAI's request ID lets support trace a refused grant.
	if id := boundedCode(response.Header.Get("X-Request-Id")); id != "" {
		if detail != "" {
			detail += "; "
		}
		detail += "request " + id
	}
	if decoded.Error == "invalid_grant" {
		if detail != "" {
			return tokenResponse{}, fmt.Errorf("%w: %s", ErrInvalidGrant, detail)
		}
		return tokenResponse{}, ErrInvalidGrant
	}
	if response.StatusCode != http.StatusOK || decoded.Error != "" {
		message := fmt.Sprintf("ChatGPT token request failed (HTTP %d %s)", response.StatusCode, boundedCode(decoded.Error))
		if detail != "" {
			message += ": " + detail
		}
		return tokenResponse{}, errors.New(message)
	}
	return decoded, nil
}

type discovery struct {
	Issuer             string `json:"issuer"`
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}

// metadata reads OpenID configuration once. Every advertised endpoint must
// be HTTPS on the configured authorization host.
func (c *Client) metadata(ctx context.Context) (*discovery, error) {
	c.mu.Lock()
	cached := c.discovery
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	base, err := url.Parse(strings.TrimRight(c.AuthBase, "/"))
	if err != nil {
		return nil, err
	}
	var meta discovery
	if err := c.getJSON(ctx, base.String()+"/.well-known/openid-configuration", &meta); err != nil {
		return nil, err
	}
	for _, endpoint := range []string{meta.JWKSURI, meta.RevocationEndpoint} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host != base.Host {
			return nil, errors.New("ChatGPT sign-in configuration is invalid")
		}
	}
	if meta.Issuer == "" {
		return nil, errors.New("ChatGPT sign-in configuration is invalid")
	}
	c.mu.Lock()
	c.discovery = &meta
	c.mu.Unlock()
	return &meta, nil
}

func (c *Client) getJSON(ctx context.Context, target string, value any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return errors.New("ChatGPT sign-in configuration is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("ChatGPT sign-in configuration is unavailable (HTTP %d)", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxBody)).Decode(value)
}

// VerifyIDToken checks an RS256 ID token's signature against OpenAI's JWKS
// and its issuer, audience, expiry, and nonce.
func (c *Client) VerifyIDToken(ctx context.Context, raw, clientID, nonce string) (Identity, error) {
	invalid := errors.New("ChatGPT returned an invalid ID token")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || len(raw) > maxToken {
		return Identity{}, invalid
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if decodeSegment(parts[0], &header) != nil || header.Alg != "RS256" {
		return Identity{}, invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, invalid
	}
	meta, err := c.metadata(ctx)
	if err != nil {
		return Identity{}, err
	}
	key, err := c.key(ctx, meta, header.Kid)
	if err != nil {
		return Identity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return Identity{}, invalid
	}
	var claims struct {
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		Expires  float64         `json:"exp"`
		Nonce    string          `json:"nonce"`
		Subject  string          `json:"sub"`
		Email    string          `json:"email"`
	}
	if decodeSegment(parts[1], &claims) != nil {
		return Identity{}, invalid
	}
	var audiences []string
	var single string
	if json.Unmarshal(claims.Audience, &single) == nil {
		audiences = []string{single}
	} else if json.Unmarshal(claims.Audience, &audiences) != nil {
		return Identity{}, invalid
	}
	now := c.Now()
	switch {
	case claims.Issuer != meta.Issuer,
		!slices.Contains(audiences, clientID),
		!now.Before(time.Unix(int64(claims.Expires), 0).Add(clockSkew)),
		!constantEqual(claims.Nonce, nonce),
		claims.Subject == "" || len(claims.Subject) > 256:
		return Identity{}, invalid
	}
	email := claims.Email
	if len(email) > 320 || strings.ContainsAny(email, "\r\n\x00<>") {
		email = ""
	}
	return Identity{Subject: claims.Subject, Email: email}, nil
}

func (c *Client) key(ctx context.Context, meta *discovery, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	key, fresh := c.keys[kid], c.Now().Sub(c.keysAt) < jwksLifetime
	c.mu.Unlock()
	if key != nil && fresh {
		return key, nil
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, meta.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, item := range set.Keys {
		if item.Kty != "RSA" || item.Use != "" && item.Use != "sig" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(item.N)
		e, errE := base64.RawURLEncoding.DecodeString(item.E)
		if errN != nil || errE != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
			continue
		}
		exponent := int(new(big.Int).SetBytes(e).Int64())
		if exponent < 3 {
			continue
		}
		keys[item.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
	}
	c.mu.Lock()
	c.keys, c.keysAt = keys, c.Now()
	c.mu.Unlock()
	if keys[kid] == nil {
		return nil, errors.New("ChatGPT ID token signing key is unknown")
	}
	return keys[kid], nil
}

func decodeSegment(segment string, value any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

func constantEqual(a, b string) bool {
	if len(a) != len(b) || a == "" {
		return false
	}
	var diff byte
	for index := range len(a) {
		diff |= a[index] ^ b[index]
	}
	return diff == 0
}

// boundedDescription keeps an OAuth error_description readable for the
// operator: letters, digits, spaces, and common punctuation, at most 200
// characters. Anything token-shaped is dropped by the character filter's
// length cap and by removing words longer than 40 characters.
func boundedDescription(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			strings.ContainsRune(" .,:;'()-_/", r):
			builder.WriteRune(r)
		case r == '\n' || r == '\t':
			builder.WriteRune(' ')
		}
	}
	words := strings.Fields(builder.String())
	kept := words[:0]
	for _, word := range words {
		if len(word) <= 40 {
			kept = append(kept, word)
		}
	}
	result := strings.Join(kept, " ")
	if len(result) > 200 {
		result = result[:200]
	}
	return result
}

// boundedCode keeps an OAuth error code printable and short; it is shown to
// the operator and must not echo arbitrary upstream text.
func boundedCode(code string) string {
	var builder strings.Builder
	for _, r := range code {
		if builder.Len() >= 64 {
			break
		}
		if r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
