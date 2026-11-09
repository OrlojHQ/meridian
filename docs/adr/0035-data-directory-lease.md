# ADR 0035: Hold the data directory with a lease

- Status: Accepted
- Date: 2026-11-09
- Extends: ADRs 0003 and 0011

## Context

The control plane keeps its state in one SQLite database in WAL mode
(ADR 0003). SQLite keeps several processes consistent only while they share
POSIX advisory locks and the WAL's shared-memory index. Operations already
require one control-plane replica, and backup, restore, transcript-key
rotation, upgrade, and CAS deletion require a stopped control plane
(ADR 0011), but nothing enforced either rule.

A development setup showed the cost. The Compose stack bind-mounts
`.local/meridian-data` into a `meridiand` container. A second `meridiand`
started on the macOS host with the same directory. Neither POSIX locks nor
the shared-memory index cross Docker Desktop's VM boundary, so both daemons
wrote to the database as if alone, and it became malformed: transcripts,
most Threads, and several Capsule rows were lost. The same happens to an
operator who starts a new daemon before the old one stops, or points two
installations at one volume.

## Decision

A new `internal/datadir` package holds a data directory for one owner at a
time, with two mechanisms:

- an exclusive, non-blocking `flock` on `meridiand.lock`, which excludes a
  second owner on the same kernel; and
- a lease file, `meridiand.lease`, naming the owner (description, PID, host,
  random instance ID, start time) with a heartbeat refreshed every 5 seconds.
  File contents do cross bind mounts, so this excludes an owner on the other
  side of a VM boundary.

Acquiring fails with "data directory is in use" when the flock is held, or
when the lease names an owner on another host whose heartbeat is younger than
30 seconds. A heartbeat from the future also counts as live. The error names
the owner and the lease path, so an operator can stop it, wait for expiry, or
remove a lease that will not expire. When the flock is free and the lease
names this host, its owner has exited (the kernel drops a process's locks), so
a crashed daemon restarts at once. The lease is written by atomic rename and
confirmed after a 250 ms settle, so two owners starting at the same moment on
either side of a VM boundary do not both proceed.

The holder rereads the lease on every heartbeat. If another owner has
replaced it, or it could not be refreshed for half the expiry, the holder
reports the lease lost. `meridiand` takes the lease before it writes anything
to the data directory, releases it on shutdown, and exits immediately when it
is lost, without a graceful drain, so it writes nothing more.

The offline maintenance operations that mutate an installation hold the same
lease for their whole run: backup creation, transcript-key rotation, restore
over an existing installation (whose lease files move aside with it), and CAS
deletion. They therefore refuse to run beside a daemon or each other.

The lease file is operational metadata, mode 0600, and holds no secret.

## Consequences

A second daemon or a maintenance command on a directory in use fails at once
with an actionable error instead of corrupting the database.

A daemon on another host that stops without releasing its lease blocks the
directory for up to 30 seconds. A daemon whose host is suspended for longer
than that may find its lease taken on resume; it then exits.

A hostname that two machines share defeats the cross-host check, because each
would treat the other's lease as its own crashed predecessor once it holds its
local flock. Distinct hostnames are assumed.

The lease does not make a network filesystem safe for SQLite. The data
directory still belongs on local storage or a single-writer volume.
