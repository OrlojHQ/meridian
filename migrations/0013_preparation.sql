CREATE TABLE capsule_preparations (
 capsule_id TEXT PRIMARY KEY REFERENCES capsules(id),
 metadata TEXT NOT NULL
);
ALTER TABLE setup_moment_cache RENAME TO setup_moment_cache_old;
CREATE TABLE setup_moment_cache (
 project_id TEXT NOT NULL REFERENCES projects(id),
 config_hash TEXT NOT NULL CHECK(length(config_hash)=64 AND config_hash=lower(config_hash)),
 moment_id TEXT NOT NULL UNIQUE,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id, config_hash),
 FOREIGN KEY(moment_id,project_id) REFERENCES moments(id,project_id)
);
-- Old keys did not bind source or platform. Retain their Moments for normal GC,
-- but do not promote them into the new preparation cache.
DROP TABLE setup_moment_cache_old;

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
CREATE TABLE project_preparation_policy (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)),
 generation INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0)
);
