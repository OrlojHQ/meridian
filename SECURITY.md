# Security policy

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Use GitHub's private vulnerability reporting for this repository. If that option is unavailable, contact the OrlojHQ maintainers privately through an address listed on the organization profile.

Include affected versions or commits, reproduction steps, impact, and any suggested mitigation. Do not include live credentials or unnecessarily access other users' data. Maintainers will acknowledge a complete report as soon as practical and coordinate validation, remediation, and disclosure.

## Supported versions

Meridian has not published a release and currently provides no supported production version. Security fixes apply to the latest development branch until a version-support policy is announced.

Release configuration creates draft releases only. A published first release
must pass `docs/first-release-checklist.md`; local builds must not claim GitHub
provenance, keyless signing, or publication.

## Current security posture

The Docker Capsule provider is intended only for one trusted operator on a
local development host. The Kubernetes Agent Sandbox provider orchestrates
lifecycle but Agent Sandbox itself is not an isolation boundary. Do not claim
untrusted multi-tenancy without a separately validated hardened runtime such as
gVisor or Kata, dedicated infrastructure controls, and external review.

Kubernetes administrators, node agents, etcd/backup operators, and principals
with namespaced Secret read access can recover Capsule protocol tokens. The
chart creates no public preview/PTY ingress and no cluster-scoped RBAC, but its
NetworkPolicies cannot identify the Kubernetes API server and require an
operator-supplied CIDR. CSI Snapshot/Clone remains explicitly unsupported.

Capsule workloads must never receive Docker/runtime sockets or control-plane
credentials. Harness/setup code is hostile, and PTY output or Git diffs may
expose secrets even when normal records omit their content. Read [the threat
model](docs/threat-model.md), [Kubernetes Agent Sandbox
operations](docs/kubernetes-agentsandbox.md), [harness
guidance](docs/harness-configuration.md), and [Docker development
guidance](docs/docker-development.md) before use.

Prometheus metrics and OTLP traces deliberately omit content-bearing labels and
attributes. Metrics are unauthenticated, disabled by default, and loopback-only
unless an operator explicitly acknowledges unsafe exposure. Local backups
contain SQLite metadata and complete Moment artifacts, which may include source
or secrets from repository files; encrypt and restrict backup media. Runtime
supervisor tokens are excluded from backup and recreated through
ownership-checked recovery. See [release and operations](docs/operations.md).

Explicit structured Threads are a narrow exception to the normal no-prompt
persistence policy. Their typed content blocks are stored only as bounded
AES-256-GCM ciphertext under random per-Thread keys; Runs, PTY traffic, diffs,
and implicit harness prompts remain transient. Thread metadata (including
roles, kinds, timestamps, counts, and adapter identity) is not encrypted.

The installation transcript key is a separate restrictive file, never part of
SQLite or a Meridian backup. Backups with retained Threads are unrecoverable
without a separately protected matching key. Protect, test, and rotate that key
according to [release and operations](docs/operations.md) and [ADR
0012](docs/adr/0012-structured-thread-transcript-encryption.md). Crypto-shred is
logical key destruction, not a storage-media sanitization guarantee.

At-rest encryption does not hide Thread content from the trusted local daemon
or user: `meridiand` holds the key and returns plaintext through explicit
Thread APIs. A host, daemon, browser, or loopback-API compromise can therefore
read active transcripts. Harness adapters retain responsibility for upstream
agent/tool semantics; Meridian provides no model and no agent loop.
