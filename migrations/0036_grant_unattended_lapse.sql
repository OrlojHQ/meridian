-- Coordination grants lapse on unattended time instead of expiring at an
-- absolute deadline (ADR 0059).
--
-- last_operator_at moves only on operator API actions and never backwards.
-- lapsed_at marks that the lapse was applied to running subagents; it is
-- cleared when the operator returns. A lapsed grant stays 'active' in the
-- store, so the state lifecycle trigger is unchanged, and ended grants keep
-- their attendance fields frozen.

ALTER TABLE coordination_grants ADD COLUMN last_operator_at TEXT NOT NULL DEFAULT '';
ALTER TABLE coordination_grants ADD COLUMN unattended_limit_seconds INTEGER NOT NULL DEFAULT 86400
    CHECK (unattended_limit_seconds BETWEEN 300 AND 259200);
ALTER TABLE coordination_grants ADD COLUMN lapsed_at TEXT;

-- Active grants keep the time they had left: the limit is the remaining
-- deadline, capped at 72 hours, and the operator is treated as present at
-- the deadline minus that limit, so the grant stops spending when it would
-- have expired. A grant within five minutes of its deadline gets the
-- five-minute minimum and still stops at its old deadline.
UPDATE coordination_grants
SET unattended_limit_seconds = MAX(300, MIN(259200,
    CAST(round((julianday(deadline_at) - julianday('now')) * 86400) AS INTEGER)))
WHERE state = 'active';

UPDATE coordination_grants
SET last_operator_at = strftime('%Y-%m-%dT%H:%M:%fZ', deadline_at,
    '-' || unattended_limit_seconds || ' seconds')
WHERE state = 'active';

-- Ended grants never spend again; their attendance is informational.
UPDATE coordination_grants SET last_operator_at = created_at WHERE state <> 'active';

DROP INDEX coordination_grants_active;
ALTER TABLE coordination_grants DROP COLUMN deadline_at;
CREATE INDEX coordination_grants_active ON coordination_grants(state, created_at, id);

CREATE TRIGGER coordination_grants_attendance_insert
BEFORE INSERT ON coordination_grants
WHEN julianday(NEW.last_operator_at) IS NULL OR NEW.lapsed_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

CREATE TRIGGER coordination_grants_attendance
BEFORE UPDATE OF last_operator_at, unattended_limit_seconds, lapsed_at ON coordination_grants
WHEN julianday(NEW.last_operator_at) IS NULL
    OR julianday(NEW.last_operator_at) < julianday(OLD.last_operator_at)
    OR (NEW.lapsed_at IS NOT NULL AND julianday(NEW.lapsed_at) IS NULL)
    OR (OLD.state <> 'active' AND (
        NEW.last_operator_at IS NOT OLD.last_operator_at
        OR NEW.unattended_limit_seconds IS NOT OLD.unattended_limit_seconds
        OR NEW.lapsed_at IS NOT OLD.lapsed_at))
BEGIN
    SELECT RAISE(ABORT, 'coordination grant lifecycle violation');
END;

-- Spawn limits count links created in a rolling window.
CREATE INDEX thread_links_grant_created ON thread_links(grant_id, created_at);
