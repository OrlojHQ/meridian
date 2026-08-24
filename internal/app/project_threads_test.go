package app_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

func TestProjectThreadProvisioningPromotionReplayAndFailure(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ids := &testIDs{}
	queue := &recordingQueue{}
	runtime := &structuredRuntime{}
	key, err := transcripts.NewInstallationKey("project-thread-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service := app.NewService(store, clock, ids, queue)
	service.ConfigureThreads(runtime, key)
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertProject(ctx, domain.Project{
			ID: "project-spawn", Name: "project", CreatedAt: now,
			UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	input := app.CreateProjectThreadInput{
		ProjectID: "project-spawn", Harness: "mock", Prompt: "never store this prompt",
		IdempotencyKey: "spawn-one",
	}
	created, err := service.CreateProjectThread(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Intent.State != domain.ProjectThreadProvisioning {
		t.Fatalf("state = %s", created.Intent.State)
	}
	if _, err := service.GetThread(ctx, created.Intent.ThreadID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Thread existed before Capsule Ready: %v", err)
	}
	for _, path := range []string{store.Path(), store.Path() + "-wal"} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		if bytes.Contains(raw, []byte(input.Prompt)) {
			t.Fatalf("plaintext prompt found in %s", path)
		}
	}
	replayed, err := service.CreateProjectThread(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Intent.ID != created.Intent.ID ||
		replayed.Intent.CapsuleID != created.Intent.CapsuleID ||
		replayed.Intent.ThreadID != created.Intent.ThreadID {
		t.Fatalf("idempotent replay changed resources: %#v / %#v", created, replayed)
	}
	different := input
	different.Prompt = "different"
	if _, err := service.CreateProjectThread(ctx, different); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("different idempotent request error = %v", err)
	}

	capsule := setProjectThreadCapsuleState(
		t, ctx, store, created.Intent.CapsuleID, domain.CapsuleReady,
	)
	if err := service.ReconcileProjectThreadCapsule(ctx, capsule); err != nil {
		t.Fatal(err)
	}
	promoted, err := service.GetProjectThreadIntent(ctx, created.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Intent.State != domain.ProjectThreadReady ||
		promoted.Thread == nil || promoted.Run == nil ||
		promoted.Thread.MessageCount != 1 {
		t.Fatalf("promoted result = %#v", promoted)
	}
	replayed, err = service.CreateProjectThread(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Intent.ID != created.Intent.ID {
		t.Fatalf("ready replay intent = %s", replayed.Intent.ID)
	}
	runtime.mu.Lock()
	if runtime.starts[promoted.Intent.RunID] != 1 ||
		runtime.sends[string(promoted.Intent.MessageID)] != 1 {
		t.Fatalf("starts/sends = %#v/%#v", runtime.starts, runtime.sends)
	}
	runtime.mu.Unlock()

	failed, err := service.CreateProjectThread(ctx, app.CreateProjectThreadInput{
		ProjectID: "project-spawn", Harness: "mock", Prompt: "encrypted failure prompt",
		IdempotencyKey: "spawn-failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	failedCapsule := setProjectThreadCapsuleState(
		t, ctx, store, failed.Intent.CapsuleID, domain.CapsuleFailed,
	)
	if err := service.ReconcileProjectThreadCapsule(ctx, failedCapsule); err != nil {
		t.Fatal(err)
	}
	failedResult, err := service.GetProjectThreadIntent(ctx, failed.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedResult.Intent.State != domain.ProjectThreadFailed ||
		failedResult.Intent.FailureCode != "capsule_failed" {
		t.Fatalf("failed intent = %#v", failedResult.Intent)
	}
	if _, err := service.GetThread(ctx, failed.Intent.ThreadID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed provisioning created Thread: %v", err)
	}
}

func TestProjectThreadRecoveryPromotesWithoutSending(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runtime := &structuredRuntime{}
	key, err := transcripts.NewInstallationKey("project-thread-recovery", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service := app.NewService(store, &testClock{now: now}, &testIDs{}, &recordingQueue{})
	service.ConfigureThreads(runtime, key)
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertProject(ctx, domain.Project{
			ID: "project-recovery", Name: "project", CreatedAt: now,
			UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateProjectThread(ctx, app.CreateProjectThreadInput{
		ProjectID: "project-recovery", Harness: "mock", Prompt: "recover me",
		IdempotencyKey: "recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	setProjectThreadCapsuleState(t, ctx, store, created.Intent.CapsuleID, domain.CapsuleReady)
	if err := service.RecoverProjectThreads(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := service.GetProjectThreadIntent(ctx, created.Intent.ID)
	if err != nil || result.Intent.State != domain.ProjectThreadReady {
		t.Fatalf("recovered intent = %#v, %v", result, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.starts[created.Intent.RunID] != 1 {
		t.Fatalf("runtime starts = %#v", runtime.starts)
	}
	if runtime.sends[string(created.Intent.MessageID)] != 0 {
		t.Fatalf("recovery resent user message: %#v", runtime.sends)
	}
}

func setProjectThreadCapsuleState(
	t *testing.T,
	ctx context.Context,
	store *sqlite.Store,
	id domain.CapsuleID,
	state domain.CapsuleState,
) domain.Capsule {
	t.Helper()
	var capsule domain.Capsule
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		capsule, err = tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.State = state
		if state == domain.CapsuleReady {
			capsule.ProviderResourceID = "resource-" + string(id)
		}
		capsule.UpdatedAt = capsule.UpdatedAt.Add(time.Second)
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, previous)
	}); err != nil {
		t.Fatal(err)
	}
	return capsule
}
