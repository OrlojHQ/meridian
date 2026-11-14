-- Per-user ownership (ADR 0046). Every root object and every row that
-- belongs to one carries the user who owns it. Existing rows all belong to
-- the bootstrap owner, the single-user mode user.
--
-- Owners are checked here as a second line behind internal/app:
-- * a root row must name an existing user;
-- * a child row's owner must equal its parent's;
-- * a join row may link only two rows with the same owner;
-- * an owner never changes once written.
-- 'owner violation' is a cross-owner reference and reads as not found.
-- 'owner missing' and 'owner immutable' are server bugs.
--
-- The columns default to '' only because SQLite cannot add a NOT NULL
-- column without a default. The store always writes the owner, and the
-- triggers refuse a row that would keep the default. A later table rebuild
-- must copy owner_user_id and keep these triggers.

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    issuer TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL CHECK (role IN ('member', 'admin')),
    disabled_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((issuer = '') = (subject = ''))
);

-- A user is (issuer, subject). The bootstrap owner has neither until the
-- installation is claimed.
CREATE UNIQUE INDEX users_identity ON users(issuer, subject) WHERE issuer <> '';

INSERT INTO users(id, role, created_at, updated_at)
VALUES (
    'usr_bootstrap_owner', 'admin',
    strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);

ALTER TABLE projects ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE secrets ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE provider_connections ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE harness_setups ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE capsules ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE timelines ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE moments ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE project_thread_intents ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE setup_moment_cache ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE project_preparation_policy ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE coordination_grants ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE threads ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE deliveries ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE capsule_preparations ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE capsule_startup ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_messages ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_blocks ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_deliveries ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_links ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE coordination_requests ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE harness_setup_revisions ADD COLUMN owner_user_id TEXT NOT NULL DEFAULT '';

-- These triggers refuse every UPDATE of an immutable row. The backfill
-- below is the one write they must allow; they are recreated unchanged.
DROP TRIGGER timelines_immutable_update;
DROP TRIGGER moments_immutable_update;
DROP TRIGGER threads_deleted_immutable;
DROP TRIGGER thread_messages_immutable_update;
DROP TRIGGER thread_blocks_immutable_update;
DROP TRIGGER thread_deliveries_immutable;
DROP TRIGGER setup_moment_cache_kind_update;

UPDATE projects SET owner_user_id = 'usr_bootstrap_owner';
UPDATE secrets SET owner_user_id = 'usr_bootstrap_owner';
UPDATE provider_connections SET owner_user_id = 'usr_bootstrap_owner';
UPDATE harness_setups SET owner_user_id = 'usr_bootstrap_owner';
UPDATE capsules SET owner_user_id = 'usr_bootstrap_owner';
UPDATE timelines SET owner_user_id = 'usr_bootstrap_owner';
UPDATE moments SET owner_user_id = 'usr_bootstrap_owner';
UPDATE project_thread_intents SET owner_user_id = 'usr_bootstrap_owner';
UPDATE setup_moment_cache SET owner_user_id = 'usr_bootstrap_owner';
UPDATE project_preparation_policy SET owner_user_id = 'usr_bootstrap_owner';
UPDATE coordination_grants SET owner_user_id = 'usr_bootstrap_owner';
UPDATE runs SET owner_user_id = 'usr_bootstrap_owner';
UPDATE threads SET owner_user_id = 'usr_bootstrap_owner';
UPDATE deliveries SET owner_user_id = 'usr_bootstrap_owner';
UPDATE capsule_preparations SET owner_user_id = 'usr_bootstrap_owner';
UPDATE capsule_startup SET owner_user_id = 'usr_bootstrap_owner';
UPDATE thread_messages SET owner_user_id = 'usr_bootstrap_owner';
UPDATE thread_blocks SET owner_user_id = 'usr_bootstrap_owner';
UPDATE thread_deliveries SET owner_user_id = 'usr_bootstrap_owner';
UPDATE thread_links SET owner_user_id = 'usr_bootstrap_owner';
UPDATE coordination_requests SET owner_user_id = 'usr_bootstrap_owner';
UPDATE harness_setup_revisions SET owner_user_id = 'usr_bootstrap_owner';

