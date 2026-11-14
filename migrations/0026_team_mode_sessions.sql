-- Team mode and user sessions (ADR 0046).
--
-- The installation row records whether the installation has been claimed.
-- Until the claim it is in single-user mode: the installation bearer and the
-- loopback cookie authenticate as the bootstrap owner. The claim binds one
-- OIDC identity to the bootstrap owner and turns on team mode, once.
CREATE TABLE installation (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    mode TEXT NOT NULL CHECK (mode IN ('single_user', 'team')),
    oidc_issuer TEXT NOT NULL DEFAULT '',
    claimed_at TEXT,
    CHECK ((mode = 'team') = (claimed_at IS NOT NULL AND oidc_issuer <> ''))
);

INSERT INTO installation(id, mode) VALUES (1, 'single_user');

-- Team mode is permanent: no write turns it back off or changes the issuer.
CREATE TRIGGER installation_claim_once
BEFORE UPDATE ON installation
WHEN OLD.mode = 'team'
BEGIN
    SELECT RAISE(ABORT, 'installation is already claimed');
END;

CREATE TRIGGER installation_not_deleted
BEFORE DELETE ON installation
BEGIN
    SELECT RAISE(ABORT, 'installation row is permanent');
END;

-- A browser session. Only the SHA-256 digest of the session token is
-- stored; the token itself is the cookie value and is never written.
CREATE TABLE user_sessions (
    id TEXT PRIMARY KEY,
    token_digest BLOB NOT NULL UNIQUE CHECK (length(token_digest) = 32),
    user_id TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    last_used_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);

CREATE INDEX user_sessions_user ON user_sessions(user_id, created_at);

CREATE TRIGGER user_sessions_identity_immutable
BEFORE UPDATE OF id, token_digest, user_id, created_at, expires_at ON user_sessions
BEGIN
    SELECT RAISE(ABORT, 'session identity is immutable');
END;

CREATE TRIGGER user_sessions_revocation_final
BEFORE UPDATE OF revoked_at ON user_sessions
WHEN OLD.revoked_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'a revoked session stays revoked');
END;
