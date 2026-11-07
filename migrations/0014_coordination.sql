ALTER TABLE capsules ADD COLUMN role TEXT NOT NULL DEFAULT 'main'
    CHECK (role IN ('main', 'sub'));
ALTER TABLE capsules ADD COLUMN lead_thread_id TEXT
    CHECK (lead_thread_id IS NULL OR length(lead_thread_id) BETWEEN 1 AND 128);
ALTER TABLE capsules ADD COLUMN link_id TEXT
    CHECK (link_id IS NULL OR length(link_id) BETWEEN 1 AND 128);

CREATE INDEX capsules_project_role ON capsules(project_id, role, created_at, id);
CREATE INDEX capsules_live_state ON capsules(state, desired_state);

CREATE TRIGGER capsules_role_consistent_insert
BEFORE INSERT ON capsules
WHEN NOT (
    (NEW.role = 'main' AND NEW.lead_thread_id IS NULL AND NEW.link_id IS NULL) OR
    (NEW.role = 'sub' AND NEW.lead_thread_id IS NOT NULL AND NEW.link_id IS NOT NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'Capsule role lifecycle violation');
END;

CREATE TRIGGER capsules_role_immutable
BEFORE UPDATE OF role, lead_thread_id, link_id ON capsules
WHEN NOT (
    OLD.role IS NEW.role AND OLD.lead_thread_id IS NEW.lead_thread_id
    AND OLD.link_id IS NEW.link_id
)
BEGIN
    SELECT RAISE(ABORT, 'Capsule role lifecycle violation');
END;

CREATE TABLE coordination_grants (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    root_thread_id TEXT NOT NULL UNIQUE CHECK (length(root_thread_id) BETWEEN 1 AND 128),
    root_capsule_id TEXT NOT NULL UNIQUE REFERENCES capsules(id),
    state TEXT NOT NULL CHECK (state IN ('active', 'revoked', 'expired')),
    spawn_approval TEXT NOT NULL CHECK (spawn_approval IN ('auto')),
    max_depth INTEGER NOT NULL CHECK (max_depth = 1),
    max_children INTEGER NOT NULL CHECK (max_children BETWEEN 1 AND 16),
    max_descendants INTEGER NOT NULL CHECK (max_descendants BETWEEN max_children AND 32),
    max_live_capsules INTEGER NOT NULL CHECK (max_live_capsules BETWEEN 1 AND 16),
    max_messages_per_link INTEGER NOT NULL CHECK (max_messages_per_link BETWEEN 1 AND 500),
    allowed_harnesses TEXT NOT NULL CHECK (json_valid(allowed_harnesses)
        AND json_type(allowed_harnesses) = 'array' AND length(allowed_harnesses) <= 4096),
    deadline_at TEXT NOT NULL,
    on_deadline TEXT NOT NULL CHECK (on_deadline IN ('cancel_and_pause', 'stop_new_work')),
    descendants_created INTEGER NOT NULL DEFAULT 0
        CHECK (descendants_created BETWEEN 0 AND max_descendants),
    revoked_at TEXT,
    revoke_reason TEXT NOT NULL DEFAULT ''
        CHECK (revoke_reason IN ('', 'operator', 'deadline', 'lead_ended')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    CHECK (
        (state = 'active' AND revoked_at IS NULL AND revoke_reason = '') OR
        (state <> 'active' AND revoked_at IS NOT NULL AND revoke_reason <> '')
    )
);

CREATE INDEX coordination_grants_active ON coordination_grants(state, deadline_at);

CREATE TRIGGER coordination_grants_lifecycle
BEFORE UPDATE OF state ON coordination_grants
WHEN NOT (OLD.state = NEW.state OR OLD.state = 'active')
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

CREATE TRIGGER coordination_grants_identity_immutable
BEFORE UPDATE OF id, project_id, root_thread_id, root_capsule_id, spawn_approval,
    max_depth, max_children, max_descendants, max_live_capsules,
    max_messages_per_link, allowed_harnesses, deadline_at, on_deadline, created_at
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
    state TEXT NOT NULL CHECK (state IN ('active', 'closed')),
    close_reason TEXT NOT NULL DEFAULT ''
        CHECK (close_reason IN ('', 'cancelled', 'failed', 'ended', 'revoked')),
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
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    UNIQUE (parent_thread_id, name),
    CHECK (
        (state = 'active' AND close_reason = '') OR
        (state = 'closed' AND close_reason <> '')
    ),
    CHECK ((report_seq = 0) = (last_report_status = ''))
);

CREATE INDEX thread_links_parent ON thread_links(parent_thread_id, created_at, id);
CREATE INDEX thread_links_grant ON thread_links(grant_id, state, created_at, id);

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

CREATE TABLE coordination_requests (
    id TEXT PRIMARY KEY,
    grant_id TEXT NOT NULL REFERENCES coordination_grants(id),
    capsule_id TEXT NOT NULL REFERENCES capsules(id),
    request_key TEXT NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    canonical_digest TEXT NOT NULL
        CHECK (length(canonical_digest) = 64 AND canonical_digest = lower(canonical_digest)),
    kind TEXT NOT NULL CHECK (kind IN ('spawn', 'send', 'cancel')),
    link_id TEXT NOT NULL REFERENCES thread_links(id),
    result_message_id TEXT NOT NULL DEFAULT '' CHECK (length(result_message_id) <= 128),
    created_at TEXT NOT NULL,
    UNIQUE (capsule_id, request_key)
);

CREATE TRIGGER coordination_requests_immutable
BEFORE UPDATE ON coordination_requests
BEGIN
    SELECT RAISE(ABORT, 'coordination request lifecycle violation');
END;
