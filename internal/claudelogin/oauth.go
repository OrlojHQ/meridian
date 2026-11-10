// Package claudelogin runs Claude Code's own OAuth login on the control plane.
//
// The public client, authorize URL, and token URL are the ones Claude Code
// uses. Meridian holds the PKCE verifier and exchanges the authorization code.
// Access and refresh tokens are returned to the caller to seal; this package
// does not store them, log them, or follow token-endpoint redirects.
package claudelogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ClientID is Claude Code's public OAuth client. The credential it mints
	// is the one Claude Code consumes.
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// AuthorizeURL is Claude's browser sign-in page.
	AuthorizeURL = "https://claude.ai/oauth/authorize"
	// TokenURL exchanges an authorization code and refreshes an access token.
	TokenURL = "https://platform.claude.com/v1/oauth/token"
	// ManualRedirectURI is the code-display callback Claude Code uses when the
	// browser cannot return to loopback. The page shows an authorization code.
	ManualRedirectURI = "https://platform.claude.com/oauth/code/callback"
	// Scopes are the set Claude Code's own account login carries. A setup
	// token from `claude setup-token` is narrower (user:inference only) and
	// is not what this ceremony requests.
	Scopes = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"

	userAgent   = "claude-cli/2.1.319 (external, cli)"
	oauthBeta   = "oauth-2025-04-20"
	refreshSkew = time.Minute
	maxTokenLen = 1900
)

// Credential is an OAuth login. Formatting it never prints the tokens.
type Credential struct {
	access  string
	refresh string
	expires time.Time
}

// Format redacts both tokens for every fmt verb.
func (c Credential) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "claudelogin.Credential{}")
}

// AccessToken is the bearer the gateway attaches. It is not the refresh token.
func (c Credential) AccessToken() string { return c.access }

// NeedsRefresh reports whether the access token should be renewed before use.
// A credential with no expiry is treated as due, so an undated record cannot
// be sent forever.
func (c Credential) NeedsRefresh(now time.Time) bool {
	return c.expires.IsZero() || !now.Add(refreshSkew).Before(c.expires)
}

type storedCredential struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

