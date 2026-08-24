package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestPublishDeduplicatesConcurrentlyAndReopens(t *testing.T) {
	data := []byte("immutable artifact")
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	results := make([]string, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			artifact, err := store.Publish(context.Background(), bytes.NewReader(data))
			if err != nil {
				t.Errorf("publish: %v", err)
				return
			}
			results[index] = artifact.Digest
		}()
	}
	wait.Wait()
	for _, digest := range results {
		if digest == "" || digest != results[0] {
			t.Fatalf("dedup digests differ: %#v", results)
		}
	}
	reopened, err := Open(filepath.Dir(store.root))
	if err != nil {
		t.Fatal(err)
	}
	reader, size, err := reopened.Open(context.Background(), results[0])
	if err != nil {
		t.Fatal(err)
	}
	value, err := io.ReadAll(reader)
	if err != nil || reader.Close() != nil {
		t.Fatal(err)
	}
	if size != int64(len(data)) || !bytes.Equal(value, data) {
		t.Fatalf("reopened artifact differs: size=%d value=%q", size, value)
	}
	info, err := os.Stat(filepath.Join(store.blobs, results[0][:2], results[0]))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("blob mode = %o", info.Mode().Perm())
	}
}

func TestRejectsCorruptionInvalidDigestsAndSymlinks(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Publish(context.Background(), bytes.NewBufferString("correct"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.blobs, artifact.Digest[:2], artifact.Digest)
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), artifact.Digest); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("corrupt open error = %v", err)
	}
	if _, _, err := store.Open(context.Background(), "../bad"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid digest error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, "outside"), path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), artifact.Digest); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("symlink open error = %v", err)
	}
}

func TestInterruptedPublicationLeavesNoAddressableBlob(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Publish(ctx, bytes.NewReader(make([]byte, 1024))); !errors.Is(err, context.Canceled) {
		t.Fatalf("publish error = %v", err)
	}
	entries, err := os.ReadDir(store.temp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files remain: %v", entries)
	}
}

func TestPublicationFsyncAndLinkFailuresLeaveNoBlob(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		inject func(*Store)
	}{
		{
			name: "fsync",
			inject: func(store *Store) {
				store.syncFile = func(*os.File) error { return errors.New("injected fsync failure") }
			},
		},
		{
			name: "link",
			inject: func(store *Store) {
				store.link = func(string, string) error { return errors.New("injected link failure") }
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			test.inject(store)
			if _, err := store.Publish(context.Background(), strings.NewReader("unpublished")); err == nil {
				t.Fatal("fault-injected publication unexpectedly succeeded")
			}
			entries, err := os.ReadDir(store.temp)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary files survived failed publication: %v", entries)
			}
			var blobs int
			if err := filepath.WalkDir(store.blobs, func(_ string, entry os.DirEntry, err error) error {
				if err == nil && entry.Type().IsRegular() {
					blobs++
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if blobs != 0 {
				t.Fatalf("%d blobs survived failed publication", blobs)
			}
		})
	}
}

func TestReopenCleansTempsAndCancelledReaderStops(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Publish(context.Background(), bytes.NewReader(bytes.Repeat([]byte("x"), 4096)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.temp, ".publish-orphan"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.temp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("orphan cleanup = %v, %v", entries, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Open(ctx, artifact.Digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open error = %v", err)
	}
}

func TestOpenRejectsSymlinkPrefixAttack(t *testing.T) {
	dataDir := t.TempDir()
	store, err := Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(store.blobs, "aa")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(context.Background(), digest); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("prefix attack error = %v", err)
	}
}

func TestOpenRejectsSymlinkDataDirectory(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(parent, "data")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlink data directory accepted")
	}
}
