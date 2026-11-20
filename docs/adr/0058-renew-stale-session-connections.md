# ADR 0058: Renew a structured session's provider connection before a prompt

- Status: Accepted; implemented in v0.14.0
- Date: 2026-11-19
- Amends: ADR 0037 (local provider gateway)
- Extends: ADRs 0024 and 0027 (provider connections), ADR 0025 (Thread
  attention and live refresh), ADR 0044 (stopped runtimes)

## Context

A structured Thread's harness process lives as long as its Run, which stays
`Running` between turns (ADR 0025's amendment). Its provider connection is
fixed when the process starts: `capsuled` puts the gateway URL, the lease
token, and the trust bundle into the harness environment, and nothing
updates them afterward.

All three go stale while the process lives on:

- A lease expires 24 hours after it was issued.
- Leases, the gateway CA, and the local gateway's port live only in
  `meridiand` memory (ADR 0037). After a daemon restart, such as quitting and
  reopening the desktop app, the old port has no listener, the old CA signs
  nothing, and the old token is unknown.
- Revoking a connection cancels its leases, and the harness keeps the dead
  token.

ADR 0037 noted that "leases die with the daemon, so a new port after a
restart changes nothing for Capsules". That holds only if something then
restarts the harness, and nothing did. A Thread the operator came back to
the next day, or after reopening the app, took the prompt and showed
Working. Claude Code then retried for minutes against a refused connection,
with no sign of it in the Thread. The only way out was a manual stop and
resume, or a new Capsule.

Persisting the CA, the leases, and the port would keep old processes working
across restarts. It would also undo ADR 0037's choices: the CA would become a
long-lived key on disk, and leases would outlive the daemon that vouches for
them. Expiry and revocation would still strand a process.

## Decision

### Renew before a prompt is appended

Before a prompt is appended to a structured Thread, from the operator, a
routine, coordination, or a rework request, `meridiand` checks whether the
session's harness can still reach the gateway. The session is **stale** when
the daemon has a provider gateway, the current Run is `Running`, and either:

- this daemon process started the Run with a lease, and the lease has less
  than two hours left, has expired, or was revoked; or
- this daemon process did not start the Run (an earlier process did) and
  the Project grants that harness a provider connection.

A Run this process started without a connection is never stale. What the
daemon knows about each Run's lease is a SHA-256 digest of the token, kept in
memory and dropped when the Run ends. The token itself is not kept beyond
what the gateway already holds.

A stale session is renewed only when nothing would be lost:

- the Thread's turn state (ADR 0025) is idle and no permission or input
  request is waiting;
- the transcript holds harness resume state, so the new process continues
  the same harness conversation instead of starting an empty one;
- the request is not a replay of one already applied, and the caller's
  expected Thread version is current.

Otherwise the prompt goes to the existing session as before. A turn that is
already running is never interrupted to renew it.

### Renewal is a stop and resume the daemon does itself

Renewal is the same stop and resume an operator can do by hand, run in one
call:

1. Pending frames are ingested, then the Thread's sync lock is held for the
   rest of the renewal.
2. The current Run moves to `Cancelling` and the runtime is asked to cancel
   it, bounded by the usual 5-second stop wait. When the runtime confirms the
   stop, the Run becomes `Cancelled`.
3. A new Run starts with a `resume` frame carrying the latest resume state.
   `runtimeProviderConnection` issues it a new lease with the current trust
   bundle and gateway URL, as for any session start.
4. The prompt is appended to and delivered on the new Run.

The old Run's own `end` frame is not ingested, because the Thread moves to
the new Run before the sync lock is released. The transcript therefore shows
no stop the operator did not ask for. The `run.cancelling` and
`run.cancelled` events record the change.

If the runtime has stopped, renewal returns that error and runtime recovery
handles it (ADR 0044). If the old harness does not confirm its stop, its Run
is force-ended as a manual stop would be, and the request fails with the
harness-unresponsive error. A second process resuming the same harness
session could fork it, so renewal does not start one while the first may
still be alive. In both cases the Thread is left as a stopped session that
the operator can resume.

### Harness retries are visible

The Claude Code adapter forwards Claude Code's `system/api_retry` records as
`error` frames with code `provider_retry` and `retryable` set. The summary
holds only Claude Code's fixed error category, the HTTP status, and the
attempt counts, never provider response text. The browser shows a run of
retries as a single notice that updates with each attempt. A retry does not
end the turn. A retry can still happen mid-turn, for example when the daemon
restarts during a turn. In that case the operator now sees why the turn is
stalled, and the next prompt after the turn ends renews the session.

This needs the new adapter, which ships in the Capsule image. Capsules
created from older images get renewal, which is daemon-side, but not the
retry notice.

## Consequences

- Coming back to a Thread after a day, or after the daemon restarted, works
  without a manual stop, resume, or new Capsule. The first prompt waits
  for a harness restart, a few seconds for Claude Code.
- The renewed harness is a new process. Anything that lived only inside the
  old process, such as a background shell Claude Code started, is gone. The
  harness conversation, workspace, and Capsule are kept.
- No security property of ADR 0037 changes. Leases still last at most 24
  hours and are never persisted. The CA still exists only in daemon memory
  and is not written to disk. The gateway port is still random. Each renewal
  issues one more lease, which is counted against the existing per-gateway
  lease cap, and the superseded lease expires on its own schedule.
- A session renewed because its connection was revoked gets the Project's
  current grant. Without one it starts with no connection, as any new session
  would. If the revoked connection is still granted, the start fails and the
  Thread is left stopped, as before.
- A turn longer than the renewal margin can still outlive its lease. That
  turn's harness reports retries, and the next prompt renews the session.
