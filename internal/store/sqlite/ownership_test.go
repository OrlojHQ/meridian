package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/migrations"
)

// ownedTables are the tables migration 0023 gives an owner column.
var ownedTables = []string{
	"projects", "secrets", "provider_connections", "harness_setups",
	"capsules", "timelines", "moments", "project_thread_intents", "setup_moment_cache",
	"project_preparation_policy", "coordination_grants", "runs", "threads", "deliveries",
	"capsule_preparations", "capsule_startup", "thread_messages", "thread_blocks",
	"thread_deliveries", "thread_links", "coordination_requests", "harness_setup_revisions",
}

func TestOwnershipMigrationStampsEveryExistingRow(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "meridian.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		prefix, _, _ := strings.Cut(entry.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if err != nil || version >= 23 {
			continue
		}
		content, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if _, err := db.Exec("INSERT INTO schema_migrations VALUES (?, ?, ?)",
			version, entry.Name(), "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// A version 22 installation with rows in tables whose triggers refuse
	// every UPDATE: Timelines and ThreadMessages.
	const at = "2026-01-01T00:00:00Z"
	for _, statement := range []string{
		`INSERT INTO projects(id, name, created_at, updated_at, resource_version) VALUES ('p1', 'p', '` + at + `', '` + at + `', 1)`,
		`INSERT INTO capsules(id, project_id, timeline_id, name, state, desired_state, created_at, updated_at, resource_version)
			VALUES ('c1', 'p1', 't1', 'c', 'Ready', 'Ready', '` + at + `', '` + at + `', 1)`,
		`INSERT INTO timelines(id, project_id, capsule_id, reason, created_at) VALUES ('t1', 'p1', 'c1', 'root', '` + at + `')`,
		`INSERT INTO threads(id, capsule_id, state, adapter_id, wrapped_dek, kek_id, kek_version, envelope_version, created_at, updated_at, resource_version)
			VALUES ('th1', 'c1', 'active', 'a', zeroblob(64), 'k', 1, 1, '` + at + `', '` + at + `', 1)`,
		`INSERT INTO thread_messages(id, thread_id, sequence, role, kind, created_at) VALUES ('m1', 'th1', 1, 'user', 'prompt', '` + at + `')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var role string
	if err := store.db.QueryRow("SELECT role FROM users WHERE id = ?", domain.BootstrapOwnerID).Scan(&role); err != nil || role != "admin" {
		t.Fatalf("bootstrap owner = %q, %v", role, err)
	}
	for _, table := range ownedTables {
		var unowned int
		if err := store.db.QueryRow(
			"SELECT COUNT(*) FROM "+table+" WHERE owner_user_id <> ?", domain.BootstrapOwnerID,
		).Scan(&unowned); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if unowned != 0 {
			t.Fatalf("%s has %d rows not owned by the bootstrap owner", table, unowned)
		}
	}
	// ownedTables is exactly the set with an owner column, and each one
	// refuses an owner change.
	rows, err := store.db.Query(`SELECT m.name FROM sqlite_master m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'owner_user_id' ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	var withOwner []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		// Idempotency outcomes key by owner (migration 0024); the system's
		// key owner is empty, so they are not owned rows.
		if name == "idempotency_outcomes" {
			continue
		}
		withOwner = append(withOwner, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), ownedTables...)
	sort.Strings(want)
	if strings.Join(withOwner, ",") != strings.Join(want, ",") {
		t.Fatalf("tables with an owner = %v, want %v", withOwner, want)
	}
	for _, table := range ownedTables {
		for _, trigger := range []string{table + "_owner_insert", table + "_owner_immutable"} {
			var count int
			if err := store.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?", trigger).Scan(&count); err != nil || count != 1 {
				t.Fatalf("trigger %s: %d, %v", trigger, count, err)
			}
		}
	}
	// The immutability triggers the backfill lifted are back.
	if _, err := store.db.Exec("UPDATE timelines SET reason = 'fork' WHERE id = 't1'"); err == nil {
		t.Fatal("Timeline became mutable")
	}
	if _, err := store.db.Exec("UPDATE thread_messages SET role = 'assistant' WHERE id = 'm1'"); err == nil {
		t.Fatal("ThreadMessage became mutable")
	}
}

