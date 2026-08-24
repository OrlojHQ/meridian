# Filesystem Moments and lineage

A Moment is an immutable, deterministic tar snapshot of `/workspace` plus a
small canonical JSON manifest. It never contains RAM, running processes, open
sockets, PTY history, prompts, or terminal state. Restored software must restart
its own processes.

Each Capsule owns exactly one Timeline. Initial Capsule creation makes a root
Timeline transactionally. `shard create` and `rewind` both create a new Capsule
and a new descendant Timeline whose `forkedFromMomentId` records the source and
whose reason is `shard` or `rewind`. There is deliberately no Shard entity.
Rewind does not alter or delete the source Timeline or any later Moment.

`seal` captures a final Moment and moves the source Capsule to the terminal
`Sealed` state. A sealed Capsule cannot start Runs, capture another Moment,
pause, resume, delete, or otherwise mutate. Replaying the same seal
idempotency key returns the original result.

## Local artifact storage

Archive and manifest blobs are content-addressed by lowercase SHA-256 beneath
the daemon data directory. Publication streams through a same-filesystem
mode-0600 temporary file, verifies existing deduplicated blobs, fsyncs, and
publishes atomically. SQLite records a Moment only after both blobs are
published. A crash can therefore leave an unreachable complete blob, but never
a visible Moment pointing to a partial blob. Automatic retention and garbage
collection are not implemented yet.

The local store uses mode-0700 directories and is intended for one trusted OS
user. Moments inherit the confidentiality of every repository file they
capture. Repository files can contain credentials even though the manifest
deliberately excludes credential values, prompts, PTY data, full diffs,
supervisor tokens, and host paths. Protect, encrypt, back up, and delete the
data directory according to the source repository's sensitivity.

## Archive and portability limits

`capsuled` captures regular files, directories, executable bits, and
workspace-contained relative symlinks without following symlinks. It rejects
special files, hard links, traversal, absolute paths, symlink escapes,
duplicates, xattrs, and bounded-size violations. Restore verifies the expected
archive digest and validates the complete expansion before replacing existing
workspace contents.

The Docker adapter transports snapshots only through its authenticated,
loopback-only `capsuled` protocol. Capsules never receive the Docker socket,
host paths, or additional privilege. Descendants use the Moment's immutable
image identity. Filesystem semantics can still vary by provider and host
(permissions, Unicode normalization, case sensitivity, and maximum file
sizes), so Moments are not claimed to be universally portable across provider
classes.
