-- The test harness used to be advertised in the installation catalog, so
-- Projects created from that catalog saved a harness allowlist entry named
-- mock. Drop those saved entries once, with no error, so launchers stop
-- offering it. Rows written after this migration, including an explicit
-- test allowlist, are not touched.

UPDATE projects
SET harness_images = COALESCE((
        SELECT json_group_array(json(entry.value))
        FROM json_each(projects.harness_images) AS entry
        WHERE json_extract(entry.value, '$.name') != 'mock'
    ), '[]'),
    resource_version = resource_version + 1
WHERE json_valid(harness_images)
    AND EXISTS (
        SELECT 1
        FROM json_each(projects.harness_images) AS entry
        WHERE json_extract(entry.value, '$.name') = 'mock'
    );
