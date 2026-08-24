package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/migrations"
	_ "modernc.org/sqlite"
)

func TestUpgradeFromEveryMigrationBoundary(t *testing.T) {
	t.Parallel()
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for boundary := 0; boundary <= len(entries); boundary++ {
		boundary := boundary
		t.Run(fmt.Sprintf("version_%d", boundary), func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, "meridian.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				CREATE TABLE schema_migrations (
					version INTEGER PRIMARY KEY,
					name TEXT NOT NULL UNIQUE,
					applied_at TEXT NOT NULL
				)`); err != nil {
				t.Fatal(err)
			}
			for index, entry := range entries {
				if index >= boundary || entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
					continue
				}
				content, err := migrations.Files.ReadFile(entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(string(content)); err != nil {
					t.Fatalf("apply fixture %s: %v", entry.Name(), err)
				}
				prefix, _, _ := strings.Cut(entry.Name(), "_")
				version, _ := strconv.Atoi(prefix)
				if _, err := db.Exec(
					"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
					version, entry.Name(), "2026-01-01T00:00:00Z",
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.Open(context.Background(), dataDir)
			if err != nil {
				t.Fatalf("upgrade from boundary %d: %v", boundary, err)
			}
			version, err := store.SchemaVersion(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if version != sqlite.CurrentSchemaVersion {
				t.Fatalf("schema version = %d, want %d", version, sqlite.CurrentSchemaVersion)
			}
			store.Close()
		})
	}
}

func TestRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations(version, name, applied_at)
		VALUES (999, '0999_future.sql', '2026-01-01T00:00:00Z')
	`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := sqlite.Open(context.Background(), dataDir); err == nil ||
		!strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("newer schema error = %v", err)
	}
}

func TestRejectsTamperedMigrationRecord(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := sqlite.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"UPDATE schema_migrations SET name = '0005_tampered.sql' WHERE version = 5",
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(context.Background(), dataDir); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("tampered migration error = %v", err)
	}
}
