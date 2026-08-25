ALTER TABLE projects
    ADD COLUMN harness_images TEXT NOT NULL DEFAULT '[]';
ALTER TABLE capsules
    ADD COLUMN image_reference TEXT NOT NULL DEFAULT '';
