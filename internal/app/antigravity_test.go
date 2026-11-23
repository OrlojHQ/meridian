package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

const agyLink = "https://accounts.google.com/o/oauth2/auth?client_id=" + antigravityauth.ClientID +
	"&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback&response_type=code&state=s"

func agyProfile(email, refresh string) []antigravityauth.File {
	return []antigravityauth.File{
		{Path: antigravityauth.TokenFile, Content: []byte(`{"token":{"access_token":"access-marker","token_type":"Bearer","refresh_token":"` + refresh + `"},"auth_method":"consumer","id_token_email":"` + email + `"}`)},
		{Path: antigravityauth.StateFile, Content: []byte(`{"methodId":"oauth-personal"}`)},
	}
}

// fakeSignInCapsules stands in for the provider and capsuled: it records
// which sign-in Capsules exist and runs agy's login as a lookup.
type fakeSignInCapsules struct {
	ports.CapsuleProvider
	mu       sync.Mutex
	live     map[string]bool
	created  []ports.CreateCapsuleRequest
	link     string
	refresh  string
	createOK chan struct{}
}

func newFakeSignInCapsules() *fakeSignInCapsules {
	return &fakeSignInCapsules{live: map[string]bool{}, link: agyLink, refresh: "refresh-marker"}
}

func (f *fakeSignInCapsules) ResourceID(id domain.CapsuleID) string { return "res-" + string(id) }

func (f *fakeSignInCapsules) Create(ctx context.Context, request ports.CreateCapsuleRequest) (ports.ProviderResource, error) {
	if f.createOK != nil {
		select {
		case <-f.createOK:
		case <-ctx.Done():
			return ports.ProviderResource{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, request)
	id := f.ResourceID(request.CapsuleID)
	f.live[id] = true
	return ports.ProviderResource{ID: id, State: ports.ProviderReady}, nil
}

func (f *fakeSignInCapsules) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, id)
	return nil
}

func (f *fakeSignInCapsules) StartHarnessSignIn(_ context.Context, id, harness string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live[id] || harness != "agy" {
		return "", domain.ErrNotFound
	}
	return f.link, nil
}

func (f *fakeSignInCapsules) FinishHarnessSignIn(_ context.Context, id, code string) ([]antigravityauth.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live[id] {
		return nil, domain.ErrNotFound
	}
	if code != "4/0good" {
		return nil, ports.ErrHarnessSignInRejected
	}
	return agyProfile("dev@example.com", f.refresh), nil
}

func (f *fakeSignInCapsules) CancelHarnessSignIn(context.Context, string) error { return nil }

func (f *fakeSignInCapsules) liveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

