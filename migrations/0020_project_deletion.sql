-- Project deletion is a durable lifecycle. active is the normal state.
-- deleting means cascade cleanup is in progress. failed means an owned
-- container could not be removed; the failure text is the reason and a later
-- delete resumes. deleted is a tombstone: transcripts, artifacts, environment,
-- and exclusive named-secret ciphertext are already gone.
ALTER TABLE projects ADD COLUMN state TEXT NOT NULL DEFAULT 'active'
    CHECK (state IN ('active', 'deleting', 'failed', 'deleted'));
ALTER TABLE projects ADD COLUMN failure TEXT NOT NULL DEFAULT ''
    CHECK (length(failure) <= 4096);

-- Moments, timelines, and Thread rows stay immutable for ordinary lifecycle
-- operations. Project deletion is the only path that removes them, and only
-- while the Project is deleting or already deleted.
DROP TRIGGER moments_immutable_delete;
CREATE TRIGGER moments_immutable_delete
BEFORE DELETE ON moments
WHEN NOT EXISTS (
    SELECT 1 FROM projects
    WHERE id = OLD.project_id AND state IN ('deleting', 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'Moments are immutable');
END;

DROP TRIGGER timelines_immutable_delete;
CREATE TRIGGER timelines_immutable_delete
BEFORE DELETE ON timelines
WHEN NOT EXISTS (
    SELECT 1 FROM projects
    WHERE id = OLD.project_id AND state IN ('deleting', 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'Timelines are immutable');
END;

DROP TRIGGER threads_no_delete;
CREATE TRIGGER threads_no_delete
BEFORE DELETE ON threads
WHEN NOT EXISTS (
    SELECT 1 FROM capsules c
    JOIN projects p ON p.id = c.project_id
    WHERE c.id = OLD.capsule_id AND p.state IN ('deleting', 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'Threads require crypto-shred deletion');
END;

DROP TRIGGER thread_messages_immutable_delete;
CREATE TRIGGER thread_messages_immutable_delete
BEFORE DELETE ON thread_messages
WHEN NOT EXISTS (
    SELECT 1 FROM threads t
    JOIN capsules c ON c.id = t.capsule_id
    JOIN projects p ON p.id = c.project_id
    WHERE t.id = OLD.thread_id AND p.state IN ('deleting', 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'ThreadMessages are immutable');
END;

DROP TRIGGER thread_blocks_immutable_delete;
CREATE TRIGGER thread_blocks_immutable_delete
BEFORE DELETE ON thread_blocks
WHEN NOT EXISTS (
    SELECT 1 FROM threads t
    JOIN capsules c ON c.id = t.capsule_id
    JOIN projects p ON p.id = c.project_id
    WHERE t.id = OLD.thread_id AND p.state IN ('deleting', 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'ThreadBlocks are immutable');
END;

DROP TRIGGER thread_deliveries_no_delete;
CREATE TRIGGER thread_deliveries_no_delete
BEFORE DELETE ON thread_deliveries
WHEN NOT EXISTS (
    SELECT 1 FROM threads t
    JOIN capsules c ON c.id = t.capsule_id
    JOIN projects p ON p.id = c.project_id
    WHERE t.id = OLD.thread_id AND p.state IN ('deleting', 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'Thread deliveries are durable');
END;
