ALTER TABLE capsules
    ADD COLUMN last_activity_at TEXT NOT NULL DEFAULT '';
UPDATE capsules
SET last_activity_at = created_at
WHERE last_activity_at = '';

CREATE INDEX capsules_idle_candidates
    ON capsules(state, desired_state, maintenance, last_activity_at, id);

ALTER TABLE moments
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'timeline'
        CHECK (kind IN ('timeline', 'setup_cache'));

CREATE TABLE setup_moment_cache (
    project_id TEXT PRIMARY KEY REFERENCES projects(id),
    config_hash TEXT NOT NULL CHECK (
        length(config_hash) = 64 AND config_hash = lower(config_hash)
    ),
    moment_id TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    FOREIGN KEY (moment_id, project_id) REFERENCES moments(id, project_id)
);

CREATE TRIGGER setup_moment_cache_kind_insert
BEFORE INSERT ON setup_moment_cache
WHEN NOT EXISTS (
    SELECT 1 FROM moments
    WHERE id = NEW.moment_id
      AND project_id = NEW.project_id
      AND project_setup_hash = NEW.config_hash
      AND kind = 'setup_cache'
)
BEGIN
    SELECT RAISE(ABORT, 'setup Moment cache mismatch');
END;

CREATE TRIGGER setup_moment_cache_kind_update
BEFORE UPDATE ON setup_moment_cache
WHEN NOT EXISTS (
    SELECT 1 FROM moments
    WHERE id = NEW.moment_id
      AND project_id = NEW.project_id
      AND project_setup_hash = NEW.config_hash
      AND kind = 'setup_cache'
)
BEGIN
    SELECT RAISE(ABORT, 'setup Moment cache mismatch');
END;
