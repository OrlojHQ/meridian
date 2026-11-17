package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// Migration 0032 edits sqlite_master directly. The result must still pass
// SQLite's own integrity check, and every pooled connection must see the
// widened CHECK.
func TestRepositoryAccessSchemaEditIsSound(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var result string
	if err := store.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		t.Fatalf("integrity_check = %q, %v", result, err)
	}
	var conns []*sql.Conn
	for range 2 {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conns = append(conns, conn)
	}
	for index, conn := range conns {
		var definition string
		if err := conn.QueryRowContext(ctx,
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'capsules'").Scan(&definition); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(definition, "'repository_access'") {
			t.Fatalf("connection %d sees the old capsules CHECK", index)
		}
	}
}
