package app_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type rebindFixture struct {
	identity     *app.Identity
	store        *sqlite.Store
	party        *fakeIssuerParty
	clock        *testClock
	owner        context.Context
	admin        context.Context
	ownerSession string
	ownerPAT     string
	bob          principal.Principal
	bobSession   string
}

func newRebindFixture(t *testing.T) rebindFixture {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
	party := &fakeIssuerParty{issuer: fakeIssuer}
	identity, err := app.NewIdentity(ctx, store, clock, &testIDs{}, party)
	if err != nil {
		t.Fatal(err)
	}
	party.as("alice")
	owner := operatorContext()
	token, err := identity.Claim(owner, signIn(t, identity).ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	minted, err := identity.MintToken(owner, "laptop", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	party.as("bob")
	bobLogin := signIn(t, identity)
	bob, _, err := identity.Session(ctx, bobLogin.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	return rebindFixture{identity, store, party, clock, owner, principal.With(ctx, principal.BreakGlass()), token, minted.Value, bob, bobLogin.SessionToken}
}

func verifyRecovery(t *testing.T, f rebindFixture, recovery app.IdentityRebind, subject string) {
	t.Helper()
	f.party.as(subject)
	address, err := url.Parse(recovery.SignInURL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.identity.CompleteSignIn(context.Background(), address.Query().Get("state"), "code")
	if err != nil || !result.RebindReady || result.ClaimToken != "" || result.SessionToken != "" {
		t.Fatalf("recovery callback = %+v, %v", result, err)
	}
	if _, err := f.identity.CompleteSignIn(context.Background(), address.Query().Get("state"), "code"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("callback replay = %v", err)
	}
}

func TestIdentityRebindRequiresBreakGlassAndPreservesOwnership(t *testing.T) {
	f := newRebindFixture(t)
	id := principal.BootstrapOwner
	for _, ctx := range []context.Context{context.Background(), f.owner, principal.WithSystem(f.owner), principal.With(f.owner, f.bob)} {
		if _, err := f.identity.BeginIdentityRebind(ctx, id); err == nil {
			t.Fatal("non-break-glass caller started recovery")
		}
		if _, err := f.identity.CompleteIdentityRebind(ctx, id, "bad"); err == nil {
			t.Fatal("non-break-glass caller completed recovery")
		}
	}
	service := app.NewService(f.store, f.clock, &testIDs{}, &recordingQueue{})
	project, err := service.CreateProject(f.owner, "keep", "keep")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := f.identity.BeginIdentityRebind(f.admin, id)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.identity.CompleteIdentityRebind(f.admin, id, recovery.Token); done || err != nil {
		t.Fatalf("before callback = %v, %v", done, err)
	}
	verifyRecovery(t, f, recovery, "replacement")
	// The callback does not create a new member or session, nor accept the
	// completion token as a user credential.
	members, err := f.identity.Members(f.admin)
	if err != nil || len(members) != 2 {
		t.Fatalf("callback created membership: %+v, %v", members, err)
	}
	if _, _, err := f.identity.Session(f.owner, recovery.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("recovery token used as session: %v", err)
	}
	if _, err := f.identity.CompleteIdentityRebind(f.admin, f.bob.UserID(), recovery.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("token transplanted to another member: %v", err)
	}
	var dropped []domain.UserID
	f.identity.OnMemberDisabled(func(_ context.Context, id domain.UserID) { dropped = append(dropped, id) })
	if done, err := f.identity.CompleteIdentityRebind(f.admin, id, recovery.Token); !done || err != nil {
		t.Fatalf("complete recovery = %v, %v", done, err)
	}
	if len(dropped) != 1 || dropped[0] != id {
		t.Fatalf("live credential revocation = %v", dropped)
	}
	if _, err := f.identity.CompleteIdentityRebind(f.admin, id, recovery.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("replayed recovery: %v", err)
	}
	if _, _, err := f.identity.Session(f.owner, f.ownerSession); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old session survived: %v", err)
	}
	if _, _, err := f.identity.Token(f.owner, f.ownerPAT); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old token survived: %v", err)
	}
	if _, _, err := f.identity.Session(f.owner, f.bobSession); err != nil {
		t.Fatalf("another member's session revoked: %v", err)
	}
	member, err := f.identity.Member(f.owner)
	if err != nil || member.Subject != "replacement" || member.Role != domain.UserAdmin || !member.DisabledAt.IsZero() || member.Email != "replacement@example.com" {
		t.Fatalf("rebound member = %+v, %v", member, err)
	}
	if _, err := service.GetProject(f.owner, project.ID); err != nil {
		t.Fatalf("lost objects: %v", err)
	}
	f.party.as("replacement")
	login := signIn(t, f.identity)
	caller, _, err := f.identity.Session(f.owner, login.SessionToken)
	if err != nil || caller.UserID() != id {
		t.Fatalf("replacement sign-in = %+v, %v", caller, err)
	}
	page, err := service.ListAuditRecords(f.admin, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range page.Items {
		if record.Action == domain.AuditMemberIdentityRebind {
			count++
			if record.PrincipalKind != "break_glass" || record.ObjectID != string(id) || record.Result != domain.AuditAllowed {
				t.Fatalf("rebind audit = %+v", record)
			}
		}
		if strings.Contains(record.ObjectID, recovery.Token) || strings.Contains(record.ObjectID, "replacement") {
			t.Fatal("recovery secret or claims leaked to audit")
		}
	}
	if count != 1 {
		t.Fatalf("rebind audit count = %d", count)
	}
}

func TestIdentityRebindRefusesCollisionExpiryAndConcurrentChange(t *testing.T) {
	for _, scenario := range []string{"collision", "expiry", "concurrent", "disabled", "audit failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRebindFixture(t)
			id := principal.BootstrapOwner
			if scenario == "disabled" {
				// Recovering a disabled member must not re-enable them or grant admin.
				id = f.bob.UserID()
				if err := f.identity.DisableMember(f.admin, id); err != nil {
					t.Fatal(err)
				}
			}
			recovery, err := f.identity.BeginIdentityRebind(f.admin, id)
			if err != nil {
				t.Fatal(err)
			}
			subject := "replacement"
			if scenario == "collision" {
				subject = "bob"
			}
			verifyRecovery(t, f, recovery, subject)
			var raw *sql.DB
			if scenario == "expiry" {
				f.clock.mu.Lock()
				f.clock.now = f.clock.now.Add(11 * time.Minute)
				f.clock.mu.Unlock()
			}
			if scenario == "concurrent" {
				second, err := f.identity.BeginIdentityRebind(f.admin, id)
				if err != nil {
					t.Fatal(err)
				}
				verifyRecovery(t, f, second, "another")
				if _, err := f.identity.CompleteIdentityRebind(f.admin, id, second.Token); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "audit failure" {
				raw, err = sql.Open("sqlite", f.store.Path())
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON audit_records BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			done, err := f.identity.CompleteIdentityRebind(f.admin, id, recovery.Token)
			if scenario == "disabled" {
				if err != nil || !done {
					t.Fatalf("disabled member recovery = %v, %v", done, err)
				}
				system := principal.WithSystem(context.Background())
				if err := f.store.View(system, func(r ports.Reader) error {
					user, err := r.GetUser(system, id)
					if err == nil && (user.DisabledAt.IsZero() || user.Role != domain.UserMember) {
						t.Fatalf("disabled member changed: %+v", user)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || done {
				t.Fatalf("unsafe recovery = %v, %v", done, err)
			}
			if scenario == "collision" || scenario == "audit failure" {
				user, err := f.identity.Member(f.owner)
				if err != nil || user.Subject != "alice" {
					t.Fatalf("partial identity change = %+v, %v", user, err)
				}
				if _, _, err := f.identity.Session(f.owner, f.ownerSession); err != nil {
					t.Fatalf("partial session revocation: %v", err)
				}
				if _, _, err := f.identity.Token(f.owner, f.ownerPAT); err != nil {
					t.Fatalf("partial token revocation: %v", err)
				}
			}
		})
	}
}

func TestIdentityRebindIsSingleUseUnderConcurrentCompletion(t *testing.T) {
	f := newRebindFixture(t)
	recovery, err := f.identity.BeginIdentityRebind(f.admin, principal.BootstrapOwner)
	if err != nil {
		t.Fatal(err)
	}
	verifyRecovery(t, f, recovery, "replacement")
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, err := f.identity.CompleteIdentityRebind(f.admin, principal.BootstrapOwner, recovery.Token)
			results <- err
		}()
	}
	close(start)
	succeeded, refused := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, domain.ErrNotFound) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("concurrent outcomes = %d success, %d refused", succeeded, refused)
	}
}

func TestIdentityRebindBoundsPendingCeremoniesAndChecksGroupPolicy(t *testing.T) {
	f := newRebindFixture(t)
	if _, err := f.identity.BeginIdentityRebind(f.admin, "missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing target = %v", err)
	}
	var first app.IdentityRebind
	for index := 0; index < 8; index++ {
		recovery, err := f.identity.BeginIdentityRebind(f.admin, principal.BootstrapOwner)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = recovery
		}
	}
	if _, err := f.identity.BeginIdentityRebind(f.admin, principal.BootstrapOwner); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("unbounded recovery state: %v", err)
	}
	f.party.deny = true
	address, _ := url.Parse(first.SignInURL)
	if _, err := f.identity.CompleteSignIn(context.Background(), address.Query().Get("state"), "code"); !errors.Is(err, app.ErrSignInDenied) {
		t.Fatalf("group policy bypass = %v", err)
	}
	if done, err := f.identity.CompleteIdentityRebind(f.admin, principal.BootstrapOwner, first.Token); done || err != nil {
		t.Fatalf("denied identity verified: %v, %v", done, err)
	}
	f.clock.mu.Lock()
	f.clock.now = f.clock.now.Add(11 * time.Minute)
	f.clock.mu.Unlock()
	if _, err := f.identity.BeginIdentityRebind(f.admin, principal.BootstrapOwner); err != nil {
		t.Fatalf("expired recovery state not pruned: %v", err)
	}
}
