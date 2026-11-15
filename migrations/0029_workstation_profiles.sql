-- Workstation profiles (ADR 0049).
--
-- A profile groups the owner's default harness setups (harness_setups.default,
-- unchanged here) with a git commit identity and a default provider connection
-- per harness. One row per user. A profile never moves between members: the
-- departed-member transfer leaves it with the person it names, so its owner
-- trigger has no transfer exception.
CREATE TABLE user_profiles (
    owner_user_id TEXT PRIMARY KEY REFERENCES users(id),
    git_name TEXT NOT NULL DEFAULT '',
    git_email TEXT NOT NULL DEFAULT '',
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    updated_at TEXT NOT NULL
);

CREATE TRIGGER user_profiles_owner_insert
BEFORE INSERT ON user_profiles
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER user_profiles_owner_immutable
BEFORE UPDATE OF owner_user_id ON user_profiles
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- A default connection is a suggestion, never a grant: the gateway reads only
-- project_provider_connections. It names only the owner's own connection.
CREATE TABLE user_profile_connections (
    owner_user_id TEXT NOT NULL REFERENCES user_profiles(owner_user_id),
    harness TEXT NOT NULL,
    connection_id TEXT NOT NULL REFERENCES provider_connections(id),
    PRIMARY KEY (owner_user_id, harness)
);

CREATE TRIGGER user_profile_connections_owner_insert
BEFORE INSERT ON user_profile_connections
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
    OR NEW.owner_user_id IS NOT (SELECT owner_user_id FROM provider_connections WHERE id = NEW.connection_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER user_profile_connections_owner_immutable
BEFORE UPDATE OF owner_user_id, connection_id ON user_profile_connections
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    OR NEW.owner_user_id IS NOT (SELECT owner_user_id FROM provider_connections WHERE id = NEW.connection_id)
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- The import report for one setup revision, sealed in its own envelope whose
-- id binds the revision's owner (owner:<owner>:<revision>:report). It follows
-- its revision's owner, like capsule_harness_setups follows its Capsule.
CREATE TABLE harness_setup_reports (
    revision_id TEXT PRIMARY KEY REFERENCES harness_setup_revisions(id),
    key_id TEXT NOT NULL,
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL
);

-- The profile identity a Capsule was created with. It follows its Capsule's
-- owner. A clean Capsule has no row.
CREATE TABLE capsule_profile_pins (
    capsule_id TEXT PRIMARY KEY REFERENCES capsules(id),
    git_name TEXT NOT NULL DEFAULT '',
    git_email TEXT NOT NULL DEFAULT '',
    pinned_at TEXT NOT NULL
);
