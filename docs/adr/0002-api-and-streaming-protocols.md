# ADR 0002: Use REST/OpenAPI with explicit streaming protocols

- Status: Accepted
- Date: 2026-08-23

## Context

Lifecycle operations, durable activity, and interactive terminals have different transport needs. One protocol would either underspecify long-lived interaction or make ordinary API operations harder to integrate and generate clients for.

## Decision

- Lifecycle and query operations use versioned HTTP REST endpoints described by an OpenAPI 3.1 contract.
- Durable, ordered activity uses a one-way event stream. The initial transport should be Server-Sent Events unless implementation evidence requires another HTTP streaming format.
- Interactive PTY sessions use WebSocket because they need bidirectional, low-latency byte transport, terminal resize messages, and explicit session closure.
- Event envelopes will carry stable identifiers, event type, sequence or cursor, timestamp, schema version, and payload.
- Generated clients derive from the checked-in OpenAPI contract. Streaming and WebSocket protocols receive separate, versioned documentation because OpenAPI alone does not completely define their runtime semantics.

## Consequences

Ordinary API clients remain easy to generate and inspect. Event consumers can resume from a cursor once that behavior is implemented. PTY concerns do not leak into resource endpoints. Authentication and authorization must be enforced consistently across all three transports.
