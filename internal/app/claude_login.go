package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/claudelogin"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/providergateway"
)

const claudeLoginTTL = 10 * time.Minute

const (
	claudePending    = "pending"
	claudeExchanging = "exchanging"
	claudeComplete   = "complete"
	claudeFailed     = "failed"
)

// ClaudeLoginStart is the browser half of a sign-in. The PKCE verifier is not
// in this value and is never written to the API or the database.
type ClaudeLoginStart struct {
	LoginID      string    `json:"loginId"`
	AuthorizeURL string    `json:"authorizeUrl"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Manual       bool      `json:"manual"`
}

// ClaudeLoginStatus is safe to show in the UI. It never includes a token or
// an authorization code.
type ClaudeLoginStatus struct {
	Status       string `json:"status"`
	ConnectionID string `json:"connectionId,omitempty"`
}

type claudeCeremony struct {
	id           string
	state        string
	verifier     string
	redirect     string
	connectionID string
	expires      time.Time
	status       string
	resultID     string
	// starter is who began the ceremony. The connection it writes is
	// written as starter, including from the unauthenticated callback.
	starter principal.Principal
}

// BeginClaudeLogin starts a Claude account sign-in. host is the daemon host
// the operator's browser used. A loopback host gets a localhost callback on
// that port, which is the redirect Claude Code's own login uses. Any other
// host uses Claude's code-display page, and the operator submits that code
// back to Meridian. The verifier stays in daemon memory until it expires.
func (s *Service) BeginClaudeLogin(ctx context.Context, host, connectionID string) (ClaudeLoginStart, error) {
	starter, err := principal.Require(ctx)
	if err != nil {
		return ClaudeLoginStart{}, err
	}
	if s.secretKey == nil || s.gateway == nil {
		return ClaudeLoginStart{}, domain.ErrUnsupported
	}
	if connectionID != "" {
		if err := s.requireOfficialAnthropic(ctx, connectionID); err != nil {
			return ClaudeLoginStart{}, err
		}
	}
	redirect, manual := claudeCallbackRedirect(host)
	challenge, err := claudelogin.Begin(redirect, manual)
	if err != nil {
		return ClaudeLoginStart{}, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	loginID, err := claudeLoginID()
	if err != nil {
		return ClaudeLoginStart{}, err
	}
	now := s.clock.Now()
	item := &claudeCeremony{
		id: loginID, state: challenge.State, verifier: challenge.Verifier,
		redirect: redirect, connectionID: connectionID,
		expires: now.Add(claudeLoginTTL), status: claudePending,
		starter: starter,
	}
	if manual {
		item.redirect = claudelogin.ManualRedirectURI
	}
	s.claudeMu.Lock()
	defer s.claudeMu.Unlock()
	s.sweepClaude(now)
	if s.countClaudeActive() >= 8 {
		return ClaudeLoginStart{}, fmt.Errorf("%w: too many Claude sign-ins are waiting", domain.ErrConflict)
	}
	if s.claudePending == nil {
		s.claudePending = map[string]*claudeCeremony{}
		s.claudeByState = map[string]string{}
	}
	s.claudePending[loginID] = item
	s.claudeByState[item.state] = loginID
	return ClaudeLoginStart{LoginID: loginID, AuthorizeURL: challenge.URL, ExpiresAt: item.expires, Manual: manual}, nil
}

// CompleteClaudeLogin exchanges a code the operator submitted. The code may
// be `code#state`, which is what Claude's code page shows. The code is not
// stored.
func (s *Service) CompleteClaudeLogin(ctx context.Context, loginID, pasted string) (ClaudeLoginStatus, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ClaudeLoginStatus{}, err
	}
	code, state, err := splitClaudeCode(pasted)
	if err != nil {
		return ClaudeLoginStatus{}, err
	}
	return s.finishClaudeLogin(ctx, loginID, state, code, false)
}