CREATE TRIGGER timelines_immutable_update
BEFORE UPDATE ON timelines BEGIN
    SELECT RAISE(ABORT, 'Timelines are immutable');
END;

CREATE TRIGGER moments_immutable_update
BEFORE UPDATE ON moments BEGIN
    SELECT RAISE(ABORT, 'Moments are immutable');
END;

CREATE TRIGGER threads_deleted_immutable
BEFORE UPDATE ON threads
WHEN OLD.state = 'deleted'
BEGIN
    SELECT RAISE(ABORT, 'deleted Threads are immutable');
END;

CREATE TRIGGER thread_messages_immutable_update
BEFORE UPDATE ON thread_messages BEGIN
    SELECT RAISE(ABORT, 'ThreadMessages are immutable');
END;

CREATE TRIGGER thread_blocks_immutable_update
BEFORE UPDATE ON thread_blocks BEGIN
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

CREATE TRIGGER setup_moment_cache_kind_update
BEFORE UPDATE ON setup_moment_cache
WHEN NOT EXISTS (
    SELECT 1 FROM moments
    WHERE id = NEW.moment_id
      AND project_id = NEW.project_id
      AND project_setup_hash = NEW.config_hash
      AND kind = 'setup_cache'
)
BEGIN
    SELECT RAISE(ABORT, 'setup Moment cache mismatch');
END;

CREATE INDEX projects_owner ON projects(owner_user_id);
CREATE INDEX secrets_owner ON secrets(owner_user_id);
CREATE INDEX provider_connections_owner ON provider_connections(owner_user_id);
CREATE INDEX harness_setups_owner ON harness_setups(owner_user_id);
CREATE INDEX capsules_owner ON capsules(owner_user_id);

CREATE TRIGGER projects_owner_insert
BEFORE INSERT ON projects
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER secrets_owner_insert
BEFORE INSERT ON secrets
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER provider_connections_owner_insert
BEFORE INSERT ON provider_connections
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER harness_setups_owner_insert
BEFORE INSERT ON harness_setups
WHEN NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.owner_user_id)
BEGIN
    SELECT RAISE(ABORT, 'owner missing');
END;

