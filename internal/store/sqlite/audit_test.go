package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func TestAuditRecordsAreAppendOnlyFilteredAndPruned(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	records := []domain.AuditRecord{
		{RecordedAt: old, PrincipalKind: "user", UserID: "usr_a", Action: domain.AuditSecretPut, ObjectType: "secret", ObjectID: "a", Result: domain.AuditAllowed},
		{RecordedAt: recent, PrincipalKind: "user", UserID: "usr_b", Action: domain.AuditCrossOwnerAccess, ObjectType: "project", ObjectID: "p", Result: domain.AuditDenied},
		{RecordedAt: recent, PrincipalKind: "system", Action: domain.AuditGrantRevoke, ObjectType: "coordination_grant", ObjectID: "g", Result: domain.AuditAllowed},
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		for _, record := range records {
			if err := tx.AppendAuditRecord(ctx, record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A record must match its principal kind.
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.AppendAuditRecord(ctx, domain.AuditRecord{
			RecordedAt: recent, PrincipalKind: "system", UserID: "usr_a",
			Action: domain.AuditSecretPut, ObjectType: "secret", ObjectID: "x", Result: domain.AuditAllowed,
		})
	}); err == nil {
		t.Fatal("system record with a user stored")
	}
	if _, err := store.db.Exec("UPDATE audit_records SET result = 'allowed' WHERE object_id = 'p'"); err == nil ||
		!strings.Contains(err.Error(), "append-only") {
		t.Fatalf("audit update = %v", err)
	}

	list := func(user domain.UserID, after int64, limit int) ([]domain.AuditRecord, bool) {
		t.Helper()
		var items []domain.AuditRecord
		var more bool
		if err := store.View(ctx, func(r ports.Reader) error {
			var err error
			items, more, err = r.ListAuditRecords(ctx, user, after, limit)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return items, more
	}
	if all, more := list("", 0, 10); len(all) != 3 || more || all[0].Sequence >= all[1].Sequence {
		t.Fatalf("all records = %+v, more %v", all, more)
	}
	if mine, _ := list("usr_b", 0, 10); len(mine) != 1 || mine[0].ObjectID != "p" || mine[0].Result != domain.AuditDenied {
		t.Fatalf("usr_b records = %+v", mine)
	}
	first, more := list("", 0, 1)
	if len(first) != 1 || !more {
		t.Fatalf("first page = %+v, more %v", first, more)
	}
	if rest, _ := list("", first[0].Sequence, 10); len(rest) != 2 {
		t.Fatalf("records after %d = %+v", first[0].Sequence, rest)
	}

	var pruned int
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		pruned, err = tx.PruneAuditRecords(ctx, recent.Add(-time.Hour))
		return err
	}); err != nil || pruned != 1 {
		t.Fatalf("pruned = %d, %v", pruned, err)
	}
	if all, _ := list("", 0, 10); len(all) != 2 {
		t.Fatalf("records after prune = %+v", all)
	}
}
