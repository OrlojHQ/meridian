-- Personal access tokens (ADR 0046): how a member's CLI and TUI act as that
-- member in team mode. A token is shown once; only its SHA-256 digest is
-- stored. It expires (90 days by default, at most a year) and can be
-- read-only.
CREATE TABLE personal_access_tokens (
    id TEXT PRIMARY KEY,
    token_digest BLOB NOT NULL UNIQUE CHECK (length(token_digest) = 32),
    user_id TEXT NOT NULL REFERENCES users(id),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    read_only INTEGER NOT NULL CHECK (read_only IN (0, 1)),
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);

CREATE INDEX personal_access_tokens_user ON personal_access_tokens(user_id, created_at);

CREATE TRIGGER personal_access_tokens_identity_immutable
BEFORE UPDATE OF id, token_digest, user_id, name, read_only, created_at, expires_at ON personal_access_tokens
BEGIN
    SELECT RAISE(ABORT, 'token identity is immutable');
END;

CREATE TRIGGER personal_access_tokens_revocation_final
BEFORE UPDATE OF revoked_at ON personal_access_tokens
WHEN OLD.revoked_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a revoked token stays revoked');
END;