func antigravityService(t *testing.T) (*Service, ports.Store, *fakeSignInCapsules) {
	t.Helper()
	ctx := operatorContext()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	s := NewService(db, connectionClock{}, &connectionIDs{}, connectionQueue{})
	s.ConfigureSecrets(key)
	s.ConfigureHarnessPacks([]domain.HarnessPack{{HarnessImage: domain.HarnessImage{Name: "agy", ImageReference: "meridian-capsule-agy:test"}}})
	fake := newFakeSignInCapsules()
	s.ConfigureAntigravity(fake, fake)
	return s, db, fake
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitCode(t *testing.T, s *Service, id string) AntigravitySignIn {
	t.Helper()
	var view AntigravitySignIn
	waitFor(t, "awaiting_code", func() bool {
		var err error
		view, err = s.AntigravitySignInStatus(operatorContext(), id)
		return err == nil && view.State != AntigravityStarting
	})
	return view
}

func TestAntigravitySignInSealsAgyProfile(t *testing.T) {
	ctx := operatorContext()
	s, db, fake := antigravityService(t)
	if _, err := s.StartAntigravitySignIn(ctx, ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start before the sweep: %v", err)
	}
	if err := s.SweepAntigravitySignIns(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	if !s.AntigravityAvailable() {
		t.Fatal("Antigravity sign-in unavailable")
	}
	start, err := s.StartAntigravitySignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if start.State != AntigravityStarting || start.AuthorizationURL != "" {
		t.Fatalf("start = %+v", start)
	}
	view := awaitCode(t, s, start.ID)
	if view.State != AntigravityAwaitingCode || view.AuthorizationURL != agyLink {
		t.Fatalf("view = %+v", view)
	}
	request := fake.created[0]
	if request.ImageReference != "meridian-capsule-agy:test" || request.RepositoryURL != "" || request.GitCredential != nil || len(request.Setup) != 0 {
		t.Fatalf("sign-in Capsule = %+v", request)
	}
	if _, err := s.SendAntigravitySignInCode(ctx, start.ID, "4/0 two"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("malformed code: %v", err)
	}
	done, err := s.SendAntigravitySignInCode(ctx, start.ID, " 4/0good ")
	if err != nil {
		t.Fatal(err)
	}
	if done.State != AntigravityCompleted || done.ConnectionID == "" || done.Account != "dev@example.com" {
		t.Fatalf("done = %+v", done)
	}
	waitFor(t, "sign-in Capsule deletion", func() bool { return fake.liveCount() == 0 })
	stored := connectionByID(t, db, done.ConnectionID)
	if stored.Provider != "antigravity" || stored.Account != "dev@example.com" || stored.CredentialGeneration != 1 || stored.Revoked ||
		bytes.Contains(stored.Ciphertext, []byte("refresh-marker")) || bytes.Contains(stored.Ciphertext, []byte("access-marker")) {
		t.Fatalf("stored = %+v", stored)
	}
	if _, err := s.SendAntigravitySignInCode(ctx, start.ID, "4/0good"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second code: %v", err)
	}
	// Another principal never sees the attempt.
	other, _ := principal.User("someone-else")
	if _, err := s.AntigravitySignInStatus(principal.With(context.Background(), other), start.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other user's status: %v", err)
	}
	// The generic save cannot create or rekey this type.
	if _, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "Google", Provider: "antigravity", APIKey: "pasted-token"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("create through save: %v", err)
	}
	if _, err := s.PutProviderConnection(ctx, done.ConnectionID, ConnectionInput{Name: "Google", Provider: "antigravity", APIKey: "pasted-token", ExpectedResourceVersion: stored.ResourceVersion}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("rekey through save: %v", err)
	}
}

func TestAntigravityRejectedCodeAndCancelFreeTheCapsule(t *testing.T) {
	ctx := operatorContext()
	s, _, fake := antigravityService(t)
	if err := s.SweepAntigravitySignIns(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	start, err := s.StartAntigravitySignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitCode(t, s, start.ID)
	failed, err := s.SendAntigravitySignInCode(ctx, start.ID, "4/0wrong")
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != AntigravityFailed || !strings.Contains(failed.Failure, "did not accept") {
		t.Fatalf("failed = %+v", failed)
	}
	waitFor(t, "sign-in Capsule deletion", func() bool { return fake.liveCount() == 0 })

	// Cancel while the Capsule is still starting.
	fake.createOK = make(chan struct{})
	pending, err := s.StartAntigravitySignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelAntigravitySignIn(ctx, pending.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AntigravitySignInStatus(ctx, pending.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancelled status: %v", err)
	}
	waitFor(t, "every slot free", func() bool {
		s.antigravity.mu.Lock()
		defer s.antigravity.mu.Unlock()
		for _, busy := range s.antigravity.busy {
			if busy {
				return false
			}
		}
		return true
	})
	if fake.liveCount() != 0 {
		t.Fatal("a cancelled sign-in left its Capsule")
	}
	// Every slot taken by a different user refuses a further attempt.
	close(fake.createOK)
	fake.createOK = nil
	userContext := func(n int) context.Context {
		user, _ := principal.User(principal.UserID("user-" + string(rune('a'+n))))
		return principal.With(context.Background(), user)
	}
	for n := range antigravitySlots {
		if _, err := s.StartAntigravitySignIn(userContext(n), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StartAntigravitySignIn(userContext(antigravitySlots), ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start beyond the slots: %v", err)
	}
}

func TestAntigravityNewSignInReplacesTheUsersEarlierOne(t *testing.T) {
	ctx := operatorContext()
	s, _, fake := antigravityService(t)
	if err := s.SweepAntigravitySignIns(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	first, err := s.StartAntigravitySignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	awaitCode(t, s, first.ID)
	// Another user's attempt is left alone.
	other, _ := principal.User("someone-else")
	otherCtx := principal.With(context.Background(), other)
	theirs, err := s.StartAntigravitySignIn(otherCtx, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartAntigravitySignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.AntigravitySignInStatus(ctx, first.ID)
	if err != nil || old.State != AntigravityFailed || !strings.Contains(old.Failure, "newer") {
		t.Fatalf("replaced attempt = %+v, %v", old, err)
	}
	if _, err := s.SendAntigravitySignInCode(ctx, first.ID, "4/0good"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("code for the replaced attempt: %v", err)
	}
	if view := awaitCode(t, s, second.ID); view.State != AntigravityAwaitingCode {
		t.Fatalf("new attempt = %+v", view)
	}
	waitFor(t, "the other user's attempt", func() bool {
		view, err := s.AntigravitySignInStatus(otherCtx, theirs.ID)
		return err == nil && view.State == AntigravityAwaitingCode
	})
	// The replaced attempt's Capsule is deleted; the two live ones remain.
	waitFor(t, "the replaced Capsule's deletion", func() bool { return fake.liveCount() == 2 })
}

func TestAntigravityProfileReachesOnlyAgyAndSignOutStopsIt(t *testing.T) {
	ctx := operatorContext()
	s, db, fake := antigravityService(t)
	if err := s.SweepAntigravitySignIns(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	signIn := func(connection string) AntigravitySignIn {
		t.Helper()
		start, err := s.StartAntigravitySignIn(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		awaitCode(t, s, start.ID)
		done, err := s.SendAntigravitySignInCode(ctx, start.ID, "4/0good")
		if err != nil || done.State != AntigravityCompleted {
			t.Fatalf("sign-in = %+v, %v", done, err)
		}
		waitFor(t, "sign-in Capsule deletion", func() bool { return fake.liveCount() == 0 })
		return done
	}
	done := signIn("")
	project, capsule := readyCapsule(t, s, db)
	if err := s.GrantProviderConnection(ctx, project.ID, "codex", done.ConnectionID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("grant to codex: %v", err)
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "agy", done.ConnectionID); err != nil {
		t.Fatal(err)
	}
	// No provider gateway is configured: this connection does not need one.
	runtime, err := s.runtimeProviderConnection(ctx, capsule, "agy-structured")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Provider != "antigravity" || runtime.URL != "" || runtime.Token != "" {
		t.Fatalf("runtime = %+v", runtime)
	}
	if account, err := antigravityauth.Check(runtime.Profile); err != nil || account != "dev@example.com" {
		t.Fatalf("profile = %q, %v", account, err)
	}

	// agy reporting auth_required marks the connection, and the next start
	// asks for a new sign-in instead of sending the rejected profile.
	s.noteAntigravityAuthRequired(ctx, capsule, "agy-structured")
	marked := connectionByID(t, db, done.ConnectionID)
	if !marked.NeedsSignIn {
		t.Fatal("connection not marked")
	}
	if _, err := s.runtimeProviderConnection(ctx, capsule, "agy"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start after auth_required: %v", err)
	}
	// Signing in again replaces the profile and clears the mark. The old
	// Capsule's failure no longer applies to the new generation.
	fake.refresh = "refresh-second"
	signIn(done.ConnectionID)
	again := connectionByID(t, db, done.ConnectionID)
	if again.NeedsSignIn || again.CredentialGeneration != 2 {
		t.Fatalf("after sign-in again = %+v", again)
	}
	s.noteAntigravityAuthRequired(ctx, capsule, "agy")
	if connectionByID(t, db, done.ConnectionID).NeedsSignIn {
		t.Fatal("a stale profile's failure marked the new sign-in")
	}
	runtime, err = s.runtimeProviderConnection(ctx, capsule, "agy")
	if err != nil || !bytes.Contains(runtime.Profile[0].Content, []byte("refresh-second")) {
		t.Fatalf("runtime after sign-in again: %v", err)
	}

	// Sign-out keeps the row with an empty envelope; no Capsule receives it.
	current := connectionByID(t, db, done.ConnectionID)
	out, err := s.PutProviderConnection(ctx, done.ConnectionID, ConnectionInput{Name: current.Name, Provider: "antigravity", Revoked: true, ExpectedResourceVersion: current.ResourceVersion})
	if err != nil || !out.Revoked {
		t.Fatalf("sign out = %+v, %v", out, err)
	}
	if _, err := s.runtimeProviderConnection(ctx, capsule, "agy"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start after sign-out: %v", err)
	}
	if _, err := s.StartAntigravitySignIn(ctx, done.ConnectionID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("sign in again after sign-out: %v", err)
	}
}

func TestAntigravityUnavailableWithoutAgyPack(t *testing.T) {
	s, _, _ := antigravityService(t)
	s.ConfigureHarnessPacks(nil)
	if s.AntigravityAvailable() {
		t.Fatal("available without an agy pack")
	}
	if _, err := s.StartAntigravitySignIn(operatorContext(), ""); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("start: %v", err)
	}
}

func TestAntigravitySignInRefusesAForeignLink(t *testing.T) {
	ctx := operatorContext()
	s, _, fake := antigravityService(t)
	fake.link = strings.Replace(agyLink, "accounts.google.com", "accounts.google.com.evil.test", 1)
	if err := s.SweepAntigravitySignIns(asSystem(ctx)); err != nil {
		t.Fatal(err)
	}
	start, err := s.StartAntigravitySignIn(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	view := awaitCode(t, s, start.ID)
	if view.State != AntigravityFailed || view.AuthorizationURL != "" {
		t.Fatalf("view = %+v", view)
	}
	waitFor(t, "sign-in Capsule deletion", func() bool { return fake.liveCount() == 0 })
}
