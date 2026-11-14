package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
)

const (
	// A handoff ticket must be redeemed soon after the browser reaches the
	// shell's loopback listener.
	desktopHandoffTTL  = 2 * time.Minute
	maxDesktopHandoffs = 16
	// Random values from the shell are 32 bytes, base64url without padding.
	desktopValueLength = 43
)

// DesktopSignIn is what the desktop shell sends when it opens a sign-in in
// the system browser (ADR 0047). Port is the shell's loopback listener on
// 127.0.0.1. State lets the shell recognize the redirect it is waiting for.
// Challenge is the base64url SHA-256 of a verifier only the shell holds;
// redeeming the ticket needs that verifier.
type DesktopSignIn struct {
	Port      int
	State     string
	Challenge string
}

// DesktopHandoff is where the system browser goes after the issuer
// verified the member: the shell's loopback listener, carrying the shell's
// state and a single-use ticket.
type DesktopHandoff struct {
	Port   int
	State  string
	Ticket string
}

type desktopHandoff struct {
	identity  oidcauth.Identity
	challenge string
	expires   time.Time
}

// ValidDesktopSignIn checks the shell's parameters. The port must be a
// non-privileged port and state and challenge 32 random bytes in base64url.
func ValidDesktopSignIn(request DesktopSignIn) error {
	if request.Port < 1024 || request.Port > 65535 ||
		!desktopValue(request.State) || !desktopValue(request.Challenge) {
		return fmt.Errorf("%w: desktop sign-in parameters are not valid", domain.ErrInvalid)
	}
	return nil
}

func desktopValue(value string) bool {
	if len(value) != desktopValueLength {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

// BeginDesktopSignIn starts an OIDC sign-in whose callback hands off to
// the desktop shell instead of setting a cookie in the system browser.
// Like a browser sign-in, it returns a binding for the system browser's
// callback: the binding is checked before the handoff ticket is minted.
func (i *Identity) BeginDesktopSignIn(ctx context.Context, request DesktopSignIn) (string, string, error) {
	if err := ValidDesktopSignIn(request); err != nil {
		return "", "", err
	}
	return i.beginSignIn(ctx, &request)
}

// handOff stores a verified identity behind a single-use ticket. Nothing
// is granted yet: redemption decides between a claim and a session, so a
// member disabled meanwhile is still refused.
func (i *Identity) handOff(request DesktopSignIn, verified oidcauth.Identity, now time.Time) (SignInResult, error) {
	ticket, digest, err := newToken()
	if err != nil {
		return SignInResult{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.pruneLocked(now)
	if len(i.handoffs) >= maxDesktopHandoffs {
		return SignInResult{}, fmt.Errorf("%w: too many desktop sign-ins are waiting", domain.ErrResourceExhausted)
	}
	i.handoffs[digest] = desktopHandoff{identity: verified, challenge: request.Challenge, expires: now.Add(desktopHandoffTTL)}
	return SignInResult{Desktop: &DesktopHandoff{Port: request.Port, State: request.State, Ticket: ticket}}, nil
}

// RedeemDesktopSignIn exchanges a handoff ticket and the shell's verifier
// for what the browser callback would have set: a claim token before the
// claim, or a member session. The ticket is consumed by any attempt, so a
// wrong verifier cannot be retried.
func (i *Identity) RedeemDesktopSignIn(ctx context.Context, ticket, verifier string) (SignInResult, error) {
	if !i.SignInAvailable() {
		return SignInResult{}, domain.ErrUnsupported
	}
	if ticket == "" || len(ticket) > 128 || !desktopValue(verifier) {
		return SignInResult{}, domain.ErrNotFound
	}
	now := i.clock.Now()
	digest := sha256.Sum256([]byte(ticket))
	i.mu.Lock()
	i.pruneLocked(now)
	handoff, ok := i.handoffs[digest]
	delete(i.handoffs, digest)
	i.mu.Unlock()
	if !ok || !now.Before(handoff.expires) {
		return SignInResult{}, domain.ErrNotFound
	}
	challenge := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(challenge[:])), []byte(handoff.challenge)) != 1 {
		return SignInResult{}, domain.ErrNotFound
	}
	return i.finishSignIn(ctx, handoff.identity, now)
}
