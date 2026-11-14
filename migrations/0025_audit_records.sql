-- Append-only audit records (ADR 0046): who did what to which object, and
-- whether it was allowed, denied, or failed. A record names ids only. It
-- never holds a secret value, prompt, transcript text, diff, or file
-- content.
--
-- No API edits or deletes a record, and these triggers refuse every UPDATE.
-- Only the daemon's retention sweep deletes, oldest first. The table is not
-- tamper-evident: anyone who can write the database can change it.
CREATE TABLE audit_records (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    recorded_at TEXT NOT NULL,
    principal_kind TEXT NOT NULL CHECK (
        principal_kind IN ('user', 'break_glass', 'system', 'capsule')
    ),
    -- The acting user, or the owner a Capsule acted for. Empty for the
    -- system and break-glass principals.
    user_id TEXT NOT NULL DEFAULT '',
    -- The acting Capsule, for a Capsule principal.
    capsule_id TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL CHECK (length(action) BETWEEN 1 AND 64),
    object_type TEXT NOT NULL CHECK (length(object_type) BETWEEN 1 AND 64),
    object_id TEXT NOT NULL CHECK (length(object_id) <= 256),
    result TEXT NOT NULL CHECK (result IN ('allowed', 'denied', 'failed')),
    CHECK ((principal_kind IN ('user', 'capsule')) = (user_id <> '')),
    CHECK ((principal_kind = 'capsule') = (capsule_id <> ''))
);

CREATE INDEX audit_records_user ON audit_records(user_id, sequence);
CREATE INDEX audit_records_recorded_at ON audit_records(recorded_at);

CREATE TRIGGER audit_records_append_only
BEFORE UPDATE ON audit_records
BEGIN
    SELECT RAISE(ABORT, 'audit records are append-only');
END;
