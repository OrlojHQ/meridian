package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const (
	// Sessions end after 12 hours idle or 7 days, whichever comes first.
	sessionIdleTimeout = 12 * time.Hour
	sessionLifetime    = 7 * 24 * time.Hour
	// sessionTouchInterval bounds last-used writes to one a minute.
	sessionTouchInterval = time.Minute
	signInAttemptTTL     = 10 * time.Minute
	maxSignInAttempts    = 64
	claimTTL             = 10 * time.Minute
	maxPendingClaims     = 8
)

// ErrSignInDenied reports an identity the issuer authenticated that may not
// sign in: it lacks the required group, belongs to a disabled member, or
// arrives before the installation is claimed.
var ErrSignInDenied = errors.New("sign-in is not allowed")

// IdentityProvider is the OIDC relying party Identity signs users in with.
type IdentityProvider interface {
	Issuer() string
	Begin(context.Context) (oidcauth.Attempt, error)
	Complete(context.Context, oidcauth.Attempt, string) (oidcauth.Identity, error)
}

// Identity is the installation's sign-in: its mode, OIDC ceremonies, the
// one-time claim, and user sessions (ADR 0046). It runs before any
// principal exists, so it is not a Service method set: each credential is
// verified first, and only then does it read or write as the system.
type Identity struct {
	store    ports.Store
	clock    ports.Clock
	ids      ports.IDSource
	provider IdentityProvider

	team atomic.Bool

	mu       sync.Mutex
	attempts map[string]signInAttempt
	claims   map[[32]byte]pendingClaim
	disabled []MemberDisabled
}

type signInAttempt struct {
	attempt oidcauth.Attempt
	expires time.Time
}

type pendingClaim struct {
	identity oidcauth.Identity
	expires  time.Time
}

// SignInResult is a completed sign-in. Exactly one field is set: a session
// token in team mode, or a claim token before the claim.
type SignInResult struct {
	SessionToken string
	ClaimToken   string
}