// CompleteClaudeCallback exchanges the code Claude redirected to loopback.
// It is safe to call without an installation session or a principal: the
// single-use state and the in-memory verifier are what authorize it, and the
// connection is written as the principal that began the sign-in.
func (s *Service) CompleteClaudeCallback(ctx context.Context, state, code string) error {
	if _, _, err := splitClaudeCode(code); err != nil {
		return err
	}
	_, err := s.finishClaudeLogin(ctx, "", state, code, true)
	return err
}

// ClaudeLoginStatus reports a sign-in the installation started. Pending,
// complete, and failed are the only results.
func (s *Service) ClaudeLoginStatus(ctx context.Context, loginID string) (ClaudeLoginStatus, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ClaudeLoginStatus{}, err
	}
	s.claudeMu.Lock()
	defer s.claudeMu.Unlock()
	s.sweepClaude(s.clock.Now())
	item := s.claudePending[loginID]
	if item == nil || !startedBy(ctx, item.starter) {
		return ClaudeLoginStatus{}, domain.ErrNotFound
	}
	return item.publicStatus(), nil
}

// SetClaudeTokenURL points token exchange at a loopback test server, or back
// at Claude's official token URL. It is not an operator setting.
func (s *Service) SetClaudeTokenURL(raw string) error {
	if err := claudelogin.AllowedEndpoint(raw); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	s.claudeTokenURL = raw
	return nil
}

func (s *Service) finishClaudeLogin(ctx context.Context, loginID, state, code string, callback bool) (ClaudeLoginStatus, error) {
	claimed, done, err := s.claimClaude(ctx, loginID, state, callback)
	if done || err != nil {
		if done {
			return claimed.publicStatus(), nil
		}
		return ClaudeLoginStatus{}, err
	}
	ctx = principal.With(ctx, claimed.starter)
	cred, err := claudelogin.Exchange(ctx, s.claudeTokenEndpoint(), claimed.redirect, claimed.verifier, code, claimed.state, s.clock.Now())
	claimed.verifier = ""
	if err != nil {
		s.finishClaude(claimed.id, claudeFailed, "")
		return ClaudeLoginStatus{}, fmt.Errorf("%w: Claude sign-in failed", domain.ErrInvalid)
	}
	saved, err := s.saveClaudeCredential(ctx, claimed.connectionID, cred)
	if err != nil {
		s.finishClaude(claimed.id, claudeFailed, "")
		return ClaudeLoginStatus{}, err
	}
	s.finishClaude(claimed.id, claudeComplete, saved.ID)
	return ClaudeLoginStatus{Status: claudeComplete, ConnectionID: saved.ID}, nil
}

// claimClaude finds the ceremony a completion names. The callback is
// authorized by its single-use state alone; a code submission must also
// come from the user who began the sign-in.
func (s *Service) claimClaude(ctx context.Context, loginID, state string, callback bool) (claudeCeremony, bool, error) {
	s.claudeMu.Lock()
	defer s.claudeMu.Unlock()
	now := s.clock.Now()
	s.sweepClaude(now)
	var item *claudeCeremony
	if callback {
		item = s.claudePending[s.claudeByState[state]]
		if item == nil || subtle.ConstantTimeCompare([]byte(item.state), []byte(state)) != 1 {
			return claudeCeremony{}, false, domain.ErrNotFound
		}
	} else {
		item = s.claudePending[loginID]
		if item == nil || !startedBy(ctx, item.starter) {
			return claudeCeremony{}, false, domain.ErrNotFound
		}
		if state != "" && subtle.ConstantTimeCompare([]byte(item.state), []byte(state)) != 1 {
			item.status = claudeFailed
			item.verifier = ""
			return claudeCeremony{}, false, fmt.Errorf("%w: that code is for a different Claude sign-in", domain.ErrInvalid)
		}
	}
	switch item.status {
	case claudeComplete:
		return *item, true, nil
	case claudeFailed:
		return claudeCeremony{}, false, fmt.Errorf("%w: Claude sign-in failed", domain.ErrInvalid)
	case claudeExchanging:
		return claudeCeremony{}, false, fmt.Errorf("%w: Claude sign-in is already finishing", domain.ErrConflict)
	}
	if !now.Before(item.expires) || item.verifier == "" {
		item.status = claudeFailed
		item.verifier = ""
		return claudeCeremony{}, false, fmt.Errorf("%w: Claude sign-in expired", domain.ErrInvalid)
	}
	claimed := *item
	item.verifier = ""
	item.status = claudeExchanging
	return claimed, false, nil
}

