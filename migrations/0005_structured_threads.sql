CREATE UNIQUE INDEX runs_id_capsule ON runs(id, capsule_id);

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    capsule_id TEXT NOT NULL REFERENCES capsules(id),
    state TEXT NOT NULL CHECK (state IN ('active', 'paused', 'archived', 'deleted')),
    current_run_id TEXT,
    adapter_id TEXT NOT NULL CHECK (length(adapter_id) BETWEEN 1 AND 128),
    wrapped_dek BLOB,
    kek_id TEXT NOT NULL CHECK (length(kek_id) BETWEEN 1 AND 128),
    kek_version INTEGER NOT NULL CHECK (kek_version > 0),
    envelope_version INTEGER NOT NULL CHECK (envelope_version = 1),
    message_count INTEGER NOT NULL DEFAULT 0 CHECK (message_count BETWEEN 0 AND 100000),
    encrypted_bytes INTEGER NOT NULL DEFAULT 0 CHECK (encrypted_bytes BETWEEN 0 AND 268435456),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    UNIQUE (id, capsule_id),
    FOREIGN KEY (current_run_id, capsule_id) REFERENCES runs(id, capsule_id),
    CHECK (
        (state = 'deleted' AND wrapped_dek IS NULL AND deleted_at IS NOT NULL
            AND current_run_id IS NULL) OR
        (state <> 'deleted' AND wrapped_dek IS NOT NULL AND length(wrapped_dek) = 64
            AND deleted_at IS NULL)
    )
);

CREATE UNIQUE INDEX threads_one_active_per_capsule
    ON threads(capsule_id) WHERE state = 'active';
CREATE INDEX threads_capsule_history
    ON threads(capsule_id, created_at, id);
CREATE INDEX threads_current_run
    ON threads(current_run_id) WHERE current_run_id IS NOT NULL;
CREATE INDEX threads_required_key
    ON threads(kek_id, kek_version) WHERE wrapped_dek IS NOT NULL;

CREATE TABLE thread_messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads(id),
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 100000),
    role TEXT NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    kind TEXT NOT NULL CHECK (kind IN ('prompt', 'response', 'tool_call', 'tool_result', 'status')),
    created_at TEXT NOT NULL,
    UNIQUE (id, thread_id),
    UNIQUE (thread_id, sequence)
);
CREATE INDEX thread_messages_order ON thread_messages(thread_id, sequence);

CREATE TABLE thread_blocks (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    message_id TEXT NOT NULL,
    message_sequence INTEGER NOT NULL CHECK (message_sequence BETWEEN 1 AND 100000),
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 128),
    kind TEXT NOT NULL CHECK (kind IN ('text', 'json', 'tool_call', 'tool_result', 'error')),
    envelope_version INTEGER NOT NULL CHECK (envelope_version = 1),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) BETWEEN 32 AND 1048640),
    created_at TEXT NOT NULL,
    UNIQUE (message_id, sequence),
    FOREIGN KEY (message_id, thread_id) REFERENCES thread_messages(id, thread_id),
    FOREIGN KEY (thread_id, message_sequence) REFERENCES thread_messages(thread_id, sequence)
);
CREATE INDEX thread_blocks_order ON thread_blocks(thread_id, message_sequence, sequence);

CREATE TABLE thread_deliveries (
    message_id TEXT PRIMARY KEY REFERENCES thread_messages(id),
    thread_id TEXT NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id),
    controller_id TEXT NOT NULL UNIQUE CHECK (length(controller_id) BETWEEN 1 AND 160),
    created_at TEXT NOT NULL,
    delivered_at TEXT,
    FOREIGN KEY (message_id, thread_id) REFERENCES thread_messages(id, thread_id),
    FOREIGN KEY (run_id) REFERENCES runs(id)
);
CREATE INDEX thread_deliveries_pending
    ON thread_deliveries(thread_id, run_id) WHERE delivered_at IS NULL;

CREATE TRIGGER threads_lifecycle_update
BEFORE UPDATE OF state ON threads
WHEN NOT (
    OLD.state = NEW.state OR
    (OLD.state = 'active' AND NEW.state IN ('paused', 'archived', 'deleted')) OR
    (OLD.state = 'paused' AND NEW.state IN ('active', 'archived', 'deleted')) OR
    (OLD.state = 'archived' AND NEW.state = 'deleted')
)
BEGIN
    SELECT RAISE(ABORT, 'invalid Thread lifecycle transition');
