-- Working notes and Project notes (ADR 0060).
--
-- A note scope is one Project's notes (grant_id = '') or one lead tree's
-- working notes (grant_id names the Coordination Grant). It holds the
-- scope's data key, wrapped with the transcript KEK; the key is created
-- with the first note and a crypto-shred clears it. Names, sizes, digests
-- and provenance are plaintext and content-free. Content exists only as
-- AES-GCM envelopes under the scope's key.
--
-- Owners never change except by a departed-member transfer (ADR 0046),
-- which also re-wraps every key for its new owner.

CREATE TABLE note_scopes (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grant_id TEXT NOT NULL DEFAULT '' CHECK (length(grant_id) <= 128),
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    wrapped_dek BLOB,
    kek_id TEXT NOT NULL DEFAULT '',
    kek_version INTEGER NOT NULL DEFAULT 0 CHECK (kek_version >= 0),
    delivery TEXT NOT NULL DEFAULT 'all_threads'
        CHECK (delivery IN ('all_threads', 'leads_only', 'off')),
    revision_counter INTEGER NOT NULL DEFAULT 0 CHECK (revision_counter >= 0),
    headlined_seq INTEGER NOT NULL DEFAULT 0 CHECK (headlined_seq >= 0),
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((wrapped_dek IS NULL) = (kek_id = ''))
);

CREATE UNIQUE INDEX note_scopes_key ON note_scopes(project_id, grant_id);

CREATE TRIGGER note_scopes_owner_insert
BEFORE INSERT ON note_scopes
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER note_scopes_owner_immutable
BEFORE UPDATE OF owner_user_id ON note_scopes
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TABLE notes (
    id TEXT PRIMARY KEY,
    scope_id TEXT NOT NULL REFERENCES note_scopes(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grant_id TEXT NOT NULL DEFAULT '',
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 48),
    head_seq INTEGER NOT NULL CHECK (head_seq > 0),
    live INTEGER NOT NULL CHECK (live IN (0, 1)),
    size INTEGER NOT NULL CHECK (size BETWEEN 0 AND 32768),
    digest TEXT NOT NULL,
    head_kind TEXT NOT NULL,
    head_writer TEXT NOT NULL CHECK (head_writer IN ('agent', 'operator')),
    head_link_name TEXT NOT NULL DEFAULT '' CHECK (length(head_link_name) <= 48),
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (live = 1 OR size = 0)
);

CREATE UNIQUE INDEX notes_name ON notes(scope_id, name);

CREATE TRIGGER notes_owner_insert
BEFORE INSERT ON notes
WHEN NEW.owner_user_id IS NOT (SELECT owner_user_id FROM note_scopes WHERE id = NEW.scope_id)
    OR NEW.project_id IS NOT (SELECT project_id FROM note_scopes WHERE id = NEW.scope_id)
    OR NEW.grant_id IS NOT (SELECT grant_id FROM note_scopes WHERE id = NEW.scope_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER notes_owner_immutable
BEFORE UPDATE OF owner_user_id, scope_id, project_id, grant_id ON notes
WHEN NEW.scope_id IS NOT OLD.scope_id OR NEW.project_id IS NOT OLD.project_id
    OR NEW.grant_id IS NOT OLD.grant_id
    OR (NEW.owner_user_id IS NOT OLD.owner_user_id AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    ))
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- A revision is immutable except that its ciphertext may be dropped: past
-- the retained window, by "delete with history", or by a crypto-shred.
CREATE TABLE note_revisions (
    note_id TEXT NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL CHECK (seq > 0),
    scope_id TEXT NOT NULL REFERENCES note_scopes(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grant_id TEXT NOT NULL DEFAULT '',
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    scope_seq INTEGER NOT NULL CHECK (scope_seq > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 48),
    kind TEXT NOT NULL CHECK (kind IN (
        'operator_write', 'agent_write', 'approved_proposal', 'approved_with_edits', 'restore', 'delete'
    )),
    writer TEXT NOT NULL CHECK (writer IN ('agent', 'operator')),
    writer_user_id TEXT NOT NULL DEFAULT '',
    source_thread_id TEXT NOT NULL DEFAULT '',
    source_capsule_id TEXT NOT NULL DEFAULT '',
    source_link_id TEXT NOT NULL DEFAULT '',
    source_link_name TEXT NOT NULL DEFAULT '' CHECK (length(source_link_name) <= 48),
    source_lead_thread_id TEXT NOT NULL DEFAULT '',
    source_grant_id TEXT NOT NULL DEFAULT '',
    working_note_id TEXT NOT NULL DEFAULT '',
    working_note_seq INTEGER NOT NULL DEFAULT 0 CHECK (working_note_seq >= 0),
    working_note_name TEXT NOT NULL DEFAULT '' CHECK (length(working_note_name) <= 48),
    working_capsule_id TEXT NOT NULL DEFAULT '',
    proposal_id TEXT NOT NULL DEFAULT '',
    base_seq INTEGER NOT NULL DEFAULT 0 CHECK (base_seq >= 0),
    restored_from_seq INTEGER NOT NULL DEFAULT 0 CHECK (restored_from_seq >= 0),
    digest TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size BETWEEN 0 AND 32768),
    ciphertext BLOB,
    created_at TEXT NOT NULL,
    PRIMARY KEY (note_id, seq),
    CHECK (kind <> 'delete' OR ciphertext IS NULL)
);

