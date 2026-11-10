package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/chatgptauth"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
)

// ChatGPT plan connections (ADR 0036). The daemon owns every sign-in secret:
// the PKCE verifier, state, and nonce stay in memory here, and the resulting
// token set is sealed in the connection envelope. Nothing in this file sends
// a token to a Capsule, the browser, or the CLI.

const (
	chatGPTAttemptTTL     = 10 * time.Minute
	chatGPTMaxAttempts    = 8
	chatGPTRefreshMargin  = 5 * time.Minute
	chatGPTHostIDFile     = "chatgpt-host-id"
	chatGPTDefaultName    = "ChatGPT"
	chatGPTRevokeAttempts = 3
)

// ChatGPTSignInState is the lifecycle of one sign-in attempt.
type ChatGPTSignInState string

const (
	ChatGPTSignInPending   ChatGPTSignInState = "pending"
	ChatGPTSignInCompleted ChatGPTSignInState = "completed"
	ChatGPTSignInFailed    ChatGPTSignInState = "failed"
)

// ChatGPTSignIn is the public view of an attempt. It never includes a token,
// the PKCE verifier, or the nonce.
type ChatGPTSignIn struct {
	ID               string             `json:"id"`
	AuthorizationURL string             `json:"authorizationUrl"`
	RedirectURI      string             `json:"redirectUri"`
	Listening        bool               `json:"listening"`
	ExpiresAt        time.Time          `json:"expiresAt"`
	State            ChatGPTSignInState `json:"state"`
	ConnectionID     string             `json:"connectionId,omitempty"`
	Failure          string             `json:"failure,omitempty"`
}

type chatGPTAttempt struct {
	view       ChatGPTSignIn
	attempt    chatgptauth.Attempt
	connection string
	completing bool
}

type chatGPTState struct {
	client     *chatgptauth.Client
	hostIDPath string
	listen     string

	mu       sync.Mutex
	attempts map[string]*chatGPTAttempt
	server   *http.Server
	refresh  map[string]*sync.Mutex
}

// ConfigureChatGPT enables ChatGPT plan sign-in. dataDir holds the
// installation's OAuth host ID. A nil client uses OpenAI's production server.
func (s *Service) ConfigureChatGPT(dataDir string, client *chatgptauth.Client) {
	if client == nil {
		client = chatgptauth.New()
	}
	s.chatgpt = &chatGPTState{
		client: client, hostIDPath: filepath.Join(dataDir, chatGPTHostIDFile), listen: chatgptauth.CallbackAddress,
		attempts: map[string]*chatGPTAttempt{}, refresh: map[string]*sync.Mutex{},
	}
}

// ChatGPTAvailable reports whether ChatGPT sign-in can be offered.
func (s *Service) ChatGPTAvailable() bool {
	return s.chatgpt != nil && s.gateway != nil && s.secretKey != nil
}

// CloseChatGPT stops a pending loopback listener.
func (s *Service) CloseChatGPT() {
	if s.chatgpt == nil {
		return
	}
	s.chatgpt.mu.Lock()
	server := s.chatgpt.server
	s.chatgpt.server = nil
	s.chatgpt.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}

var hostIDPattern = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// chatGPTHostID returns the installation's stable ext_agent_host_id, creating
// it on first use. It is opaque and not derived from any user identity.
func (s *Service) chatGPTHostID() (string, error) {
	path := s.chatgpt.hostIDPath
	if raw, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(raw))
		if !hostIDPattern.MatchString(id) {
			return "", fmt.Errorf("ChatGPT host ID in %s is invalid", path)
		}
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	id := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return s.chatGPTHostID()
	}
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(id + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", err
	}
	return id, file.Close()
}

