# ADR 0025: Refresh live runtime state and surface content-free Thread attention

- Status: Accepted
- Date: 2026-09-27

## Context

Run state and structured adapter frames reach durable storage only when a
client reads them: `GetRun` consults the Capsule runtime and Thread block reads
ingest pending frames. Listing Runs returns stored state. An operator watching
the working set therefore sees an exited agent as still running, and cannot
see that an unwatched structured session is blocked on a permission or input
request until opening that Thread.

The request itself exists only inside the encrypted transcript. ADR 0012 keeps
bounded adapter metadata out of plaintext Thread metadata and lifecycle
events, so a persisted "awaiting" column would add new plaintext at rest.

## Decision

- `meridiand` runs a background refresh every `--runtime-refresh-interval`
  (default 5s; 0 disables). Each pass reconciles at most 100 started,
  non-terminal Runs whose Capsule is Ready, desired Ready, and not in
  maintenance. Native Runs apply the runtime's state through the existing
  `GetRun` path. Structured Runs share startup recovery's reconciliation:
  apply `GetStructured` state and ingest pending frames into the encrypted
  Thread transcript.
- The refresh never starts, resends, resumes, or cancels work, never wakes a
  paused Capsule, and never captures Moments. Queued Runs remain owned by
  startup recovery and explicit idempotent retries. It uses only the existing
  authenticated `capsuled` transport; no new Capsule-facing surface exists.
- Capsule activity is recorded only when ingestion persists agent output.
  Heartbeats, cursor advances, and replay gaps do not count, preserving ADR
  0019's rule that reconciliation polls and passive reads do not keep a
  Capsule active.
- Thread responses gain an optional content-free `awaiting` object:
  `kind` (`permission` or `input`) and `since`. The daemon derives it in
  memory, only for an active Thread whose current Run is non-terminal, by
  decrypting at most the trailing 256 messages. Each response answers the most
  recent outstanding request of its kind, matching the browser Thread view.
  The result is cached in memory by message count and is never persisted,
  logged, or emitted in events, metrics, or traces. It carries no summary,
  options, prompt, or tool text.
- The browser working set reads only Capsule, Run, and Thread summaries. It
  never polls Git status, workspace files, or previews, which record Capsule
  activity, and never fetches transcripts to compute status.

## Consequences

The working set reflects agents nobody is watching, including exited native
Runs and structured sessions waiting on the operator. Structured transcripts
are persisted continuously while a session runs, which reduces replay gaps
when no client is attached. Ciphertext therefore accumulates for unwatched
Threads the operator started, but no new category of content is stored.

Authenticated API readers learn that a request is pending and since when. That
is within ADR 0012's decryption boundary, which already includes the daemon and
the trusted user. SQLite and backup exposure are unchanged because nothing
new is written in plaintext.

Each refresh pass costs one runtime status request per live Run plus frame
ingestion. Awaiting derivation decrypts only when a Thread's message count
has changed since the last read. The cache is lost on restart and recomputed
on the next read. A request more than 256 messages old is not reported.
