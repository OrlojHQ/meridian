package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestGrantLapseMigrationKeepsRemainingTime(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsBefore(t, db, 36)
	now := time.Now().UTC()
	stamp := func(at time.Time) string { return at.Format(time.RFC3339Nano) }
	created := now.Add(-2 * time.Hour)
	later := now.Add(5 * time.Hour)
	soon := now.Add(time.Minute)
	statements := []string{
		`INSERT INTO projects(owner_user_id, id, name, created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'p1', 'p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	}
	for _, grant := range []struct{ id, state, deadline, revoked, reason string }{
		{"later", "active", stamp(later), "NULL", "''"},
		{"soon", "active", stamp(soon), "NULL", "''"},
		{"expired", "expired", stamp(created.Add(time.Hour)), "'" + stamp(created.Add(time.Hour)) + "'", "'deadline'"},
	} {
		statements = append(statements,
			`INSERT INTO capsules(owner_user_id, id, project_id, timeline_id, name, state, desired_state, created_at, updated_at, resource_version)
				VALUES ('usr_bootstrap_owner', 'cap-`+grant.id+`', 'p1', 'tl-`+grant.id+`', '`+grant.id+`', 'Ready', 'Ready',
					'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
			`INSERT INTO timelines(owner_user_id, id, project_id, capsule_id, reason, created_at)
				VALUES ('usr_bootstrap_owner', 'tl-`+grant.id+`', 'p1', 'cap-`+grant.id+`', 'root', '2026-01-01T00:00:00Z')`,
			`INSERT INTO coordination_grants(owner_user_id, id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
					max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link, allowed_harnesses,
					deadline_at, on_deadline, descendants_created, revoked_at, revoke_reason,
					created_at, updated_at, resource_version)
				VALUES ('usr_bootstrap_owner', '`+grant.id+`', 'p1', 'thread-`+grant.id+`', 'cap-`+grant.id+`', '`+grant.state+`', 'auto',
					1, 4, 8, 4, 50, '["mock"]', '`+grant.deadline+`', 'cancel_and_pause', 3, `+grant.revoked+`, `+grant.reason+`,
					'`+stamp(created)+`', '`+stamp(created)+`', 2)`,
		)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	near := func(got, want time.Time, slack time.Duration) bool {
		return got.Sub(want).Abs() <= slack
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		grant, err := reader.GetCoordinationGrant(ctx, "later")
		if err != nil {
			return err
		}
		// The remaining deadline becomes the limit, starting now.
		if (grant.UnattendedLimit-5*time.Hour).Abs() > 5*time.Second || !near(grant.LapsesAt(), later, 5*time.Second) ||
			!near(grant.LastOperatorAt, now, 10*time.Second) || !grant.LapsedAt.IsZero() ||
			grant.DescendantsCreated != 3 || !grant.Usable(now) {
			t.Fatalf("migrated active grant = %+v", grant)
		}
		// Too little time left for the minimum: it still stops at its deadline.
		grant, err = reader.GetCoordinationGrant(ctx, "soon")
		if err != nil {
			return err
		}
		if grant.UnattendedLimit != domain.CoordinationMinUnattended || !near(grant.LapsesAt(), soon, time.Millisecond) {
			t.Fatalf("migrated short grant = %+v", grant)
		}
		grant, err = reader.GetCoordinationGrant(ctx, "expired")
		if err != nil {
			return err
		}
		if grant.State != domain.GrantExpired || grant.RevokeReason != domain.RevokeDeadline ||
			!near(grant.LastOperatorAt, created, time.Millisecond) || grant.Usable(now) {
			t.Fatalf("migrated expired grant = %+v", grant)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", store.Path()+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	// Seed the Capsule like the rows above, without foreign keys.
	if _, err := raw.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO capsules(owner_user_id, id, project_id, timeline_id, name, state, desired_state,
			created_at, updated_at, resource_version)
		VALUES ('usr_bootstrap_owner', 'cap-new', 'p1', 'tl-new', 'new', 'Ready', 'Ready',
			'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	insertGrant := func(id, columns, values string) string {
		return `INSERT INTO coordination_grants(owner_user_id, id, project_id, root_thread_id, root_capsule_id, state,
				spawn_approval, max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link,
				allowed_harnesses, on_deadline, created_at, updated_at, resource_version` + columns + `)
			VALUES ('usr_bootstrap_owner', '` + id + `', 'p1', 'thread-` + id + `', 'cap-new', 'active', 'auto',
				1, 4, 8, 4, 50, '[]', 'cancel_and_pause', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1` + values + `)`
	}
	for name, statement := range map[string]string{
		"deadline column":       `SELECT deadline_at FROM coordination_grants`,
		"short limit":           `UPDATE coordination_grants SET unattended_limit_seconds = 60 WHERE id = 'later'`,
		"long limit":            `UPDATE coordination_grants SET unattended_limit_seconds = 259201 WHERE id = 'later'`,
		"attendance backwards":  `UPDATE coordination_grants SET last_operator_at = '2026-01-01T00:00:00Z' WHERE id = 'later'`,
		"attendance unparsable": `UPDATE coordination_grants SET last_operator_at = 'soon' WHERE id = 'later'`,
		"ended grant reopened":  `UPDATE coordination_grants SET last_operator_at = '2099-01-01T00:00:00Z' WHERE id = 'expired'`,
		"ended grant lapsed":    `UPDATE coordination_grants SET lapsed_at = '2099-01-01T00:00:00Z' WHERE id = 'expired'`,
		"ended grant limit":     `UPDATE coordination_grants SET unattended_limit_seconds = 600 WHERE id = 'expired'`,
		"expired reactivated":   `UPDATE coordination_grants SET state = 'active', revoked_at = NULL, revoke_reason = '' WHERE id = 'expired'`,
		"insert lapsed": insertGrant("new", ", last_operator_at, lapsed_at",
			", '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'"),
		"insert without attendance": insertGrant("new", "", ""),
	} {
		if _, err := raw.Exec(statement); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, statement := range []string{
		insertGrant("new", ", last_operator_at", ", '2026-01-01T00:00:00Z'"),
		`UPDATE coordination_grants SET lapsed_at = '2099-01-01T00:00:00Z' WHERE id = 'later'`,
		`UPDATE coordination_grants SET last_operator_at = '2099-01-02T00:00:00Z', lapsed_at = NULL,
			unattended_limit_seconds = 259200 WHERE id = 'later'`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("attendance step %q: %v", statement, err)
		}
	}
}
