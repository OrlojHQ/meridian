package sqlite_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestReportHeadlineMigrationAndTriggers(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "meridian.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsBefore(t, db, 37)
	const at = "'2026-01-01T00:00:00Z'"
	for _, statement := range []string{
		`INSERT INTO projects(owner_user_id, id, name, created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'p1', 'p', ` + at + `, ` + at + `, 1)`,
		`INSERT INTO capsules(owner_user_id, id, project_id, timeline_id, name, state, desired_state, created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'lead', 'p1', 'tl-lead', 'lead', 'Ready', 'Ready', ` + at + `, ` + at + `, 1)`,
		`INSERT INTO capsules(owner_user_id, id, project_id, timeline_id, name, state, desired_state, role, lead_thread_id, link_id,
				created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'child', 'p1', 'tl-child', 'child', 'Ready', 'Ready', 'sub', 'lead-thread', 'link1',
				` + at + `, ` + at + `, 1)`,
		`INSERT INTO timelines(owner_user_id, id, project_id, capsule_id, reason, created_at)
			VALUES ('usr_bootstrap_owner', 'tl-lead', 'p1', 'lead', 'root', ` + at + `),
				('usr_bootstrap_owner', 'tl-child', 'p1', 'child', 'root', ` + at + `)`,
		`INSERT INTO project_thread_intents(owner_user_id, id, project_id, capsule_id, thread_id, run_id, message_id, block_id,
				capsule_name, requested_name, harness, idempotency_key, state, wrapped_dek, kek_id, kek_version,
				envelope_version, ciphertext, created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'intent', 'p1', 'child', 'child-thread', 'run', 'msg', 'blk', 'child', 'child', 'mock', 'k',
				'provisioning', zeroblob(64), 'kek', 1, 1, zeroblob(32), ` + at + `, ` + at + `, 1)`,
		`INSERT INTO coordination_grants(owner_user_id, id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
				max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link, allowed_harnesses,
				on_deadline, descendants_created, last_operator_at, created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'g1', 'p1', 'lead-thread', 'lead', 'active', 'auto', 1, 4, 8, 4, 50, '["mock"]',
				'cancel_and_pause', 1, ` + at + `, ` + at + `, ` + at + `, 2)`,
		`INSERT INTO thread_links(owner_user_id, id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
				child_capsule_id, depth, name, harness, state, report_seq, report_delivered_seq, last_report_status,
				last_report_at, created_at, updated_at, resource_version)
			VALUES ('usr_bootstrap_owner', 'link1', 'g1', 'lead-thread', 'lead', 'intent', 'child-thread', 'child', 1, 'one', 'mock',
				'active', 2, 1, 'turn_ended', ` + at + `, ` + at + `, ` + at + `, 3)`,
	} {
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
	if err := store.View(ctx, func(reader ports.Reader) error {
		link, err := reader.GetThreadLink(ctx, "link1")
		if err != nil {
			return err
		}
		// Reports already delivered in full count as shown.
		if link.ReportHeadlinedSeq != 1 || link.ReportDeliveredSeq != 1 || link.ReportSeq != 2 {
			t.Fatalf("migrated link = %+v", link)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, refused := range []string{
		`UPDATE thread_links SET report_headlined_seq = 0 WHERE id = 'link1'`,
		`UPDATE thread_links SET report_headlined_seq = 3 WHERE id = 'link1'`,
	} {
		if _, err := db.Exec(refused); err == nil {
			t.Fatalf("accepted: %s", refused)
		}
	}
	if _, err := db.Exec(`UPDATE thread_links SET report_headlined_seq = 2 WHERE id = 'link1'`); err != nil {
		t.Fatalf("headline the latest report: %v", err)
	}
}
