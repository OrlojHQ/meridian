# ADR 0001: Keep Meridian coding-specific

- Status: Accepted
- Date: 2026-08-23

## Context

Meridian needs a clear boundary from Orloj and from the coding agents it hosts. Without one, the control plane could drift into a general workflow orchestrator, agent harness, source-control system, or virtualization implementation.

## Decision

Meridian is coding-specific infrastructure for isolated, resumable development environments. It owns Capsule lifecycle coordination, workspace access, review and delivery integration, credential brokering, and the interfaces around existing isolation providers.

Meridian does not:

- implement general DAG, delegation, or multi-agent orchestration;
- require or reimplement Orloj;
- replace Git, a coding-agent harness, or a model provider;
- build a hypervisor or microVM monitor; or
- claim that a review-oriented diff prevents data exfiltration.

The control plane, in-Capsule agent, provider adapters, credential broker, ingress/egress controls, review clients, and artifact storage remain separate architectural boundaries.

## Consequences

Provider and agent integrations stay behind explicit contracts. Product behavior can evolve without coupling Meridian to one runtime, model, or orchestration engine. Features outside this boundary require a new ADR.
