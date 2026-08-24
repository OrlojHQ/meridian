# ADR 0013: Supervise structured adapters through a bounded private protocol

- Status: Accepted
- Date: 2026-08-24

## Context

Structured Thread presentation needs typed harness events without making
Meridian an agent loop or parsing terminal output. Repository-selected adapters,
their output, and their child processes are hostile. PTY attachment already has
different framing, lifecycle, and input semantics and must remain compatible.

## Decision

- Keep native/PTY execution unchanged. A v1 harness profile opts into
  `structured` interaction explicitly, is non-PTY, and launches one adapter
  executable directly with an argument array in a validated workspace
  directory.
- Use the closed `meridian.adapter.v1` LF-delimited JSON union. Frames negotiate
  capabilities and cover start/resume, messages, tool summaries, status,
  errors, permission/input round trips, heartbeat, cancellation,
  acknowledgement, cursors, gaps, and end. Unknown frame types, fields,
  versions, CRLF records, unterminated records, and oversized values fail
  closed.
- `capsuled` owns the adapter process group, timeout, serialized stdin writer,
  incremental decoder, cancellation, and bounded in-memory replay ring.
  Monotonic supervisor cursors, explicit replay gaps, and stateless polling
  allow disconnect and reconnect without tying process lifetime to a daemon
  request.

Harness-specific drivers retain ownership of upstream session, model, tool,
permission, and compaction semantics. Meridian does not implement an agent
loop.
- Use dedicated authenticated, versioned private Capsule HTTP endpoints. Do not
  overload the PTY WebSocket or expose these endpoints through the public API.
- Expose structured execution as an optional runtime port. Docker uses its
  protected loopback binding and Agent Sandbox uses its owned pod
  port-forward. The fake provider remains unsupported and providers advertise
  the capability only with end-to-end transport.
- Preserve complete typed frames only in the transient structured runtime
  return path. Existing durable Run lifecycle events contain type and cursor
  metadata only; transcript persistence remains the Thread layer's encrypted
  responsibility, including bounded opaque frame metadata inside ciphertext.

## Consequences

Adapters can retain all model, tool, permission, and compaction behavior while
Meridian provides bounded transport and reconnection. Slow consumers cannot
block adapter stdout indefinitely because replay is decoupled and bounded;
falling behind produces a visible gap. A `capsuled` restart still loses its
in-memory child registry, as in ADR 0007, and status then reports the session as
missing rather than claiming process continuity. The Thread service reconciles
that missing runtime as a failed terminal session without re-sending durable
messages automatically. A client must retry the original idempotent mutation;
the Thread layer's durable content-free delivery acknowledgement then sends one
pending frame or returns the already acknowledged result. Meridian transports
these frames and does not implement, infer, or recover an agent loop.
