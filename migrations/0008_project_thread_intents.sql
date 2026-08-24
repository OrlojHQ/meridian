CREATE TABLE project_thread_intents (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    capsule_id TEXT NOT NULL UNIQUE REFERENCES capsules(id),
    thread_id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL UNIQUE,
    message_id TEXT NOT NULL UNIQUE,
    block_id TEXT NOT NULL UNIQUE,
    capsule_name TEXT NOT NULL CHECK (length(capsule_name) BETWEEN 1 AND 128),
    requested_name TEXT NOT NULL CHECK (length(requested_name) <= 128),
    harness TEXT NOT NULL CHECK (length(harness) BETWEEN 1 AND 128),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    state TEXT NOT NULL CHECK (state IN ('provisioning', 'ready', 'failed')),
    failure_code TEXT NOT NULL DEFAULT '' CHECK (length(failure_code) <= 64),
    failure_message TEXT NOT NULL DEFAULT '' CHECK (length(failure_message) <= 256),
    wrapped_dek BLOB,
    kek_id TEXT NOT NULL CHECK (length(kek_id) BETWEEN 1 AND 128),
    kek_version INTEGER NOT NULL CHECK (kek_version > 0),
    envelope_version INTEGER NOT NULL CHECK (envelope_version = 1),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) BETWEEN 32 AND 1048640),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    UNIQUE (project_id, idempotency_key),
    CHECK (
        (state = 'ready' AND wrapped_dek IS NULL AND failure_code = '' AND failure_message = '') OR
        (state = 'provisioning' AND length(wrapped_dek) = 64
            AND failure_code = '' AND failure_message = '') OR
        (state = 'failed' AND length(wrapped_dek) = 64 AND failure_code <> '')
    )
);

CREATE INDEX project_thread_intents_recovery
    ON project_thread_intents(state, created_at, id);

CREATE TRIGGER project_thread_intents_lifecycle
BEFORE UPDATE OF state ON project_thread_intents
WHEN NOT (
    OLD.state = NEW.state OR
    (OLD.state = 'provisioning' AND NEW.state IN ('ready', 'failed'))
)
BEGIN
    SELECT RAISE(ABORT, 'invalid Project Thread intent lifecycle transition');
END;

CREATE TRIGGER project_thread_intents_ready_requires_resources
BEFORE UPDATE OF state ON project_thread_intents
WHEN NEW.state = 'ready' AND (
    NOT EXISTS (
        SELECT 1 FROM threads
        WHERE id = NEW.thread_id AND capsule_id = NEW.capsule_id
    ) OR NOT EXISTS (
        SELECT 1 FROM runs
        WHERE id = NEW.run_id AND capsule_id = NEW.capsule_id
    )
)
BEGIN
    SELECT RAISE(ABORT, 'Project Thread promotion requires Thread and Run');
END;

CREATE TRIGGER project_thread_intents_identity_immutable
BEFORE UPDATE OF id, project_id, capsule_id, thread_id, run_id, message_id,
    block_id, capsule_name, requested_name, harness, idempotency_key,
    envelope_version, ciphertext, created_at
ON project_thread_intents
BEGIN
    SELECT RAISE(ABORT, 'Project Thread intent identity is immutable');
END;

CREATE TRIGGER project_thread_intents_no_delete
BEFORE DELETE ON project_thread_intents
BEGIN
    SELECT RAISE(ABORT, 'Project Thread intents are durable');
END;
