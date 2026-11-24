-- Match Worker Model spawns fall back to the harness default (ADR 0031 amendment).
--
-- thread_links gains model_fallback: 1 when the requested model came from the
-- grant's Match setting rather than an explicit spawn override or a Custom
-- Worker Model. Such a subagent starts on its harness's default model when the
-- harness cannot apply the requested one, instead of failing. model_state
-- widens to include 'default', recording that it did.

PRAGMA writable_schema = ON;

UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (model_state IN ('''', ''pending'', ''requested'', ''applied''))',
    'CHECK (model_state IN ('''', ''pending'', ''requested'', ''applied'', ''default''))'
)
WHERE type = 'table' AND name = 'thread_links';

PRAGMA writable_schema = RESET;

ALTER TABLE thread_links ADD COLUMN model_fallback INTEGER NOT NULL DEFAULT 0
    CHECK (model_fallback IN (0, 1));

-- Fail the migration if the model_state CHECK was not widened.
CREATE TEMP TABLE thread_links_model_state_widened(ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO thread_links_model_state_widened(ok)
SELECT CASE
    WHEN sql LIKE '%''applied'', ''default''%' THEN 1
    ELSE 0
END
FROM sqlite_master
WHERE type = 'table' AND name = 'thread_links';
DROP TABLE thread_links_model_state_widened;
