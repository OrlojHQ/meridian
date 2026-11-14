package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/principal"
)

func auditActions(t *testing.T, s *app.Service, ctx context.Context) []domain.AuditRecord {
	t.Helper()
	page, err := s.ListAuditRecords(ctx, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestAuditRecordsActionsDenialsAndFailures(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	input := app.SecretInput{Purpose: domain.SecretGitHubAPI, Value: "alice-token"}
	secret, err := s.PutSecret(f.alice, "github_api", input, 0, "put-1")
	if err != nil {
		t.Fatal(err)
	}
	// A replay performs nothing new and records nothing new.
	if _, err := s.PutSecret(f.alice, "github_api", input, 0, "put-1"); err != nil {
		t.Fatal(err)
	}
	// A stale version is a failed write.
	if _, err := s.PutSecret(f.alice, "github_api", input, secret.ResourceVersion+5, "put-2"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale secret write = %v", err)
	}
	if _, err := s.DeleteSecret(f.alice, "github_api", secret.ResourceVersion, "delete-1"); err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(f.alice, "alice", "project-1")
	if err != nil {
		t.Fatal(err)
	}
	// Bob naming Alice's Project is a denied cross-owner access; Bob
	// listing his own Projects names nothing of Alice's.
	if _, err := s.GetProject(f.bob, project.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Bob GetProject = %v", err)
	}
	if _, err := s.ListProjects(f.bob, 0, 50); err != nil {
		t.Fatal(err)
	}

	type line struct {
		user   domain.UserID
		action domain.AuditAction
		object string
		result domain.AuditResult
	}
	var got []line
	for _, record := range auditActions(t, s, f.alice) {
		got = append(got, line{record.UserID, record.Action, record.ObjectType + "/" + record.ObjectID, record.Result})
	}
	want := []line{
		{principal.BootstrapOwner, domain.AuditSecretPut, "secret/github_api", domain.AuditAllowed},
		{principal.BootstrapOwner, domain.AuditSecretPut, "secret/github_api", domain.AuditFailed},
		{principal.BootstrapOwner, domain.AuditSecretDelete, "secret/github_api", domain.AuditAllowed},
		{"usr_bob", domain.AuditCrossOwnerAccess, "project/" + string(project.ID), domain.AuditDenied},
	}
	if len(got) != len(want) {
		t.Fatalf("records = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("record %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAuditRecordsAreReadByOwnerOrAdmin(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	project, err := s.CreateProject(f.alice, "alice", "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(f.bob, project.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
	input := app.SecretInput{Purpose: domain.SecretGitHubAPI, Value: "token"}
	if _, err := s.PutSecret(f.alice, "alice_token", input, 0, "alice-put"); err != nil {
		t.Fatal(err)
	}

	// The bootstrap owner is an admin and reads every record; Bob, a
	// member, reads only his own.
	if records := auditActions(t, s, f.alice); len(records) != 2 {
		t.Fatalf("admin records = %+v", records)
	}
	bob := auditActions(t, s, f.bob)
	if len(bob) != 1 || bob[0].UserID != "usr_bob" || bob[0].Action != domain.AuditCrossOwnerAccess {
		t.Fatalf("Bob's records = %+v", bob)
	}
	asCapsule, ok := principal.Capsule(principal.BootstrapOwner, "capsule")
	if !ok {
		t.Fatal("Capsule principal")
	}
	if _, err := s.ListAuditRecords(principal.With(context.Background(), asCapsule), 0, 50); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Capsule audit read = %v", err)
	}
	if records := auditActions(t, s, principal.With(context.Background(), principal.BreakGlass())); len(records) != 2 {
		t.Fatalf("break-glass records = %+v", records)
	}

	// Retention is daemon work.
	if _, err := s.PruneAuditRecords(f.alice, time.Nanosecond); !errors.Is(err, principal.ErrMissing) {
		t.Fatalf("user pruned the audit log: %v", err)
	}
	system := principal.WithSystem(context.Background())
	if pruned, err := s.PruneAuditRecords(system, 0); err != nil || pruned != 0 {
		t.Fatalf("zero retention pruned %d, %v", pruned, err)
	}
	if pruned, err := s.PruneAuditRecords(system, time.Nanosecond); err != nil || pruned != 2 {
		t.Fatalf("pruned %d, %v", pruned, err)
	}
	if records := auditActions(t, s, f.alice); len(records) != 0 {
		t.Fatalf("records after retention = %+v", records)
	}
}
