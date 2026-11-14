package app_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

const fakeIssuer = "https://issuer.example"

// fakeIssuerParty signs in whoever the test names next. Begin hands out a
// fresh state; Complete accepts only the code "code".
type fakeIssuerParty struct {
	mu     sync.Mutex
	issuer string
	next   oidcauth.Identity
	deny   bool
	count  int
}

func (f *fakeIssuerParty) Issuer() string { return f.issuer }

func (f *fakeIssuerParty) Begin(context.Context) (oidcauth.Attempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	state := "state-" + string(rune('a'+f.count))
	return oidcauth.Attempt{State: state, Nonce: "nonce", Verifier: "verifier", URL: f.issuer + "/authorize?state=" + state}, nil
}

func (f *fakeIssuerParty) Complete(_ context.Context, _ oidcauth.Attempt, code string) (oidcauth.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deny {
		return oidcauth.Identity{}, oidcauth.ErrDenied
	}
	if code != "code" {
		return oidcauth.Identity{}, errors.New("bad code")
	}
	identity := f.next
	identity.Issuer = f.issuer
	return identity, nil
}

func (f *fakeIssuerParty) as(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = oidcauth.Identity{Subject: subject, Email: subject + "@example.com", Name: strings.ToUpper(subject)}
}

func signIn(t *testing.T, identity *app.Identity) app.SignInResult {
	t.Helper()
	target, err := identity.BeginSignIn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state := target[strings.Index(target, "state=")+len("state="):]
	result, err := identity.CompleteSignIn(context.Background(), state, "code")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestClaimTurnsOnTeamModeOnceAndSignsMembersIn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, dir)
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
	if identity.Mode() != domain.SingleUserMode || !identity.Claimable() {
		t.Fatalf("fresh installation mode %s claimable %v", identity.Mode(), identity.Claimable())
	}

	// Before the claim a sign-in yields only a claim token, never a session.
	party.as("alice")
	pending := signIn(t, identity)
	if pending.ClaimToken == "" || pending.SessionToken != "" {
		t.Fatalf("pre-claim sign-in = %+v", pending)
	}
	if _, _, err := identity.Session(ctx, pending.ClaimToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("claim token used as a session: %v", err)
	}
	// The claim needs the bootstrap owner, as the installation bearer
	// authenticates in single-user mode; the system cannot claim.
	if _, err := identity.Claim(principal.WithSystem(ctx), pending.ClaimToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("system claimed: %v", err)
	}
	owner := principal.With(ctx, principal.Owner())
	if _, err := identity.Claim(owner, "not-a-claim"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("claim with an unknown token = %v", err)
	}
	party.as("alice")
	pending = signIn(t, identity)
	session, err := identity.Claim(owner, pending.ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Mode() != domain.TeamMode || identity.Claimable() {
		t.Fatal("claim did not turn on team mode")
	}
	caller, _, err := identity.Session(ctx, session)
	if err != nil || caller.UserID() != principal.BootstrapOwner {
		t.Fatalf("owner session = %+v, %v", caller, err)
	}
	member, err := identity.Member(principal.With(ctx, caller))
	if err != nil || member.Subject != "alice" || member.Role != domain.UserAdmin || member.Email != "alice@example.com" {
		t.Fatalf("claimed owner = %+v, %v", member, err)
	}
	party.as("mallory")
	if result, err := identity.CompleteSignIn(ctx, "state-x", "code"); !errors.Is(err, domain.ErrNotFound) || result.SessionToken != "" {
		t.Fatalf("unknown state = %+v, %v", result, err)
	}

	// After the claim, Alice signs in again as the owner; Bob becomes a
	// member on first sign-in.
	party.as("alice")
	again := signIn(t, identity)
	if caller, _, err := identity.Session(ctx, again.SessionToken); err != nil || caller.UserID() != principal.BootstrapOwner {
		t.Fatalf("owner second sign-in = %+v, %v", caller, err)
	}
	party.as("bob")
	bobResult := signIn(t, identity)
	bob, bobSession, err := identity.Session(ctx, bobResult.SessionToken)
	if err != nil || bob.UserID() == principal.BootstrapOwner {
		t.Fatalf("Bob session = %+v, %v", bob, err)
	}
	bobCtx := principal.With(ctx, bob)
	if member, err := identity.Member(bobCtx); err != nil || member.Role != domain.UserMember || member.Subject != "bob" {
		t.Fatalf("Bob = %+v, %v", member, err)
	}

	// Each member sees and revokes only their own sessions.
	ownerSessions, err := identity.Sessions(principal.With(ctx, caller))
	if err != nil || len(ownerSessions) != 2 {
		t.Fatalf("owner sessions = %+v, %v", ownerSessions, err)
	}
	if err := identity.RevokeSession(bobCtx, ownerSessions[0].ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Bob revoked the owner's session: %v", err)
	}
	if err := identity.SignOut(bobCtx, bobSession); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Session(ctx, bobResult.SessionToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("signed-out session = %v", err)
	}

	// Sessions end after 12 hours idle.
	clock.mu.Lock()
	clock.now = clock.now.Add(13 * time.Hour)
	clock.mu.Unlock()
	if _, _, err := identity.Session(ctx, again.SessionToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("idle session = %v", err)
	}

	// A disabled member cannot sign in.
	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("UPDATE users SET disabled_at = '2026-11-14T00:00:00Z' WHERE subject = 'bob'"); err != nil {
		t.Fatal(err)
	}
	party.as("bob")
	target, err := identity.BeginSignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.CompleteSignIn(ctx, target[strings.Index(target, "state=")+6:], "code"); !errors.Is(err, app.ErrSignInDenied) {
		t.Fatalf("disabled sign-in = %v", err)
	}
	party.deny = true
	target, err = identity.BeginSignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.CompleteSignIn(ctx, target[strings.Index(target, "state=")+6:], "code"); !errors.Is(err, app.ErrSignInDenied) {
		t.Fatalf("denied identity = %v", err)
	}

	// The claim and sign-ins are in the audit log.
	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	page, err := service.ListAuditRecords(principal.WithSystem(ctx), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[domain.AuditAction]int{}
	for _, record := range page.Items {
		actions[record.Action]++
	}
	if actions[domain.AuditInstallationClaim] != 1 || actions[domain.AuditSignIn] != 3 || actions[domain.AuditSignOut] != 1 {
		t.Fatalf("audit actions = %v", actions)
	}

	// The claim is permanent and pins its issuer.
	if _, err := app.NewIdentity(ctx, store, clock, &testIDs{}, &fakeIssuerParty{issuer: "https://other.example"}); err == nil {
		t.Fatal("a claimed installation accepted another issuer")
	}
	reopened, err := app.NewIdentity(ctx, store, clock, &testIDs{}, party)
	if err != nil || reopened.Mode() != domain.TeamMode {
		t.Fatalf("reopened mode = %v, %v", reopened.Mode(), err)
	}
}

func TestPersonalAccessTokensActAsTheirMemberUntilExpiryOrRevocation(t *testing.T) {
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
	owner := principal.With(ctx, principal.Owner())
	// Single-user mode has no personal access tokens.
	if _, err := identity.MintToken(owner, "laptop", false, 0); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("single-user mint = %v", err)
	}
	party.as("alice")
	if _, err := identity.Claim(owner, signIn(t, identity).ClaimToken); err != nil {
		t.Fatal(err)
	}
	party.as("bob")
	bob, _, err := identity.Session(ctx, signIn(t, identity).SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	bobCtx := principal.With(ctx, bob)

	for _, lifetime := range []time.Duration{time.Hour, 366 * 24 * time.Hour} {
		if _, err := identity.MintToken(bobCtx, "laptop", false, lifetime); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("lifetime %s = %v", lifetime, err)
		}
	}
	if _, err := identity.MintToken(bobCtx, "", false, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty name = %v", err)
	}
	minted, err := identity.MintToken(bobCtx, "laptop", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(minted.Value, app.TokenPrefix) || minted.Record.ExpiresAt.Sub(minted.Record.CreatedAt) != 90*24*time.Hour {
		t.Fatalf("minted = %+v", minted.Record)
	}
	caller, readOnly, err := identity.Token(ctx, minted.Value)
	if err != nil || caller.UserID() != bob.UserID() || !readOnly {
		t.Fatalf("token = %+v %v %v", caller, readOnly, err)
	}
	if _, _, err := identity.Token(ctx, app.TokenPrefix+"forged"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("forged token = %v", err)
	}
	// The owner cannot see or revoke Bob's token.
	ownerTokens, err := identity.Tokens(owner)
	if err != nil || len(ownerTokens) != 0 {
		t.Fatalf("owner tokens = %+v, %v", ownerTokens, err)
	}
	if err := identity.RevokeToken(owner, minted.Record.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("owner revoked Bob's token: %v", err)
	}

	// Tokens stop at expiry.
	short, err := identity.MintToken(bobCtx, "short", false, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(25 * time.Hour)
	clock.mu.Unlock()
	if _, _, err := identity.Token(ctx, short.Value); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired token = %v", err)
	}
	if _, _, err := identity.Token(ctx, minted.Value); err != nil {
		t.Fatalf("unexpired token = %v", err)
	}
	if err := identity.RevokeToken(bobCtx, minted.Record.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Token(ctx, minted.Value); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoked token = %v", err)
	}

	// A disabled member's tokens stop working.
	live, err := identity.MintToken(bobCtx, "live", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("UPDATE users SET disabled_at = '2026-11-15T00:00:00Z' WHERE subject = 'bob'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Token(ctx, live.Value); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disabled member's token = %v", err)
	}

	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	page, err := service.ListAuditRecords(principal.WithSystem(ctx), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	created, revoked := 0, 0
	for _, record := range page.Items {
		switch record.Action {
		case domain.AuditTokenCreate:
			created++
		case domain.AuditTokenRevoke:
			revoked++
		}
	}
	if created != 3 || revoked != 1 {
		t.Fatalf("token audit = %d created, %d revoked", created, revoked)
	}
}

func TestAdminsManageMembershipWithoutReachingObjects(t *testing.T) {
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
	var disabledHook []domain.UserID
	identity.OnMemberDisabled(func(_ context.Context, user domain.UserID) { disabledHook = append(disabledHook, user) })
	owner := principal.With(ctx, principal.Owner())
	party.as("alice")
	adminSession, err := identity.Claim(owner, signIn(t, identity).ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := identity.Session(ctx, adminSession)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := principal.With(ctx, admin)
	party.as("bob")
	bobResult := signIn(t, identity)
	bob, _, err := identity.Session(ctx, bobResult.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	bobCtx := principal.With(ctx, bob)
	bobToken, err := identity.MintToken(bobCtx, "laptop", false, 0)
	if err != nil {
		t.Fatal(err)
	}

	// A member cannot administer; an admin and break-glass can.
	if _, err := identity.Members(bobCtx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member listed members: %v", err)
	}
	if err := identity.DisableMember(bobCtx, principal.BootstrapOwner); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member disabled the owner: %v", err)
	}
	members, err := identity.Members(adminCtx)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %+v, %v", members, err)
	}
	breakGlass := principal.With(ctx, principal.BreakGlass())
	if members, err := identity.Members(breakGlass); err != nil || len(members) != 2 {
		t.Fatalf("break-glass members = %+v, %v", members, err)
	}

	// The last enabled admin cannot be disabled or demoted.
	if err := identity.DisableMember(adminCtx, admin.UserID()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last admin disabled: %v", err)
	}
	if err := identity.SetMemberRole(breakGlass, admin.UserID(), domain.UserMember); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("last admin demoted: %v", err)
	}
	if err := identity.SetMemberRole(adminCtx, bob.UserID(), "owner"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown role = %v", err)
	}

	// Disabling Bob ends his session and token and runs the hooks.
	if err := identity.DisableMember(adminCtx, bob.UserID()); err != nil {
		t.Fatal(err)
	}
	if len(disabledHook) != 1 || disabledHook[0] != bob.UserID() {
		t.Fatalf("disable hooks = %v", disabledHook)
	}
	if _, _, err := identity.Session(ctx, bobResult.SessionToken); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disabled session = %v", err)
	}
	if _, _, err := identity.Token(ctx, bobToken.Value); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("disabled token = %v", err)
	}
	if identity.MemberActive(ctx, bob) || !identity.MemberActive(ctx, admin) {
		t.Fatal("MemberActive disagrees with membership")
	}
	party.as("bob")
	target, err := identity.BeginSignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.CompleteSignIn(ctx, target[strings.Index(target, "state=")+6:], "code"); !errors.Is(err, app.ErrSignInDenied) {
		t.Fatalf("disabled sign-in = %v", err)
	}

	// Re-enabled, Bob signs in again; his old credentials stay revoked.
	if err := identity.EnableMember(breakGlass, bob.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Token(ctx, bobToken.Value); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("re-enabled member's old token = %v", err)
	}
	party.as("bob")
	again := signIn(t, identity)
	if _, _, err := identity.Session(ctx, again.SessionToken); err != nil {
		t.Fatalf("re-enabled sign-in = %v", err)
	}

	// With a second admin, the first can step down.
	if err := identity.SetMemberRole(adminCtx, bob.UserID(), domain.UserAdmin); err != nil {
		t.Fatal(err)
	}
	if err := identity.SetMemberRole(adminCtx, admin.UserID(), domain.UserMember); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Members(adminCtx); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("demoted admin still administers: %v", err)
	}
	if err := identity.RevokeMemberCredentials(principal.With(ctx, bob), admin.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := identity.Session(ctx, adminSession); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoked admin session = %v", err)
	}

	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	page, err := service.ListAuditRecords(principal.WithSystem(ctx), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[domain.AuditAction]int{}
	for _, record := range page.Items {
		actions[record.Action]++
	}
	if actions[domain.AuditMemberDisable] != 1 || actions[domain.AuditMemberEnable] != 1 ||
		actions[domain.AuditMemberRole] != 2 || actions[domain.AuditMemberRevoke] != 1 {
		t.Fatalf("membership audit = %v", actions)
	}
}
