-- Allow 'upgrading' as a Capsule maintenance state for in-place official pack
-- image upgrades (ADR 0064).

ALTER TABLE capsules ADD COLUMN maintenance_next TEXT NOT NULL DEFAULT ''
    CHECK (maintenance_next IN ('', 'capture', 'seal', 'restore', 'upgrading'));
UPDATE capsules SET maintenance_next = maintenance;
DROP INDEX capsules_idle_candidates;
ALTER TABLE capsules DROP COLUMN maintenance;
ALTER TABLE capsules RENAME COLUMN maintenance_next TO maintenance;
CREATE INDEX capsules_idle_candidates
    ON capsules(state, desired_state, maintenance, last_activity_at, id);