func (s *Service) finishClaude(loginID, status, connectionID string) {
	s.claudeMu.Lock()
	defer s.claudeMu.Unlock()
	item := s.claudePending[loginID]
	if item == nil {
		return
	}
	item.status = status
	item.resultID = connectionID
	item.verifier = ""
}

func (item *claudeCeremony) publicStatus() ClaudeLoginStatus {
	status := item.status
	if status == claudeExchanging {
		status = claudePending
	}
	result := ClaudeLoginStatus{Status: status}
	if status == claudeComplete {
		result.ConnectionID = item.resultID
	}
	return result
}

func (s *Service) sweepClaude(now time.Time) {
	for id, item := range s.claudePending {
		if item.status == claudePending && !now.Before(item.expires) {
			item.status = claudeFailed
			item.verifier = ""
		}
		if !now.Before(item.expires.Add(claudeLoginTTL)) && item.status != claudeExchanging {
			delete(s.claudePending, id)
			delete(s.claudeByState, item.state)
		}
	}
}

func (s *Service) countClaudeActive() int {
	count := 0
	for _, item := range s.claudePending {
		if item.status == claudePending || item.status == claudeExchanging {
			count++
		}
	}
	return count
}

func (s *Service) requireOfficialAnthropic(ctx context.Context, id string) error {
	return s.store.View(ctx, func(r ports.Reader) error {
		item, err := r.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if item.Provider != providergateway.Anthropic {
			return fmt.Errorf("%w: Claude sign-in only replaces an official Anthropic connection", domain.ErrInvalid)
		}
		return nil
	})
}

func (s *Service) saveClaudeCredential(ctx context.Context, connectionID string, cred claudelogin.Credential) (domain.ProviderConnection, error) {
	payload, err := cred.Stored()
	if err != nil {
		return domain.ProviderConnection{}, fmt.Errorf("%w: Claude sign-in failed", domain.ErrInvalid)
	}
	defer wipe(payload)
	name, version, id := "Claude", domain.ResourceVersion(0), connectionID
	if id == "" {
		id, name, version, err = s.claudeConnectionTarget(ctx)
		if err != nil {
			return domain.ProviderConnection{}, err
		}
	} else if err := s.store.View(ctx, func(r ports.Reader) error {
		item, err := r.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if item.Provider != providergateway.Anthropic {
			return fmt.Errorf("%w: Claude sign-in only replaces an official Anthropic connection", domain.ErrInvalid)
		}
		name, version = item.Name, item.ResourceVersion
		return nil
	}); err != nil {
		return domain.ProviderConnection{}, err
	}
	return s.PutProviderConnection(ctx, id, ConnectionInput{
		Name: name, Provider: providergateway.Anthropic, APIKey: string(payload),
		CredentialKind: providergateway.CredentialOAuth, ExpectedResourceVersion: version,
		signIn: true,
	})
}

// claudeConnectionTarget prefers an existing official Anthropic login so
// sign-in replaces it instead of accumulating connections.
func (s *Service) claudeConnectionTarget(ctx context.Context) (id, name string, version domain.ResourceVersion, err error) {
	items, err := s.ListProviderConnections(ctx)
	if err != nil {
		return "", "", 0, err
	}
	var plain, revoked domain.ProviderConnection
	var havePlain, haveRevoked bool
	for _, item := range items {
		if item.Provider != providergateway.Anthropic {
			continue
		}
		if !item.Revoked && item.CredentialKind == providergateway.CredentialOAuth {
			return item.ID, item.Name, item.ResourceVersion, nil
		}
		if !item.Revoked && !havePlain {
			plain, havePlain = item, true
		}
		if !haveRevoked {
			revoked, haveRevoked = item, true
		}
	}
	if havePlain {
		return plain.ID, plain.Name, plain.ResourceVersion, nil
	}
	if haveRevoked {
		return revoked.ID, revoked.Name, revoked.ResourceVersion, nil
	}
	return "", "Claude", 0, nil
}