CREATE INDEX note_revisions_scope ON note_revisions(scope_id, scope_seq);
CREATE INDEX note_revisions_capsule ON note_revisions(source_capsule_id, created_at)
    WHERE source_capsule_id <> '';

CREATE TRIGGER note_revisions_owner_insert
BEFORE INSERT ON note_revisions
WHEN NEW.owner_user_id IS NOT (SELECT owner_user_id FROM notes WHERE id = NEW.note_id)
    OR NEW.scope_id IS NOT (SELECT scope_id FROM notes WHERE id = NEW.note_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER note_revisions_immutable
BEFORE UPDATE ON note_revisions
WHEN NEW.note_id IS NOT OLD.note_id OR NEW.seq IS NOT OLD.seq
    OR NEW.scope_id IS NOT OLD.scope_id OR NEW.scope_seq IS NOT OLD.scope_seq
    OR NEW.name IS NOT OLD.name OR NEW.kind IS NOT OLD.kind OR NEW.writer IS NOT OLD.writer
    OR NEW.digest IS NOT OLD.digest OR NEW.size IS NOT OLD.size
    OR NEW.created_at IS NOT OLD.created_at
    OR (NEW.ciphertext IS NOT NULL AND NEW.ciphertext IS NOT OLD.ciphertext)
BEGIN
    SELECT RAISE(ABORT, 'note revision immutable');
END;

CREATE TRIGGER note_revisions_owner_immutable
BEFORE UPDATE OF owner_user_id ON note_revisions
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- A proposal names one Project note and carries its full new content, a
-- new note, or a deletion. It never holds a free-text reason.
CREATE TABLE note_proposals (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 48),
    is_delete INTEGER NOT NULL CHECK (is_delete IN (0, 1)),
    base_seq INTEGER NOT NULL CHECK (base_seq >= 0),
    state TEXT NOT NULL CHECK (state IN (
        'pending', 'approved', 'approved_with_edits', 'rejected', 'stale', 'expired'
    )),
    reason TEXT NOT NULL DEFAULT '' CHECK (reason IN ('', 'operator', 'owner_changed', 'shredded', 'purged')),
    origin TEXT NOT NULL CHECK (origin IN ('agent', 'promotion')),
    source_thread_id TEXT NOT NULL DEFAULT '',
    source_capsule_id TEXT NOT NULL DEFAULT '',
    source_link_id TEXT NOT NULL DEFAULT '',
    source_link_name TEXT NOT NULL DEFAULT '' CHECK (length(source_link_name) <= 48),
    source_lead_thread_id TEXT NOT NULL DEFAULT '',
    source_grant_id TEXT NOT NULL DEFAULT '',
    working_note_id TEXT NOT NULL DEFAULT '',
    working_note_seq INTEGER NOT NULL DEFAULT 0 CHECK (working_note_seq >= 0),
    working_note_name TEXT NOT NULL DEFAULT '' CHECK (length(working_note_name) <= 48),
    working_capsule_id TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    request_digest TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size BETWEEN 0 AND 32768),
    ciphertext BLOB,
    resolved_seq INTEGER NOT NULL DEFAULT 0 CHECK (resolved_seq >= 0),
    resolved_by TEXT NOT NULL DEFAULT '',
    resolved_at TEXT NOT NULL DEFAULT '',
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (is_delete = 0 OR size = 0)
);

CREATE UNIQUE INDEX note_proposals_key ON note_proposals(source_capsule_id, idempotency_key)
    WHERE origin = 'agent';
CREATE INDEX note_proposals_project ON note_proposals(project_id, state, created_at);
CREATE INDEX note_proposals_thread ON note_proposals(source_thread_id, created_at);

CREATE TRIGGER note_proposals_owner_insert
BEFORE INSERT ON note_proposals
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER note_proposals_owner_immutable
BEFORE UPDATE OF owner_user_id, project_id ON note_proposals
WHEN NEW.project_id IS NOT OLD.project_id
    OR (NEW.owner_user_id IS NOT OLD.owner_user_id AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    ))
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- A Thread's "Include Project notes" choice and the content-free set of
-- note revisions its latest session received. thread_id is plain text: a
-- choice is written before a Project Thread's row exists.
CREATE TABLE thread_notes (
    thread_id TEXT PRIMARY KEY CHECK (length(thread_id) BETWEEN 1 AND 128),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    choice TEXT NOT NULL DEFAULT '' CHECK (choice IN ('', 'include', 'exclude')),
    delivered TEXT NOT NULL DEFAULT '[]' CHECK (length(delivered) <= 16384),
    delivered_at TEXT NOT NULL DEFAULT '',
    indexed INTEGER NOT NULL DEFAULT 0 CHECK (indexed IN (0, 1)),
    updated_at TEXT NOT NULL
);

CREATE TRIGGER thread_notes_owner_insert
BEFORE INSERT ON thread_notes
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER thread_notes_owner_immutable
BEFORE UPDATE OF owner_user_id, project_id ON thread_notes
WHEN NEW.project_id IS NOT OLD.project_id
    OR (NEW.owner_user_id IS NOT OLD.owner_user_id AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    ))
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- A routine delivers Project notes to its scheduled Threads only when it
-- opts in (ADR 0057 as amended by ADR 0060).
ALTER TABLE routines ADD COLUMN include_notes INTEGER NOT NULL DEFAULT 0 CHECK (include_notes IN (0, 1));
