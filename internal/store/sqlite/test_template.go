package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// testTemplate is one fully migrated database, built once per test binary.
// Replaying every migration for each test fixture costs about 0.1 s, and
// about 3 s under the race detector; copying the migrated file is the same
// schema for a fraction of that. Production binaries never use it: Open
// seeds only when testing.Testing() reports a test binary.
var testTemplate struct {
	once sync.Once
	data []byte
	err  error
}

func seedTestDatabase(path string) error {
	testTemplate.once.Do(func() {
		testTemplate.data, testTemplate.err = buildTestTemplate()
	})
	if testTemplate.err != nil {
		return fmt.Errorf("build test database template: %w", testTemplate.err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("seed test database: %w", err)
	}
	if _, err := file.Write(testTemplate.data); err != nil {
		_ = file.Close()
		return fmt.Errorf("seed test database: %w", err)
	}
	return file.Close()
}

func buildTestTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "meridian-sqlite-template-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	store, err := open(context.Background(), dir, false)
	if err != nil {
		return nil, err
	}
	// Fold the write-ahead log into the main file so one file is the whole
	// database.
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, "meridian.db"))
}
