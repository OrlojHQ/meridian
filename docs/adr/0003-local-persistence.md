# ADR 0003: Use SQLite WAL and local artifacts first

- Status: Accepted
- Date: 2026-08-23

## Context

The first deployment target is a trusted single-user installation. It needs durable metadata and artifacts without introducing an external database or object store before their operational cost is justified.

## Decision

The initial control plane will store relational metadata in SQLite with write-ahead logging enabled. Large or opaque artifacts belong in a configured local artifact directory, not in SQLite blobs. Database and artifact paths must be explicit, relocatable, permission-restricted, and included together in backup and restore procedures.

Code must preserve a storage boundary so a later multi-node deployment can adopt a network database and object storage without changing domain semantics. SQLite is not a coordination mechanism across multiple control-plane processes.

## Consequences

Single-node installation and backup remain simple. WAL improves read/write concurrency but creates `-wal` and `-shm` files that backup and cleanup logic must handle. Local disk capacity, filesystem durability, and co-location constrain availability and scale.