// StartChatGPTSignIn begins registration, or reauthorization when
// connectionID names an existing ChatGPT connection. With listen, the daemon
// also accepts the browser's loopback callback itself; the attempt can always
// be completed by CompleteChatGPTSignIn instead.
func (s *Service) StartChatGPTSignIn(ctx context.Context, connectionID string, listen bool) (ChatGPTSignIn, error) {
	if !s.ChatGPTAvailable() {
		return ChatGPTSignIn{}, domain.ErrUnsupported
	}
	clientID, loginHint := "", ""
	if connectionID != "" {
		connection, set, err := s.chatGPTTokens(ctx, connectionID)
		if err != nil {
			return ChatGPTSignIn{}, err
		}
		clientID, loginHint = set.ClientID, connection.Account
	}
	hostID, err := s.chatGPTHostID()
	if err != nil {
		return ChatGPTSignIn{}, err
	}
	attempt, err := chatgptauth.NewAttempt(clientID)
	if err != nil {
		return ChatGPTSignIn{}, err
	}
	state := s.chatgpt
	now := s.clock.Now().UTC()
	state.mu.Lock()
	state.pruneLocked(now)
	if len(state.attempts) >= chatGPTMaxAttempts {
		state.mu.Unlock()
		return ChatGPTSignIn{}, fmt.Errorf("%w: too many ChatGPT sign-ins in progress", domain.ErrConflict)
	}
	item := &chatGPTAttempt{
		attempt: attempt, connection: connectionID,
		view: ChatGPTSignIn{
			ID: s.ids.NewID(), AuthorizationURL: state.client.AuthorizationURL(attempt, hostID, loginHint),
			RedirectURI: chatgptauth.RedirectURI, ExpiresAt: now.Add(chatGPTAttemptTTL), State: ChatGPTSignInPending,
		},
	}
	state.attempts[item.view.ID] = item
	if listen {
		item.view.Listening = s.startChatGPTListenerLocked()
	}
	view := item.view
	state.mu.Unlock()
	time.AfterFunc(chatGPTAttemptTTL+time.Second, s.stopIdleChatGPTListener)
	return view, nil
}

// ChatGPTSignInStatus returns an attempt's current view.
func (s *Service) ChatGPTSignInStatus(id string) (ChatGPTSignIn, error) {
	if s.chatgpt == nil {
		return ChatGPTSignIn{}, domain.ErrUnsupported
	}
	s.chatgpt.mu.Lock()
	defer s.chatgpt.mu.Unlock()
	s.chatgpt.pruneLocked(s.clock.Now().UTC())
	item := s.chatgpt.attempts[id]
	if item == nil {
		return ChatGPTSignIn{}, domain.ErrNotFound
	}
	return item.view, nil
}

