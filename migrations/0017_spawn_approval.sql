-- Operator spawn approvals and grant updates. SQLite cannot widen CHECK
-- constraints, so grants, links and requests are rebuilt.

CREATE TEMP TABLE coordination_requests_keep AS SELECT * FROM coordination_requests;
DROP TABLE coordination_requests;
CREATE TEMP TABLE thread_links_keep AS SELECT * FROM thread_links;
DROP TABLE thread_links;

CREATE TABLE coordination_grants_next (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    root_thread_id TEXT NOT NULL UNIQUE CHECK (length(root_thread_id) BETWEEN 1 AND 128),
    root_capsule_id TEXT NOT NULL UNIQUE REFERENCES capsules(id),
    state TEXT NOT NULL CHECK (state IN ('active', 'revoked', 'expired')),
    spawn_approval TEXT NOT NULL CHECK (spawn_approval IN ('auto', 'operator')),
    max_depth INTEGER NOT NULL CHECK (max_depth = 1),
    max_children INTEGER NOT NULL CHECK (max_children BETWEEN 1 AND 16),
    max_descendants INTEGER NOT NULL CHECK (max_descendants BETWEEN max_children AND 32),
    max_live_capsules INTEGER NOT NULL CHECK (max_live_capsules BETWEEN 1 AND 16),
    max_messages_per_link INTEGER NOT NULL CHECK (max_messages_per_link BETWEEN 1 AND 500),
    allowed_harnesses TEXT NOT NULL CHECK (json_valid(allowed_harnesses)
        AND json_type(allowed_harnesses) = 'array' AND length(allowed_harnesses) <= 4096),
    deadline_at TEXT NOT NULL,
    on_deadline TEXT NOT NULL CHECK (on_deadline IN ('cancel_and_pause', 'stop_new_work')),
    descendants_created INTEGER NOT NULL DEFAULT 0 CHECK (descendants_created >= 0),
    revoked_at TEXT,
    revoke_reason TEXT NOT NULL DEFAULT ''
        CHECK (revoke_reason IN ('', 'operator', 'deadline', 'lead_ended')),
    archive_after_seconds INTEGER NOT NULL DEFAULT 86400
        CHECK (archive_after_seconds = 0 OR archive_after_seconds BETWEEN 60 AND 2592000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    CHECK (
        (state = 'active' AND revoked_at IS NULL AND revoke_reason = '') OR
        (state <> 'active' AND revoked_at IS NOT NULL AND revoke_reason <> '')
    )
);

INSERT INTO coordination_grants_next
SELECT id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
    max_depth, max_children, max_descendants, max_live_capsules, max_messages_per_link,
    allowed_harnesses, deadline_at, on_deadline, descendants_created, revoked_at,
    revoke_reason, archive_after_seconds, created_at, updated_at, resource_version
FROM coordination_grants;

DROP TABLE coordination_grants;
ALTER TABLE coordination_grants_next RENAME TO coordination_grants;

CREATE INDEX coordination_grants_active ON coordination_grants(state, deadline_at);

CREATE TRIGGER coordination_grants_lifecycle
BEFORE UPDATE OF state ON coordination_grants
WHEN NOT (OLD.state = NEW.state OR OLD.state = 'active')
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

-- Identity stays fixed. Limits, deadline, spawn approval and archiveAfter
-- may change on an operator PATCH.
CREATE TRIGGER coordination_grants_identity_immutable
BEFORE UPDATE OF id, project_id, root_thread_id, root_capsule_id, max_depth, created_at
ON coordination_grants
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

CREATE TRIGGER coordination_grants_no_delete
BEFORE DELETE ON coordination_grants
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

CREATE TABLE thread_links (
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
    state TEXT NOT NULL CHECK (state IN ('pending_approval', 'active', 'closed')),
    close_reason TEXT NOT NULL DEFAULT ''
        CHECK (close_reason IN ('', 'cancelled', 'failed', 'ended', 'revoked', 'denied', 'detached')),
    report_seq INTEGER NOT NULL DEFAULT 0 CHECK (report_seq >= 0),
    report_delivered_seq INTEGER NOT NULL DEFAULT 0
        CHECK (report_delivered_seq BETWEEN 0 AND report_seq),
    report_message_seq INTEGER NOT NULL DEFAULT 0 CHECK (report_message_seq BETWEEN 0 AND 100000),
    last_report_status TEXT NOT NULL DEFAULT ''
        CHECK (last_report_status IN ('', 'turn_ended', 'failed')),
    last_report_reason TEXT NOT NULL DEFAULT ''
        CHECK (last_report_reason IN ('', 'cancelled', 'capsule_failed', 'session_ended', 'revoked', 'denied')),
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
        (state = 'pending_approval' AND close_reason = '') OR
        (state = 'active' AND close_reason = '') OR
        (state = 'closed' AND close_reason <> '')
    ),
    CHECK ((report_seq = 0) = (last_report_status = '')),
    CHECK (archived_at IS NULL OR promoted_at IS NULL)
);

