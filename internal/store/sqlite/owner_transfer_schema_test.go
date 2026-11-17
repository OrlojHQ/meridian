package sqlite

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/ports"
)

// ownerTablesThatStay hold rows that a departed-member transfer deliberately
// leaves with their owner (ADR 0038, 0046, 0049, 0051).
var ownerTablesThatStay = []string{
	"github_connections", "idempotency_outcomes", "provider_connections",
	"user_profile_connections", "user_profiles",
}

// Every owned table must either move on a departed-member transfer or be
// named as staying, and a moved table's owner trigger must allow exactly the
// move owner_transfers names. A new table that misses either step makes
// TransferOwnership fail with "owner immutable" or strands rows on a user
// that is about to be deleted.
func TestEveryOwnedTableHasATransferRule(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rows, err := store.db.Query(`
		SELECT m.name FROM sqlite_master m
		WHERE m.type = 'table'
			AND EXISTS (SELECT 1 FROM pragma_table_info(m.name) WHERE name = 'owner_user_id')
		ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	var owned []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		owned = append(owned, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range owned {
		if !slices.Contains(transferredTables, table) && !slices.Contains(ownerTablesThatStay, table) {
			t.Errorf("%s has owner_user_id but is neither in transferredTables nor in ownerTablesThatStay", table)
		}
	}
	for _, table := range transferredTables {
		if !slices.Contains(owned, table) {
			t.Errorf("transferredTables names %s, which has no owner_user_id", table)
			continue
		}
		var definition string
		err := store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`,
			table+"_owner_immutable").Scan(&definition)
		if err != nil {
			continue
		}
		if !strings.Contains(definition, "owner_transfers") {
			t.Errorf("%s_owner_immutable does not allow a departed-member transfer", table)
		}
	}
}

func TestTransferMovesProjectChecks(t *testing.T) {
	ctx := context.Background()
	store, now := openProfileStore(t)
	at := formatTime(now)
	for _, statement := range []string{
		`INSERT INTO users(id, role, created_at, updated_at, disabled_at) VALUES ('usr_departed', 'member', '` + at + `', '` + at + `', '` + at + `')`,
		`INSERT INTO projects(id, name, owner_user_id, created_at, updated_at, resource_version) VALUES ('p1', 'p', 'usr_departed', '` + at + `', '` + at + `', 1)`,
		`INSERT INTO project_checks(project_id, owner_user_id, checks_json, updated_at) VALUES ('p1', 'usr_departed', '[]', '` + at + `')`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.TransferOwnership(ctx, "usr_departed", "usr_other")
	}); err != nil {
		t.Fatalf("transfer = %v", err)
	}
	var owner string
	if err := store.db.QueryRow(`SELECT owner_user_id FROM project_checks WHERE project_id = 'p1'`).Scan(&owner); err != nil || owner != "usr_other" {
		t.Fatalf("project_checks owner = %q, %v", owner, err)
	}
}
