-- Per-lead worker model defaults and per-subagent requested model (ADR 0031 amendment).
--
-- coordination_grants gains worker_model_mode ('match' or 'custom'),
-- worker_harness, and worker_model (bounded opaque model ID, never prompt content).
-- thread_links gains requested_model, model_state, and model_selecting_since so
-- a spawned subagent applies its chosen model strictly before its first prompt
-- with no silent fallback, and widens last_report_reason to include
-- 'model_unavailable'.

PRAGMA writable_schema = ON;

UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (last_report_reason IN ('''', ''cancelled'', ''capsule_failed'', ''session_ended'', ''revoked'', ''denied''))',
    'CHECK (last_report_reason IN ('''', ''cancelled'', ''capsule_failed'', ''session_ended'', ''revoked'', ''denied'', ''model_unavailable''))'
)
WHERE type = 'table' AND name = 'thread_links';

PRAGMA writable_schema = RESET;

ALTER TABLE coordination_grants ADD COLUMN worker_model_mode TEXT NOT NULL DEFAULT 'match'
    CHECK (worker_model_mode IN ('match', 'custom'));
ALTER TABLE coordination_grants ADD COLUMN worker_harness TEXT NOT NULL DEFAULT ''
    CHECK (length(worker_harness) <= 128);
ALTER TABLE coordination_grants ADD COLUMN worker_model TEXT NOT NULL DEFAULT ''
    CHECK (length(worker_model) <= 256);

CREATE TRIGGER coordination_grants_worker_model_insert
BEFORE INSERT ON coordination_grants
WHEN (NEW.worker_model_mode = 'match' AND (NEW.worker_harness <> '' OR NEW.worker_model <> ''))
    OR (NEW.worker_model_mode = 'custom' AND length(NEW.worker_model) < 1)
BEGIN
    SELECT RAISE(ABORT, 'coordination grant worker model violation');
END;

CREATE TRIGGER coordination_grants_worker_model_update
BEFORE UPDATE OF worker_model_mode, worker_harness, worker_model ON coordination_grants
WHEN (NEW.worker_model_mode = 'match' AND (NEW.worker_harness <> '' OR NEW.worker_model <> ''))
    OR (NEW.worker_model_mode = 'custom' AND length(NEW.worker_model) < 1)
BEGIN
    SELECT RAISE(ABORT, 'coordination grant worker model violation');
END;

ALTER TABLE thread_links ADD COLUMN requested_model TEXT NOT NULL DEFAULT ''
    CHECK (length(requested_model) <= 256);
ALTER TABLE thread_links ADD COLUMN model_state TEXT NOT NULL DEFAULT ''
    CHECK (model_state IN ('', 'pending', 'requested', 'applied'));
ALTER TABLE thread_links ADD COLUMN model_selecting_since TEXT;

-- Fail the migration if last_report_reason CHECK was not widened.
CREATE TEMP TABLE thread_links_reason_widened(ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO thread_links_reason_widened(ok)
SELECT CASE
    WHEN sql LIKE '%''model_unavailable''%' THEN 1
    ELSE 0
END
FROM sqlite_master
WHERE type = 'table' AND name = 'thread_links';
DROP TABLE thread_links_reason_widened;
