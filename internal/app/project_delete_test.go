package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/artifacts"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

func TestProjectDeleteCascadesDestroysCredentialsAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dataDir := t.TempDir()
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	artifactStore, err := artifacts.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	service.ConfigureTemporal(nil, artifactStore)
	reconciler.OnCapsuleSettled(func(ctx context.Context, capsule domain.Capsule) {
		if err := service.AdvanceProjectDeletion(ctx, capsule.ProjectID); err != nil {
			t.Errorf("advance project deletion: %v", err)
		}
	})

	const secretName = "PROJECT_TOKEN"
	secret, err := service.PutSecret(ctx, secretName, app.SecretInput{
		Purpose: domain.SecretHarnessEnv, Value: "plaintext-must-not-remain",
	}, 0, "put-token")
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProjectConfigured(ctx, "demo", app.ProjectConfiguration{
		HarnessSecretNames: []string{secretName},
		Setup:              []string{"echo", "setup"},
	}, "create-demo")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := service.UpdateProjectEnvironment(ctx, project.ID, app.EnvironmentMutation{
		Setup:                   &[]string{"echo", "kept-until-delete"},
		ExpectedResourceVersion: project.ResourceVersion,
	}, "environment")
	if err != nil {
		t.Fatal(err)
	}
	if environment.Generation == 0 || len(environment.Setup) != 2 {
		t.Fatalf("environment = %#v", environment)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "work", "create-work")
	if err != nil {
		t.Fatal(err)
	}
	capsule = waitCapsule(t, ctx, service, capsule.ID, domain.CapsuleReady)
	if capsule.ProviderResourceID == "" || capsule.TimelineID == "" {
		t.Fatalf("capsule = %#v", capsule)
	}
	canary := []byte("transcript-ciphertext-canary")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		now := time.Now().UTC()
		if err := tx.InsertThread(ctx, domain.Thread{
			ID: "thread-1", CapsuleID: capsule.ID, State: domain.ThreadActive,
			AdapterID: "mock", WrappedDEK: bytes.Repeat([]byte{9}, 64),
			KEKID: "kek", KEKVersion: 1, EnvelopeVersion: 1,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.AppendThreadMessage(ctx, domain.ThreadMessage{
			ID: "message-1", ThreadID: "thread-1", Sequence: 1,
			Role: domain.ThreadRoleUser, Kind: domain.ThreadMessagePrompt, CreatedAt: now,
			Blocks: []domain.ThreadBlock{{
				ID: "block-1", ThreadID: "thread-1", MessageID: "message-1",
				MessageSequence: 1, Sequence: 1, Kind: domain.ThreadBlockText,
				EnvelopeVersion: 1, Ciphertext: transcriptEnvelope(canary), CreatedAt: now,
			}},
		})
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := artifactStore.Publish(ctx, strings.NewReader("project-archive"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := artifactStore.Publish(ctx, strings.NewReader("project-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := artifactStore.Publish(ctx, strings.NewReader("shared-archive"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertMoment(ctx, domain.Moment{
			ID: "moment-1", ProjectID: project.ID, CapsuleID: capsule.ID, TimelineID: capsule.TimelineID,
			Name: "snap", ArchiveSHA256: shared.Digest, ArchiveSize: shared.Size,
			ManifestSHA256: manifest.Digest, ImageDigest: "local",
			ProjectSetupHash: strings.Repeat("ab", 32), CreatedAt: time.Now().UTC(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	other, otherCapsule := readyProject(t, ctx, service, "other", "create-other", "other-capsule")
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertMoment(ctx, domain.Moment{
			ID: "moment-2", ProjectID: other.ID, CapsuleID: otherCapsule.ID, TimelineID: otherCapsule.TimelineID,
			Name: "keep", ArchiveSHA256: shared.Digest, ArchiveSize: shared.Size,
			ManifestSHA256: archive.Digest, ImageDigest: "local",
			ProjectSetupHash: strings.Repeat("cd", 32), CreatedAt: time.Now().UTC(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queryCount(store, "DELETE FROM moments WHERE id = ?", "moment-1"); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("active project allowed moment deletion: %v", err)
	}

	accepted, err := service.DeleteProject(ctx, project.ID, environment.ResourceVersion, "delete-demo")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State != domain.ProjectDeleting {
		t.Fatalf("accepted state = %s", accepted.State)
	}
	replay, err := service.DeleteProject(ctx, project.ID, environment.ResourceVersion, "delete-demo")
	if err != nil {
		t.Fatal(err)
	}
	if replay.ResourceVersion != accepted.ResourceVersion || replay.State != accepted.State {
		t.Fatalf("replay = %#v, accepted = %#v", replay, accepted)
	}
	deleted := waitProject(t, ctx, service, project.ID, domain.ProjectDeleted)
	if deleted.GitSecretName != "" || len(deleted.HarnessSecretNames) != 0 || len(deleted.Setup) != 0 || deleted.Failure != "" {
		t.Fatalf("tombstone = %#v", deleted)
	}
	again, err := service.DeleteProject(ctx, project.ID, accepted.ResourceVersion, "delete-demo-again")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != domain.ProjectDeleted {
		t.Fatalf("second delete = %#v", again)
	}
	if secret.Ciphertext == nil || secret.Nonce == nil {
		t.Fatal("fixture secret had no ciphertext")
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM secrets WHERE ciphertext = ?", secret.Ciphertext); err != nil || count != 0 {
		t.Fatalf("ciphertext rows = %d, %v", count, err)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM secrets WHERE name = ?", secretName); err != nil || count != 0 {
		t.Fatalf("secret rows = %d, %v", count, err)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM thread_blocks WHERE thread_id = ?", "thread-1"); err != nil || count != 0 {
		t.Fatalf("thread blocks = %d, %v", count, err)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM threads WHERE id = ?", "thread-1"); err != nil || count != 0 {
		t.Fatalf("threads = %d, %v", count, err)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM moments WHERE project_id = ?", project.ID); err != nil || count != 0 {
		t.Fatalf("moments = %d, %v", count, err)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM project_preparation_policy WHERE project_id = ?", project.ID); err != nil || count != 0 {
		t.Fatalf("environment rows = %d, %v", count, err)
	}
	waitArtifactGone(t, ctx, artifactStore, manifest.Digest)
	if _, _, err := artifactStore.Open(ctx, shared.Digest); err != nil {
		t.Fatalf("shared archive was removed while another project references it: %v", err)
	}
	resource, err := provider.Get(ctx, capsule.ProviderResourceID)
	if err != nil || resource.State != ports.ProviderDeleted {
		t.Fatalf("provider resource = %#v, %v", resource, err)
	}
	listed, err := service.ListProjects(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed.Items {
		if item.ID == project.ID {
			t.Fatal("deleted project is still listed")
		}
	}

	otherEnvironment, err := service.GetProject(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteProject(ctx, other.ID, otherEnvironment.ResourceVersion, "delete-other"); err != nil {
		t.Fatal(err)
	}
	waitProject(t, ctx, service, other.ID, domain.ProjectDeleted)
	waitArtifactGone(t, ctx, artifactStore, shared.Digest)
	waitArtifactGone(t, ctx, artifactStore, archive.Digest)
}

func TestProjectDeleteKeepsASecretAnotherProjectStillNames(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := app.NewService(store, &testClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	if _, err := service.PutSecret(ctx, "SHARED_TOKEN", app.SecretInput{
		Purpose: domain.SecretHarnessEnv, Value: "shared-secret",
	}, 0, "put-shared"); err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateProjectConfigured(ctx, "first", app.ProjectConfiguration{
		HarnessSecretNames: []string{"SHARED_TOKEN"},
	}, "create-first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateProjectConfigured(ctx, "second", app.ProjectConfiguration{
		HarnessSecretNames: []string{"SHARED_TOKEN"},
	}, "create-second"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteProject(ctx, first.ID, first.ResourceVersion, "delete-first"); err != nil {
		t.Fatal(err)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM secrets WHERE name = ?", "SHARED_TOKEN"); err != nil || count != 1 {
		t.Fatalf("shared secret rows after the first project = %d, %v", count, err)
	}
	listed, err := service.ListProjects(ctx, 0, 10)
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("remaining projects = %#v, %v", listed.Items, err)
	}
	second := listed.Items[0]
	deleted, err := service.DeleteProject(ctx, second.ID, second.ResourceVersion, "delete-second")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.State != domain.ProjectDeleted {
		t.Fatalf("second delete = %#v", deleted)
	}
	if count, err := queryCount(store, "SELECT COUNT(*) FROM secrets WHERE name = ?", "SHARED_TOKEN"); err != nil || count != 0 {
		t.Fatalf("secret rows after the last project = %d, %v", count, err)
	}
}

func TestProjectDeleteFailureResumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{Failures: map[string]int{"delete": 5}})
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	reconciler.OnCapsuleSettled(func(ctx context.Context, capsule domain.Capsule) {
		if err := service.AdvanceProjectDeletion(ctx, capsule.ProjectID); err != nil {
			t.Errorf("advance project deletion: %v", err)
		}
	})
	project, capsule := readyProject(t, ctx, service, "stuck", "create-stuck", "stuck-capsule")
	resourceID := capsule.ProviderResourceID
	accepted, err := service.DeleteProject(ctx, project.ID, project.ResourceVersion, "delete-stuck")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.State != domain.ProjectDeleting {
		t.Fatalf("accepted = %#v", accepted)
	}
	failed := waitProject(t, ctx, service, project.ID, domain.ProjectFailed)
	if !strings.Contains(failed.Failure, "injected fake provider delete failure") {
		t.Fatalf("failure = %q", failed.Failure)
	}
	capsule, err = service.GetCapsule(ctx, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.State != domain.CapsuleFailed || capsule.DesiredState != domain.IntentDeleted {
		t.Fatalf("capsule after cleanup failure = %#v", capsule)
	}
	resource, err := provider.Get(ctx, resourceID)
	if err != nil || resource.State == ports.ProviderDeleted {
		t.Fatalf("provider resource was removed despite the failure: %#v, %v", resource, err)
	}
	if _, err := service.DeleteProject(ctx, project.ID, failed.ResourceVersion, "delete-stuck-resume"); err != nil {
		t.Fatal(err)
	}
	waitProject(t, ctx, service, project.ID, domain.ProjectDeleted)
	resource, err = provider.Get(ctx, resourceID)
	if err != nil || resource.State != ports.ProviderDeleted {
		t.Fatalf("provider resource after resume = %#v, %v", resource, err)
	}
}

func TestProjectDeleteRejectsAnActiveRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &testClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	reconciler := app.NewReconciler(store, fake.New(fake.Options{}), clock, ids)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	project, capsule := readyProject(t, ctx, service, "busy", "create-busy", "busy-capsule")
	now := time.Now().UTC()
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.InsertRun(ctx, domain.Run{
			ID: "run-1", CapsuleID: capsule.ID, Harness: "mock", State: domain.RunRunning,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeleteProject(ctx, project.ID, project.ResourceVersion, "delete-busy"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("delete with an active run = %v", err)
	}
	current, err := service.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.ProjectActive {
		t.Fatalf("project state = %s", current.State)
	}
	capsule, err = service.GetCapsule(ctx, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
		t.Fatalf("capsule changed despite the conflict: %#v", capsule)
	}
}

func readyProject(
	t *testing.T,
	ctx context.Context,
	service *app.Service,
	name, projectKey, capsuleKey string,
) (domain.Project, domain.Capsule) {
	t.Helper()
	project, err := service.CreateProject(ctx, name, projectKey)
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, name+"-capsule", capsuleKey)
	if err != nil {
		t.Fatal(err)
	}
	return project, waitCapsule(t, ctx, service, capsule.ID, domain.CapsuleReady)
}

func waitCapsule(
	t *testing.T,
	ctx context.Context,
	service *app.Service,
	id domain.CapsuleID,
	want domain.CapsuleState,
) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		capsule, err := service.GetCapsule(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == want {
			return capsule
		}
		if capsule.State == domain.CapsuleFailed && want != domain.CapsuleFailed {
			t.Fatalf("capsule failed: %s", capsule.Failure)
		}
		if time.Now().After(deadline) {
			t.Fatalf("capsule state = %s, want %s (%s)", capsule.State, want, capsule.Failure)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Artifact bytes are removed after the Project tombstone commits, so a reader
// can observe Deleted before the unreferenced blobs are gone.
func waitArtifactGone(t *testing.T, ctx context.Context, store *artifacts.Store, digest string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		reader, _, err := store.Open(ctx, digest)
		if errors.Is(err, domain.ErrNotFound) {
			return
		}
		if reader != nil {
			reader.Close()
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("artifact %s = %v", digest, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitProject(
	t *testing.T,
	ctx context.Context,
	service *app.Service,
	id domain.ProjectID,
	want domain.ProjectState,
) domain.Project {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var project domain.Project
	for {
		var err error
		project, err = service.GetProject(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if project.State == want {
			return project
		}
		if time.Now().After(deadline) {
			t.Fatalf("project state = %s, want %s (%s)", project.State, want, project.Failure)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func transcriptEnvelope(payload []byte) []byte {
	envelope := []byte{'M', 'T', 'F', '1'}
	envelope = append(envelope, bytes.Repeat([]byte{1}, 12)...)
	envelope = append(envelope, payload...)
	return append(envelope, bytes.Repeat([]byte{2}, 16)...)
}

func queryCount(store *sqlite.Store, query string, args ...any) (int, error) {
	db, err := sql.Open("sqlite", store.Path())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if strings.HasPrefix(strings.TrimSpace(query), "DELETE") {
		_, err = db.Exec(query, args...)
		return 0, err
	}
	var count int
	err = db.QueryRow(query, args...).Scan(&count)
	return count, err
}
