-- One GitHub login per owner (ADR 0051).
--
-- The token is an owner-bound envelope under the credential KEK. Login,
-- status, and scopes are metadata. The row does not move on a departed-member
-- transfer: the login stays with the person who connected it. Deleting the
-- member deletes the row, which removes the ciphertext.

CREATE TABLE github_connections (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL UNIQUE REFERENCES users(id),
    login TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'needs_reconnect')),
    reason TEXT NOT NULL DEFAULT '' CHECK (reason IN ('', 'invalid', 'expired', 'missing_access')),
    scopes TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL CHECK (source IN ('device', 'host', 'token')),
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    kek_id TEXT NOT NULL,
    kek_version INTEGER NOT NULL CHECK (kek_version > 0),
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    checked_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    CHECK ((status = 'active') = (reason = ''))
);

CREATE TRIGGER github_connections_owner_insert
BEFORE INSERT ON github_connections
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER github_connections_owner_immutable
BEFORE UPDATE OF owner_user_id ON github_connections
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;
