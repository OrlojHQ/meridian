package app_test

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type fakePrepuller struct {
	mu          sync.Mutex
	pulled      []string
	inFlight    int
	maxInFlight int
	delay       time.Duration
	failRef     string
	failErr     error
}

func (f *fakePrepuller) ImageAvailable(context.Context, string) (bool, error) {
	return false, nil
}

func (f *fakePrepuller) PrepullImage(ctx context.Context, ref string) error {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.delay):
		}
	}
	f.mu.Lock()
	f.pulled = append(f.pulled, ref)
	f.mu.Unlock()
	if ref == f.failRef {
		return f.failErr
	}
	return nil
}

func TestPrepullOfficialPacksDeduplicatesAndSkipsCustomImages(t *testing.T) {
	ctx := principal.WithSystem(context.Background())
	userCtx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "meridian.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	clock := &testClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	svc := app.NewService(store, clock, ids, &recordingQueue{})
	packs := domain.InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v0.18.0", "")
	svc.ConfigureHarnessPacks(packs)

	_, err = svc.CreateProjectConfigured(userCtx, "p1", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{
			{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.16.0"},
			{Name: "codex", ImageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.17.0"},
			{Name: "custom", ImageReference: "ghcr.io/example/my-custom:latest"},
			{Name: "pi", ImageReference: "ghcr.io/orlojhq/meridian-capsule-pi@sha256:" + strings.Repeat("a", 64)},
		},
	}, "key-p1")
	if err != nil {
		t.Fatalf("create p1: %v", err)
	}
	_, err = svc.CreateProjectConfigured(userCtx, "p2", app.ProjectConfiguration{
		HarnessImages: []domain.HarnessImage{
			{Name: "claude", ImageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.15.0"},
			{Name: "agy", ImageReference: "ghcr.io/orlojhq/meridian-capsule-agy:v0.17.0"},
		},
	}, "key-p2")
	if err != nil {
		t.Fatalf("create p2: %v", err)
	}

	prepuller := &fakePrepuller{
		delay:   15 * time.Millisecond,
		failRef: "ghcr.io/orlojhq/meridian-capsule-codex:v0.18.0",
		failErr: &domain.StartupFailure{Code: domain.PullRegistryUnreachable},
	}
	var logs bytes.Buffer
	svc.PrepullOfficialPacks(ctx, prepuller, log.New(&logs, "", 0))

	got := append([]string(nil), prepuller.pulled...)
	slices.Sort(got)
	want := []string{
		"ghcr.io/orlojhq/meridian-capsule-agy:v0.18.0",
		"ghcr.io/orlojhq/meridian-capsule-claude:v0.18.0",
		"ghcr.io/orlojhq/meridian-capsule-codex:v0.18.0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pulled = %v, want %v", got, want)
	}
	if prepuller.maxInFlight > 2 {
		t.Fatalf("max concurrent prepulls = %d, want <= 2", prepuller.maxInFlight)
	}
	if !strings.Contains(logs.String(), "registry_unreachable") {
		t.Fatalf("expected bounded error code in log, got %q", logs.String())
	}
	var _ ports.ImagePrepuller = prepuller
}
