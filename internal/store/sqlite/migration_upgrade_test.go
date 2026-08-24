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
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
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

func TestMigrationSixBackfillsActivityAndTimelineMomentKind(t *testing.T) {
	ctx := context.Background()
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
		)`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "000") || entry.Name() >= "0006" {
			continue
		}
		content, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
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
	created := "2026-08-24T12:00:00Z"
	hash := strings.Repeat("a", 64)
	seed, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`
		INSERT INTO projects(id, name, repository_url, setup_argv, image_reference, created_at, updated_at, resource_version)
		VALUES ('project', 'project', '', '[]', '', ?, ?, 1)`,
		created, created,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`
		INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, provider_resource_id,
			origin_moment_id, restore_complete, maintenance, failure, created_at, updated_at, resource_version)
		VALUES ('capsule', 'project', 'timeline', 'capsule', 'Ready', 'Ready', 'resource',
			NULL, 1, '', '', ?, ?, 1)`,
		created, created,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`
		INSERT INTO timelines(id, project_id, capsule_id, forked_from_moment_id, reason, created_at)
		VALUES ('timeline', 'project', 'capsule', NULL, 'root', ?)`,
		created,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`
		INSERT INTO moments(id, project_id, capsule_id, timeline_id, parent_moment_id, name,
			archive_sha256, archive_size, manifest_sha256, image_digest, project_setup_hash,
			git_branch, git_head, git_dirty_summary, created_at, final)
		VALUES ('moment', 'project', 'capsule', 'timeline', NULL, 'checkpoint',
			?, 1, ?, 'sha256:image', ?, '', '', '', ?, 0)`,
		hash, strings.Repeat("b", 64), hash, created,
	); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wantTime, _ := time.Parse(time.RFC3339, created)
	if err := store.View(ctx, func(reader ports.Reader) error {
		capsule, err := reader.GetCapsule(ctx, "capsule")
		if err != nil {
			return err
		}
		if !capsule.LastActivityAt.Equal(wantTime) {
			t.Fatalf("last activity = %s, want %s", capsule.LastActivityAt, wantTime)
		}
		moment, err := reader.GetMoment(ctx, "moment")
		if err != nil {
			return err
		}
		if moment.Kind != domain.MomentTimeline {
			t.Fatalf("migrated Moment kind = %q", moment.Kind)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
