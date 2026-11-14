package app_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func desktopRequest(seed byte) (app.DesktopSignIn, string) {
	verifier := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('a'+seed)), 32)))
	challenge := sha256.Sum256([]byte(verifier))
	state := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(string(rune('A'+seed)), 32)))
	return app.DesktopSignIn{Port: 49152 + int(seed), State: state, Challenge: base64.RawURLEncoding.EncodeToString(challenge[:])}, verifier
}

func desktopSignIn(t *testing.T, identity *app.Identity, request app.DesktopSignIn) *app.DesktopHandoff {
	t.Helper()
	target, binding, err := identity.BeginDesktopSignIn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	state := target[strings.Index(target, "state=")+len("state="):]
	// The desktop attempt's callback runs in the system browser and needs
	// that browser's binding like any other sign-in.
	if _, err := identity.CompleteSignIn(context.Background(), state, "code", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("desktop callback without its binding = %v", err)
	}
	target, binding, err = identity.BeginDesktopSignIn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	state = target[strings.Index(target, "state=")+len("state="):]
	result, err := identity.CompleteSignIn(context.Background(), state, "code", binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Desktop == nil || result.SessionToken != "" || result.ClaimToken != "" {
		t.Fatalf("desktop callback = %+v", result)
	}
	return result.Desktop
}

func TestDesktopHandoffNeedsTheShellsVerifierAndIsSingleUse(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
	party := &fakeIssuerParty{issuer: fakeIssuer}
	identity, err := app.NewIdentity(ctx, store, clock, &testIDs{}, party)
	if err != nil {
		t.Fatal(err)
	}

	// Malformed shell parameters start nothing.
	request, verifier := desktopRequest(1)
	for _, bad := range []app.DesktopSignIn{
		{Port: 80, State: request.State, Challenge: request.Challenge},
		{Port: 70000, State: request.State, Challenge: request.Challenge},
		{Port: request.Port, State: "short", Challenge: request.Challenge},
		{Port: request.Port, State: request.State, Challenge: request.Challenge + "A"},
	} {
		if _, _, err := identity.BeginDesktopSignIn(ctx, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("BeginDesktopSignIn(%+v) = %v", bad, err)
		}
	}

	// Before the claim, the handoff redeems to a claim token, never a
	// session, and the callback itself grants nothing.
	party.as("alice")
	handoff := desktopSignIn(t, identity, request)
	if handoff.Port != request.Port || handoff.State != request.State || handoff.Ticket == "" {
		t.Fatalf("handoff = %+v", handoff)
	}
	if _, _, err := identity.Session(ctx, handoff.Ticket); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ticket used as a session: %v", err)
	}
	// Someone who saw the ticket in the browser's history but lacks the
	// verifier gets nothing, and the ticket is spent.
	_, otherVerifier := desktopRequest(2)
	if _, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, otherVerifier); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong verifier = %v", err)
	}
	if _, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, verifier); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ticket reused after a failed redemption = %v", err)
	}
	handoff = desktopSignIn(t, identity, request)
	pending, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, verifier)
	if err != nil || pending.ClaimToken == "" || pending.SessionToken != "" {
		t.Fatalf("pre-claim redemption = %+v, %v", pending, err)
	}
	if _, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, verifier); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ticket redeemed twice = %v", err)
	}
	if _, err := identity.Claim(principal.With(ctx, principal.Owner()), pending.ClaimToken); err != nil {
		t.Fatal(err)
	}

	// In team mode it redeems to a member session.
	party.as("bob")
	handoff = desktopSignIn(t, identity, request)
	signedIn, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, verifier)
	if err != nil || signedIn.SessionToken == "" {
		t.Fatalf("team redemption = %+v, %v", signedIn, err)
	}
	bob, _, err := identity.Session(ctx, signedIn.SessionToken)
	if err != nil || bob.UserID() == principal.BootstrapOwner {
		t.Fatalf("Bob's desktop session = %+v, %v", bob, err)
	}

	// A ticket expires two minutes after the callback.
	handoff = desktopSignIn(t, identity, request)
	clock.mu.Lock()
	clock.now = clock.now.Add(3 * time.Minute)
	clock.mu.Unlock()
	if _, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, verifier); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired ticket = %v", err)
	}

	// Membership is decided at redemption: a member disabled after the
	// callback is refused.
	handoff = desktopSignIn(t, identity, request)
	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("UPDATE users SET disabled_at = '2026-11-14T00:00:00Z' WHERE subject = 'bob'"); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.RedeemDesktopSignIn(ctx, handoff.Ticket, verifier); !errors.Is(err, app.ErrSignInDenied) {
		t.Fatalf("disabled member's ticket = %v", err)
	}

	// A plain sign-in is unchanged by the desktop path.
	party.as("carol")
	if result := signIn(t, identity); result.SessionToken == "" || result.Desktop != nil {
		t.Fatalf("browser sign-in = %+v", result)
	}
}