END;

CREATE TRIGGER threads_ownership_immutable
BEFORE UPDATE OF id, capsule_id, adapter_id, created_at, envelope_version ON threads
BEGIN
    SELECT RAISE(ABORT, 'Thread ownership metadata is immutable');
END;

CREATE TRIGGER threads_key_removal_requires_delete
BEFORE UPDATE OF wrapped_dek ON threads
WHEN NEW.wrapped_dek IS NULL AND NEW.state <> 'deleted'
BEGIN
    SELECT RAISE(ABORT, 'Thread key removal requires deletion');
END;

CREATE TRIGGER threads_deleted_immutable
BEFORE UPDATE ON threads
WHEN OLD.state = 'deleted'
BEGIN
    SELECT RAISE(ABORT, 'deleted Threads are immutable');
END;

CREATE TRIGGER threads_no_delete
BEFORE DELETE ON threads
BEGIN
    SELECT RAISE(ABORT, 'Threads require crypto-shred deletion');
END;

CREATE TRIGGER thread_messages_append_order
BEFORE INSERT ON thread_messages
WHEN NOT EXISTS (
    SELECT 1 FROM threads
    WHERE id = NEW.thread_id AND state = 'active'
      AND message_count + 1 = NEW.sequence
      AND message_count < 100000
)
BEGIN
    SELECT RAISE(ABORT, 'ThreadMessage append order or lifecycle violation');
END;

CREATE TRIGGER thread_messages_count
AFTER INSERT ON thread_messages
BEGIN
    UPDATE threads
    SET message_count = message_count + 1,
        updated_at = NEW.created_at,
        resource_version = resource_version + 1
    WHERE id = NEW.thread_id;
END;

CREATE TRIGGER thread_blocks_append_order
BEFORE INSERT ON thread_blocks
WHEN NOT EXISTS (
    SELECT 1
    FROM threads t
    WHERE t.id = NEW.thread_id AND t.state = 'active'
      AND t.encrypted_bytes + length(NEW.ciphertext) <= 268435456
) OR NEW.sequence <> (
    SELECT COALESCE(MAX(sequence), 0) + 1
    FROM thread_blocks WHERE message_id = NEW.message_id
)
BEGIN
    SELECT RAISE(ABORT, 'ThreadBlock append order, lifecycle, or quota violation');
END;

CREATE TRIGGER thread_blocks_bytes
AFTER INSERT ON thread_blocks
BEGIN
    UPDATE threads
    SET encrypted_bytes = encrypted_bytes + length(NEW.ciphertext)
    WHERE id = NEW.thread_id;
END;

CREATE TRIGGER thread_messages_immutable_update
BEFORE UPDATE ON thread_messages BEGIN
    SELECT RAISE(ABORT, 'ThreadMessages are immutable');
END;
CREATE TRIGGER thread_messages_immutable_delete
BEFORE DELETE ON thread_messages BEGIN
    SELECT RAISE(ABORT, 'ThreadMessages are immutable');
END;
CREATE TRIGGER thread_blocks_immutable_update
BEFORE UPDATE ON thread_blocks BEGIN
    SELECT RAISE(ABORT, 'ThreadBlocks are immutable');
END;
CREATE TRIGGER thread_blocks_immutable_delete
BEFORE DELETE ON thread_blocks BEGIN
    SELECT RAISE(ABORT, 'ThreadBlocks are immutable');
END;

CREATE TRIGGER thread_deliveries_immutable
BEFORE UPDATE ON thread_deliveries
WHEN OLD.message_id <> NEW.message_id OR OLD.thread_id <> NEW.thread_id OR
     OLD.run_id <> NEW.run_id OR OLD.controller_id <> NEW.controller_id OR
     OLD.created_at <> NEW.created_at OR OLD.delivered_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'Thread deliveries are immutable after acknowledgement');
END;
CREATE TRIGGER thread_deliveries_no_delete
BEFORE DELETE ON thread_deliveries BEGIN
    SELECT RAISE(ABORT, 'Thread deliveries are durable');
END;
