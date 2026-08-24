# ADR 0019: Pause idle Capsules and reuse setup Moments

- Status: Accepted
- Date: 2026-08-24

## Context

Thread-first sessions always receive a fresh Capsule, but keeping inactive
compute running wastes local or cluster capacity and repeating deterministic
clone and setup work increases startup latency. Runtime idleness, provider
lifetime, artifact retention, and prepared-workspace reuse are different
clocks and must not be collapsed into one TTL.

## Decision

- Maintain a durable Capsule activity clock from authenticated user and
  session use: Thread mutations, Run or adapter progress, PTY traffic, preview
  traffic, sync, file access, and delivery activity. Health probes,
  reconciliation polls, and passive status reads do not keep a Capsule active.
- After the configured idle interval, reconcile a quiescent Capsule to the
  provider's pause or suspend operation. Active setup, archive transfer, Run,
  delivery, PTY, or preview work blocks pausing. Providers without the required
  pause semantics fail that policy explicitly rather than simulating it by
  destroying the workspace.
- Wake a paused Capsule on an authenticated operation that needs its runtime or
  filesystem. Resume is asynchronous and idempotent; the requested operation
  waits for readiness or returns observable pending state instead of bypassing
  the provider boundary.
- After clone and successful setup, capture an explicitly preparation-only
  Meridian Moment eligible for reuse. A fresh Capsule may restore that Moment
  and still remains a new Capsule with new runtime credentials, Thread, and
  mutable lineage.
- Key setup reuse by a versioned prepare hash over every input that can affect
  prepared bytes, including canonical repository identity and exact source
  revision, immutable image identity, setup argument array, relevant Project
  configuration, platform, and preparation-protocol version. A mismatch or
  unverifiable input runs preparation normally.
- Never include broker grants, resolved harness secrets, runtime tokens,
  transcripts, local overlays, session state, or post-setup user work in a
  reusable setup Moment. Verify the archive and manifest before every restore.
- Keep separate policy values and audit reasons for idle pause, absolute
  provider or Capsule lifetime, setup-cache retention, ordinary Moment
  retention, and ticket or credential expiry. Pause does not renew or defeat an
  absolute deletion TTL; cache expiry does not delete a live Capsule or a
  user-created Moment.

## Consequences

Idle compute can stop while durable workspace state remains available, and
fresh Thread startup can skip deterministic preparation without sharing a
mutable session. Waking adds latency and active tunnels terminate on pause.
Repository setup that depends on undeclared time, network, mutable package
indexes, or external state may produce a technically matching but stale cache,
so policy may disable reuse and cache identity must remain visible.

This ADR defines target behavior; existing provider TTL and preparation-marker
behavior do not by themselves implement the activity clock or setup-Moment
cache.
