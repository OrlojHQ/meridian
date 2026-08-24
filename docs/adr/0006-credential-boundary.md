# ADR 0006: Keep reusable credentials outside Capsules

- Status: Accepted
- Date: 2026-08-23

## Context

Repositories, setup scripts, dependencies, agents, and tools inside a Capsule may be hostile. Copying long-lived credentials into that environment makes compromise durable and difficult to contain.

## Decision

The control-plane credential broker is a security boundary. It obtains or derives narrowly scoped, short-lived credentials for an authenticated Capsule and operation. Capsules receive only the minimum temporary material needed, preferably through workload identity or an authenticated broker operation.

Meridian must not persist credential plaintext in its database, artifact store, snapshots, logs, events, command history, or generated configuration. Stored secrets, if unavoidable, must use an external secret manager or envelope encryption with keys outside the database and artifacts. Delivery operations with broader effects require explicit authorization and, where configured, human approval. Redaction is defense in depth, not the primary control.

## Consequences

Integrations must support expiration, revocation, scope, and audit. Some developer tools may need adapters rather than static environment variables. Snapshot and diagnostic workflows must be tested to ensure credential material is excluded.
