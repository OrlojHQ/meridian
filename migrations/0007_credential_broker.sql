ALTER TABLE projects
    ADD COLUMN git_secret_name TEXT NOT NULL DEFAULT '';
ALTER TABLE projects
    ADD COLUMN harness_secret_names TEXT NOT NULL DEFAULT '[]';

CREATE TABLE secrets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
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
    resource_version INTEGER NOT NULL CHECK (resource_version > 0)
);
CREATE INDEX secrets_purpose_name ON secrets(purpose, name);
