CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0)
);

CREATE TABLE capsules (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL,
    state TEXT NOT NULL,
    desired_state TEXT NOT NULL,
    provider_resource_id TEXT NOT NULL DEFAULT '',
    failure TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0)
);
CREATE INDEX capsules_project_order ON capsules(project_id, created_at, id);
CREATE INDEX capsules_recovery ON capsules(state, desired_state);

CREATE TABLE idempotency_outcomes (
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    outcome BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (scope, key)
);

CREATE TABLE events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    resource_version INTEGER NOT NULL,
    data BLOB
);
CREATE INDEX events_aggregate_order
    ON events(aggregate_type, aggregate_id, sequence);
