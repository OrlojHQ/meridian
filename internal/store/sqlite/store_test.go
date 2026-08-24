package sqlite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

func TestMigrationsPersistenceEventsAndConflicts(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 23, 1, 2, 3, 456, time.UTC)
	project := domain.Project{
		ID: "project-1", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	capsule := domain.Capsule{
		ID: "capsule-1", ProjectID: project.ID, Name: "capsule",
		State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.Event{
			ID: "event-1", AggregateType: "capsule", AggregateID: string(capsule.ID),
			Type: "first", Timestamp: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		if err := tx.AppendEvent(ctx, domain.Event{
			ID: "event-2", AggregateType: "capsule", AggregateID: string(capsule.ID),
			Type: "second", Timestamp: now.Add(time.Second), ResourceVersion: 2,
		}); err != nil {
			return err
		}
		return tx.PutIdempotency(ctx, ports.IdempotencyRecord{
			Scope: "test", Key: "key", Outcome: []byte(`{"ok":true}`), CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}

	capsule.ResourceVersion = 2
	capsule.State = domain.CapsulePreparing
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.UpdateCapsule(ctx, capsule, 99)
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("UpdateCapsule conflict = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var migrationCount int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != CurrentSchemaVersion {
		t.Fatalf("migration count = %d", migrationCount)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		gotProject, err := reader.GetProject(ctx, project.ID)
		if err != nil {
			return err
		}
		if gotProject.Name != project.Name || !gotProject.CreatedAt.Equal(project.CreatedAt) {
			t.Fatalf("persisted project = %#v", gotProject)
		}
		gotCapsule, err := reader.GetCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if gotCapsule.State != domain.CapsuleCreating {
			t.Fatalf("rolled back capsule = %#v", gotCapsule)
		}
		events, err := reader.ListEvents(ctx, "capsule", string(capsule.ID))
		if err != nil {
			return err
		}
		if len(events) != 2 || events[0].Type != "first" || events[0].Sequence >= events[1].Sequence {
			t.Fatalf("events out of order: %#v", events)
		}
		record, err := reader.GetIdempotency(ctx, "test", "key")
		if err != nil {
			return err
		}
		if string(record.Outcome) != `{"ok":true}` {
			t.Fatalf("idempotency outcome = %s", record.Outcome)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database permissions = %o", info.Mode().Perm())
	}
	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q", journalMode)
	}
	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign keys = %d", foreignKeys)
	}
}

func TestCapsuleActivityAndSetupMomentCachePersistence(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	var capsule domain.Capsule
	var moment domain.Moment
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		project := domain.Project{
			ID: "project-cache", Name: "cache",
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, project); err != nil {
			return err
		}
		if err := tx.InsertCapsule(ctx, domain.Capsule{
			ID: "capsule-cache", ProjectID: project.ID, Name: "capsule",
			State: domain.CapsulePreparing, DesiredState: domain.IntentReady,
			LastActivityAt: now, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		var err error
		capsule, err = tx.GetCapsule(ctx, "capsule-cache")
		if err != nil {
			return err
		}
		moment = domain.Moment{
			ID: "moment-cache", ProjectID: project.ID, CapsuleID: capsule.ID,
			TimelineID: capsule.TimelineID, Name: "Internal setup cache",
			ArchiveSHA256: strings.Repeat("b", 64), ArchiveSize: 42,
			ManifestSHA256:   strings.Repeat("c", 64),
			ImageDigest:      "sha256:" + strings.Repeat("d", 64),
			ProjectSetupHash: hash, CreatedAt: now, Kind: domain.MomentSetupCache,
		}
		if err := tx.InsertMoment(ctx, moment); err != nil {
			return err
		}
		return tx.PutSetupMomentCache(ctx, domain.SetupMomentCache{
			ProjectID: project.ID, ConfigHash: hash, MomentID: moment.ID, CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		got, err := reader.GetCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if !got.LastActivityAt.Equal(now) {
			t.Fatalf("last activity = %s, want %s", got.LastActivityAt, now)
		}
		cache, err := reader.GetSetupMomentCache(ctx, capsule.ProjectID, hash)
		if err != nil {
			return err
		}
		if cache.MomentID != moment.ID {
			t.Fatalf("cache = %#v", cache)
		}
		if _, err := reader.GetSetupMomentCache(
			ctx, capsule.ProjectID, strings.Repeat("e", 64),
		); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("hash mismatch cache lookup = %v", err)
		}
		gotMoment, err := reader.GetMoment(ctx, cache.MomentID)
		if err != nil {
			return err
		}
		if gotMoment.Kind != domain.MomentSetupCache {
			t.Fatalf("Moment kind = %q", gotMoment.Kind)
		}
		listed, more, err := reader.ListMoments(ctx, capsule.TimelineID, ports.Page{Limit: 10})
		if err != nil {
			return err
		}
		if more || len(listed) != 0 {
			t.Fatalf("setup cache leaked into timeline: %#v", listed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestThreadCRUDOrderingImmutabilityAndCryptoShred(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	key, err := transcripts.NewInstallationKey("test-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	dek, err := key.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	defer zeroTestBytes(dek)
	wrapped, err := key.WrapDEK("thread-1", dek)
	if err != nil {
		t.Fatal(err)
	}
	thread := domain.Thread{
		ID: "thread-1", CapsuleID: "capsule-1", State: domain.ThreadActive,
		AdapterID: "test-adapter", WrappedDEK: wrapped, KEKID: key.ID,
		KEKVersion: key.Version, EnvelopeVersion: transcripts.EnvelopeVersion,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, domain.Project{
			ID: "project-1", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		if err := tx.InsertCapsule(ctx, domain.Capsule{
			ID: "capsule-1", ProjectID: "project-1", Name: "capsule",
			State: domain.CapsuleReady, DesiredState: domain.IntentReady,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.InsertThread(ctx, thread)
	}); err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= 25; sequence++ {
		created := now.Add(time.Duration(sequence) * time.Second)
		metadata := transcripts.FrameMetadata{
			ThreadID: thread.ID, MessageID: domain.ThreadMessageID("message-" + string(rune('a'+sequence))),
			MessageSequence: sequence, BlockSequence: 1, Role: domain.ThreadRoleUser,
			MessageKind: domain.ThreadMessagePrompt, BlockKind: domain.ThreadBlockText,
			CreatedAt: created.Format(time.RFC3339Nano),
		}
		envelope, err := transcripts.Seal(dek, metadata, []byte("private"))
		if err != nil {
			t.Fatal(err)
		}
		message := domain.ThreadMessage{
			ID: metadata.MessageID, ThreadID: thread.ID, Sequence: sequence,
			Role: metadata.Role, Kind: metadata.MessageKind, CreatedAt: created,
			Blocks: []domain.ThreadBlock{{
				ID:       domain.ThreadBlockID("block-" + string(rune('a'+sequence))),
				ThreadID: thread.ID, MessageID: metadata.MessageID,
				MessageSequence: sequence, Sequence: 1, Kind: metadata.BlockKind,
				EnvelopeVersion: transcripts.EnvelopeVersion, Ciphertext: envelope,
				CreatedAt: created,
			}},
		}
		if err := store.Transact(ctx, func(tx ports.Transaction) error {
			return tx.AppendThreadMessage(ctx, message)
		}); err != nil {
			t.Fatalf("append %d: %v", sequence, err)
		}
	}
	var got domain.Thread
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		got, err = reader.GetActiveThread(ctx, thread.CapsuleID)
		if err != nil {
			return err
		}
		messages, more, err := reader.ListThreadMessages(ctx, thread.ID, 5, 10)
		if err != nil {
			return err
		}
		if !more || len(messages) != 10 || messages[0].Sequence != 6 ||
			messages[9].Sequence != 15 {
			t.Fatalf("ordered page = %#v, more=%t", messages, more)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got.MessageCount != 25 || got.EncryptedBytes <= 0 {
		t.Fatalf("Thread counters = %#v", got)
	}
	quotaMessage := domain.ThreadMessage{
		ID: "quota-message", ThreadID: thread.ID, Sequence: transcripts.MaxMessagesPerThread + 1,
		Role: domain.ThreadRoleUser, Kind: domain.ThreadMessagePrompt, CreatedAt: now,
		Blocks: []domain.ThreadBlock{{
			ID: "quota-block", ThreadID: thread.ID, MessageID: "quota-message",
			MessageSequence: transcripts.MaxMessagesPerThread + 1, Sequence: 1,
			Kind: domain.ThreadBlockText, EnvelopeVersion: transcripts.EnvelopeVersion,
			Ciphertext: bytes.Repeat([]byte{1}, 32), CreatedAt: now,
		}},
	}
	copy(quotaMessage.Blocks[0].Ciphertext[:4], []byte("MTF1"))
	if _, err := store.db.ExecContext(
		ctx, "UPDATE threads SET message_count = ? WHERE id = ?",
		transcripts.MaxMessagesPerThread, thread.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.AppendThreadMessage(ctx, quotaMessage)
	}); err == nil {
		t.Fatal("Thread message quota was not enforced")
	}
	if _, err := store.db.ExecContext(
		ctx, "UPDATE threads SET message_count = ?, encrypted_bytes = ? WHERE id = ?",
		got.MessageCount, transcripts.MaxThreadEncryptedBytes, thread.ID,
	); err != nil {
		t.Fatal(err)
	}
	quotaMessage.Sequence = got.MessageCount + 1
	quotaMessage.Blocks[0].MessageSequence = quotaMessage.Sequence
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.AppendThreadMessage(ctx, quotaMessage)
	}); err == nil {
		t.Fatal("Thread encrypted-byte quota was not enforced")
	}
	if _, err := store.db.ExecContext(
		ctx, "UPDATE threads SET encrypted_bytes = ? WHERE id = ?",
		got.EncryptedBytes, thread.ID,
	); err != nil {
		t.Fatal(err)
	}
	rotatedKey, err := transcripts.NewInstallationKey(key.ID, key.Version+1)
	if err != nil {
		t.Fatal(err)
	}
	defer rotatedKey.Zero()
	rewrapped, err := transcripts.RewrapDEK(thread.ID, got.WrappedDEK, key, rotatedKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.RewrapThreadKey(
			ctx, thread.ID, got.ResourceVersion, rewrapped,
			rotatedKey.ID, rotatedKey.Version, now.Add(30*time.Minute),
		)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		got, err = reader.GetThread(ctx, thread.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.KEKVersion != rotatedKey.Version || got.ResourceVersion != 27 {
		t.Fatalf("rewrapped Thread = %#v", got)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE thread_blocks SET ciphertext = ? WHERE id = ?",
		[]byte("replacement"), "block-b"); err == nil {
		t.Fatal("ciphertext update unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM thread_messages WHERE thread_id = ?", thread.ID); err == nil {
		t.Fatal("ThreadMessage delete unexpectedly succeeded")
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.CryptoShredThread(ctx, thread.ID, got.ResourceVersion, now.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		deleted, err := reader.GetThread(ctx, thread.ID)
		if err != nil {
			return err
		}
		if deleted.State != domain.ThreadDeleted || len(deleted.WrappedDEK) != 0 ||
			deleted.MessageCount != 25 {
			t.Fatalf("deleted Thread = %#v", deleted)
		}
		listed, more, err := reader.ListThreads(ctx, thread.CapsuleID, ports.Page{Limit: 10})
		if err != nil {
			return err
		}
		if more || len(listed) != 0 {
			t.Fatalf("normal Thread list exposed deleted Thread: %#v", listed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var ciphertextRows int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM thread_blocks WHERE thread_id = ?", thread.ID,
	).Scan(&ciphertextRows); err != nil || ciphertextRows != 25 {
		t.Fatalf("retained ciphertext rows = %d, %v", ciphertextRows, err)
	}
}

func TestOneActiveThreadPerCapsuleConcurrent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertProject(ctx, domain.Project{
			ID: "project", Name: "project", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.InsertCapsule(ctx, domain.Capsule{
			ID: "capsule", ProjectID: "project", Name: "capsule",
			State: domain.CapsuleReady, DesiredState: domain.IntentReady,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	key, err := transcripts.NewInstallationKey("key", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	var successes atomic.Int32
	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			dek := bytes.Repeat([]byte{byte(index + 1)}, 32)
			id := domain.ThreadID("thread-" + string(rune('a'+index)))
			wrapped, wrapErr := key.WrapDEK(id, dek)
			if wrapErr != nil {
				t.Error(wrapErr)
				return
			}
			err := store.Transact(ctx, func(tx ports.Transaction) error {
				return tx.InsertThread(ctx, domain.Thread{
					ID: id, CapsuleID: "capsule", State: domain.ThreadActive,
					AdapterID: "adapter", WrappedDEK: wrapped, KEKID: key.ID,
					KEKVersion: key.Version, EnvelopeVersion: transcripts.EnvelopeVersion,
					CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
				})
			})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, domain.ErrConflict) {
				t.Errorf("insert error = %v", err)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("active Thread insert successes = %d", successes.Load())
	}
}

func zeroTestBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func TestOpenRejectsUnsafeDatabasePathAndEscapesFileURL(t *testing.T) {
	ctx := context.Background()
	specialDirectory := filepath.Join(t.TempDir(), "state with spaces # and %")
	store, err := Open(ctx, specialDirectory)
	if err != nil {
		t.Fatalf("open special path: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	unsafeDirectory := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(unsafeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(unsafeDirectory, "meridian.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, unsafeDirectory); err == nil {
		t.Fatal("expected database symlink rejection")
	}
}