func (s *Service) claudeAccess(ctx context.Context, connection domain.ProviderConnection, raw []byte) (string, error) {
	if connection.CredentialKind != providergateway.CredentialOAuth {
		return string(raw), nil
	}
	if len(raw) == 0 || raw[0] != '{' {
		if len(raw) == 0 {
			return "", fmt.Errorf("%w: claude login is empty", domain.ErrConflict)
		}
		return string(raw), nil
	}
	cred, err := claudelogin.ParseStored(raw)
	if err != nil {
		return "", fmt.Errorf("%w: claude login is unreadable", domain.ErrConflict)
	}
	if !cred.NeedsRefresh(s.clock.Now()) {
		return cred.AccessToken(), nil
	}
	return s.refreshClaude(ctx, connection.ID)
}

func (s *Service) refreshClaude(ctx context.Context, id string) (string, error) {
	s.claudeRefresh.Lock()
	defer s.claudeRefresh.Unlock()
	for range 2 {
		connection, raw, err := s.readConnectionSecret(ctx, id)
		if err != nil {
			return "", err
		}
		if connection.Revoked || connection.CredentialKind != providergateway.CredentialOAuth || len(raw) == 0 || raw[0] != '{' {
			wipe(raw)
			return "", fmt.Errorf("%w: claude login is unavailable", domain.ErrConflict)
		}
		cred, err := claudelogin.ParseStored(raw)
		wipe(raw)
		if err != nil {
			return "", fmt.Errorf("%w: claude login is unreadable", domain.ErrConflict)
		}
		if !cred.NeedsRefresh(s.clock.Now()) {
			return cred.AccessToken(), nil
		}
		next, err := claudelogin.Refresh(ctx, s.claudeTokenEndpoint(), cred, s.clock.Now())
		if err != nil {
			return "", fmt.Errorf("%w: claude login needs a new sign-in", domain.ErrConflict)
		}
		if _, err := s.saveClaudeCredential(ctx, connection.ID, next); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			return "", err
		}
		return next.AccessToken(), nil
	}
	return "", domain.ErrConflict
}

func (s *Service) readConnectionSecret(ctx context.Context, id string) (domain.ProviderConnection, []byte, error) {
	var connection domain.ProviderConnection
	var raw []byte
	err := s.store.View(ctx, func(r ports.Reader) error {
		item, err := r.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		opened, err := s.secretKey.OpenSetup(connectionEnvelopeID(item), item.KeyID, item.Nonce, item.Ciphertext)
		if err != nil {
			return err
		}
		raw = append([]byte(nil), opened...)
		wipe(opened)
		connection = item
		return nil
	})
	if err != nil {
		wipe(raw)
		return domain.ProviderConnection{}, nil, err
	}
	return connection, raw, nil
}

func (s *Service) claudeTokenEndpoint() string {
	if s.claudeTokenURL != "" {
		return s.claudeTokenURL
	}
	return claudelogin.TokenURL
}

func claudeCallbackRedirect(host string) (string, bool) {
	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		return "", true
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", true
	}
	ip := net.ParseIP(hostname)
	if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", true
	}
	return "http://localhost:" + strconv.Itoa(number) + "/callback", false
}

func splitClaudeCode(pasted string) (code, state string, err error) {
	pasted = strings.TrimSpace(pasted)
	if pasted == "" || len(pasted) > 2048 {
		return "", "", fmt.Errorf("%w: Claude sign-in code was rejected", domain.ErrInvalid)
	}
	if left, right, ok := strings.Cut(pasted, "#"); ok {
		if strings.Contains(right, "#") || left == "" || right == "" {
			return "", "", fmt.Errorf("%w: Claude sign-in code was rejected", domain.ErrInvalid)
		}
		return left, right, nil
	}
	return pasted, "", nil
}

func claudeLoginID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
