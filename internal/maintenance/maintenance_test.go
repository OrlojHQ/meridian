package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/artifacts"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

func TestBackupVerifyRestoreAndCAS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifacts.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := artifactStore.Publish(ctx, strings.NewReader("archive"))
	if err != nil {
		t.Fatal(err)
	}
	manifestArtifact, err := artifactStore.Publish(ctx, strings.NewReader("manifest"))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := artifactStore.Publish(ctx, strings.NewReader("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = store.Transact(ctx, func(tx ports.Transaction) error {
		project := domain.Project{
			ID: "project", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		capsule := domain.Capsule{
			ID: "capsule", ProjectID: project.ID, Name: "capsule",
			State: domain.CapsuleReady, DesiredState: domain.IntentReady,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		return tx.InsertMoment(ctx, domain.Moment{
			ID: "moment", ProjectID: project.ID, CapsuleID: capsule.ID,
			TimelineID: "root-capsule", Name: "moment",
			ArchiveSHA256: archive.Digest, ArchiveSize: archive.Size,
			ManifestSHA256: manifestArtifact.Digest, ImageDigest: "image@sha256:test",
			ProjectSetupHash: strings.Repeat("0", 64), CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := VerifyCAS(ctx, dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.ReferencedBlobs != 2 || report.EligibleBlobs != 1 || report.DeletedBlobs != 0 {
		t.Fatalf("unexpected dry-run report: %+v", report)
	}

	backupDir := filepath.Join(t.TempDir(), "backup")
	created, err := CreateBackup(ctx, dataDir, backupDir)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(ctx, backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if verified.SchemaVersion != sqlite.CurrentSchemaVersion || len(verified.Files) != len(created.Files) {
		t.Fatalf("unexpected manifest: %+v", verified)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(ctx, backupDir, restored, false); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCAS(ctx, restored, false); err != nil {
		t.Fatalf("verify restored CAS: %v", err)
	}

	report, err = VerifyCAS(ctx, dataDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedBlobs != 1 || report.DeletedBytes != int64(len("orphan")) {
		t.Fatalf("unexpected delete report: %+v (orphan %s)", report, orphan.Digest)
	}
}

func TestVerifyRejectsCorruptionSymlinkAndTraversal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := artifacts.Open(dataDir); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(t.TempDir(), "backup")
	if _, err := CreateBackup(ctx, dataDir, backupDir); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(backupDir, "meridian.db")
	file, err := os.OpenFile(database, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := VerifyBackup(ctx, backupDir); err == nil {
		t.Fatal("corrupt backup unexpectedly verified")
	}

	manifestPath := filepath.Join(backupDir, manifestFilename)
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	content = bytes.Replace(content, []byte(`"meridian.db"`), []byte(`"../escape.db"`), 1)
	if err := os.WriteFile(manifestPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, backupDir); err == nil {
		t.Fatal("traversal manifest unexpectedly verified")
	}

	symlinkDir := filepath.Join(t.TempDir(), "symlink-backup")
	if err := os.Symlink(backupDir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, symlinkDir); err == nil {
		t.Fatal("symlink backup unexpectedly verified")
	}
}

func TestRestorePreservesExistingInstallationOnFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source")
	store, err := sqlite.Open(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, err := artifacts.Open(source); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := CreateBackup(ctx, source, backup); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "keep")
	if err := os.WriteFile(marker, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackup(ctx, backup, destination, false); err == nil {
		t.Fatal("restore without replace unexpectedly succeeded")
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "existing" {
		t.Fatalf("existing installation changed: %q, %v", content, err)
	}
}

func TestCASDeletionVerifiesAllBlobsBeforeMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	artifactStore, err := artifacts.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := artifactStore.Publish(ctx, strings.NewReader("first orphan"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := artifactStore.Publish(ctx, strings.NewReader("second orphan"))
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(dataDir, "artifacts", "sha256", second.Digest[:2], second.Digest)
	if err := os.WriteFile(secondPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyCAS(ctx, dataDir, true); err == nil {
		t.Fatal("corrupt CAS unexpectedly garbage-collected")
	}
	firstPath := filepath.Join(dataDir, "artifacts", "sha256", first.Digest[:2], first.Digest)
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("verified blob was deleted before full preflight: %v", err)
	}
}

func TestBackupTranscriptCiphertextKeyExclusionDeepVerifyAndRestore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := transcripts.DefaultKeyPath(dataDir)
	key, err := transcripts.OpenOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	dek, err := key.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	defer zeroMaintenanceBytes(dek)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	wrapped, err := key.WrapDEK("thread", dek)
	if err != nil {
		t.Fatal(err)
	}
	metadata := transcripts.FrameMetadata{
		ThreadID: "thread", MessageID: "message", MessageSequence: 1,
		BlockSequence: 1, Role: domain.ThreadRoleUser,
		MessageKind: domain.ThreadMessagePrompt, BlockKind: domain.ThreadBlockText,
		CreatedAt: now.Format(time.RFC3339Nano),
	}
	envelope, err := transcripts.Seal(dek, metadata, []byte("backup secret sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, domain.Project{
			ID: "project", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		if err := tx.InsertCapsule(ctx, domain.Capsule{
			ID: "capsule", ProjectID: "project", Name: "capsule",
			State: domain.CapsuleReady, DesiredState: domain.IntentReady,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		if err := tx.InsertThread(ctx, domain.Thread{
			ID: "thread", CapsuleID: "capsule", State: domain.ThreadActive,
			AdapterID: "adapter", WrappedDEK: wrapped, KEKID: key.ID,
			KEKVersion: key.Version, EnvelopeVersion: transcripts.EnvelopeVersion,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.AppendThreadMessage(ctx, domain.ThreadMessage{
			ID: "message", ThreadID: "thread", Sequence: 1,
			Role: metadata.Role, Kind: metadata.MessageKind, CreatedAt: now,
			Blocks: []domain.ThreadBlock{{
				ID: "block", ThreadID: "thread", MessageID: "message",
				MessageSequence: 1, Sequence: 1, Kind: metadata.BlockKind,
				EnvelopeVersion: transcripts.EnvelopeVersion,
				Ciphertext:      envelope, CreatedAt: now,
			}},
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("backup secret sentinel")
	liveDatabase, err := os.ReadFile(filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(liveDatabase, sentinel) {
		t.Fatal("transcript plaintext found in live SQLite database")
	}
	backupDir := filepath.Join(t.TempDir(), "backup")
	manifest, err := CreateBackup(ctx, dataDir, backupDir)
	if err != nil {
		t.Fatal(err)
	}
	backupDatabase, err := os.ReadFile(filepath.Join(backupDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(backupDatabase, sentinel) {
		t.Fatal("transcript plaintext found in backup SQLite database")
	}
	if len(manifest.RequiredTranscriptKeys) != 1 ||
		manifest.RequiredTranscriptKeys[0].ID != key.ID ||
		manifest.RequiredTranscriptKeys[0].Version != key.Version {
		t.Fatalf("key requirements = %+v", manifest.RequiredTranscriptKeys)
	}
	for _, file := range manifest.Files {
		if strings.Contains(file.Path, "key") {
			t.Fatalf("key unexpectedly in backup: %s", file.Path)
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(backupDir, manifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(manifestBytes, sentinel) {
		t.Fatal("transcript plaintext found in backup manifest")
	}
	if _, err := os.Stat(filepath.Join(backupDir, "transcript-keys")); !os.IsNotExist(err) {
		t.Fatalf("key directory included in backup: %v", err)
	}
	if _, err := VerifyBackup(ctx, backupDir); err != nil {
		t.Fatalf("checksum-only verify: %v", err)
	}
	wrongPath := filepath.Join(t.TempDir(), "keys", "wrong.key")
	wrongKey, err := transcripts.OpenOrCreateKey(wrongPath)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey.Zero()
	if _, err := VerifyBackupWithKey(ctx, backupDir, wrongPath); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("wrong key verify = %v", err)
	}
	if _, err := VerifyBackupWithKey(ctx, backupDir, keyPath); err != nil {
		t.Fatalf("deep verify: %v", err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackup(ctx, backupDir, restored, false); err == nil {
		t.Fatal("restore without required transcript key succeeded")
	}
	if _, err := RestoreBackupWithOptions(ctx, backupDir, restored, RestoreOptions{
		TranscriptKeyFile: wrongPath,
	}); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("restore wrong key = %v", err)
	}
	if _, err := os.Stat(restored); !os.IsNotExist(err) {
		t.Fatalf("failed restore changed destination: %v", err)
	}
	if err := os.Mkdir(restored, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(restored, "keep")
	if err := os.WriteFile(marker, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackupWithOptions(ctx, backupDir, restored, RestoreOptions{
		Replace: true, TranscriptKeyFile: wrongPath,
	}); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("replacement restore wrong key = %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "existing" {
		t.Fatalf("key mismatch changed existing destination: %q, %v", content, err)
	}
	if err := os.RemoveAll(restored); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackupWithOptions(ctx, backupDir, restored, RestoreOptions{
		TranscriptKeyFile: keyPath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackupWithKey(ctx, backupDir, transcripts.DefaultKeyPath(restored)); err != nil {
		t.Fatalf("restored key cannot decrypt backup: %v", err)
	}
}

func TestTranscriptKeyRotationSuccessRollbackAndBackupRestore(t *testing.T) {
	ctx := context.Background()
	dataDir, keyPath := createRotationFixture(t, ctx)

	wrongDir := filepath.Join(t.TempDir(), "wrong")
	wrongPath := filepath.Join(wrongDir, "wrong.key")
	wrong, err := transcripts.OpenOrCreateKey(wrongPath)
	if err != nil {
		t.Fatal(err)
	}
	wrong.Zero()
	if _, err := RotateTranscriptKey(ctx, dataDir, wrongPath, ""); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("wrong old key rotation = %v", err)
	}

	rotationTestHook = func(index int) error {
		if index == 1 {
			return errors.New("injected mid-rotation failure")
		}
		return nil
	}
	if _, err := RotateTranscriptKey(ctx, dataDir, keyPath, ""); err == nil ||
		!strings.Contains(err.Error(), "injected mid-rotation failure") {
		t.Fatalf("injected rotation failure = %v", err)
	}
	rotationTestHook = nil
	old, err := transcripts.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if old.Version != 1 {
		t.Fatalf("failed rotation activated version %d", old.Version)
	}
	old.Zero()

	newDir := filepath.Join(t.TempDir(), "new")
	newPath := filepath.Join(newDir, "replacement.key")
	newKey, err := transcripts.NewInstallationKey("rotation-key", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := transcripts.WriteNewKey(newPath, newKey); err != nil {
		t.Fatal(err)
	}
	newKey.Zero()
	report, err := RotateTranscriptKey(ctx, dataDir, keyPath, newPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.ThreadsRewrapped != 1 || report.KeyID != "rotation-key" || report.KeyVersion != 2 {
		t.Fatalf("rotation report = %+v", report)
	}
	active, err := transcripts.LoadKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != report.KeyID || active.Version != report.KeyVersion {
		t.Fatalf("active key metadata = %s/%d", active.ID, active.Version)
	}
	active.Zero()

	backup := filepath.Join(t.TempDir(), "backup")
	manifest, err := CreateBackup(ctx, dataDir, backup)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.RequiredTranscriptKeys) != 1 ||
		manifest.RequiredTranscriptKeys[0].ID != report.KeyID ||
		manifest.RequiredTranscriptKeys[0].Version != report.KeyVersion {
		t.Fatalf("rotated backup requirements = %+v", manifest.RequiredTranscriptKeys)
	}
	if _, err := VerifyBackupWithKey(ctx, backup, keyPath); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := RestoreBackupWithOptions(ctx, backup, restored, RestoreOptions{
		TranscriptKeyFile: keyPath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackupWithKey(
		ctx, backup, transcripts.DefaultKeyPath(restored),
	); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptKeyRotationRejectsUnsafeReplacementKeys(t *testing.T) {
	ctx := context.Background()
	dataDir, keyPath := createRotationFixture(t, ctx)
	unsafeDir := filepath.Join(t.TempDir(), "unsafe")
	unsafePath := filepath.Join(unsafeDir, "key")
	key, err := transcripts.OpenOrCreateKey(unsafePath)
	if err != nil {
		t.Fatal(err)
	}
	key.Zero()
	if err := os.Chmod(unsafePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateTranscriptKey(ctx, dataDir, keyPath, unsafePath); err == nil {
		t.Fatal("mode-0644 replacement key was accepted")
	}
	if err := os.Chmod(unsafePath, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(unsafeDir, "link")
	if err := os.Symlink(unsafePath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateTranscriptKey(ctx, dataDir, keyPath, symlink); err == nil {
		t.Fatal("symlink replacement key was accepted")
	}
}

func TestTranscriptKeyRotationResumesTransitionalCrash(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("database-committed-%t", committed), func(t *testing.T) {
			ctx := context.Background()
			dataDir, keyPath := createRotationFixture(t, ctx)
			oldKey, err := transcripts.LoadKey(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			newKey, err := transcripts.NewInstallationKey(oldKey.ID, oldKey.Version+1)
			if err != nil {
				t.Fatal(err)
			}
			if err := transcripts.ActivateTransition(keyPath, newKey, oldKey); err != nil {
				t.Fatal(err)
			}
			if committed {
				if _, err := rewrapTranscriptKeysWithHook(
					ctx, filepath.Join(dataDir, "meridian.db"), oldKey, newKey, nil,
				); err != nil {
					t.Fatal(err)
				}
			}
			oldKey.Zero()
			newKey.Zero()
			report, err := RotateTranscriptKey(ctx, dataDir, keyPath, "")
			if err != nil {
				t.Fatal(err)
			}
			active, err := transcripts.LoadKey(keyPath)
			if err != nil {
				t.Fatal(err)
			}
			defer active.Zero()
			if active.Transitional() || active.ID != report.KeyID ||
				active.Version != report.KeyVersion {
				t.Fatalf("resumed active key/report = %s/%d transition=%t %+v",
					active.ID, active.Version, active.Transitional(), report)
			}
			if err := deepVerifyTranscripts(
				ctx, filepath.Join(dataDir, "meridian.db"), active,
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func createRotationFixture(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := transcripts.DefaultKeyPath(dataDir)
	key, err := transcripts.OpenOrCreateKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, domain.Project{
			ID: "rotation-project", Name: "rotation",
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		for index := 1; index <= 2; index++ {
			capsuleID := domain.CapsuleID(fmt.Sprintf("rotation-capsule-%d", index))
			if err := tx.InsertCapsule(ctx, domain.Capsule{
				ID: capsuleID, ProjectID: "rotation-project", Name: string(capsuleID),
				State: domain.CapsuleReady, DesiredState: domain.IntentReady,
				CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			}); err != nil {
				return err
			}
			threadID := domain.ThreadID(fmt.Sprintf("rotation-thread-%d", index))
			dek, err := key.NewDEK()
			if err != nil {
				return err
			}
			wrapped, err := key.WrapDEK(threadID, dek)
			if err != nil {
				zeroMaintenanceBytes(dek)
				return err
			}
			messageID := domain.ThreadMessageID(fmt.Sprintf("rotation-message-%d", index))
			created := now.Add(time.Duration(index) * time.Second)
			metadata := transcripts.FrameMetadata{
				ThreadID: threadID, MessageID: messageID, MessageSequence: 1,
				BlockSequence: 1, Role: domain.ThreadRoleUser,
				MessageKind: domain.ThreadMessagePrompt, BlockKind: domain.ThreadBlockText,
				CreatedAt: created.Format(time.RFC3339Nano),
			}
			envelope, err := transcripts.Seal(dek, metadata, []byte("rotation secret"))
			zeroMaintenanceBytes(dek)
			if err != nil {
				return err
			}
			if err := tx.InsertThread(ctx, domain.Thread{
				ID: threadID, CapsuleID: capsuleID, State: domain.ThreadActive,
				AdapterID: "adapter", WrappedDEK: wrapped, KEKID: key.ID,
				KEKVersion: key.Version, EnvelopeVersion: transcripts.EnvelopeVersion,
				CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			}); err != nil {
				return err
			}
			if err := tx.AppendThreadMessage(ctx, domain.ThreadMessage{
				ID: messageID, ThreadID: threadID, Sequence: 1,
				Role: metadata.Role, Kind: metadata.MessageKind, CreatedAt: created,
				Blocks: []domain.ThreadBlock{{
					ID:       domain.ThreadBlockID(fmt.Sprintf("rotation-block-%d", index)),
					ThreadID: threadID, MessageID: messageID,
					MessageSequence: 1, Sequence: 1, Kind: metadata.BlockKind,
					EnvelopeVersion: transcripts.EnvelopeVersion,
					Ciphertext:      envelope, CreatedAt: created,
				}},
			}); err != nil {
				return err
			}
		}
		thread, err := tx.GetThread(ctx, "rotation-thread-2")
		if err != nil {
			return err
		}
		return tx.CryptoShredThread(ctx, thread.ID, thread.ResourceVersion, now.Add(time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	key.Zero()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dataDir, keyPath
}

func zeroMaintenanceBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
