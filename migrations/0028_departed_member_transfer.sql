-- Departed-member transfer (ADR 0046). An owner never changes, except by
-- the break-glass transfer of a departed (disabled) member's objects to
-- another member. The transfer writes one owner_transfers row inside its
-- transaction, moves every owned row from that member to the other, and
-- deletes the row before committing. Each owner trigger allows exactly that
-- move while the row exists, and nothing else.
--
-- Provider connections never move: a personal Claude or ChatGPT login stays
-- with its account holder (ADR 0038), so their trigger is unchanged.
CREATE TABLE owner_transfers (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    from_user TEXT NOT NULL REFERENCES users(id),
    to_user TEXT NOT NULL REFERENCES users(id),
    CHECK (from_user <> to_user)
);

DROP TRIGGER projects_owner_immutable;
CREATE TRIGGER projects_owner_immutable
BEFORE UPDATE OF owner_user_id ON projects
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER secrets_owner_immutable;
CREATE TRIGGER secrets_owner_immutable
BEFORE UPDATE OF owner_user_id ON secrets
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER harness_setups_owner_immutable;
CREATE TRIGGER harness_setups_owner_immutable
BEFORE UPDATE OF owner_user_id ON harness_setups
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER capsules_owner_immutable;
CREATE TRIGGER capsules_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsules
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER timelines_owner_immutable;
CREATE TRIGGER timelines_owner_immutable
BEFORE UPDATE OF owner_user_id ON timelines
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER moments_owner_immutable;
CREATE TRIGGER moments_owner_immutable
BEFORE UPDATE OF owner_user_id ON moments
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER project_thread_intents_owner_immutable;
CREATE TRIGGER project_thread_intents_owner_immutable
BEFORE UPDATE OF owner_user_id ON project_thread_intents
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER setup_moment_cache_owner_immutable;
CREATE TRIGGER setup_moment_cache_owner_immutable
BEFORE UPDATE OF owner_user_id ON setup_moment_cache
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER project_preparation_policy_owner_immutable;
CREATE TRIGGER project_preparation_policy_owner_immutable
BEFORE UPDATE OF owner_user_id ON project_preparation_policy
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER coordination_grants_owner_immutable;
CREATE TRIGGER coordination_grants_owner_immutable
BEFORE UPDATE OF owner_user_id ON coordination_grants
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER runs_owner_immutable;
CREATE TRIGGER runs_owner_immutable
BEFORE UPDATE OF owner_user_id ON runs
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER threads_owner_immutable;
CREATE TRIGGER threads_owner_immutable
BEFORE UPDATE OF owner_user_id ON threads
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER deliveries_owner_immutable;
CREATE TRIGGER deliveries_owner_immutable
BEFORE UPDATE OF owner_user_id ON deliveries
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER capsule_preparations_owner_immutable;
CREATE TRIGGER capsule_preparations_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsule_preparations
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER capsule_startup_owner_immutable;
CREATE TRIGGER capsule_startup_owner_immutable
BEFORE UPDATE OF owner_user_id ON capsule_startup
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER thread_messages_owner_immutable;
CREATE TRIGGER thread_messages_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_messages
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER thread_blocks_owner_immutable;
CREATE TRIGGER thread_blocks_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_blocks
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER thread_deliveries_owner_immutable;
CREATE TRIGGER thread_deliveries_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_deliveries
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER thread_links_owner_immutable;
CREATE TRIGGER thread_links_owner_immutable
BEFORE UPDATE OF owner_user_id ON thread_links
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER coordination_requests_owner_immutable;
CREATE TRIGGER coordination_requests_owner_immutable
BEFORE UPDATE OF owner_user_id ON coordination_requests
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

DROP TRIGGER harness_setup_revisions_owner_immutable;
CREATE TRIGGER harness_setup_revisions_owner_immutable
BEFORE UPDATE OF owner_user_id ON harness_setup_revisions
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
    AND NOT EXISTS (
        SELECT 1 FROM owner_transfers
        WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id
    )
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

-- Immutable rows still refuse every change, except the transfer's own
-- owner move while its owner_transfers row exists. Each trigger is
-- otherwise recreated unchanged.
DROP TRIGGER timelines_immutable_update;
CREATE TRIGGER timelines_immutable_update
BEFORE UPDATE ON timelines
WHEN NOT (NEW.owner_user_id IS NOT OLD.owner_user_id AND EXISTS (SELECT 1 FROM owner_transfers WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id))
BEGIN
    SELECT RAISE(ABORT, 'Timelines are immutable');
END;

DROP TRIGGER moments_immutable_update;
CREATE TRIGGER moments_immutable_update
BEFORE UPDATE ON moments
WHEN NOT (NEW.owner_user_id IS NOT OLD.owner_user_id AND EXISTS (SELECT 1 FROM owner_transfers WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id))
BEGIN
    SELECT RAISE(ABORT, 'Moments are immutable');
END;

DROP TRIGGER threads_deleted_immutable;
CREATE TRIGGER threads_deleted_immutable
BEFORE UPDATE ON threads
WHEN (OLD.state = 'deleted')
    AND NOT (NEW.owner_user_id IS NOT OLD.owner_user_id AND EXISTS (SELECT 1 FROM owner_transfers WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id))
BEGIN
    SELECT RAISE(ABORT, 'deleted Threads are immutable');
END;

DROP TRIGGER thread_messages_immutable_update;
CREATE TRIGGER thread_messages_immutable_update
BEFORE UPDATE ON thread_messages
WHEN NOT (NEW.owner_user_id IS NOT OLD.owner_user_id AND EXISTS (SELECT 1 FROM owner_transfers WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id))
BEGIN
    SELECT RAISE(ABORT, 'ThreadMessages are immutable');
END;

DROP TRIGGER thread_blocks_immutable_update;
CREATE TRIGGER thread_blocks_immutable_update
BEFORE UPDATE ON thread_blocks
WHEN NOT (NEW.owner_user_id IS NOT OLD.owner_user_id AND EXISTS (SELECT 1 FROM owner_transfers WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id))
BEGIN
    SELECT RAISE(ABORT, 'ThreadBlocks are immutable');
END;

DROP TRIGGER thread_deliveries_immutable;
CREATE TRIGGER thread_deliveries_immutable
BEFORE UPDATE ON thread_deliveries
WHEN (OLD.message_id <> NEW.message_id OR OLD.thread_id <> NEW.thread_id OR
     OLD.run_id <> NEW.run_id OR OLD.controller_id <> NEW.controller_id OR
     OLD.created_at <> NEW.created_at OR OLD.delivered_at IS NOT NULL)
    AND NOT (NEW.owner_user_id IS NOT OLD.owner_user_id AND EXISTS (SELECT 1 FROM owner_transfers WHERE id = 1 AND from_user = OLD.owner_user_id AND to_user = NEW.owner_user_id))
BEGIN
    SELECT RAISE(ABORT, 'Thread deliveries are immutable after acknowledgement');
END;