// Stored is the JSON sealed under the installation KEK. The caller wipes it.
func (c Credential) Stored() ([]byte, error) {
	if !validToken(c.access) || !validToken(c.refresh) || c.expires.IsZero() {
		return nil, errors.New("claude credential is incomplete")
	}
	raw, err := json.Marshal(storedCredential{
		AccessToken:  c.access,
		RefreshToken: c.refresh,
		ExpiresAt:    c.expires.UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	if len(raw) > 4096 {
		return nil, errors.New("claude credential is too long")
	}
	return raw, nil
}

// ParseStored reads a sealed OAuth record. A raw setup token is not JSON and
// is rejected here; the caller keeps sending that string unchanged.
func ParseStored(raw []byte) (Credential, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var stored storedCredential
	if err := decoder.Decode(&stored); err != nil {
		return Credential{}, errors.New("claude credential is unreadable")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Credential{}, errors.New("claude credential is unreadable")
	}
	cred := Credential{
		access:  stored.AccessToken,
		refresh: stored.RefreshToken,
		expires: time.UnixMilli(stored.ExpiresAt),
	}
	if !validToken(cred.access) || !validToken(cred.refresh) || stored.ExpiresAt <= 0 {
		return Credential{}, errors.New("claude credential is incomplete")
	}
	return cred, nil
}

// Challenge is the browser half of a sign-in. Verifier stays on the daemon.
type Challenge struct {
	Verifier string
	State    string
	URL      string
}

// Begin builds a PKCE sign-in. manual uses Claude's code-display page because
// the browser cannot be redirected back to this daemon. Otherwise redirectURI
// is the daemon's loopback callback, matching Claude Code's own login.
func Begin(redirectURI string, manual bool) (Challenge, error) {
	if manual {
		redirectURI = ManualRedirectURI
	} else if err := validateLoopbackRedirect(redirectURI); err != nil {
		return Challenge{}, err
	}
	verifier, err := randomToken(32)
	if err != nil {
		return Challenge{}, err
	}
	state, err := randomToken(32)
	if err != nil {
		return Challenge{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {Scopes},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	if manual {
		query.Set("code", "true")
	}
	return Challenge{
		Verifier: verifier,
		State:    state,
		URL:      AuthorizeURL + "?" + strings.ReplaceAll(query.Encode(), "+", "%20"),
	}, nil
}

// Exchange trades an authorization code for a credential. The error never
// includes the code, the verifier, or the response body.
func Exchange(ctx context.Context, tokenURL, redirectURI, verifier, code, state string, now time.Time) (Credential, error) {
	if !validSecret(code) || !validSecret(state) || !validSecret(verifier) {
		return Credential{}, errors.New("claude sign-in code was rejected")
	}
	body, err := postToken(ctx, tokenURL, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     ClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}, false)
	if err != nil {
		return Credential{}, err
	}
	return parseToken(body, "", now)
}

// Refresh trades a stored refresh token for a new access token. An omitted
// refresh token in the response keeps the previous one. A present but empty
// refresh token is a refusal.
func Refresh(ctx context.Context, tokenURL string, current Credential, now time.Time) (Credential, error) {
	if !validToken(current.refresh) {
		return Credential{}, errors.New("claude login cannot be renewed")
	}
	body, err := postToken(ctx, tokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     ClientID,
		"refresh_token": current.refresh,
	}, true)
	if err != nil {
		return Credential{}, err
	}
	return parseToken(body, current.refresh, now)
}

func parseToken(body []byte, previousRefresh string, now time.Time) (Credential, error) {
	// Extra fields (scope, expires_at) are ignored. A reflected token in an
	// unknown field is not copied into the error. A present refresh_token
	// that is null or empty is a refusal; only an omitted one keeps the
	// previous refresh token.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Credential{}, errors.New("claude token response was unreadable")
	}
	var access, tokenType string
	var expiresIn int64
	if err := json.Unmarshal(envelope["access_token"], &access); err != nil {
		return Credential{}, errors.New("claude token response was unreadable")
	}
	if raw, ok := envelope["token_type"]; ok {
		if err := json.Unmarshal(raw, &tokenType); err != nil || (tokenType != "" && !strings.EqualFold(tokenType, "Bearer")) {
			return Credential{}, errors.New("claude token response was unreadable")
		}
	}
	if err := json.Unmarshal(envelope["expires_in"], &expiresIn); err != nil || expiresIn < 60 || expiresIn > 366*24*60*60 {
		return Credential{}, errors.New("claude token response was unreadable")
	}
	refresh := previousRefresh
	if raw, ok := envelope["refresh_token"]; ok {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || !validToken(value) {
			return Credential{}, errors.New("claude token response was unreadable")
		}
		refresh = value
	}
	cred := Credential{
		access:  access,
		refresh: refresh,
		expires: now.Add(time.Duration(expiresIn) * time.Second),
	}
	if !validToken(cred.access) || !validToken(cred.refresh) {
		return Credential{}, errors.New("claude token response was unreadable")
	}
	return cred, nil
}

func postToken(ctx context.Context, endpoint string, payload map[string]string, refresh bool) ([]byte, error) {
	if err := AllowedEndpoint(endpoint); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	if refresh {
		request.Header.Set("Anthropic-Beta", oauthBeta)
	}
	response, err := tokenClient.Do(request)
	if err != nil {
		return nil, errors.New("claude token endpoint was unreachable")
	}
	defer response.Body.Close()
	// Discard the body on failure so a reflected code or token cannot land in
	// an error string. Do not follow redirects: the code and refresh token
	// are in this request.
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, fmt.Errorf("claude token endpoint returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		return nil, errors.New("claude token response was unreadable")
	}
	return body, nil
}

// AllowedEndpoint is the official token URL, or an HTTP loopback URL used by tests.
func AllowedEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid claude token endpoint")
	}
	if parsed.Scheme == "https" && parsed.Host == "platform.claude.com" && parsed.Path == "/v1/oauth/token" {
		return nil
	}
	if parsed.Scheme == "http" && loopback(parsed.Hostname()) {
		return nil
	}
	return errors.New("invalid claude token endpoint")
}

func validateLoopbackRedirect(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/callback" {
		return errors.New("invalid claude sign-in redirect")
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "localhost" {
		return errors.New("invalid claude sign-in redirect")
	}
	port := parsed.Port()
	if port == "" || len(port) > 5 {
		return errors.New("invalid claude sign-in redirect")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("invalid claude sign-in redirect")
		}
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func validToken(value string) bool {
	if len(value) < 8 || len(value) > maxTokenLen {
		return false
	}
	return validSecret(value)
}

func validSecret(value string) bool {
	if value == "" || len(value) > 2048 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-._~+/=", r):
		default:
			return false
		}
	}
	return true
}

var tokenClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