INSERT INTO thread_links (
    id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
    child_capsule_id, depth, name, harness, state, close_reason, report_seq,
    report_delivered_seq, report_message_seq, last_report_status, last_report_reason,
    last_report_at, messages_forwarded, operator_messages, operator_noticed,
    archived_at, promoted_at, created_at, updated_at, resource_version
)
SELECT
    id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id, child_thread_id,
    child_capsule_id, depth, name, harness, state, close_reason, report_seq,
    report_delivered_seq, report_message_seq, last_report_status, last_report_reason,
    last_report_at, messages_forwarded, operator_messages, operator_noticed,
    archived_at, promoted_at, created_at, updated_at, resource_version
FROM thread_links_keep;
DROP TABLE thread_links_keep;

CREATE INDEX thread_links_parent ON thread_links(parent_thread_id, created_at, id);
CREATE INDEX thread_links_grant ON thread_links(grant_id, state, created_at, id);
CREATE INDEX thread_links_archivable ON thread_links(updated_at, id)
    WHERE archived_at IS NULL AND promoted_at IS NULL;

CREATE TRIGGER thread_links_lifecycle
BEFORE UPDATE OF state ON thread_links
WHEN NOT (
    OLD.state IS NEW.state OR
    (OLD.state = 'pending_approval' AND NEW.state IN ('active', 'closed')) OR
    (OLD.state = 'active' AND NEW.state = 'closed')
)
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

CREATE TRIGGER thread_links_promotion
BEFORE UPDATE OF promoted_at, close_reason ON thread_links
WHEN (OLD.promoted_at IS NOT NULL AND NEW.promoted_at IS NOT OLD.promoted_at)
    OR (NEW.close_reason = 'detached' AND NEW.promoted_at IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

CREATE TABLE coordination_requests (
    id TEXT PRIMARY KEY,
    grant_id TEXT NOT NULL REFERENCES coordination_grants(id),
    capsule_id TEXT NOT NULL REFERENCES capsules(id),
    request_key TEXT NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    canonical_digest TEXT NOT NULL
        CHECK (length(canonical_digest) = 64 AND canonical_digest = lower(canonical_digest)),
    kind TEXT NOT NULL CHECK (kind IN ('spawn', 'send', 'cancel')),
    link_id TEXT NOT NULL REFERENCES thread_links(id),
    state TEXT NOT NULL CHECK (state IN ('pending_approval', 'completed', 'rejected')),
    result_message_id TEXT NOT NULL DEFAULT '' CHECK (length(result_message_id) <= 128),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    UNIQUE (capsule_id, request_key)
);

INSERT INTO coordination_requests (
    id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
    state, result_message_id, created_at, updated_at, resource_version
)
SELECT
    id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
    'completed', result_message_id, created_at, created_at, 1
FROM coordination_requests_keep;
DROP TABLE coordination_requests_keep;

CREATE INDEX coordination_requests_pending ON coordination_requests(state, created_at, id)
    WHERE state = 'pending_approval';

CREATE TRIGGER coordination_requests_identity_immutable
BEFORE UPDATE OF id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
    result_message_id, created_at
ON coordination_requests
BEGIN
    SELECT RAISE(ABORT, 'coordination request lifecycle violation');
END;

CREATE TRIGGER coordination_requests_lifecycle
BEFORE UPDATE OF state ON coordination_requests
WHEN NOT (
    OLD.state IS NEW.state OR
    (OLD.state = 'pending_approval' AND NEW.state IN ('completed', 'rejected'))
)
BEGIN
    SELECT RAISE(ABORT, 'coordination request lifecycle violation');
END;

CREATE TRIGGER coordination_requests_no_delete
BEFORE DELETE ON coordination_requests
BEGIN
    SELECT RAISE(ABORT, 'coordination request lifecycle violation');
END;