// NewIdentity reads the installation's mode. provider is nil when no
// issuer is configured. A claimed installation keeps the issuer it was
// claimed with: configuring another is refused, since members are
// identified by (issuer, subject).
func NewIdentity(ctx context.Context, store ports.Store, clock ports.Clock, ids ports.IDSource, provider IdentityProvider) (*Identity, error) {
	identity := &Identity{
		store: newScopedStore(store, clock), clock: clock, ids: ids, provider: provider,
		attempts: map[string]signInAttempt{}, claims: map[[32]byte]pendingClaim{},
	}
	var installation domain.Installation
	if err := identity.store.View(principal.WithSystem(ctx), func(reader ports.Reader) error {
		var err error
		installation, err = reader.GetInstallation(ctx)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read installation mode: %w", err)
	}
	if installation.Mode == domain.TeamMode {
		if provider != nil && provider.Issuer() != installation.OIDCIssuer {
			return nil, fmt.Errorf("the installation was claimed with OIDC issuer %q; configure that issuer", installation.OIDCIssuer)
		}
		identity.team.Store(true)
	}
	return identity, nil
}

// Mode is the installation's mode.
func (i *Identity) Mode() domain.InstallationMode {
	if i != nil && i.team.Load() {
		return domain.TeamMode
	}
	return domain.SingleUserMode
}

// SignInAvailable reports whether an issuer is configured.
func (i *Identity) SignInAvailable() bool { return i != nil && i.provider != nil }

// Claimable reports whether the installation can still be claimed.
func (i *Identity) Claimable() bool { return i.SignInAvailable() && !i.team.Load() }

// BeginSignIn starts an OIDC sign-in and returns the issuer URL to send the
// browser to. Before the claim, the only sign-in is the claim's.
func (i *Identity) BeginSignIn(ctx context.Context) (string, error) {
	if !i.SignInAvailable() {
		return "", domain.ErrUnsupported
	}
	now := i.clock.Now()
	i.mu.Lock()
	i.pruneLocked(now)
	full := len(i.attempts) >= maxSignInAttempts
	i.mu.Unlock()
	if full {
		return "", fmt.Errorf("%w: too many sign-ins are waiting", domain.ErrResourceExhausted)
	}
	attempt, err := i.provider.Begin(ctx)
	if err != nil {
		return "", err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.attempts[attempt.State] = signInAttempt{attempt: attempt, expires: now.Add(signInAttemptTTL)}
	return attempt.URL, nil
}

// CompleteSignIn finishes the sign-in that state names. The state is
// single-use: a second callback with it finds nothing.
func (i *Identity) CompleteSignIn(ctx context.Context, state, code string) (SignInResult, error) {
	if !i.SignInAvailable() {
		return SignInResult{}, domain.ErrUnsupported
	}
	now := i.clock.Now()
	i.mu.Lock()
	i.pruneLocked(now)
	pending, ok := i.attempts[state]
	delete(i.attempts, state)
	i.mu.Unlock()
	if !ok || state == "" {
		return SignInResult{}, domain.ErrNotFound
	}
	verified, err := i.provider.Complete(ctx, pending.attempt, code)
	if errors.Is(err, oidcauth.ErrDenied) {
		return SignInResult{}, ErrSignInDenied
	}
	if err != nil {
		return SignInResult{}, fmt.Errorf("%w: sign-in failed", domain.ErrInvalid)
	}
	if !i.team.Load() {
		token, digest, err := newToken()
		if err != nil {
			return SignInResult{}, err
		}
		i.mu.Lock()
		defer i.mu.Unlock()
		for key, claim := range i.claims {
			if !now.Before(claim.expires) {
				delete(i.claims, key)
			}
		}
		if len(i.claims) >= maxPendingClaims {
			return SignInResult{}, fmt.Errorf("%w: too many claims are waiting", domain.ErrResourceExhausted)
		}
		i.claims[digest] = pendingClaim{identity: verified, expires: now.Add(claimTTL)}
		return SignInResult{ClaimToken: token}, nil
	}
	token, err := i.signIn(ctx, verified, now)
	if err != nil {
		return SignInResult{}, err
	}
	return SignInResult{SessionToken: token}, nil
}

// signIn finds or creates the member identity names and opens a session.
// Without a required group, every identity the issuer authenticates
// becomes a member on first sign-in (ADR 0046).
func (i *Identity) signIn(ctx context.Context, verified oidcauth.Identity, now time.Time) (string, error) {
	system := principal.WithSystem(ctx)
	var token string
	err := i.store.Transact(system, func(tx ports.Transaction) error {
		installation, err := tx.GetInstallation(system)
		if err != nil {
			return err
		}
		if installation.Mode != domain.TeamMode || verified.Issuer != installation.OIDCIssuer {
			return ErrSignInDenied
		}
		user, err := tx.GetUserByIdentity(system, verified.Issuer, verified.Subject)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			user = domain.User{
				ID: domain.UserID("usr_" + i.ids.NewID()), Issuer: verified.Issuer, Subject: verified.Subject,
				DisplayName: verified.Name, Email: verified.Email, Role: domain.UserMember, CreatedAt: now,
			}
			if err := tx.InsertUser(system, user); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if !user.DisabledAt.IsZero() {
				return ErrSignInDenied
			}
			if err := tx.UpdateUserProfile(system, user.ID, verified.Name, verified.Email); err != nil {
				return err
			}
		}
		token, err = i.openSessionTx(system, tx, user.ID, now, domain.AuditSignIn)
		return err
	})
	return token, err
}

// openSessionTx stores a new session for user and audits action as that
// user. The token is returned once and only its digest is stored.
func (i *Identity) openSessionTx(ctx context.Context, tx ports.Transaction, user domain.UserID, now time.Time, action domain.AuditAction) (string, error) {
	token, digest, err := newToken()
	if err != nil {
		return "", err
	}
	session := domain.UserSession{
		ID: "ses_" + i.ids.NewID(), TokenDigest: digest, UserID: user,
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(sessionLifetime),
	}
	if err := tx.InsertUserSession(ctx, session); err != nil {
		return "", err
	}
	if err := i.auditAs(ctx, tx, user, action, "session", session.ID, now); err != nil {
		return "", err
	}
	return token, nil
}

func (i *Identity) auditAs(ctx context.Context, tx ports.Transaction, user domain.UserID, action domain.AuditAction, objectType, objectID string, now time.Time) error {
	actor, ok := principal.User(user)
	if !ok {
		return principal.ErrMissing
	}
	record, _ := auditRecordFor(principal.With(ctx, actor), now, action, objectType, objectID, domain.AuditAllowed)
	return tx.AppendAuditRecord(ctx, record)
}

// Claim binds a claim token's identity to the bootstrap owner and turns on
// team mode (ADR 0046). ctx must be the bootstrap owner, authenticated by
// the installation bearer: the claim needs both the token and the identity.
// It happens once. It returns a session for the owner's browser.
func (i *Identity) Claim(ctx context.Context, claimToken string) (string, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return "", err
	}
	if caller.Kind() != principal.KindUser || caller.UserID() != principal.BootstrapOwner || !i.Claimable() {
		return "", domain.ErrNotFound
	}
	now := i.clock.Now()
	digest := sha256.Sum256([]byte(claimToken))
	i.mu.Lock()
	pending, ok := i.claims[digest]
	delete(i.claims, digest)
	i.mu.Unlock()
	if !ok || !now.Before(pending.expires) {
		return "", domain.ErrNotFound
	}
	system := principal.WithSystem(ctx)
	var token string
	err = i.store.Transact(system, func(tx ports.Transaction) error {
		owner := domain.User{
			ID: principal.BootstrapOwner, Issuer: pending.identity.Issuer, Subject: pending.identity.Subject,
			DisplayName: pending.identity.Name, Email: pending.identity.Email,
		}
		if err := tx.ClaimInstallation(system, pending.identity.Issuer, owner, now); err != nil {
			return err
		}
		if err := i.auditAs(system, tx, owner.ID, domain.AuditInstallationClaim, "installation", "installation", now); err != nil {
			return err
		}
		token, err = i.openSessionTx(system, tx, owner.ID, now, domain.AuditSignIn)
		return err
	})
	if err != nil {
		return "", err
	}
	i.team.Store(true)
	return token, nil
}

