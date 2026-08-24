# ADR 0005: Require providers to advertise capabilities

- Status: Accepted
- Date: 2026-08-23

## Context

Isolation providers differ in snapshot support, pause behavior, networking, resource controls, architecture, and security boundary. A lowest-common-denominator interface would hide meaningful constraints, while provider-specific conditionals throughout the product would destroy portability.

## Decision

Each provider adapter will advertise a versioned, machine-readable capability set before scheduling. Capabilities include supported lifecycle operations, filesystem snapshots and copy-on-write forks, pause semantics, architectures, resource limits, ingress, egress controls, workload identity, and isolation class.

Requests declare required capabilities. Scheduling fails clearly when no provider satisfies them; Meridian does not silently downgrade a security or durability requirement. Capability names describe observable semantics rather than vendor implementation details. Provider-specific extensions remain namespaced.

## Consequences

Clients can explain unavailable operations before attempting them. New provider features can be adopted without widening every implementation immediately. Capability negotiation and conformance tests become part of the provider contract.
