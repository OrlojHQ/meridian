package app_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

func disableMember(t *testing.T, f twoOwners, id domain.UserID) {
	t.Helper()
	raw, err := sql.Open("sqlite", f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("UPDATE users SET disabled_at = '2026-11-14T00:00:00Z' WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
}

func TestBreakGlassTransfersADepartedMembersWork(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	if err := s.ConfigureProviderGateway("https://meridian.example", false); err != nil {
		t.Fatal(err)
	}
	defer s.CloseProviderGateway()
	breakGlass := principal.With(context.Background(), principal.BreakGlass())
	project, err := s.CreateProject(f.bob, "bob", "bob-project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(f.bob, project.ID, "capsule", "bob-capsule")
	if err != nil {
		t.Fatal(err)
	}
	input := app.SecretInput{Purpose: domain.SecretGitHubAPI, Value: "bob-token"}
	if _, err := s.PutSecret(f.bob, "github_api", input, 0, "bob-secret"); err != nil {
		t.Fatal(err)
	}
	connection, err := s.PutProviderConnection(f.bob, "", app.ConnectionInput{Name: "OpenAI", Provider: "openai", APIKey: "bob-upstream-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantProviderConnection(f.bob, project.ID, "codex", connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSecret(f.alice, "github_api", app.SecretInput{Purpose: domain.SecretGitHubAPI, Value: "alice-token"}, 0, "alice-secret"); err != nil {
		t.Fatal(err)
	}

	// Only break-glass transfers, and only a disabled member's work.
	if err := s.TransferDepartedMember(f.alice, "usr_bob", principal.BootstrapOwner); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an admin transferred: %v", err)
	}
	if err := s.TransferDepartedMember(breakGlass, "usr_bob", principal.BootstrapOwner); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("an enabled member's work moved: %v", err)
	}
	disableMember(t, f, "usr_bob")
	// Alice already has a secret named github_api.
	if err := s.TransferDepartedMember(breakGlass, "usr_bob", principal.BootstrapOwner); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("secret name clash = %v", err)
	}
	page, err := s.ListSecrets(f.alice, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteSecret(f.alice, "github_api", page.Items[0].ResourceVersion, "alice-delete"); err != nil {
		t.Fatal(err)
	}
	if err := s.TransferDepartedMember(breakGlass, "usr_bob", principal.BootstrapOwner); err != nil {
		t.Fatal(err)
	}

	// Alice now owns Bob's Project, Capsule, and secret; the secret still
	// opens, now bound to her.
	if _, err := s.GetProject(f.alice, project.ID); err != nil {
		t.Fatalf("transferred Project = %v", err)
	}
	moved, err := s.GetCapsule(f.alice, capsule.ID)
	if err != nil || moved.OwnerUserID != principal.BootstrapOwner {
		t.Fatalf("transferred Capsule = %+v, %v", moved, err)
	}
	system := principal.WithSystem(context.Background())
	if err := f.store.View(system, func(reader ports.Reader) error {
		secret, err := reader.GetSecret(system, principal.BootstrapOwner, "github_api")
		if err != nil {
			return err
		}
		payload, err := f.key.Open(secret)
		if err != nil || payload.Value != "bob-token" {
			t.Fatalf("transferred secret = %+v, %v", payload, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Bob's provider login stays his, and its Project grant is gone.
	if grant, err := s.ProjectProviderConnection(f.alice, project.ID, "codex"); err != nil || grant != "" {
		t.Fatalf("Project grant after transfer = %q, %v", grant, err)
	}
	connections, err := s.ListProviderConnections(f.alice)
	if err != nil || len(connections) != 0 {
		t.Fatalf("Alice's connections = %+v, %v", connections, err)
	}
	records := auditActions(t, s, f.alice)
	found := false
	for _, record := range records {
		found = found || (record.Action == domain.AuditMemberTransfer && record.PrincipalKind == "break_glass")
	}
	if !found {
		t.Fatalf("no transfer record in %+v", records)
	}
}

func TestBreakGlassDeletesADepartedMembersWork(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	breakGlass := principal.With(context.Background(), principal.BreakGlass())
	project, err := s.CreateProject(f.bob, "bob", "bob-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSecret(f.bob, "github_api", app.SecretInput{Purpose: domain.SecretGitHubAPI, Value: "bob-token"}, 0, "bob-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteDepartedMember(breakGlass, "usr_bob"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("an enabled member's work deleted: %v", err)
	}
	disableMember(t, f, "usr_bob")
	if _, err := s.DeleteDepartedMember(f.alice, "usr_bob"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an admin deleted: %v", err)
	}
	result, err := s.DeleteDepartedMember(breakGlass, "usr_bob")
	if err != nil {
		t.Fatal(err)
	}
	if result.Projects != 1 || result.Secrets != 1 {
		t.Fatalf("deleted = %+v", result)
	}
	system := principal.WithSystem(context.Background())
	deleting, err := s.GetProject(system, project.ID)
	if err != nil || (deleting.State != domain.ProjectDeleting && deleting.State != domain.ProjectDeleted) {
		t.Fatalf("departed Project = %+v, %v", deleting, err)
	}
	if err := f.store.View(system, func(reader ports.Reader) error {
		if _, err := reader.GetSecret(system, "usr_bob", "github_api"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("departed secret = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Repeating finds nothing new to delete.
	again, err := s.DeleteDepartedMember(breakGlass, "usr_bob")
	if err != nil || again.Secrets != 0 {
		t.Fatalf("second delete = %+v, %v", again, err)
	}
}
