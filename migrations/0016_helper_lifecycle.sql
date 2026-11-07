-- Helper lifecycle: auto-archive after the grant's archive_after_seconds,
-- promotion of a helper to a main Capsule, and detaching it from its lead.

ALTER TABLE coordination_grants ADD COLUMN archive_after_seconds INTEGER NOT NULL DEFAULT 86400
    CHECK (archive_after_seconds = 0 OR archive_after_seconds BETWEEN 60 AND 2592000);

DROP TRIGGER coordination_grants_identity_immutable;
CREATE TRIGGER coordination_grants_identity_immutable
BEFORE UPDATE OF id, project_id, root_thread_id, root_capsule_id, spawn_approval,
    max_depth, max_children, max_descendants, max_live_capsules,
    max_messages_per_link, allowed_harnesses, deadline_at, on_deadline,
    archive_after_seconds, created_at
ON coordination_grants
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

-- A promoted helper becomes a main Capsule. It keeps its lead and link until
-- it is detached, and nothing ever turns a main Capsule back into a helper.
ALTER TABLE capsules ADD COLUMN promoted_at TEXT;

DROP TRIGGER capsules_role_immutable;
CREATE TRIGGER capsules_role_lifecycle
BEFORE UPDATE OF role, lead_thread_id, link_id, promoted_at ON capsules
WHEN NOT (
    (OLD.role IS NEW.role AND OLD.lead_thread_id IS NEW.lead_thread_id
        AND OLD.link_id IS NEW.link_id AND OLD.promoted_at IS NEW.promoted_at) OR
    (OLD.role = 'sub' AND NEW.role = 'main' AND OLD.promoted_at IS NULL
        AND NEW.promoted_at IS NOT NULL AND (
            (NEW.lead_thread_id IS OLD.lead_thread_id AND NEW.link_id IS OLD.link_id) OR
            (NEW.lead_thread_id IS NULL AND NEW.link_id IS NULL))) OR
    (OLD.role = 'main' AND NEW.role = 'main' AND OLD.promoted_at IS NOT NULL
        AND NEW.promoted_at IS OLD.promoted_at AND OLD.link_id IS NOT NULL
        AND NEW.lead_thread_id IS NULL AND NEW.link_id IS NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'Capsule role lifecycle violation');
END;

-- SQLite cannot widen a CHECK constraint, so thread_links is rebuilt. Its
-- coordination_requests rows are set aside first: dropping a referenced
-- parent table would otherwise fail the foreign key check.
CREATE TEMP TABLE coordination_requests_keep AS SELECT * FROM coordination_requests;
DELETE FROM coordination_requests;

CREATE TABLE thread_links_next (
    id TEXT PRIMARY KEY,
    grant_id TEXT NOT NULL REFERENCES coordination_grants(id),
    parent_thread_id TEXT NOT NULL CHECK (length(parent_thread_id) BETWEEN 1 AND 128),
    parent_capsule_id TEXT NOT NULL REFERENCES capsules(id),
    child_intent_id TEXT NOT NULL UNIQUE REFERENCES project_thread_intents(id),
    child_thread_id TEXT NOT NULL UNIQUE CHECK (length(child_thread_id) BETWEEN 1 AND 128),
    child_capsule_id TEXT NOT NULL UNIQUE REFERENCES capsules(id),
    depth INTEGER NOT NULL CHECK (depth BETWEEN 1 AND 3),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 48 AND name NOT GLOB '*[^a-z0-9-]*'),
    harness TEXT NOT NULL CHECK (length(harness) BETWEEN 1 AND 128),
    state TEXT NOT NULL CHECK (state IN ('active', 'closed')),
    close_reason TEXT NOT NULL DEFAULT ''
        CHECK (close_reason IN ('', 'cancelled', 'failed', 'ended', 'revoked', 'detached')),
    report_seq INTEGER NOT NULL DEFAULT 0 CHECK (report_seq >= 0),
    report_delivered_seq INTEGER NOT NULL DEFAULT 0
        CHECK (report_delivered_seq BETWEEN 0 AND report_seq),
    report_message_seq INTEGER NOT NULL DEFAULT 0 CHECK (report_message_seq BETWEEN 0 AND 100000),
    last_report_status TEXT NOT NULL DEFAULT ''
        CHECK (last_report_status IN ('', 'turn_ended', 'failed')),
    last_report_reason TEXT NOT NULL DEFAULT ''
        CHECK (last_report_reason IN ('', 'cancelled', 'capsule_failed', 'session_ended', 'revoked')),
    last_report_at TEXT,
    messages_forwarded INTEGER NOT NULL DEFAULT 0 CHECK (messages_forwarded BETWEEN 0 AND 500),
    operator_messages INTEGER NOT NULL DEFAULT 0 CHECK (operator_messages >= 0),
    operator_noticed INTEGER NOT NULL DEFAULT 0 CHECK (operator_noticed >= 0),
    archived_at TEXT,
    promoted_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    UNIQUE (parent_thread_id, name),
    CHECK (
        (state = 'active' AND close_reason = '') OR
        (state = 'closed' AND close_reason <> '')
    ),
    CHECK ((report_seq = 0) = (last_report_status = '')),
    CHECK (archived_at IS NULL OR promoted_at IS NULL)
);

