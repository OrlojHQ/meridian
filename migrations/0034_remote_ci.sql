-- Remote CI on a Meridian pull request (ADR 0053).
--
-- One watch is one commit Meridian pushed. The checks are names,
-- conclusions, links, and an encrypted tail. This migration stores no token
-- and no command output from the Capsule.

CREATE TABLE remote_ci_watches (
    id TEXT PRIMARY KEY,
    capsule_id TEXT NOT NULL REFERENCES capsules(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL DEFAULT '' REFERENCES users(id),
    delivery_id TEXT NOT NULL DEFAULT '',
    repository_owner TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    pull_number INTEGER NOT NULL CHECK (pull_number > 0),
    pull_url TEXT NOT NULL DEFAULT '' CHECK (length(pull_url) <= 2048),
    git_head TEXT NOT NULL CHECK (length(git_head) <= 128),
    git_tree TEXT NOT NULL DEFAULT '' CHECK (length(git_tree) <= 128),
    state TEXT NOT NULL CHECK (state IN (
        'waiting', 'passed', 'failing', 'reconnect', 'connect', 'unreadable', 'absent'
    )),
    epoch INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt BETWEEN 0 AND 10),
    counted INTEGER NOT NULL DEFAULT 0 CHECK (counted IN (0, 1)),
    passed_count INTEGER NOT NULL DEFAULT 0 CHECK (passed_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    pending_count INTEGER NOT NULL DEFAULT 0 CHECK (pending_count >= 0),
    poll_count INTEGER NOT NULL DEFAULT 0 CHECK (poll_count >= 0),
    next_poll_at TEXT NOT NULL DEFAULT '',
    rework_sent INTEGER NOT NULL DEFAULT 0 CHECK (rework_sent IN (0, 1)),
    push_review INTEGER NOT NULL DEFAULT 0 CHECK (push_review IN (0, 1)),
    superseded INTEGER NOT NULL DEFAULT 0 CHECK (superseded IN (0, 1)),
    error_code TEXT NOT NULL DEFAULT '' CHECK (length(error_code) <= 64),
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX remote_ci_watches_head ON remote_ci_watches(capsule_id, git_head);
CREATE INDEX remote_ci_watches_due ON remote_ci_watches(state, next_poll_at);

CREATE TRIGGER remote_ci_watches_owner_insert
BEFORE INSERT ON remote_ci_watches
WHEN EXISTS (SELECT 1 FROM capsules WHERE id = NEW.capsule_id)
    AND NEW.owner_user_id IS NOT (SELECT owner_user_id FROM capsules WHERE id = NEW.capsule_id)
BEGIN
    SELECT RAISE(ABORT, 'owner violation');
END;

CREATE TRIGGER remote_ci_watches_owner_immutable
BEFORE UPDATE OF owner_user_id ON remote_ci_watches
WHEN NEW.owner_user_id IS NOT OLD.owner_user_id
BEGIN
    SELECT RAISE(ABORT, 'owner immutable');
END;

CREATE TABLE remote_ci_checks (
    watch_id TEXT NOT NULL REFERENCES remote_ci_watches(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0 AND position < 100),
    name TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('check_run', 'status')),
    conclusion TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '' CHECK (length(url) <= 2048),
    output_truncated INTEGER NOT NULL DEFAULT 0 CHECK (output_truncated IN (0, 1)),
    output_nonce BLOB,
    output_ciphertext BLOB,
    PRIMARY KEY (watch_id, position)
);