CREATE TRIGGER capsules_owner_insert
BEFORE INSERT ON capsules
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER timelines_owner_insert
BEFORE INSERT ON timelines
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER moments_owner_insert
BEFORE INSERT ON moments
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER project_thread_intents_owner_insert
BEFORE INSERT ON project_thread_intents
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER setup_moment_cache_owner_insert
BEFORE INSERT ON setup_moment_cache
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER project_preparation_policy_owner_insert
BEFORE INSERT ON project_preparation_policy
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER coordination_grants_owner_insert
BEFORE INSERT ON coordination_grants
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER runs_owner_insert
BEFORE INSERT ON runs
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER threads_owner_insert
BEFORE INSERT ON threads
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER deliveries_owner_insert
BEFORE INSERT ON deliveries
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER capsule_preparations_owner_insert
BEFORE INSERT ON capsule_preparations
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER capsule_startup_owner_insert
BEFORE INSERT ON capsule_startup
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER thread_messages_owner_insert
BEFORE INSERT ON thread_messages
WHEN EXISTS (SELECT 1 FROM threads WHERE id = NEW.thread_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM threads WHERE id = NEW.thread_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER thread_blocks_owner_insert
BEFORE INSERT ON thread_blocks
WHEN EXISTS (SELECT 1 FROM threads WHERE id = NEW.thread_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM threads WHERE id = NEW.thread_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER thread_deliveries_owner_insert
BEFORE INSERT ON thread_deliveries
WHEN EXISTS (SELECT 1 FROM threads WHERE id = NEW.thread_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM threads WHERE id = NEW.thread_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER thread_links_owner_insert
BEFORE INSERT ON thread_links
WHEN EXISTS (SELECT 1 FROM coordination_grants WHERE id = NEW.grant_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM coordination_grants WHERE id = NEW.grant_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER coordination_requests_owner_insert
BEFORE INSERT ON coordination_requests
WHEN EXISTS (SELECT 1 FROM coordination_grants WHERE id = NEW.grant_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM coordination_grants WHERE id = NEW.grant_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER harness_setup_revisions_owner_insert
BEFORE INSERT ON harness_setup_revisions
WHEN EXISTS (SELECT 1 FROM harness_setups WHERE id = NEW.setup_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM harness_setups WHERE id = NEW.setup_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER projects_owner_immutable
BEFORE UPDATE OF owner_user_id ON projects
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER secrets_owner_immutable
BEFORE UPDATE OF owner_user_id ON secrets
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER provider_connections_owner_immutable
BEFORE UPDATE OF owner_user_id ON provider_connections
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER harness_setups_owner_immutable
BEFORE UPDATE OF owner_user_id ON harness_setups
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER capsules_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsules
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER timelines_owner_immutable
BEFORE UPDATE OF owner_user_id ON timelines
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER moments_owner_immutable
BEFORE UPDATE OF owner_user_id ON moments
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER project_thread_intents_owner_immutable
BEFORE UPDATE OF owner_user_id ON project_thread_intents
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER setup_moment_cache_owner_immutable
BEFORE UPDATE OF owner_user_id ON setup_moment_cache
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER project_preparation_policy_owner_immutable
BEFORE UPDATE OF owner_user_id ON project_preparation_policy
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER coordination_grants_owner_immutable
BEFORE UPDATE OF owner_user_id ON coordination_grants
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER runs_owner_immutable
BEFORE UPDATE OF owner_user_id ON runs
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER threads_owner_immutable
BEFORE UPDATE OF owner_user_id ON threads
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER deliveries_owner_immutable
BEFORE UPDATE OF owner_user_id ON deliveries
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER capsule_preparations_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsule_preparations
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER capsule_startup_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsule_startup
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER thread_messages_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_messages
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER thread_blocks_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_blocks
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER thread_deliveries_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_deliveries
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER thread_links_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_links
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER coordination_requests_owner_immutable
BEFORE UPDATE OF owner_user_id ON coordination_requests
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER harness_setup_revisions_owner_immutable
BEFORE UPDATE OF owner_user_id ON harness_setup_revisions
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TRIGGER project_provider_connections_owner_insert
BEFORE INSERT ON project_provider_connections
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND EXISTS (SELECT 1 FROM provider_connections WHERE id = NEW.connection_id)
    AND (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
        IS NOT (SELECT owner_user_id FROM provider_connections WHERE id = NEW.connection_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER project_provider_connections_owner_update
BEFORE UPDATE OF project_id, connection_id ON project_provider_connections
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND EXISTS (SELECT 1 FROM provider_connections WHERE id = NEW.connection_id)
    AND (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
        IS NOT (SELECT owner_user_id FROM provider_connections WHERE id = NEW.connection_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER project_harness_setups_owner_insert
BEFORE INSERT ON project_harness_setups
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND EXISTS (SELECT 1 FROM harness_setups WHERE id = NEW.setup_id)
    AND (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
        IS NOT (SELECT owner_user_id FROM harness_setups WHERE id = NEW.setup_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER project_harness_setups_owner_update
BEFORE UPDATE OF project_id, setup_id ON project_harness_setups
WHEN EXISTS (SELECT 1 FROM projects WHERE id = NEW.project_id)
    AND EXISTS (SELECT 1 FROM harness_setups WHERE id = NEW.setup_id)
    AND (SELECT owner_user_id FROM projects WHERE id = NEW.project_id)
        IS NOT (SELECT owner_user_id FROM harness_setups WHERE id = NEW.setup_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER capsule_harness_setups_owner_insert
BEFORE INSERT ON capsule_harness_setups
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND EXISTS (SELECT 1 FROM harness_setup_revisions WHERE id = NEW.revision_id)
    AND (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
        IS NOT (SELECT owner_user_id FROM harness_setup_revisions WHERE id = NEW.revision_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER capsule_harness_setups_owner_update
BEFORE UPDATE OF capsule_id, revision_id ON capsule_harness_setups
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND EXISTS (SELECT 1 FROM harness_setup_revisions WHERE id = NEW.revision_id)
    AND (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
        IS NOT (SELECT owner_user_id FROM harness_setup_revisions WHERE id = NEW.revision_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;