INSERT INTO thread_links_next (
    id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
    child_capsule_id, depth, name, harness, state, close_reason, report_seq,
    report_delivered_seq, report_message_seq, last_report_status, last_report_reason,
    last_report_at, messages_forwarded, operator_messages, operator_noticed,
    created_at, updated_at, resource_version
)
SELECT
    id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
    child_capsule_id, depth, name, harness, state, close_reason, report_seq,
    report_delivered_seq, report_message_seq, last_report_status, last_report_reason,
    last_report_at, messages_forwarded, operator_messages, operator_noticed,
    created_at, updated_at, resource_version
FROM thread_links;

DROP TABLE thread_links;
ALTER TABLE thread_links_next RENAME TO thread_links;

INSERT INTO coordination_requests SELECT * FROM coordination_requests_keep;
DROP TABLE coordination_requests_keep;

CREATE INDEX thread_links_parent ON thread_links(parent_thread_id, created_at, id);
CREATE INDEX thread_links_grant ON thread_links(grant_id, state, created_at, id);
CREATE INDEX thread_links_archivable ON thread_links(updated_at, id)
    WHERE archived_at IS NULL AND promoted_at IS NULL;

CREATE TRIGGER thread_links_lifecycle
BEFORE UPDATE OF state ON thread_links
WHEN NOT (OLD.state = NEW.state OR OLD.state = 'active')
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

CREATE TRIGGER thread_links_report_monotonic
BEFORE UPDATE ON thread_links
WHEN NEW.report_seq < OLD.report_seq
    OR NEW.report_delivered_seq < OLD.report_delivered_seq
    OR NEW.report_message_seq < OLD.report_message_seq
    OR NEW.messages_forwarded < OLD.messages_forwarded
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

CREATE TRIGGER thread_links_identity_immutable
BEFORE UPDATE OF id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id,
    child_thread_id, child_capsule_id, depth, name, harness, created_at
ON thread_links
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

CREATE TRIGGER thread_links_no_delete
BEFORE DELETE ON thread_links
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

CREATE TRIGGER thread_links_operator_notices
BEFORE UPDATE ON thread_links
WHEN NEW.operator_messages < OLD.operator_messages
    OR NEW.operator_noticed < OLD.operator_noticed
    OR NEW.operator_noticed > NEW.operator_messages
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

-- Promotion is permanent, and only a promoted link may close as detached.
CREATE TRIGGER thread_links_promotion
BEFORE UPDATE OF promoted_at, close_reason ON thread_links
WHEN (OLD.promoted_at IS NOT NULL AND NEW.promoted_at IS NOT OLD.promoted_at)
    OR (NEW.close_reason = 'detached' AND NEW.promoted_at IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;
