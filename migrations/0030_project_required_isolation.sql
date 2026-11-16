-- Project isolation floor (ADR 0050).
--
-- A Project may require a minimum isolation class for every Capsule created
-- in it. The column is written once at creation and never updated: raising or
-- lowering an existing Project's floor after Capsules exist would silently
-- change the boundary those Capsules run behind. Empty means no requirement,
-- which is how every pre-0030 Project reads back.
ALTER TABLE projects ADD COLUMN required_isolation TEXT NOT NULL DEFAULT ''
    CHECK (required_isolation IN ('', 'container', 'sandbox', 'microvm'));
