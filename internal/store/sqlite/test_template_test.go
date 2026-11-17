package sqlite

import (
	"context"
	"strings"
	"testing"
)

func schemaOf(t *testing.T, store *Store) string {
	t.Helper()
	rows, err := store.db.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var kind, name, definition string
		if err := rows.Scan(&kind, &name, &definition); err != nil {
			t.Fatal(err)
		}
		out.WriteString(kind + " " + name + "\n" + definition + "\n")
	}
	return out.String()
}

// A test database seeded from the template is the same database a fresh
// migration produces: same schema, same applied migrations, same mode.
func TestSeededDatabaseMatchesAFreshMigration(t *testing.T) {
	ctx := context.Background()
	seeded, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer seeded.Close()
	migrated, err := open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if a, b := schemaOf(t, seeded), schemaOf(t, migrated); a != b {
		t.Fatalf("seeded schema differs from a fresh migration:\n--- seeded\n%s\n--- migrated\n%s", a, b)
	}
	for _, store := range []*Store{seeded, migrated} {
		version, err := store.SchemaVersion(ctx)
		if err != nil || version != CurrentSchemaVersion {
			t.Fatalf("schema version = %d, %v", version, err)
		}
		var mode, integrity string
		if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode = %q, %v", mode, err)
		}
		if err := store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity_check = %q, %v", integrity, err)
		}
	}
	// Each seeded database is its own file.
	if _, err := seeded.db.Exec(`INSERT INTO users(id, role, created_at, updated_at) VALUES ('usr_seeded', 'member', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var count int
	if err := other.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = 'usr_seeded'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("a second seeded database shares rows: %d, %v", count, err)
	}
}