func TestOwnersAreStampedInheritedAndImmutable(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`INSERT INTO users(id, role, created_at, updated_at)
		VALUES ('usr_other', 'member', ?, ?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	insertProject := func(id domain.ProjectID, owner domain.UserID) error {
		return store.Transact(ctx, func(tx ports.Transaction) error {
			return tx.InsertProject(ctx, domain.Project{
				OwnerUserID: owner, ID: id, Name: string(id),
				CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			})
		})
	}
	if err := insertProject("unowned", ""); err == nil || !strings.Contains(err.Error(), "owner missing") {
		t.Fatalf("Project without an owner = %v", err)
	}
	if err := insertProject("ghost", "usr_nobody"); err == nil {
		t.Fatal("Project owned by an unknown user was stored")
	}
	if err := insertProject("mine", domain.BootstrapOwnerID); err != nil {
		t.Fatal(err)
	}
	if err := insertProject("theirs", "usr_other"); err != nil {
		t.Fatal(err)
	}

	// A Capsule, its root Timeline, and its Run take the Project's owner.
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, domain.Capsule{
			ID: "their-capsule", ProjectID: "theirs", Name: "c", LauncherHarness: "opencode",
			State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return tx.InsertRun(ctx, domain.Run{
			ID: "their-run", CapsuleID: "their-capsule", Harness: "opencode", State: domain.RunQueued,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}
	for table, id := range map[string]string{
		"capsules": "their-capsule", "timelines": "root-their-capsule", "runs": "their-run",
	} {
		var owner string
		if err := store.db.QueryRow("SELECT owner_user_id FROM "+table+" WHERE id = ?", id).Scan(&owner); err != nil || owner != "usr_other" {
			t.Fatalf("%s owner = %q, %v", table, owner, err)
		}
	}
	capsule, err := (&repository{q: store.db}).GetCapsule(ctx, "their-capsule")
	if err != nil || capsule.OwnerUserID != "usr_other" {
		t.Fatalf("GetCapsule owner = %q, %v", capsule.OwnerUserID, err)
	}

	// A child that names an owner other than its parent's is refused.
	_, err = store.db.Exec(`INSERT INTO capsules(owner_user_id, id, project_id, timeline_id, name, state,
		desired_state, created_at, updated_at, resource_version)
		VALUES (?, 'smuggled', 'theirs', 'root-smuggled', 'c', 'Creating', 'Ready', ?, ?, 1)`,
		domain.BootstrapOwnerID, formatTime(now), formatTime(now))
	if !errors.Is(mapError(err), domain.ErrNotFound) {
		t.Fatalf("cross-owner child = %v", err)
	}

	// Owners never change.
	if _, err := store.db.Exec("UPDATE projects SET owner_user_id = ? WHERE id = 'theirs'", domain.BootstrapOwnerID); err == nil ||
		!strings.Contains(err.Error(), "owner immutable") {
		t.Fatalf("owner transfer = %v", err)
	}
}

func TestRootUpsertsAndGrantsStayWithinOneOwner(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	if _, err := store.db.Exec(`INSERT INTO users(id, role, created_at, updated_at)
		VALUES ('usr_other', 'member', ?, ?)`, formatTime(now), formatTime(now)); err != nil {
		t.Fatal(err)
	}
	theirs := domain.ProviderConnection{
		OwnerUserID: "usr_other", ID: "conn-theirs", Provider: "openai", Name: "theirs", CreatedAt: now,
		KeyID: "k", Nonce: []byte{1}, Ciphertext: []byte{1},
	}
	setup := domain.HarnessSetup{OwnerUserID: "usr_other", ID: "setup-theirs", Name: "theirs", Harness: "codex", CreatedAt: now}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.PutProviderConnection(ctx, theirs); err != nil {
			return err
		}
		if err := tx.PutHarnessSetup(ctx, setup); err != nil {
			return err
		}
		return tx.InsertProject(ctx, domain.Project{
			OwnerUserID: domain.BootstrapOwnerID, ID: "mine", Name: "mine",
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		})
	}); err != nil {
		t.Fatal(err)
	}

	// Another user's write to the same id reads as not found and changes
	// nothing.
	hijack := theirs
	hijack.OwnerUserID, hijack.Name = domain.BootstrapOwnerID, "hijacked"
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.PutProviderConnection(ctx, hijack)
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner connection write = %v", err)
	}
	hijackSetup := setup
	hijackSetup.OwnerUserID, hijackSetup.Name = domain.BootstrapOwnerID, "hijacked"
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.PutHarnessSetup(ctx, hijackSetup)
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner setup write = %v", err)
	}
	if err := store.View(ctx, func(r ports.Reader) error {
		stored, err := r.GetProviderConnection(ctx, theirs.ID)
		if err != nil {
			return err
		}
		if stored.Name != "theirs" || stored.OwnerUserID != "usr_other" {
			t.Fatalf("connection after cross-owner write = %+v", stored)
		}
		setups, err := r.ListHarnessSetups(ctx)
		if err != nil {
			return err
		}
		if len(setups) != 1 || setups[0].Name != "theirs" || setups[0].OwnerUserID != "usr_other" {
			t.Fatalf("setups after cross-owner write = %+v", setups)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The owner's own write still replaces its row.
	theirs.Name = "renamed"
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.PutProviderConnection(ctx, theirs)
	}); err != nil {
		t.Fatalf("owner write = %v", err)
	}

	// A Project cannot be granted another user's connection or setup.
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.GrantProviderConnection(ctx, "mine", "codex", theirs.ID)
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner connection grant = %v", err)
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.SetProjectHarnessSetup(ctx, "mine", "codex", setup.ID)
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-owner setup grant = %v", err)
	}
}
