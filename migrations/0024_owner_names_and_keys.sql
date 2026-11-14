-- Secret names and idempotency keys are per owner (ADR 0046). Two users can
-- each have a secret named github_api, and one user's replay key can neither
-- suppress nor replay another user's mutation.
--
-- Both tables are rebuilt: SQLite cannot change a UNIQUE or PRIMARY KEY in
-- place. No table references either one.

CREATE TABLE secrets_next (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    purpose TEXT NOT NULL CHECK (
        purpose IN ('git_https', 'git_push', 'github_api', 'harness_env')
    ),
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    kek_id TEXT NOT NULL,
    kek_version INTEGER NOT NULL CHECK (kek_version > 0),
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    owner_user_id TEXT NOT NULL DEFAULT '',
    UNIQUE (owner_user_id, name)
);

INSERT INTO secrets_next(
    id, name, purpose, envelope_version, kek_id, kek_version, nonce, ciphertext,
    created_at, updated_at, resource_version, owner_user_id
)
SELECT
    id, name, purpose, envelope_version, kek_id, kek_version, nonce, ciphertext,
    created_at, updated_at, resource_version, owner_user_id
FROM secrets;

DROP TABLE secrets;
ALTER TABLE secrets_next RENAME TO secrets;

CREATE INDEX secrets_owner_purpose_name ON secrets(owner_user_id, purpose, name);

CREATE TRIGGER secrets_owner_insert
BEFORE INSERT ON secrets
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER secrets_owner_immutable
BEFORE UPDATE OF owner_user_id ON secrets
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- Idempotency outcomes are keyed by the caller's owner. Daemon-internal
-- work that records an outcome uses the empty owner, which no user has.
CREATE TABLE idempotency_outcomes_next (
    owner_user_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    outcome BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (owner_user_id, scope, key)
);

INSERT INTO idempotency_outcomes_next(owner_user_id, scope, key, outcome, created_at)
SELECT 'usr_bootstrap_owner', scope, key, outcome, created_at FROM idempotency_outcomes;

DROP TABLE idempotency_outcomes;
ALTER TABLE idempotency_outcomes_next RENAME TO idempotency_outcomes;
