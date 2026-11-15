package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

func codexBundle(content string, extra ...harnesssetup.File) harnesssetup.Bundle {
	files := append([]harnesssetup.File{{Path: ".codex/AGENTS.md", Content: content}}, extra...)
	return harnesssetup.Bundle{Harness: "codex", Files: files}
}

func claudeBundle(content string) harnesssetup.Bundle {
	return harnesssetup.Bundle{Harness: "claude", Files: []harnesssetup.File{{Path: ".claude/CLAUDE.md", Content: content}}}
}

func excluded(path string) harnesssetup.Report {
	return harnesssetup.Report{Issues: []harnesssetup.Issue{{Path: path, Reason: "Authentication, runtime state, or unsupported file excluded"}}, Warnings: []harnesssetup.Issue{}}
}

func TestProfileImportIsAtomicAndIdempotent(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	empty, err := s.GetProfile(f.alice)
	if err != nil || empty.ResourceVersion != 0 || len(empty.Setups) != 0 {
		t.Fatalf("empty profile = %+v, %v", empty, err)
	}
	input := app.ProfileImport{
		Setups: []app.ProfileImportSetup{
			{Bundle: codexBundle("one"), Report: excluded(".codex/auth.json")},
			{Bundle: claudeBundle("one")},
		},
		Git: &app.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"},
	}
	first, err := s.ImportProfile(f.alice, input, "import-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Saved) != 2 || len(first.Unchanged) != 0 || first.Profile.ResourceVersion != 1 ||
		first.Profile.GitName != "Ada Lovelace" || len(first.Profile.Setups) != 2 {
		t.Fatalf("first import = %+v", first)
	}
	for _, setup := range first.Profile.Setups {
		if !setup.Default || setup.Name != "My "+setup.Harness+" setup" {
			t.Fatalf("imported setup = %+v", setup)
		}
	}
	if replay, err := s.ImportProfile(f.alice, input, "import-1"); err != nil || len(replay.Saved) != 2 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	changed := input
	changed.Git = nil
	if _, err := s.ImportProfile(f.alice, changed, "import-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("different import under one key = %v", err)
	}

	// The next import keeps an unchanged harness, adds a revision to the
	// changed one's existing setup, and leaves the git identity alone.
	next := app.ProfileImport{
		Setups:                  []app.ProfileImportSetup{{Bundle: codexBundle("one")}, {Bundle: claudeBundle("two")}},
		ExpectedResourceVersion: 1,
	}
	second, err := s.ImportProfile(f.alice, next, "import-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Saved) != 1 || second.Saved[0].Harness != "claude" || second.Saved[0].ResourceVersion != 2 ||
		len(second.Unchanged) != 1 || second.Unchanged[0] != "codex" || second.Profile.GitName != "Ada Lovelace" {
		t.Fatalf("second import = %+v", second)
	}
	if _, err := s.ImportProfile(f.alice, next, "import-stale"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale import = %v", err)
	}

	// Nothing is saved when any part is invalid.
	for name, bad := range map[string]app.ProfileImport{
		"bad report": {Setups: []app.ProfileImportSetup{
			{Bundle: codexBundle("three")},
			{Bundle: claudeBundle("three"), Report: harnesssetup.Report{Issues: []harnesssetup.Issue{{Path: "x\n", Reason: "r"}}}},
		}, ExpectedResourceVersion: 2},
		"duplicate harness": {Setups: []app.ProfileImportSetup{{Bundle: codexBundle("three")}, {Bundle: codexBundle("four")}}, ExpectedResourceVersion: 2},
		"unsafe identity":   {Setups: []app.ProfileImportSetup{{Bundle: codexBundle("three")}}, Git: &app.GitIdentity{Name: "Ada\n[core]", Email: "a@x"}, ExpectedResourceVersion: 2},
		"denied file": {Setups: []app.ProfileImportSetup{{Bundle: harnesssetup.Bundle{Harness: "codex", Files: []harnesssetup.File{{Path: ".codex/auth.json", Content: "{}"}}}}},
			ExpectedResourceVersion: 2},
		"nothing": {ExpectedResourceVersion: 2},
	} {
		if _, err := s.ImportProfile(f.alice, bad, "bad-"+name); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s = %v", name, err)
		}
	}
	after, err := s.GetProfile(f.alice)
	if err != nil || after.ResourceVersion != 2 {
		t.Fatalf("profile after refused imports = %+v, %v", after, err)
	}

	// Bob's profile is his own, and the break-glass admin has none.
	bobs, err := s.GetProfile(f.bob)
	if err != nil || bobs.ResourceVersion != 0 || len(bobs.Setups) != 0 {
		t.Fatalf("Bob's profile = %+v, %v", bobs, err)
	}
	if _, err := s.GetProfile(principal.With(context.Background(), principal.BreakGlass())); !errors.Is(err, principal.ErrMissing) {
		t.Fatalf("break-glass profile = %v", err)
	}
}

func TestProfileDefaultConnectionsAreTheOwnersOwn(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	system := principal.WithSystem(context.Background())
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if err := f.store.Transact(system, func(tx ports.Transaction) error {
		for _, connection := range []domain.ProviderConnection{
			{OwnerUserID: principal.BootstrapOwner, ID: "conn-alice", Provider: "openai", Name: "alice", CreatedAt: now, ResourceVersion: 1, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}},
			{OwnerUserID: principal.BootstrapOwner, ID: "conn-revoked", Provider: "openai", Name: "old", Revoked: true, CreatedAt: now, ResourceVersion: 1, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}},
			{OwnerUserID: "usr_bob", ID: "conn-bob", Provider: "openai", Name: "bob", CreatedAt: now, ResourceVersion: 1, KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1}},
		} {
			if err := tx.PutProviderConnection(system, connection); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	update := app.ProfileUpdate{GitName: "Ada", GitEmail: "ada@example.com", DefaultConnections: map[string]string{"codex": "conn-alice"}}
	profile, err := s.UpdateProfile(f.alice, update, "update-1")
	if err != nil || profile.ResourceVersion != 1 || profile.DefaultConnections["codex"] != "conn-alice" {
		t.Fatalf("update = %+v, %v", profile, err)
	}
	for name, bad := range map[string]struct {
		update app.ProfileUpdate
		want   error
	}{
		"another owner's connection": {app.ProfileUpdate{DefaultConnections: map[string]string{"codex": "conn-bob"}, ExpectedResourceVersion: 1}, domain.ErrNotFound},
		"disconnected connection":    {app.ProfileUpdate{DefaultConnections: map[string]string{"codex": "conn-revoked"}, ExpectedResourceVersion: 1}, domain.ErrConflict},
		"unknown harness":            {app.ProfileUpdate{DefaultConnections: map[string]string{"emacs": "conn-alice"}, ExpectedResourceVersion: 1}, domain.ErrInvalid},
		"half an identity":           {app.ProfileUpdate{GitName: "Ada", ExpectedResourceVersion: 1}, domain.ErrInvalid},
		"stale version":              {app.ProfileUpdate{ExpectedResourceVersion: 0}, domain.ErrConflict},
	} {
		if _, err := s.UpdateProfile(f.alice, bad.update, "bad-"+name); !errors.Is(err, bad.want) {
			t.Fatalf("%s = %v, want %v", name, err, bad.want)
		}
	}
	// A default is a suggestion: no Project grant appears.
	project, err := s.CreateProject(f.alice, "p", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.View(f.alice, func(r ports.Reader) error {
		granted, err := r.GetProjectProviderConnection(f.alice, project.ID, "codex")
		if err != nil || granted != "" {
			t.Fatalf("grant after a default = %q, %v", granted, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	audit, err := s.ListAuditRecords(f.alice, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range audit.Items {
		if record.Action == domain.AuditProfileDefaultConnections {
			found = true
			if strings.Contains(record.ObjectID, "ada") {
				t.Fatalf("audit record holds the identity: %+v", record)
			}
		}
	}
	if !found {
		t.Fatal("default connection change was not audited")
	}
}

func TestCapsulePinsTheProfileAndExplainsDifferences(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	if _, err := s.ImportProfile(f.alice, app.ProfileImport{
		Setups: []app.ProfileImportSetup{{Bundle: codexBundle("one")}},
		Git:    &app.GitIdentity{Name: "Ada Lovelace", Email: "ada@example.com"},
	}, "import-1"); err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(f.alice, "p", "p")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(f.alice, project.ID, "c", "c")
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := s.CapsuleProfileDiagnostic(f.alice, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostic.MatchesProfile || diagnostic.NothingPinned || len(diagnostic.Harnesses) != 1 ||
		diagnostic.CommitAuthor.Source != "capsule" || diagnostic.CommitAuthor.Name != "Ada Lovelace" {
		t.Fatalf("fresh Capsule diagnostic = %+v", diagnostic)
	}

	clean, err := s.CreateCapsuleForHarness(f.alice, project.ID, "clean", "", "clean", "clean")
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic, err := s.CapsuleProfileDiagnostic(f.alice, clean.ID); err != nil || !diagnostic.NothingPinned ||
		diagnostic.MatchesProfile || diagnostic.CommitAuthor.Source != "none" {
		t.Fatalf("clean Capsule diagnostic = %+v, %v", diagnostic, err)
	}

	// The profile moves on; the Capsule stays pinned and says so.
	if _, err := s.ImportProfile(f.alice, app.ProfileImport{
		Setups: []app.ProfileImportSetup{{
			Bundle: codexBundle("two", harnesssetup.File{Path: ".codex/prompts/review.md", Content: "review"}),
			Report: excluded(".codex/auth.json"),
		}},
		ExpectedResourceVersion: 1,
	}, "import-2"); err != nil {
		t.Fatal(err)
	}
	diagnostic, err = s.CapsuleProfileDiagnostic(f.alice, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	row := diagnostic.Harnesses[0]
	if diagnostic.MatchesProfile || row.Pinned == nil || row.Current == nil || row.Pinned.Revision == row.Current.Revision ||
		len(row.FilesAdded) != 1 || row.FilesAdded[0] != ".codex/prompts/review.md" || len(row.Exclusions) != 0 {
		t.Fatalf("behind Capsule diagnostic = %+v", diagnostic)
	}

	// Bob cannot read or update Alice's Capsule, and neither can the
	// Capsule itself.
	if _, err := s.CapsuleProfileDiagnostic(f.bob, capsule.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Bob's diagnostic of Alice's Capsule = %v", err)
	}
	if _, err := s.UpdateCapsuleProfile(f.bob, capsule.ID, "bob-update"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Bob's update of Alice's Capsule = %v", err)
	}
	asCapsule, ok := principal.Capsule(principal.BootstrapOwner, capsule.ID)
	if !ok {
		t.Fatal("Capsule principal")
	}
	if _, err := s.UpdateCapsuleProfile(principal.With(context.Background(), asCapsule), capsule.ID, "self-update"); !errors.Is(err, principal.ErrMissing) {
		t.Fatalf("Capsule updating itself = %v", err)
	}

	// An explicit update pins the current revision and its report.
	diagnostic, err = s.UpdateCapsuleProfile(f.alice, capsule.ID, "update-1")
	if err != nil {
		t.Fatal(err)
	}
	row = diagnostic.Harnesses[0]
	if row.Pinned == nil || row.Current == nil || row.Pinned.Revision != row.Current.Revision ||
		!row.HasReport || len(row.Exclusions) != 1 || row.Exclusions[0].Path != ".codex/auth.json" || diagnostic.MatchesProfile {
		t.Fatalf("updated Capsule diagnostic = %+v", diagnostic)
	}

	// An active session blocks the update.
	system := principal.WithSystem(context.Background())
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if err := f.store.Transact(system, func(tx ports.Transaction) error {
		return tx.InsertRun(system, domain.Run{
			ID: "run-1", CapsuleID: capsule.ID, Harness: "codex", State: domain.RunRunning,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCapsuleProfile(f.alice, capsule.ID, "update-during-run"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("update during a session = %v", err)
	}
}

func TestDepartedTransferResealsReportsAndLeavesTheProfile(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	if _, err := s.ImportProfile(f.bob, app.ProfileImport{
		Setups: []app.ProfileImportSetup{{Bundle: codexBundle("bob"), Report: excluded(".codex/auth.json")}},
		Git:    &app.GitIdentity{Name: "Bob", Email: "bob@example.com"},
	}, "bob-import"); err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(f.bob, "bob", "bob")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(f.bob, project.ID, "c", "c")
	if err != nil {
		t.Fatal(err)
	}
	disableMember(t, f, "usr_bob")
	breakGlass := principal.With(context.Background(), principal.BreakGlass())
	if err := s.TransferDepartedMember(breakGlass, "usr_bob", principal.BootstrapOwner); err != nil {
		t.Fatal(err)
	}
	// The moved Capsule's report opens for its new owner.
	diagnostic, err := s.CapsuleProfileDiagnostic(f.alice, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostic.Harnesses) != 1 || !diagnostic.Harnesses[0].HasReport || len(diagnostic.Harnesses[0].Exclusions) != 1 {
		t.Fatalf("moved Capsule diagnostic = %+v", diagnostic)
	}
	// Alice's profile did not gain Bob's default or identity.
	profile, err := s.GetProfile(f.alice)
	if err != nil || len(profile.Setups) != 0 || profile.GitName != "" {
		t.Fatalf("recipient profile = %+v, %v", profile, err)
	}
	// Deleting the departed member removes their profile.
	if _, err := s.DeleteDepartedMember(breakGlass, "usr_bob"); err != nil {
		t.Fatal(err)
	}
	system := principal.WithSystem(context.Background())
	if err := f.store.View(system, func(r ports.Reader) error {
		gone, err := r.GetUserProfile(system, "usr_bob")
		if err != nil || gone.ResourceVersion != 0 {
			t.Fatalf("departed profile = %+v, %v", gone, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A report never stores a laptop home path, even when the client sent one.
func TestImportedReportsAreStoredRedacted(t *testing.T) {
	f := newTwoOwners(t)
	s := f.service
	report := harnesssetup.Report{Issues: []harnesssetup.Issue{{
		Path: ".codex/config.toml:projects:/Users/ada/Documents/acme-secret:trust_level", Reason: "Machine trust or approval override excluded",
	}}, Warnings: []harnesssetup.Issue{}}
	if _, err := s.ImportProfile(f.alice, app.ProfileImport{Setups: []app.ProfileImportSetup{{Bundle: codexBundle("one"), Report: report}}}, "import"); err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(f.alice, "p", "p")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(f.alice, project.ID, "c", "c")
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := s.CapsuleProfileDiagnostic(f.alice, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := diagnostic.Harnesses[0].Exclusions
	if len(got) != 1 || got[0].Path != ".codex/config.toml:projects:~/…:trust_level" {
		t.Fatalf("stored exclusions = %+v", got)
	}
}