// CompleteChatGPTSignIn finishes an attempt from the redirected callback URL,
// pasted by the user or relayed by the CLI.
func (s *Service) CompleteChatGPTSignIn(ctx context.Context, id, callbackURL string) (ChatGPTSignIn, error) {
	if s.chatgpt == nil {
		return ChatGPTSignIn{}, domain.ErrUnsupported
	}
	callback, err := chatgptauth.ParseCallback(callbackURL)
	if err != nil {
		return ChatGPTSignIn{}, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	return s.completeChatGPT(ctx, func(item *chatGPTAttempt) bool { return item.view.ID == id }, callback)
}

// completeChatGPT redeems a callback for the attempt that match selects. A
// callback whose state does not match leaves the attempt pending; any
// exchange outcome ends it, so a code is never redeemed twice.
func (s *Service) completeChatGPT(ctx context.Context, match func(*chatGPTAttempt) bool, callback chatgptauth.Callback) (ChatGPTSignIn, error) {
	state := s.chatgpt
	state.mu.Lock()
	state.pruneLocked(s.clock.Now().UTC())
	var item *chatGPTAttempt
	for _, candidate := range state.attempts {
		if match(candidate) {
			item = candidate
			break
		}
	}
	switch {
	case item == nil:
		state.mu.Unlock()
		return ChatGPTSignIn{}, domain.ErrNotFound
	case item.view.State != ChatGPTSignInPending || item.completing:
		view := item.view
		state.mu.Unlock()
		return view, fmt.Errorf("%w: this ChatGPT sign-in is already finished", domain.ErrConflict)
	case callback.State != item.attempt.State:
		state.mu.Unlock()
		return ChatGPTSignIn{}, fmt.Errorf("%w: that address belongs to a different sign-in attempt", domain.ErrInvalid)
	}
	item.completing = true
	attempt, connectionID := item.attempt, item.connection
	state.mu.Unlock()

	set, err := state.client.Complete(ctx, attempt, callback)
	var connection domain.ProviderConnection
	if err == nil {
		connection, err = s.saveChatGPT(ctx, connectionID, set)
	}
	state.mu.Lock()
	item.completing = false
	if err != nil {
		item.view.State, item.view.Failure = ChatGPTSignInFailed, chatGPTFailure(err)
	} else {
		item.view.State, item.view.ConnectionID = ChatGPTSignInCompleted, connection.ID
	}
	view := item.view
	state.mu.Unlock()
	go s.stopIdleChatGPTListener()
	if err != nil {
		return view, fmt.Errorf("%w: %s", domain.ErrInvalid, view.Failure)
	}
	return view, nil
}

func chatGPTFailure(err error) string {
	switch {
	case errors.Is(err, chatgptauth.ErrDenied):
		return "ChatGPT sign-in was declined"
	case errors.Is(err, chatgptauth.ErrInvalidGrant):
		return "The sign-in code expired or was already used; start again"
	case errors.Is(err, domain.ErrConflict):
		return "The connection changed during sign-in; start again"
	}
	message := err.Error()
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

func (state *chatGPTState) pruneLocked(now time.Time) {
	for id, item := range state.attempts {
		if !item.completing && now.After(item.view.ExpiresAt) {
			delete(state.attempts, id)
		}
	}
}

// startChatGPTListenerLocked binds the loopback callback address if it is
// not already bound. It reports whether the daemon is listening.
func (s *Service) startChatGPTListenerLocked() bool {
	state := s.chatgpt
	if state.server != nil {
		return true
	}
	listener, err := net.Listen("tcp", state.listen)
	if err != nil {
		return false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+chatgptauth.CallbackPath, s.serveChatGPTCallback)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	state.server = server
	go func() { _ = server.Serve(listener) }()
	return true
}

func (s *Service) serveChatGPTCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	page := func(status int, message string) {
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Meridian</title><p>%s</p>", html.EscapeString(message))
	}
	callback, err := chatgptauth.ParseCallback(r.URL.RawQuery)
	if err != nil {
		page(http.StatusBadRequest, "This page has no ChatGPT sign-in result.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	view, err := s.completeChatGPT(ctx, func(item *chatGPTAttempt) bool {
		return item.attempt.State == callback.State
	}, callback)
	switch {
	case err == nil:
		page(http.StatusOK, "Signed in to ChatGPT. You can close this tab and return to Meridian.")
	case errors.Is(err, domain.ErrNotFound):
		page(http.StatusNotFound, "This sign-in attempt expired. Start again in Meridian.")
	default:
		message := view.Failure
		if message == "" {
			message = "ChatGPT sign-in did not complete. Start again in Meridian."
		}
		page(http.StatusBadRequest, message)
	}
}

// stopIdleChatGPTListener closes the loopback listener once no pending
// attempt is waiting on it.
func (s *Service) stopIdleChatGPTListener() {
	state := s.chatgpt
	state.mu.Lock()
	state.pruneLocked(s.clock.Now().UTC())
	waiting := false
	for _, item := range state.attempts {
		if item.view.Listening && item.view.State == ChatGPTSignInPending {
			waiting = true
		}
	}
	server := state.server
	if !waiting {
		state.server = nil
	}
	state.mu.Unlock()
	if !waiting && server != nil {
		_ = server.Close()
	}
}

// saveChatGPT stores a completed sign-in as a new connection, or as a
// reauthorization of connectionID.
func (s *Service) saveChatGPT(ctx context.Context, connectionID string, set chatgptauth.TokenSet) (domain.ProviderConnection, error) {
	plain, err := json.Marshal(set)
	if err != nil {
		return domain.ProviderConnection{}, err
	}
	defer wipe(plain)
	var result domain.ProviderConnection
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if connectionID == "" {
			items, err := tx.ListProviderConnections(ctx)
			if err != nil {
				return err
			}
			if len(items) >= 128 {
				return domain.ErrConflict
			}
			result = domain.ProviderConnection{
				ID: s.ids.NewID(), Provider: providergateway.ChatGPT, Name: chatGPTDefaultName,
				CreatedAt: s.clock.Now().UTC(),
			}
		} else {
			var err error
			result, err = tx.GetProviderConnection(ctx, connectionID)
			if err != nil {
				return err
			}
			if result.Provider != providergateway.ChatGPT || result.Revoked {
				return domain.ErrConflict
			}
		}
		result.ResourceVersion++
		result.CredentialGeneration++
		result.Account, result.NeedsSignIn = set.Email, false
		result.KeyID = s.secretKey.ID
		var err error
		result.Nonce, result.Ciphertext, err = s.secretKey.SealSetup(connectionEnvelopeID(result), plain)
		if err != nil {
			return err
		}
		if err := tx.PutProviderConnection(ctx, result); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", result.ID, "provider_connection.updated", result.ResourceVersion, nil)
	})
	return result, err
}

// chatGPTTokens opens a live ChatGPT connection's token set.
func (s *Service) chatGPTTokens(ctx context.Context, id string) (domain.ProviderConnection, chatgptauth.TokenSet, error) {
	var connection domain.ProviderConnection
	var set chatgptauth.TokenSet
	err := s.store.View(ctx, func(r ports.Reader) error {
		var err error
		connection, err = r.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if connection.Provider != providergateway.ChatGPT || connection.Revoked {
			return domain.ErrConflict
		}
		set, err = s.openChatGPT(connection)
		return err
	})
	return connection, set, err
}

