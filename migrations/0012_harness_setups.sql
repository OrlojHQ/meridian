CREATE TABLE harness_setups (
 id TEXT PRIMARY KEY,
 harness TEXT NOT NULL,
 metadata TEXT NOT NULL
);
CREATE TABLE harness_setup_revisions (
 id TEXT PRIMARY KEY,
 setup_id TEXT NOT NULL REFERENCES harness_setups(id),
 metadata TEXT NOT NULL,
 key_id TEXT NOT NULL,
 nonce BLOB NOT NULL,
 ciphertext BLOB NOT NULL
);
CREATE TABLE capsule_harness_setups (
 capsule_id TEXT NOT NULL REFERENCES capsules(id),
 harness TEXT NOT NULL,
 revision_id TEXT NOT NULL REFERENCES harness_setup_revisions(id),
 PRIMARY KEY (capsule_id, harness)
);
CREATE TABLE provider_connections (
 id TEXT PRIMARY KEY,
 metadata TEXT NOT NULL,
 key_id TEXT NOT NULL,
 nonce BLOB NOT NULL,
 ciphertext BLOB NOT NULL
);
CREATE TABLE project_provider_connections (
 project_id TEXT NOT NULL REFERENCES projects(id),
 harness TEXT NOT NULL,
 connection_id TEXT NOT NULL REFERENCES provider_connections(id),
 PRIMARY KEY(project_id,harness)
);
CREATE TABLE project_harness_setups (
 project_id TEXT NOT NULL REFERENCES projects(id),
 harness TEXT NOT NULL,
 setup_id TEXT NOT NULL,
 PRIMARY KEY(project_id,harness)
);
