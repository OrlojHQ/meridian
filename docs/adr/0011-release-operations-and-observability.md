# ADR 0011: Single-user release operations and content-free observability

- Status: Accepted
- Date: 2026-08-24

## Context

Meridian needs release artifacts, operational recovery, and useful telemetry
without broadening its initial trust model or exporting user-controlled source,
prompts, terminal data, paths, identifiers, or credentials. SQLite and the local
artifact CAS form one durability unit for visible Moments. Provider resources
and runtime credentials have different lifecycle and backup semantics.

## Decision

The first release remains single-user and self-hosted. Docker is trusted-local;
Agent Sandbox requires an independently validated hardened runtime before
hostile workloads or multi-tenancy.

Prometheus metrics use a dedicated listener that is disabled by default and
requires literal loopback unless an explicit unsafe-exposure acknowledgement is
set. Labels are fixed, bounded enumerations. OTLP tracing is disabled by default,
uses fixed span names and content-free attributes, a bounded batch queue, and a
bounded shutdown. Metrics and tracing cover HTTP, lifecycle reconciliation,
provider operations and retry/cleanup, startup recovery, Run transitions and
event gaps, Moment capture, PTY and preview sessions, and artifact retention.

Local backup is an offline operation. It uses SQLite `VACUUM INTO`, copies the
artifact CAS without following symlinks, checksums every file, writes a bounded
manifest, and atomically publishes a new directory. It excludes Docker and
Kubernetes supervisor tokens and other runtime credential state. Restore
verifies before mutation, rejects newer schemas and unsafe paths, and atomically
publishes while preserving an existing installation until success.

Schema migrations are forward-only. Newer schemas fail closed. CAS GC is
offline, dry-run by default, verifies all visible Moment references, and
requires explicit confirmation before deleting only unreachable blobs.

Release artifacts are reproducible CGO-free Linux/macOS binaries where
supported, SHA-256 checksums, SPDX SBOMs, and non-root OCI images. Tag-only CI
creates a draft release, GitHub provenance, and keyless Sigstore signatures.
No local process claims signing or publication success.

## Consequences

Operators must stop the daemon for backup, restore, upgrade, and GC. Database
backup alone does not preserve Docker volumes or Kubernetes PVC/PV state.
Restored provider credentials are recreated through ownership-checked recovery.
Telemetry is deliberately lower-cardinality and less diagnostic than
content-bearing traces. Network-exposed metrics remain an operator-created risk
even with the explicit opt-in because Meridian does not authenticate them.
