-- Bounded Capsule startup progress. Phase names are a fixed vocabulary and
-- byte counts are numeric. Registry text is never stored.

ALTER TABLE capsules ADD COLUMN failure_code TEXT NOT NULL DEFAULT ''
    CHECK (failure_code IN ('', 'registry_unreachable', 'auth_required', 'image_not_found'));

CREATE TABLE capsule_startup (
    capsule_id TEXT PRIMARY KEY REFERENCES capsules(id),
    phase TEXT NOT NULL CHECK (phase IN (
        'pulling_image', 'creating_container', 'starting_harness', 'connecting'
    )),
    phases TEXT NOT NULL CHECK (length(phases) BETWEEN 1 AND 128),
    current_bytes INTEGER NOT NULL DEFAULT 0 CHECK (
        current_bytes >= 0 AND current_bytes <= 1099511627776
    ),
    total_bytes INTEGER NOT NULL DEFAULT 0 CHECK (
        total_bytes >= 0 AND total_bytes <= 1099511627776
    ),
    updated_at TEXT NOT NULL
);
