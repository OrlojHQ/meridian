-- GitHub App for this Station (ADR 0061).
--
-- One row. The private key, webhook secret, and client secret are one
-- envelope under the installation KEK. They never leave the Station.
-- Capsules receive only a short-lived installation token for one repository.
--
-- capsule_github_access is the per-owner switch for that session token.
-- It is on by default, including for a profile saved before this column.
-- The column stores the switch only. The token is not stored here.

ALTER TABLE user_profiles ADD COLUMN capsule_github_access INTEGER NOT NULL DEFAULT 1
    CHECK (capsule_github_access IN (0, 1));

CREATE TABLE github_app (
    id TEXT PRIMARY KEY CHECK (id = 'station'),
    app_id INTEGER NOT NULL CHECK (app_id > 0),
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    client_id TEXT NOT NULL,
    html_url TEXT NOT NULL,
    owner_login TEXT NOT NULL,
    owner_type TEXT NOT NULL CHECK (owner_type IN ('User', 'Organization')),
    webhook_active INTEGER NOT NULL CHECK (webhook_active IN (0, 1)),
    created_by TEXT NOT NULL,
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    kek_id TEXT NOT NULL,
    kek_version INTEGER NOT NULL CHECK (kek_version > 0),
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0)
);

CREATE TABLE github_app_installations (
    installation_id INTEGER PRIMARY KEY CHECK (installation_id > 0),
    account_login TEXT NOT NULL,
    account_type TEXT NOT NULL CHECK (account_type IN ('User', 'Organization')),
    repository_selection TEXT NOT NULL CHECK (repository_selection IN ('all', 'selected')),
    suspended INTEGER NOT NULL CHECK (suspended IN (0, 1)),
    updated_at TEXT NOT NULL
);
