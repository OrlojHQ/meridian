-- A clone the station cannot authenticate is terminal. SQLite cannot ALTER a
-- CHECK that was added with ADD COLUMN, so the capsules table statement is
-- rewritten in place. Columns, indexes, and triggers stay as they are.

PRAGMA writable_schema = ON;

UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK (failure_code IN ('''', ''registry_unreachable'', ''auth_required'', ''image_not_found''))',
    'CHECK (failure_code IN ('''', ''registry_unreachable'', ''auth_required'', ''image_not_found'', ''repository_access''))'
)
WHERE type = 'table' AND name = 'capsules';

PRAGMA writable_schema = RESET;

-- Fail the migration if the check was not widened. A zero cannot be inserted.
CREATE TEMP TABLE capsule_failure_code_widened(ok INTEGER NOT NULL CHECK (ok = 1));
INSERT INTO capsule_failure_code_widened(ok)
SELECT CASE
    WHEN sql LIKE '%''repository_access''%' THEN 1
    ELSE 0
END
FROM sqlite_master
WHERE type = 'table' AND name = 'capsules';
DROP TABLE capsule_failure_code_widened;
