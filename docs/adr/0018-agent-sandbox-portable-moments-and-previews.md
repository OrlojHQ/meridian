# ADR 0018: Add portable Agent Sandbox Moments and previews

- Status: Accepted
- Date: 2026-08-24
- Extends: ADRs 0009 and 0010

## Context

Docker already defines portable filesystem Moment archives and loopback-only
preview semantics. Agent Sandbox has the same private `capsuled` protocol
available through an owned Kubernetes port-forward, but currently advertises
Snapshot, Clone, and Preview as unsupported. CSI snapshots and public
Kubernetes ingress have materially different semantics and must not become
provider-specific shortcuts.

## Decision

- Implement Agent Sandbox Moment capture and restore with the same bounded,
  deterministic `capsuled` workspace archive, immutable manifest, digest, CAS,
  validation, and non-destructive lineage semantics used by Docker.
- Carry archive bytes only over an authenticated, operation-scoped port-forward
  to the owned Capsule pod. `meridiand` verifies ownership and keeps control of
  the tunnel; it does not use `kubectl exec`, mount the PVC on the host, or ask
  the Agent Sandbox controller to interpret Meridian archives.
- Continue to treat CSI `VolumeSnapshot` as a provider storage artifact, not a
  Meridian Moment. CSI discovery does not satisfy portable Snapshot or Clone
  capability and no CSI object is silently substituted for an archive.
- Implement preview discovery and forwarding through the same capsuled
  loopback protocol and public ticket semantics as Docker. The pod-side target
  remains a discovered `127.0.0.1` port, and the daemon-side listener remains
  literal loopback with Capsule-and-port-scoped digest-backed tickets.
- Use a provider-owned port-forward for each preview path and bind its lifetime
  to the request or WebSocket. Validate Sandbox identity, pod ownership, port,
  limits, expiry, and revocation at every hop.
- Create no Service, Ingress, LoadBalancer, public pod-IP route, or shared
  namespace proxy for Moment or preview traffic. Do not forward installation,
  Capsule supervisor, Kubernetes, or other control-plane credentials to the
  preview application.
- Advertise Moment and Preview capabilities only when the complete archive or
  proxy path is operational and passes the same conformance contract as
  Docker.

## Consequences

Agent Sandbox can meet Meridian's portable Moment and local preview product
semantics without making Kubernetes storage or ingress objects part of the
public domain. Port-forward throughput and connection lifetime become
operational constraints, and suspension or pod replacement interrupts active
transfers and previews; callers reconcile or reconnect through bounded
operations.

This is target architecture. Agent Sandbox Snapshot, Clone, and Preview remain
unsupported in the current implementation until these paths are delivered end
to end.
