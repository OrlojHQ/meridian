-- The operator pins the Threads they come back to (ADR 0059). pinned_at is
-- presentation only: it grants nothing and no authorization check reads it.
-- Only a live (active or paused) Thread of a main Capsule can carry a pin,
-- and archiving or crypto-shredding the Thread clears it. Project deletion
-- removes the Thread rows, and the pin with them (ADR 0039).
ALTER TABLE threads ADD COLUMN pinned_at TEXT;

CREATE TRIGGER threads_pin_insert
BEFORE INSERT ON threads
WHEN NEW.pinned_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'thread pin lifecycle violation');
END;

CREATE TRIGGER threads_pin_lifecycle
BEFORE UPDATE OF pinned_at, state ON threads
WHEN NEW.pinned_at IS NOT NULL AND (
    NEW.state NOT IN ('active', 'paused')
    OR COALESCE((SELECT role FROM capsules WHERE id = NEW.capsule_id), '') <> 'main'
)
BEGIN
    SELECT RAISE(ABORT, 'thread pin lifecycle violation');
END;
