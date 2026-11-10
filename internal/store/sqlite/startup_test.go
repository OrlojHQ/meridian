package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestStartupProgressIsBoundedAndRoundTrips(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	project := domain.Project{
		ID: "project-1", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	capsule := domain.Capsule{
		ID: "capsule-1", ProjectID: project.ID, Name: "capsule",
		State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
		FailureCode: domain.PullImageNotFound,
		CreatedAt:   now, UpdatedAt: now, ResourceVersion: 1,
	}
	progress, err := domain.NewStartupProgress(
		capsule.ID, domain.PhasePullingImage, domain.ImagePullPhases(),
		312_000_000, 540_000_000, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		if err := tx.PutStartup(ctx, progress); err != nil {
			return err
		}
		hostile := progress
		hostile.Phase = domain.StartupPhase("pulling image; rm -rf /")
		if err := tx.PutStartup(ctx, hostile); err == nil {
			return errors.New("hostile phase was stored")
		}
		oversized := progress
		oversized.Pull = &domain.ImagePullProgress{CurrentBytes: domain.MaxPullBytes + 1}
		if err := tx.PutStartup(ctx, oversized); err == nil {
			return errors.New("oversized progress was stored")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		loaded, err := reader.GetCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if loaded.FailureCode != domain.PullImageNotFound {
			t.Fatalf("failure code = %q", loaded.FailureCode)
		}
		got, err := reader.GetStartup(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if got.Phase != domain.PhasePullingImage || got.Pull == nil ||
			got.Pull.CurrentBytes != 312_000_000 || got.Pull.TotalBytes != 540_000_000 ||
			len(got.Phases) != 4 {
			t.Fatalf("startup = %#v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.DeleteStartup(ctx, capsule.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		_, err := reader.GetStartup(ctx, capsule.ID)
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("deleted startup = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw := "unauthorized: token " + strings.Repeat("s", 40)
	if strings.Contains((&domain.StartupFailure{Code: domain.ClassifyPullText(raw)}).Error(), "token") {
		t.Fatal("stored classification retained registry text")
	}
}
