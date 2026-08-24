# ADR 0016: Start project sessions from Thread intent

- Status: Accepted
- Date: 2026-08-24
- Extends: ADRs 0012 and 0013

## Context

The product entry point is a conversation in a Project, not manual assembly of
a long-lived Capsule followed by a Thread. A Project describes durable
repository, image, setup, harness, and policy intent. Runtime provisioning,
private clone, setup, and adapter startup are asynchronous and may be
interrupted after any side effect.

## Decision

- Make `POST /projects/{id}/threads` the target session-creation intent. The
  accepted mutation atomically records the Project-bound Thread, its encrypted
  initial message when supplied, the desired session state, and idempotency
  metadata before runtime side effects.
- Always allocate a fresh Capsule for a new Project Thread. A Project is a
  reusable template, not a shared mutable workspace, and separate Threads do
  not silently share a Capsule.
- Return the durable intent without waiting for provider creation, clone,
  setup, or adapter start. An asynchronous reconciler advances explicit
  provisioning and session states and publishes content-free lifecycle
  progress.
- Make the creation mutation idempotent. Reusing an idempotency key with the
  same canonical request returns the same Thread intent; reuse with different
  input conflicts. Provider and runtime operations carry stable intent
  identities so retries adopt or continue owned work instead of duplicating
  Capsules or sessions.
- Reconcile a missing or interrupted runtime from durable desired state.
  Recovery may create or adopt the Capsule, restore prepared state, clone, run
  setup, and restart a session where semantics allow, but it never
  automatically resends the initial or any later prompt.
- Preserve ADR 0012's content-free delivery acknowledgement. A prompt crosses
  the Capsule boundary only from an explicit client mutation or replay of that
  same idempotent mutation; an acknowledged replay does not send it again.
- Keep the Capsule as the isolation and filesystem owner and the Thread as the
  user-facing session aggregate. Meridian still supervises an external
  harness adapter and does not implement an agent loop.

## Consequences

Users can create a Thread once and observe it become ready while reconciliation
survives daemon or provider interruptions. Fresh Capsules prevent accidental
workspace sharing but increase startup cost; ADR 0019 may reuse a
content-addressed setup Moment without reusing a mutable session.

This ADR defines a future public contract. Existing Capsule-scoped Thread
endpoints remain current behavior until the OpenAPI and implementation are
changed in a separate delivery.
