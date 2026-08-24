ALTER TABLE capsules
    ADD COLUMN timeline_id TEXT REFERENCES timelines(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE capsules
    ADD COLUMN origin_moment_id TEXT REFERENCES moments(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE capsules
    ADD COLUMN restore_complete INTEGER NOT NULL DEFAULT 1 CHECK (restore_complete IN (0, 1));
ALTER TABLE capsules
    ADD COLUMN maintenance TEXT NOT NULL DEFAULT ''
        CHECK (maintenance IN ('', 'capture', 'seal', 'restore'));

CREATE TABLE timelines (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    capsule_id TEXT NOT NULL UNIQUE REFERENCES capsules(id) DEFERRABLE INITIALLY DEFERRED,
    forked_from_moment_id TEXT,
    reason TEXT NOT NULL CHECK (reason IN ('root', 'shard', 'rewind')),
    created_at TEXT NOT NULL,
    UNIQUE (id, project_id, capsule_id),
    CHECK (
        (reason = 'root' AND forked_from_moment_id IS NULL) OR
        (reason IN ('shard', 'rewind') AND forked_from_moment_id IS NOT NULL)
    )
);

CREATE TABLE moments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    capsule_id TEXT NOT NULL REFERENCES capsules(id),
    timeline_id TEXT NOT NULL REFERENCES timelines(id),
    parent_moment_id TEXT,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
    archive_sha256 TEXT NOT NULL CHECK (
        length(archive_sha256) = 64 AND archive_sha256 = lower(archive_sha256)
    ),
    archive_size INTEGER NOT NULL CHECK (archive_size >= 0),
    manifest_sha256 TEXT NOT NULL CHECK (
        length(manifest_sha256) = 64 AND manifest_sha256 = lower(manifest_sha256)
    ),
    image_digest TEXT NOT NULL,
    project_setup_hash TEXT NOT NULL,
    git_branch TEXT NOT NULL DEFAULT '' CHECK (length(git_branch) <= 512),
    git_head TEXT NOT NULL DEFAULT '' CHECK (length(git_head) <= 128),
    git_dirty_summary TEXT NOT NULL DEFAULT '' CHECK (length(git_dirty_summary) <= 4096),
    created_at TEXT NOT NULL,
    final INTEGER NOT NULL DEFAULT 0 CHECK (final IN (0, 1)),
    UNIQUE (id, timeline_id),
    UNIQUE (id, project_id),
    FOREIGN KEY (timeline_id, project_id, capsule_id)
        REFERENCES timelines(id, project_id, capsule_id),
    FOREIGN KEY (parent_moment_id, timeline_id)
        REFERENCES moments(id, timeline_id)
);

CREATE INDEX moments_timeline_order ON moments(timeline_id, created_at, id);

CREATE TRIGGER timelines_fork_project
BEFORE INSERT ON timelines
WHEN NEW.forked_from_moment_id IS NOT NULL
BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM moments
        WHERE id = NEW.forked_from_moment_id AND project_id = NEW.project_id
    ) THEN RAISE(ABORT, 'fork Moment project mismatch') END;
END;

CREATE TRIGGER timelines_immutable_update
BEFORE UPDATE ON timelines BEGIN
    SELECT RAISE(ABORT, 'Timelines are immutable');
END;
CREATE TRIGGER timelines_immutable_delete
BEFORE DELETE ON timelines BEGIN
    SELECT RAISE(ABORT, 'Timelines are immutable');
END;
CREATE TRIGGER moments_immutable_update
BEFORE UPDATE ON moments BEGIN
    SELECT RAISE(ABORT, 'Moments are immutable');
END;
CREATE TRIGGER moments_immutable_delete
BEFORE DELETE ON moments BEGIN
    SELECT RAISE(ABORT, 'Moments are immutable');
END;

INSERT INTO timelines(id, project_id, capsule_id, reason, created_at)
    SELECT 'legacy-' || id, project_id, id, 'root', created_at FROM capsules;
UPDATE capsules SET timeline_id = 'legacy-' || id WHERE timeline_id IS NULL;

CREATE TRIGGER timelines_capsule_match_insert
BEFORE INSERT ON timelines
WHEN NOT EXISTS (
    SELECT 1 FROM capsules
    WHERE id = NEW.capsule_id AND project_id = NEW.project_id AND timeline_id = NEW.id
)
BEGIN
    SELECT RAISE(ABORT, 'Timeline Capsule mismatch');
END;

CREATE TRIGGER capsules_timeline_required_insert
BEFORE INSERT ON capsules
WHEN NEW.timeline_id IS NULL OR NEW.timeline_id = ''
BEGIN
    SELECT RAISE(ABORT, 'Capsule Timeline is required');
END;
CREATE TRIGGER capsules_timeline_required_update
BEFORE UPDATE OF timeline_id, project_id ON capsules
WHEN NEW.timeline_id IS NULL OR NEW.timeline_id = '' OR NOT EXISTS (
    SELECT 1 FROM timelines
    WHERE id = NEW.timeline_id AND project_id = NEW.project_id AND capsule_id = NEW.id
)
BEGIN
    SELECT RAISE(ABORT, 'Capsule Timeline mismatch');
END;
