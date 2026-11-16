-- Project checks and automatic rework (ADR 0051).
--
-- The command list lives with the Project. Each suite execution is a
-- check_runs row plus one check_results row per command. Output tails are
-- ciphertext only; this migration stores no plaintext.

CREATE TABLE project_checks (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    auto_rework INTEGER NOT NULL DEFAULT 1 CHECK (auto_rework IN (0, 1)),
    max_rework_attempts INTEGER NOT NULL DEFAULT 3
        CHECK (max_rework_attempts BETWEEN 0 AND 10),
    checks_json TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL
);

CREATE TRIGGER project_checks_owner_insert
BEFORE INSERT ON project_checks
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER project_checks_owner_immutable
BEFORE UPDATE OF owner_user_id ON project_checks
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TABLE capsule_check_controls (
    capsule_id TEXT PRIMARY KEY REFERENCES capsules(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    epoch INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    auto_rework_stopped INTEGER NOT NULL DEFAULT 0 CHECK (auto_rework_stopped IN (0, 1))
);

CREATE TRIGGER capsule_check_controls_owner_insert
BEFORE INSERT ON capsule_check_controls
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER capsule_check_controls_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsule_check_controls
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TABLE check_runs (
    id TEXT PRIMARY KEY,
    capsule_id TEXT NOT NULL REFERENCES capsules(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    thread_id TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL CHECK (attempt BETWEEN 0 AND 10),
    trigger TEXT NOT NULL CHECK (trigger IN ('turn_idle', 'delivery_proposed', 'run_exit', 'manual')),
    state TEXT NOT NULL CHECK (state IN (
        'queued', 'running', 'passed', 'failed', 'error', 'cancelled', 'stale'
    )),
    git_head TEXT NOT NULL DEFAULT '' CHECK (length(git_head) <= 128),
    git_tree TEXT NOT NULL DEFAULT '' CHECK (length(git_tree) <= 128),
    started_at TEXT NOT NULL DEFAULT '',
    finished_at TEXT NOT NULL DEFAULT '',
    not_before TEXT NOT NULL,
    epoch INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    basis_message_count INTEGER NOT NULL DEFAULT 0,
    basis_run_id TEXT NOT NULL DEFAULT '',
    rework_allowed INTEGER NOT NULL DEFAULT 0 CHECK (rework_allowed IN (0, 1)),
    rework_sent INTEGER NOT NULL DEFAULT 0 CHECK (rework_sent IN (0, 1)),
    restart_retried INTEGER NOT NULL DEFAULT 0 CHECK (restart_retried IN (0, 1)),
    passed_count INTEGER NOT NULL DEFAULT 0 CHECK (passed_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    error_code TEXT NOT NULL DEFAULT '' CHECK (length(error_code) <= 64),
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL
);

CREATE INDEX check_runs_capsule ON check_runs(capsule_id, created_at DESC);
CREATE INDEX check_runs_due ON check_runs(state, not_before);

CREATE TRIGGER check_runs_owner_insert
BEFORE INSERT ON check_runs
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER check_runs_owner_immutable
BEFORE UPDATE OF owner_user_id ON check_runs
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TABLE check_results (
    run_id TEXT NOT NULL REFERENCES check_runs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0 AND position < 16),
    name TEXT NOT NULL,
    argv_json TEXT NOT NULL,
    exit_code INTEGER,
    duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    timed_out INTEGER NOT NULL DEFAULT 0 CHECK (timed_out IN (0, 1)),
    output_truncated INTEGER NOT NULL DEFAULT 0 CHECK (output_truncated IN (0, 1)),
    output_nonce BLOB,
    output_ciphertext BLOB,
    PRIMARY KEY (run_id, position)
);

-- A proposed Delivery can be withheld until checks pass or attempts run out.
-- The Delivery row itself is unchanged; deleting this row releases it.
CREATE TABLE delivery_check_gates (
    delivery_id TEXT PRIMARY KEY REFERENCES deliveries(id) ON DELETE CASCADE,
    capsule_id TEXT NOT NULL REFERENCES capsules(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    created_at TEXT NOT NULL
);

CREATE INDEX delivery_check_gates_capsule ON delivery_check_gates(capsule_id);

CREATE TRIGGER delivery_check_gates_owner_insert
BEFORE INSERT ON delivery_check_gates
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER delivery_check_gates_owner_immutable
BEFORE UPDATE OF owner_user_id ON delivery_check_gates
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;
