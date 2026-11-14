package sqlite_test

import (
	"context"
	"database/sql"
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
)

// applyMigrationsBefore builds a database at the schema just below version.
func applyMigrationsBefore(t *testing.T, db *sql.DB, version int) {
	t.Helper()
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, _ := strings.Cut(entry.Name(), "_")
		number, _ := strconv.Atoi(prefix)
		if number >= version {
			continue
		}
		content, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if _, err := db.Exec(
			"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
			number, entry.Name(), "2026-01-01T00:00:00Z",
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubagentLifecycleMigrationKeepsLinksAndRequests(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsBefore(t, db, 16)
	seed, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id, name, created_at, updated_at, resource_version)
			VALUES ('p1', 'p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, created_at, updated_at, resource_version)
			VALUES ('lead', 'p1', 'tl-lead', 'lead', 'Ready', 'Ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, role, lead_thread_id, link_id,
				created_at, updated_at, resource_version)
			VALUES ('child', 'p1', 'tl-child', 'child', 'Ready', 'Ready', 'sub', 'lead-thread', 'link1',
				'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO timelines(id, project_id, capsule_id, reason, created_at)
			VALUES ('tl-lead', 'p1', 'lead', 'root', '2026-01-01T00:00:00Z'),
				('tl-child', 'p1', 'child', 'root', '2026-01-01T00:00:00Z')`,
		`INSERT INTO project_thread_intents(id, project_id, capsule_id, thread_id, run_id, message_id, block_id,
				capsule_name, requested_name, harness, idempotency_key, state, wrapped_dek, kek_id, kek_version,
				envelope_version, ciphertext, created_at, updated_at, resource_version)
			VALUES ('intent', 'p1', 'child', 'child-thread', 'run', 'msg', 'blk', 'child', 'child', 'mock', 'k',
				'provisioning', zeroblob(64), 'kek', 1, 1, zeroblob(32), '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO coordination_grants(id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
				max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link, allowed_harnesses,
				deadline_at, on_deadline, descendants_created, created_at, updated_at, resource_version)
			VALUES ('g1', 'p1', 'lead-thread', 'lead', 'active', 'auto', 1, 4, 8, 4, 50, '["mock"]',
				'2026-01-02T00:00:00Z', 'cancel_and_pause', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 2)`,
		`INSERT INTO thread_links(id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
				child_capsule_id, depth, name, harness, state, report_seq, last_report_status, last_report_at,
				operator_messages, operator_noticed, created_at, updated_at, resource_version)
			VALUES ('link1', 'g1', 'lead-thread', 'lead', 'intent', 'child-thread', 'child', 1, 'one', 'mock', 'active',
				1, 'turn_ended', '2026-01-01T00:05:00Z', 2, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:05:00Z', 3)`,
		`INSERT INTO coordination_requests(id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id, created_at)
			VALUES ('r1', 'g1', 'lead', 'spawn-one', '` + strings.Repeat("a", 64) + `', 'spawn', 'link1', '2026-01-01T00:00:00Z')`,
	} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
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
	if err := store.View(ctx, func(reader ports.Reader) error {
		grant, err := reader.GetCoordinationGrant(ctx, "g1")
		if err != nil {
			return err
		}
		// Migration 0016 backfilled the archive period that was the default
		// then; later default changes do not rewrite existing grants.
		if grant.ArchiveAfter != 24*time.Hour {
			t.Fatalf("migrated archiveAfter = %s", grant.ArchiveAfter)
		}
		link, err := reader.GetThreadLink(ctx, "link1")
		if err != nil {
			return err
		}
		if link.State != domain.LinkActive || link.OperatorMessages != 2 || link.OperatorNoticed != 1 ||
			link.ReportSeq != 1 || !link.ArchivedAt.IsZero() || !link.PromotedAt.IsZero() {
			t.Fatalf("migrated link = %#v", link)
		}
		request, err := reader.GetCoordinationRequest(ctx, "lead", "spawn-one")
		if err != nil || request.LinkID != "link1" || request.State != domain.RequestCompleted {
			t.Fatalf("migrated request = %#v, %v", request, err)
		}
		reportedAt := time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
		if due, err := reader.ListArchivableThreadLinks(ctx, reportedAt.Add(time.Hour), 10); err != nil || len(due) != 0 {
			t.Fatalf("archivable before the period = %d, %v", len(due), err)
		}
		if due, err := reader.ListArchivableThreadLinks(ctx, reportedAt.Add(25*time.Hour), 10); err != nil ||
			len(due) != 1 || due[0].ID != "link1" {
			t.Fatalf("archivable after the period = %#v, %v", due, err)
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
	for name, statement := range map[string]string{
		"detach without promotion":     `UPDATE thread_links SET state = 'closed', close_reason = 'detached' WHERE id = 'link1'`,
		"archive and promote together": `UPDATE thread_links SET archived_at = '2026-01-02T00:00:00Z', promoted_at = '2026-01-02T00:00:00Z' WHERE id = 'link1'`,
		"main without promotion":       `UPDATE capsules SET role = 'main' WHERE id = 'child'`,
		"demote":                       `UPDATE capsules SET role = 'sub' WHERE id = 'lead'`,
		"short archive period":         `UPDATE coordination_grants SET archive_after_seconds = 30 WHERE id = 'g1'`,
	} {
		if _, err := raw.Exec(statement); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, statement := range []string{
		`UPDATE thread_links SET promoted_at = '2026-01-02T00:00:00Z' WHERE id = 'link1'`,
		`UPDATE capsules SET role = 'main', promoted_at = '2026-01-02T00:00:00Z' WHERE id = 'child'`,
		`UPDATE thread_links SET state = 'closed', close_reason = 'detached' WHERE id = 'link1'`,
		`UPDATE capsules SET lead_thread_id = NULL, link_id = NULL WHERE id = 'child'`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("promotion step %q: %v", statement, err)
		}
	}
	if _, err := raw.Exec(`UPDATE thread_links SET promoted_at = NULL WHERE id = 'link1'`); err == nil {
		t.Fatal("promotion was reverted")
	}
}

func TestSpawnApprovalMigrationAllowsGrantUpdates(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsBefore(t, db, 17)
	seed, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id, name, created_at, updated_at, resource_version)
			VALUES ('p1', 'p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, created_at, updated_at, resource_version)
			VALUES ('lead', 'p1', 'tl-lead', 'lead', 'Ready', 'Ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO timelines(id, project_id, capsule_id, reason, created_at)
			VALUES ('tl-lead', 'p1', 'lead', 'root', '2026-01-01T00:00:00Z')`,
		`INSERT INTO coordination_grants(id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
				max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link, allowed_harnesses,
				deadline_at, on_deadline, descendants_created, archive_after_seconds, created_at, updated_at, resource_version)
			VALUES ('g1', 'p1', 'lead-thread', 'lead', 'active', 'auto', 1, 4, 8, 4, 50, '["mock"]',
				'2026-01-02T00:00:00Z', 'cancel_and_pause', 3, 86400, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
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
	if err := store.View(ctx, func(reader ports.Reader) error {
		grant, err := reader.GetCoordinationGrant(ctx, "g1")
		if err != nil {
			return err
		}
		if grant.SpawnApproval != domain.SpawnAuto || grant.MaxChildren != 4 {
			t.Fatalf("migrated grant = %#v", grant)
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
	for name, statement := range map[string]string{
		"identity project": `UPDATE coordination_grants SET project_id = 'p2' WHERE id = 'g1'`,
		"identity depth":   `UPDATE coordination_grants SET max_depth = 2 WHERE id = 'g1'`,
		"bad approval":     `UPDATE coordination_grants SET spawn_approval = 'lead' WHERE id = 'g1'`,
	} {
		if _, err := raw.Exec(statement); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for _, statement := range []string{
		`UPDATE coordination_grants SET spawn_approval = 'operator', max_children = 8, max_descendants = 8 WHERE id = 'g1'`,
		`UPDATE coordination_grants SET max_children = 2, max_descendants = 2 WHERE id = 'g1'`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("grant update %q: %v", statement, err)
		}
	}
}

func TestSubagentResultsMigrationAddsGitMetadataAndDone(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsBefore(t, db, 18)
	seed, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO projects(id, name, created_at, updated_at, resource_version)
			VALUES ('p1', 'p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, created_at, updated_at, resource_version)
			VALUES ('lead', 'p1', 'tl-lead', 'lead', 'Ready', 'Ready', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, role, lead_thread_id, link_id,
				created_at, updated_at, resource_version)
			VALUES ('child', 'p1', 'tl-child', 'child', 'Ready', 'Ready', 'sub', 'lead-thread', 'link1',
				'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO timelines(id, project_id, capsule_id, reason, created_at)
			VALUES ('tl-lead', 'p1', 'lead', 'root', '2026-01-01T00:00:00Z'),
				('tl-child', 'p1', 'child', 'root', '2026-01-01T00:00:00Z')`,
		`INSERT INTO project_thread_intents(id, project_id, capsule_id, thread_id, run_id, message_id, block_id,
				capsule_name, requested_name, harness, idempotency_key, state, wrapped_dek, kek_id, kek_version,
				envelope_version, ciphertext, created_at, updated_at, resource_version)
			VALUES ('intent', 'p1', 'child', 'child-thread', 'run', 'msg', 'blk', 'child', 'child', 'mock', 'k',
				'provisioning', zeroblob(64), 'kek', 1, 1, zeroblob(32), '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO coordination_grants(id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
				max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link, allowed_harnesses,
				deadline_at, on_deadline, descendants_created, archive_after_seconds, created_at, updated_at, resource_version)
			VALUES ('g1', 'p1', 'lead-thread', 'lead', 'active', 'auto', 1, 4, 8, 4, 50, '["mock"]',
				'2026-01-02T00:00:00Z', 'cancel_and_pause', 1, 86400, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 2)`,
		`INSERT INTO thread_links(id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
				child_capsule_id, depth, name, harness, state, report_seq, last_report_status, last_report_at,
				operator_messages, operator_noticed, created_at, updated_at, resource_version)
			VALUES ('link1', 'g1', 'lead-thread', 'lead', 'intent', 'child-thread', 'child', 1, 'one', 'mock', 'active',
				1, 'turn_ended', '2026-01-01T00:05:00Z', 0, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:05:00Z', 3)`,
	} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
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
	if err := store.View(ctx, func(reader ports.Reader) error {
		link, err := reader.GetThreadLink(ctx, "link1")
		if err != nil {
			return err
		}
		if link.BaseCommit != "" || link.Head != "" || link.FilesChanged != 0 || link.LastReportStatus != domain.ReportTurnEnded {
			t.Fatalf("migrated link = %#v", link)
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
	if _, err := raw.Exec(`UPDATE thread_links SET last_report_status = 'done' WHERE id = 'link1'`); err != nil {
		t.Fatalf("done status: %v", err)
	}
	if _, err := raw.Exec(`UPDATE thread_links SET last_report_status = 'blocked' WHERE id = 'link1'`); err != nil {
		t.Fatalf("blocked status: %v", err)
	}
	if _, err := raw.Exec(`UPDATE thread_links SET base_commit = 'aaaaaaaa', head = 'bbbbbbbb', files_changed = 2,
		insertions = 4, deletions = 1 WHERE id = 'link1'`); err != nil {
		t.Fatalf("git metadata: %v", err)
	}
	if _, err := raw.Exec(`UPDATE thread_links SET base_commit = 'cccccccc' WHERE id = 'link1'`); err == nil {
		t.Fatal("base_commit was rewritten")
	}
	if _, err := raw.Exec(`INSERT INTO coordination_requests(id, grant_id, capsule_id, request_key, canonical_digest,
		kind, link_id, state, created_at, updated_at, resource_version)
		VALUES ('r-report', 'g1', 'child', 'done-1', '` + strings.Repeat("b", 64) + `', 'report', 'link1',
		'completed', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatalf("report kind: %v", err)
	}
}
