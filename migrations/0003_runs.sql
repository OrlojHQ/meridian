CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    capsule_id TEXT NOT NULL REFERENCES capsules(id),
    harness TEXT NOT NULL,
    state TEXT NOT NULL,
    exit_status INTEGER,
    failure TEXT NOT NULL DEFAULT '',
    event_cursor INTEGER NOT NULL DEFAULT 0 CHECK (event_cursor >= 0),
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0)
);
CREATE INDEX runs_capsule_order ON runs(capsule_id, created_at, id);
CREATE UNIQUE INDEX runs_one_active_per_capsule
    ON runs(capsule_id)
    WHERE state IN ('Queued', 'Starting', 'Running', 'Cancelling');
CREATE INDEX runs_recovery ON runs(state);
