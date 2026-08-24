# ADR 0004: Make Moments filesystem-only and lineage non-destructive

- Status: Accepted
- Date: 2026-08-23

## Context

Users need reliable checkpoints without ambiguous promises about running processes or destructive restoration. Provider snapshot capabilities differ, so product semantics cannot depend on preserving machine memory.

## Decision

A Moment captures filesystem state only. It does not promise process memory, open sockets, terminal state, external services, or in-flight I/O. Capture must quiesce or document application-consistency limits.

Moments are immutable. Rewind never mutates or deletes the source Timeline: it creates a new writable descendant from the selected Moment. Shards likewise create independently writable descendants with explicit parent identifiers. Timeline lineage is append-only metadata; retention may remove unreachable physical data only under an explicit policy while preserving required audit records.

Each Capsule belongs to exactly one Timeline. Shard is an operation, not a
stored domain entity: both Shard and Rewind create a new Capsule and Timeline,
record the source Moment, and distinguish intent with a `shard` or `rewind`
reason. Seal captures one final Moment and makes every later source-Capsule
mutation invalid.

## Consequences

Resume hooks must restart processes after restoration. Users can explore or recover without silently losing later work. Providers may optimize storage through copy-on-write snapshots, but all providers must preserve the same filesystem and lineage semantics.