// Session resolves a session cookie to its user. An unknown, revoked,
// expired, or idle session, or one whose member is disabled, reads as not
// found.
func (i *Identity) Session(ctx context.Context, token string) (principal.Principal, string, error) {
	if !i.team.Load() || token == "" || len(token) > 128 {
		return principal.Principal{}, "", domain.ErrNotFound
	}
	digest := sha256.Sum256([]byte(token))
	system := principal.WithSystem(ctx)
	now := i.clock.Now()
	var session domain.UserSession
	var user domain.User
	if err := i.store.View(system, func(reader ports.Reader) error {
		var err error
		if session, err = reader.GetUserSessionByDigest(system, digest); err != nil {
			return err
		}
		user, err = reader.GetUser(system, session.UserID)
		return err
	}); err != nil {
		return principal.Principal{}, "", domain.ErrNotFound
	}
	if !session.RevokedAt.IsZero() || !now.Before(session.ExpiresAt) ||
		now.Sub(session.LastUsedAt) > sessionIdleTimeout || !user.DisabledAt.IsZero() {
		return principal.Principal{}, "", domain.ErrNotFound
	}
	if now.Sub(session.LastUsedAt) >= sessionTouchInterval {
		_ = i.store.Transact(system, func(tx ports.Transaction) error {
			return tx.TouchUserSession(system, session.ID, now)
		})
	}
	caller, ok := principal.User(user.ID)
	if !ok {
		return principal.Principal{}, "", domain.ErrNotFound
	}
	return caller, session.ID, nil
}

// SignOut revokes the caller's current session.
func (i *Identity) SignOut(ctx context.Context, sessionID string) error {
	return i.revoke(ctx, sessionID, domain.AuditSignOut)
}

// RevokeSession revokes one of the caller's own sessions.
func (i *Identity) RevokeSession(ctx context.Context, sessionID string) error {
	return i.revoke(ctx, sessionID, domain.AuditSessionRevoke)
}

func (i *Identity) revoke(ctx context.Context, sessionID string, action domain.AuditAction) error {
	caller, err := principal.Require(ctx)
	if err != nil {
		return err
	}
	if caller.Kind() != principal.KindUser {
		return domain.ErrNotFound
	}
	now := i.clock.Now()
	return i.store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.RevokeUserSession(ctx, sessionID, now); err != nil {
			return err
		}
		record, ok := auditRecordFor(ctx, now, action, "session", sessionID, domain.AuditAllowed)
		if !ok {
			return principal.ErrMissing
		}
		return tx.AppendAuditRecord(ctx, record)
	})
}

// Sessions lists the caller's sessions, newest first.
func (i *Identity) Sessions(ctx context.Context) ([]domain.UserSession, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return nil, err
	}
	if caller.Kind() != principal.KindUser {
		return nil, domain.ErrNotFound
	}
	var sessions []domain.UserSession
	err = i.store.View(ctx, func(reader ports.Reader) error {
		var err error
		sessions, err = reader.ListUserSessions(ctx, caller.UserID())
		return err
	})
	return sessions, err
}

// Member returns the caller's own membership row.
func (i *Identity) Member(ctx context.Context) (domain.User, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return domain.User{}, err
	}
	if caller.Kind() != principal.KindUser {
		return domain.User{}, domain.ErrNotFound
	}
	var user domain.User
	err = i.store.View(ctx, func(reader ports.Reader) error {
		var err error
		user, err = reader.GetUser(ctx, caller.UserID())
		return err
	})
	return user, err
}

// RecordBreakGlass audits one use of the installation bearer in team mode.
// Recording is best effort and never blocks the request.
func (i *Identity) RecordBreakGlass(ctx context.Context, route string) {
	if len(route) > 256 {
		route = route[:256]
	}
	record, ok := auditRecordFor(principal.With(ctx, principal.BreakGlass()), i.clock.Now(),
		domain.AuditBreakGlassUse, "route", route, domain.AuditAllowed)
	if !ok {
		return
	}
	system := principal.WithSystem(context.WithoutCancel(ctx))
	_ = i.store.Transact(system, func(tx ports.Transaction) error {
		return tx.AppendAuditRecord(system, record)
	})
}

func (i *Identity) pruneLocked(now time.Time) {
	for state, attempt := range i.attempts {
		if !now.Before(attempt.expires) {
			delete(i.attempts, state)
		}
	}
}

func newToken() (string, [32]byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", [32]byte{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, sha256.Sum256([]byte(token)), nil
}
