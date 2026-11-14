package app_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

// twoOwners is an installation with the bootstrap owner (Alice) and a
// second member (Bob). Team mode is not configurable yet, so Bob's users
// row is written directly, as the membership step will.
type twoOwners struct {
	service *app.Service
	alice   context.Context
	bob     context.Context
}

func newTwoOwners(t *testing.T) twoOwners {
	t.Helper()
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw, err := sql.Open("sqlite", store.Path()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO users(id, role, created_at, updated_at)
		VALUES ('usr_bob', 'member', '2026-11-13T00:00:00Z', '2026-11-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)},
		&testIDs{}, &recordingQueue{})
	service.ConfigureSecrets(key)
	bob, ok := principal.User("usr_bob")
	if !ok {
		t.Fatal("Bob principal")
	}
	return twoOwners{service: service, alice: ctx, bob: principal.With(context.Background(), bob)}
}

func TestAnotherOwnersObjectsDoNotExist(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	project, err := s.CreateProject(f.alice, "alice", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(f.alice, project.ID, "capsule", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}
	if capsule.OwnerUserID != principal.BootstrapOwner {
		t.Fatalf("created Capsule owner = %q", capsule.OwnerUserID)
	}

	// Bob sees none of Alice's work, by list or by id, and cannot act on it.
	notFound := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Bob %s = %v, want not found", name, err)
		}
	}
	if page, err := s.ListProjects(f.bob, 0, 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("Bob's Projects = %+v, %v", page.Items, err)
	}
	_, err = s.GetProject(f.bob, project.ID)
	notFound("GetProject", err)
	_, err = s.GetCapsule(f.bob, capsule.ID)
	notFound("GetCapsule", err)
	_, err = s.ListCapsules(f.bob, project.ID, "", 0, 50)
	notFound("ListCapsules", err)
	_, err = s.CreateCapsule(f.bob, project.ID, "intruder", "intruder-key")
	notFound("CreateCapsule in Alice's Project", err)
	_, err = s.PauseCapsule(f.bob, capsule.ID, capsule.ResourceVersion, "pause-key")
	notFound("PauseCapsule", err)
	_, err = s.DeleteProject(f.bob, project.ID, project.ResourceVersion, "delete-key")
	notFound("DeleteProject", err)
	if board, err := s.Board(f.bob, app.BoardQuery{}); err != nil || len(board.Items) != 0 {
		t.Fatalf("Bob's board = %+v, %v", board.Items, err)
	}
	_, err = s.Board(f.bob, app.BoardQuery{ProjectID: project.ID})
	notFound("Board for Alice's Project", err)

	// Bob's same idempotency key creates his own Project; it does not
	// replay Alice's.
	mine, err := s.CreateProject(f.bob, "bob", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if mine.ID == project.ID || mine.OwnerUserID != "usr_bob" {
		t.Fatalf("Bob's Project = %+v", mine)
	}
	if page, err := s.ListProjects(f.alice, 0, 50); err != nil || len(page.Items) != 1 || page.Items[0].ID != project.ID {
		t.Fatalf("Alice's Projects = %+v, %v", page.Items, err)
	}
	if page, err := s.ListProjects(f.bob, 0, 50); err != nil || len(page.Items) != 1 || page.Items[0].ID != mine.ID {
		t.Fatalf("Bob's Projects = %+v, %v", page.Items, err)
	}

	// The break-glass admin reaches no object; the system reaches every
	// owner's, one at a time.
	admin := principal.With(context.Background(), principal.BreakGlass())
	if page, err := s.ListProjects(admin, 0, 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("break-glass Projects = %+v, %v", page.Items, err)
	}
	_, err = s.GetProject(admin, project.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("break-glass GetProject = %v", err)
	}
	system := principal.WithSystem(context.Background())
	for _, id := range []domain.ProjectID{project.ID, mine.ID} {
		if _, err := s.GetProject(system, id); err != nil {
			t.Fatalf("system GetProject(%s) = %v", id, err)
		}
	}
	if page, err := s.ListProjects(system, 0, 50); err != nil || len(page.Items) != 2 {
		t.Fatalf("system Projects = %+v, %v", page.Items, err)
	}

	// A Capsule acts within its owner's rows only.
	asCapsule, ok := principal.Capsule(capsule.OwnerUserID, capsule.ID)
	if !ok {
		t.Fatal("Capsule principal")
	}
	capsuleCtx := principal.With(context.Background(), asCapsule)
	if _, err := s.GetProject(capsuleCtx, project.ID); err != nil {
		t.Fatalf("Capsule reading its owner's Project = %v", err)
	}
	_, err = s.GetProject(capsuleCtx, mine.ID)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Capsule reading another owner's Project = %v", err)
	}
}

func TestSecretNamesArePerOwner(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	input := app.SecretInput{Purpose: domain.SecretGitHubAPI, Value: "alice-token"}
	alice, err := s.PutSecret(f.alice, "github_api", input, 0, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	input.Value = "bob-token"
	bob, err := s.PutSecret(f.bob, "github_api", input, 0, "secret-key")
	if err != nil {
		t.Fatalf("Bob's secret with Alice's name = %v", err)
	}
	if alice.ID == bob.ID || alice.OwnerUserID == bob.OwnerUserID {
		t.Fatalf("secrets = %+v / %+v", alice, bob)
	}
	for who, ctx := range map[string]context.Context{"alice": f.alice, "bob": f.bob} {
		page, err := s.ListSecrets(ctx, 0, 50)
		if err != nil || len(page.Items) != 1 || page.Items[0].Name != "github_api" {
			t.Fatalf("%s's secrets = %+v, %v", who, page.Items, err)
		}
	}
	// Deleting by name deletes the caller's secret, never the other one.
	if _, err := s.DeleteSecret(f.bob, "github_api", bob.ResourceVersion, "delete-key"); err != nil {
		t.Fatal(err)
	}
	if page, err := s.ListSecrets(f.alice, 0, 50); err != nil || len(page.Items) != 1 || page.Items[0].ID != alice.ID {
		t.Fatalf("Alice's secrets after Bob's delete = %+v, %v", page.Items, err)
	}
	if page, err := s.ListSecrets(f.bob, 0, 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("Bob's secrets after delete = %+v, %v", page.Items, err)
	}
}

func TestDaemonOnlyMethodsRequireTheSystem(t *testing.T) {
	f := newTwoOwners(t)
	for name, call := range map[string]func(context.Context) error{
		"RecoverRuns":         f.service.RecoverRuns,
		"RecoverThreads":      f.service.RecoverThreads,
		"RefreshLiveRuntime":  f.service.RefreshLiveRuntime,
		"RecoverDeliveries":   f.service.RecoverDeliveries,
		"RecoverProjectThrds": f.service.RecoverProjectThreads,
	} {
		if err := call(f.alice); !errors.Is(err, principal.ErrMissing) {
			t.Fatalf("user ran %s: %v", name, err)
		}
	}
	if err := f.service.RecoverRuns(principal.WithSystem(context.Background())); err != nil {
		t.Fatalf("system RecoverRuns = %v", err)
	}
}
