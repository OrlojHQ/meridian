-- Capsule GitHub access (ADR 0061).
--
-- One switch on the owner's profile. It is on by default, including for a
-- profile saved before this column existed. The column stores the switch
-- only. The GitHub token stays in the connection envelope.

ALTER TABLE user_profiles ADD COLUMN capsule_github_access INTEGER NOT NULL DEFAULT 1
    CHECK (capsule_github_access IN (0, 1));
