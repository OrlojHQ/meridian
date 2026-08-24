# ADR 0010: Kubernetes Agent Sandbox provider and CSI capability boundary

## Status

Accepted.

## Context

Meridian needs a Kubernetes lifecycle adapter without widening its portable
`CapsuleProvider` and `CapsuleRuntime` ports or exposing Kubernetes objects in
the public API. The authoritative Agent Sandbox v0.5.6 API, released
2026-08-20, serves `Sandbox` at `agents.x-k8s.io/v1beta1`. Its direct resource
uses `spec.podTemplate`, `spec.volumeClaimTemplates`, `spec.operatingMode`, and
the inline `shutdownTime` and `shutdownPolicy` lifecycle fields. It does not
define a class or `templateRef` field on core `Sandbox`.

Meridian Moment v1 is a portable deterministic workspace archive plus an
immutable manifest and SHA-256 artifact identity. A CSI `VolumeSnapshot` is a
provider-local storage object with different durability, portability, deletion,
and restore semantics. Mapping one to the other without changing the domain
would create false portability and backup claims.

## Decision

- `internal/provider/agentsandbox` creates direct v1beta1 `Sandbox` resources.
  Names derive from the Capsule ID hash. Full identity is an annotation and a
  collision-resistant identity hash is a label.
- Adoption, mutation, port-forwarding, and deletion require all Meridian
  ownership markers and the deterministic name to agree. Deletion uses the
  observed UID as a precondition and foreground propagation.
- `Running` and `Suspended` map to `spec.operatingMode`. Readiness comes only
  from the upstream `Ready` condition; suspension completion comes from
  `Suspended=True`. Stale Suspended conditions after resume are ignored as
  required by the upstream API comments.
- Workspace persistence is one `ReadWriteOnce` PVC template mounted at
  `/workspace`. TTL uses an absolute `shutdownTime` and `shutdownPolicy=Delete`.
- The Capsule image runs `capsuled` as UID/GID 10001 with restricted pod and
  container security contexts. The protocol listens on pod loopback only.
- A high-entropy token is held in one immutable namespaced Secret, mounted
  read-only with mode 0400 requested (0440 when Kubernetes applies the Pod
  `fsGroup` to the root-owned projection), and owner-referenced to the Sandbox.
  It is never placed in labels, annotations, SQLite, logs, command arguments,
  or public responses. Kubernetes administrators, node agents, and principals
  with Secret read access can still recover it.
- Native Kubernetes port-forward is an authenticated, bounded, per-operation
  transport. Attach owns the tunnel until attachment close. No Service,
  Ingress, public preview, or direct pod-IP routing is created.
- `class` is Meridian policy metadata only. The reserved template option fails
  explicitly because core v1beta1 has no `templateRef`; Meridian does not write
  internal Agent Sandbox provenance annotations.
- CSI discovery checks the v1 VolumeSnapshot API, configured StorageClass, and
  available VolumeSnapshotClasses. Snapshot and Clone nevertheless remain
  false because Moment v1 cannot represent provider-native CSI artifacts.
  Snapshot calls return the typed unsupported error. There is no tar fallback
  advertised as CSI. Docker filesystem Moments are unchanged.

## Consequences

The provider supports lifecycle, suspend/resume, Run, Git, and PTY attachment.
Preview, Snapshot, and Clone are explicitly unavailable. Supporting CSI-backed
Moments later requires a versioned provider-native Moment reference, lineage
and restore APIs, retention/garbage-collection rules, and backup semantics
before capabilities may become true.

Agent Sandbox orchestrates workload lifecycle; it is not an isolation boundary.
Untrusted or multi-tenant workloads require a separately installed and
validated RuntimeClass such as gVisor or Kata, appropriate node/storage/network
controls, and independent security review.
