ALTER TABLE projects ADD COLUMN git_push_secret_name TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN github_api_secret_name TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN commit_author_name TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN commit_author_email TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN default_base_branch TEXT NOT NULL DEFAULT '';

CREATE TABLE deliveries (
    id TEXT PRIMARY KEY,
    capsule_id TEXT NOT NULL REFERENCES capsules(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    state TEXT NOT NULL CHECK (
        state IN ('queued', 'committing', 'pushing', 'opening_pr', 'succeeded', 'failed')
    ),
    action TEXT NOT NULL CHECK (action IN ('push', 'open_pull_request')),
    approved INTEGER NOT NULL CHECK (approved = 1),
    approved_at TEXT NOT NULL,
    expected_capsule_version INTEGER NOT NULL CHECK (expected_capsule_version > 0),
    expected_head TEXT NOT NULL CHECK (length(expected_head) BETWEEN 40 AND 128),
    expected_tree TEXT NOT NULL CHECK (length(expected_tree) BETWEEN 40 AND 128),
    remote_branch TEXT NOT NULL CHECK (length(remote_branch) BETWEEN 1 AND 255),
    destination_ref TEXT NOT NULL CHECK (
        destination_ref = 'refs/heads/' || remote_branch
    ),
    base_branch TEXT NOT NULL DEFAULT '' CHECK (length(base_branch) <= 255),
    commit_message TEXT NOT NULL DEFAULT '' CHECK (length(commit_message) <= 16384),
    pr_title TEXT NOT NULL DEFAULT '' CHECK (length(pr_title) <= 512),
    pr_body TEXT NOT NULL DEFAULT '' CHECK (length(pr_body) <= 65536),
    result_commit_sha TEXT NOT NULL DEFAULT '' CHECK (length(result_commit_sha) <= 128),
    result_pr_url TEXT NOT NULL DEFAULT '' CHECK (length(result_pr_url) <= 4096),
    result_pr_number INTEGER NOT NULL DEFAULT 0 CHECK (result_pr_number >= 0),
    failure TEXT NOT NULL DEFAULT '' CHECK (length(failure) <= 512),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resource_version INTEGER NOT NULL CHECK (resource_version > 0),
    UNIQUE (capsule_id, idempotency_key),
    CHECK (
        (state = 'failed' AND failure <> '') OR
        (state <> 'failed' AND failure = '')
    ),
    CHECK (
        action = 'open_pull_request' OR
        (pr_title = '' AND pr_body = '' AND result_pr_url = '' AND result_pr_number = 0)
    )
);

CREATE INDEX deliveries_capsule_created
    ON deliveries(capsule_id, created_at, id);
CREATE INDEX deliveries_recovery
    ON deliveries(state, updated_at, id)
    WHERE state NOT IN ('succeeded', 'failed');

CREATE TRIGGER deliveries_lifecycle
BEFORE UPDATE OF state ON deliveries
WHEN NOT (
    OLD.state = NEW.state OR
    (OLD.state = 'queued' AND NEW.state IN ('committing', 'pushing', 'failed')) OR
    (OLD.state = 'committing' AND NEW.state IN ('pushing', 'failed')) OR
    (OLD.state = 'pushing' AND NEW.state IN ('opening_pr', 'succeeded', 'failed')) OR
    (OLD.state = 'opening_pr' AND NEW.state IN ('succeeded', 'failed'))
)
BEGIN
    SELECT RAISE(ABORT, 'invalid Delivery lifecycle transition');
END;

CREATE TRIGGER deliveries_identity_immutable
BEFORE UPDATE OF id, capsule_id, project_id, action, approved, approved_at,
    expected_capsule_version, expected_head, expected_tree, remote_branch,
    destination_ref, base_branch, commit_message, pr_title, pr_body,
    idempotency_key, created_at
ON deliveries
BEGIN
    SELECT RAISE(ABORT, 'Delivery identity is immutable');
END;

CREATE TRIGGER deliveries_no_delete
BEFORE DELETE ON deliveries
BEGIN
    SELECT RAISE(ABORT, 'Deliveries are durable');
END;
