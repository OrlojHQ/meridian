package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// pendingRebind is a claim-token ceremony bound to an existing member. Only
// its digest is retained, and the browser never receives the completion token.
type pendingRebind struct {
	previous domain.User
	identity oidcauth.Identity
	expires  time.Time
}

type IdentityRebind struct {
	SignInURL string    `json:"signInUrl"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// BeginIdentityRebind starts recovery of an existing member, including the
// bootstrap owner. Both this call and completion require the installation
// bearer in team mode. Normal OIDC sign-in never enters this ceremony.
func (i *Identity) BeginIdentityRebind(ctx context.Context, id domain.UserID) (IdentityRebind, error) {
	if err := requireBreakGlass(ctx); err != nil {
		return IdentityRebind{}, err
	}
	if !i.team.Load() || !i.SignInAvailable() {
		return IdentityRebind{}, domain.ErrNotFound
	}
	system := principal.WithSystem(ctx)
	var previous domain.User
	if err := i.store.View(system, func(r ports.Reader) error {
		var err error
		previous, err = r.GetUser(system, id)
		return err
	}); err != nil {
		return IdentityRebind{}, err
	}
	token, digest, err := newToken()
	if err != nil {
		return IdentityRebind{}, err
	}
	// Discovery may contact the issuer. Do not hold the identity mutex across
	// provider work; recheck capacity under the lock before publishing state.
	attempt, err := i.provider.Begin(ctx)
	if err != nil {
		return IdentityRebind{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.clock.Now()
	i.pruneLocked(now)
	if len(i.rebinds) >= maxPendingClaims || len(i.attempts) >= maxSignInAttempts {
		return IdentityRebind{}, fmt.Errorf("%w: too many sign-ins are waiting", domain.ErrResourceExhausted)
	}
	expires := now.Add(claimTTL)
	i.rebinds[digest] = pendingRebind{previous: previous, expires: expires}
	i.attempts[attempt.State] = signInAttempt{attempt: attempt, expires: expires, rebindDigest: digest}
	return IdentityRebind{SignInURL: attempt.URL, Token: token, ExpiresAt: expires}, nil
}

// verifyRebind is reached only after CompleteSignIn has validated the OIDC
// callback and consumed its state. It issues no session and creates no user.
func (i *Identity) verifyRebind(digest [32]byte, verified oidcauth.Identity, now time.Time) (SignInResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	pending, ok := i.rebinds[digest]
	if !ok || !now.Before(pending.expires) {
		return SignInResult{}, domain.ErrNotFound
	}
	if verified.Issuer != i.provider.Issuer() || verified.Subject == "" {
		return SignInResult{}, ErrSignInDenied
	}
	pending.identity = verified
	i.rebinds[digest] = pending
	return SignInResult{RebindReady: true}, nil
}

// CompleteIdentityRebind consumes a verified recovery token. False with no
// error means the browser has not finished yet. The identity replacement,
// session/PAT revocation, and audit record commit in one transaction.
func (i *Identity) CompleteIdentityRebind(ctx context.Context, id domain.UserID, token string) (bool, error) {
	if err := requireBreakGlass(ctx); err != nil {
		return false, err
	}
	if !i.team.Load() || !i.SignInAvailable() || token == "" || len(token) > 128 {
		return false, domain.ErrNotFound
	}
	now := i.clock.Now()
	digest := sha256.Sum256([]byte(token))
	i.mu.Lock()
	pending, ok := i.rebinds[digest]
	if !ok || !now.Before(pending.expires) || pending.previous.ID != id {
		i.mu.Unlock()
		return false, domain.ErrNotFound
	}
	if pending.identity.Subject == "" {
		i.mu.Unlock()
		return false, nil
	}
	delete(i.rebinds, digest)
	i.mu.Unlock()
	system := principal.WithSystem(ctx)
	err := i.store.Transact(system, func(tx ports.Transaction) error {
		installation, err := tx.GetInstallation(system)
		if err != nil {
			return err
		}
		if installation.Mode != domain.TeamMode || pending.identity.Issuer != installation.OIDCIssuer {
			return ErrSignInDenied
		}
		existing, err := tx.GetUserByIdentity(system, pending.identity.Issuer, pending.identity.Subject)
		if err == nil && existing.ID != id {
			return fmt.Errorf("%w: identity already belongs to a member", domain.ErrConflict)
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		next := domain.User{ID: id, Issuer: pending.identity.Issuer, Subject: pending.identity.Subject,
			DisplayName: pending.identity.Name, Email: pending.identity.Email}
		if err := tx.RebindUserIdentity(system, pending.previous, next, now); err != nil {
			return err
		}
		if _, err := tx.RevokeUserCredentials(system, id, now); err != nil {
			return err
		}
		record, _ := auditRecordFor(ctx, now, domain.AuditMemberIdentityRebind, "user", string(id), domain.AuditAllowed)
		return tx.AppendAuditRecord(system, record)
	})
	if err != nil {
		return false, err
	}
	// Drop outstanding tickets and gateway leases using the same hooks as a
	// disable. The member's enabled/disabled state itself is preserved.
	i.mu.Lock()
	hooks := append([]MemberDisabled(nil), i.disabled...)
	i.mu.Unlock()
	for _, hook := range hooks {
		hook(context.WithoutCancel(ctx), id)
	}
	return true, nil
}
