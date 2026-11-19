-- Routines (ADR 0057).
--
-- A routine is a clock, a fixed prompt, and limits. The prompt, model, and
-- effort are one ciphertext sealed with the installation key; this migration
-- stores none of them in plaintext. A fire is one tick of the clock: a skip,
-- a missed time, or one fresh Capsule and Thread.
--
-- Owners never change except by a departed-member transfer (ADR 0046):
-- each owner trigger allows exactly the move that owner_transfers names.

CREATE TABLE routines (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 48),
    harness TEXT NOT NULL CHECK (length(harness) BETWEEN 1 AND 128),
    cadence TEXT NOT NULL CHECK (length(cadence) BETWEEN 1 AND 128),
    time_zone TEXT NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
    max_run_seconds INTEGER NOT NULL CHECK (max_run_seconds BETWEEN 300 AND 86400),
    catch_up_seconds INTEGER NOT NULL CHECK (catch_up_seconds BETWEEN 0 AND 86400),
    max_kept INTEGER NOT NULL CHECK (max_kept BETWEEN 1 AND 20),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    disabled_reason TEXT NOT NULL DEFAULT '' CHECK (length(disabled_reason) <= 64),
    start_failures INTEGER NOT NULL DEFAULT 0 CHECK (start_failures >= 0),
    next_fire_at TEXT NOT NULL DEFAULT '',
    last_fire_at TEXT NOT NULL DEFAULT '',
    content_nonce BLOB NOT NULL,
    content_ciphertext BLOB NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX routines_project_name ON routines(project_id, name);
CREATE INDEX routines_due ON routines(enabled, next_fire_at);

CREATE TRIGGER routines_owner_insert
BEFORE INSERT ON routines
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER routines_owner_immutable
BEFORE UPDATE OF owner_user_id ON routines
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- capsule_id and thread_id are plain text: a fire outlives the Capsule it
-- deleted, and its row says so.
CREATE TABLE routine_fires (
    id TEXT PRIMARY KEY,
    routine_id TEXT NOT NULL REFERENCES routines(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    trigger TEXT NOT NULL CHECK (trigger IN ('clock', 'catch_up', 'manual')),
    scheduled_for TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN (
        'skipped', 'missed', 'starting', 'selecting', 'running', 'cleaning', 'finished'
    )),
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN (
        '', 'proposed', 'changed', 'no_changes', 'waiting', 'timed_out', 'failed'
    )),
    reason TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 64),
    capsule_id TEXT NOT NULL DEFAULT '',
    thread_id TEXT NOT NULL DEFAULT '',
    capsule_deleted INTEGER NOT NULL DEFAULT 0 CHECK (capsule_deleted IN (0, 1)),
    model_requested INTEGER NOT NULL DEFAULT 0 CHECK (model_requested IN (0, 1)),
    effort_requested INTEGER NOT NULL DEFAULT 0 CHECK (effort_requested IN (0, 1)),
    selecting_since TEXT NOT NULL DEFAULT '',
    idle_since TEXT NOT NULL DEFAULT '',
    deadline_at TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT '',
    finished_at TEXT NOT NULL DEFAULT '',
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- One clock or catch-up fire per scheduled time: a restarted daemon finds
-- the row it already wrote instead of starting a second run.
CREATE UNIQUE INDEX routine_fires_time ON routine_fires(routine_id, scheduled_for)
    WHERE trigger <> 'manual';
CREATE INDEX routine_fires_routine ON routine_fires(routine_id, created_at DESC);
CREATE INDEX routine_fires_active ON routine_fires(state)
    WHERE state IN ('starting', 'selecting', 'running', 'cleaning');
CREATE INDEX routine_fires_capsule ON routine_fires(capsule_id) WHERE capsule_id <> '';

CREATE TRIGGER routine_fires_owner_insert
BEFORE INSERT ON routine_fires
WHEN EXISTS (SELECT 1 FROM routines WHERE id = NEW.routine_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM routines WHERE id = NEW.routine_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER routine_fires_owner_immutable
BEFORE UPDATE OF owner_user_id ON routine_fires
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;