func (s *Service) openChatGPT(connection domain.ProviderConnection) (chatgptauth.TokenSet, error) {
	var set chatgptauth.TokenSet
	raw, err := s.secretKey.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
	if err != nil {
		return set, err
	}
	defer wipe(raw)
	if err := json.Unmarshal(raw, &set); err != nil || set.ClientID == "" || set.RefreshToken == "" {
		return chatgptauth.TokenSet{}, errors.New("ChatGPT connection credentials are unreadable")
	}
	return set, nil
}

func (state *chatGPTState) refreshLock(id string) *sync.Mutex {
	state.mu.Lock()
	defer state.mu.Unlock()
	lock := state.refresh[id]
	if lock == nil {
		lock = &sync.Mutex{}
		state.refresh[id] = lock
	}
	return lock
}

// chatGPTUpstream resolves a gateway upstream for a granted ChatGPT
// connection. The returned Refresh forces rotation after an upstream 401.
func (s *Service) chatGPTUpstream(ctx context.Context, id string) (providergateway.Upstream, error) {
	key, err := s.chatGPTAccess(ctx, id, false)
	if err != nil {
		return providergateway.Upstream{}, err
	}
	return providergateway.Upstream{Key: key, Refresh: func(ctx context.Context) (string, error) {
		return s.chatGPTAccess(ctx, id, true)
	}}, nil
}

// chatGPTAccess returns a current access token, refreshing it near expiry or
// when forced. Refreshes for one connection are serialized, and the rotated
// token set is stored only if no newer generation was saved meanwhile.
func (s *Service) chatGPTAccess(ctx context.Context, id string, force bool) (string, error) {
	if s.chatgpt == nil {
		return "", domain.ErrUnsupported
	}
	lock := s.chatgpt.refreshLock(id)
	lock.Lock()
	defer lock.Unlock()
	connection, set, err := s.chatGPTTokens(ctx, id)
	if err != nil {
		return "", err
	}
	if connection.NeedsSignIn {
		return "", providergateway.ErrSignInRequired
	}
	now := s.clock.Now()
	if !force && set.ExpiresAt.After(now.Add(chatGPTRefreshMargin)) {
		return set.AccessToken, nil
	}
	next, err := s.chatgpt.client.Refresh(ctx, set)
	if errors.Is(err, chatgptauth.ErrInvalidGrant) {
		if markErr := s.markChatGPTSignIn(ctx, connection); markErr != nil {
			return "", markErr
		}
		return "", providergateway.ErrSignInRequired
	}
	if err != nil {
		// A transient refresh failure does not end a still-valid token.
		if !force && set.ExpiresAt.After(now) {
			return set.AccessToken, nil
		}
		return "", err
	}
	plain, err := json.Marshal(next)
	if err != nil {
		return "", err
	}
	defer wipe(plain)
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if current.Revoked || current.CredentialGeneration != connection.CredentialGeneration {
			return domain.ErrConflict
		}
		current.CredentialGeneration++
		current.KeyID = s.secretKey.ID
		current.Nonce, current.Ciphertext, err = s.secretKey.SealSetup(connectionEnvelopeID(current), plain)
		if err != nil {
			return err
		}
		// Rotation keeps the resource version, so it never conflicts with an
		// operator's rename or revocation.
		return tx.PutProviderConnection(ctx, current)
	})
	if err != nil {
		return "", err
	}
	return next.AccessToken, nil
}

func (s *Service) markChatGPTSignIn(ctx context.Context, seen domain.ProviderConnection) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetProviderConnection(ctx, seen.ID)
		if err != nil {
			return err
		}
		if current.Revoked || current.CredentialGeneration != seen.CredentialGeneration || current.NeedsSignIn {
			return nil
		}
		current.NeedsSignIn = true
		current.ResourceVersion++
		if err := tx.PutProviderConnection(ctx, current); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", current.ID, "provider_connection.updated", current.ResourceVersion, nil)
	})
}

// revokeChatGPTUpstream revokes a refresh token at OpenAI, retrying network
// errors and 5xx responses. It reports whether revocation was confirmed.
func (s *Service) revokeChatGPTUpstream(ctx context.Context, set chatgptauth.TokenSet) bool {
	if s.chatgpt == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	delay := 500 * time.Millisecond
	for attempt := range chatGPTRevokeAttempts {
		if s.chatgpt.client.Revoke(ctx, set) == nil {
			return true
		}
		if attempt+1 == chatGPTRevokeAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		delay *= 2
	}
	return false
}
