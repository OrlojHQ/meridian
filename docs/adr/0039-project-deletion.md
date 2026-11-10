# ADR 0039: Delete a Project by cascade

- Status: Accepted
- Date: 2026-11-10

## Context

Projects could not be deleted. Capsules and threads could, so a Project created
by mistake stayed in the sidebar until the operator moved the whole data
directory aside. That also discarded the installation API token, encryption
keys, and every stored secret.

A Project owns Capsules, threads, transcripts, Moments, a preparation
environment, and the names of installation secrets it is allowed to use. Those
dependents have different retention rules today: Capsule delete keeps encrypted
Thread history, Thread crypto-shred drops the wrapped data key and keeps
ciphertext, and Moment rows are immutable so their blobs stay reachable.

## Decision

`POST /projects/{projectId}/delete` deletes the Project and what it owns. The
request matches the other lifecycle mutations: an `Idempotency-Key` and the
Project's expected resource version. The same key returns the accepted
outcome. A later delete, including one after a restart, resumes unfinished
cleanup.

Capsules are deleted through the existing Capsule delete path. The reconciler
asks the provider to remove each owned container and volume. A sealed Capsule
can enter `Deleting` only on this path; the public Capsule delete API still
rejects a terminal Capsule. An active Run or Thread session rejects the whole
Project delete and leaves every Capsule unchanged. The operator cancels that
session first.

When every owned container is gone, Project deletion removes:

- Thread rows, wrapped data keys, and transcript ciphertext;
- Moment rows for the Project and content-addressed blobs no remaining Moment
  references;
- the Project preparation policy, setup arguments, provider-connection grants,
  and harness-setup selections;
- named secrets that no other live Project still names, including ciphertext,
  nonce, and envelope metadata.

A named secret that another live Project still names is kept. Installation
provider connections and personal harness setups are not Project-scoped; only
this Project's grant or selection is removed.

If the provider cannot remove an owned container, the Project stays in `failed`
with that reason. Capsules that were removed stay removed. Nothing is left
running without a record. Deleting again resumes at the containers that remain,
then finishes the content purge. A Project with nothing left to remove becomes
`deleted` immediately. `deleted` is a tombstone: list hides it, get still
returns it, and another delete succeeds.

The CLI, web UI, and TUI require a strong confirmation. Each one shows how many
Capsules, threads, and secrets will be removed. The CLI accepts `--yes` or the
Project name typed at the prompt. The web UI and TUI require the Project name.
After the web UI's delete is accepted, it replaces the current history entry
with the next Project (its latest Capsule, or the Project page when it has
none) or the Inbox.

This does not erase backups, filesystem snapshots, SQLite free pages, swap, or
other media. Those copies follow the installation retention policy.

## Consequences

Transcripts and artifacts are not retained with a deleted Project. A single
Thread crypto-shred still retains ciphertext; only Project deletion removes it.
Operators who need a copy take a backup first. `meridian admin cas gc` remains
the way to remove blobs that a crash leaves unreferenced after the database
commit and before the file removal.
