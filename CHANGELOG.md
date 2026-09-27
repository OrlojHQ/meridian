# Changelog

All notable changes to Meridian will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Meridian uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-09-27

### Added

- Browser application shell with Project-grouped Capsule activity, unified
  native and structured launch, primary Thread or PTY workspaces, and
  capability-gated Changes, Preview, Files, Terminal, and Activity tools.
- Tag-only releases publish and Cosign-sign official harness pack images
  (`ghcr.io/orlojhq/meridian-capsule-{opencode,pi,claude,codex}`) from the
  thin Capsule image. The installation catalog keeps the Capsule registry and
  tag, or `--official-pack-tag` when the Capsule image is digest-only.
  Released `meridiand` binaries default to those GHCR tags and Docker pulls a
  missing registry image on first Capsule create. Local `:dev` names are never
  fetched from Docker Hub. `deploy/docker/compose.release.yaml` runs a
  published `meridiand` image on the host Docker engine.
- Self-hosted control plane, scriptable CLI, terminal dashboard, and browser
  review interface for coding workspaces.
- Trusted-local Docker and Kubernetes Agent Sandbox providers with owned
  lifecycle resources, restart recovery, pause/resume, and private `capsuled`
  supervision.
- Generic harness execution with Run events, reconnectable PTYs, Git status and
  diffs, local authenticated previews, and deterministic test harnesses.
- Encrypted structured agent Threads with multi-turn messages, tool summaries,
  permission requests, reconnection, and generic, OpenCode, and Pi adapters.
- Immutable filesystem Moments, Timeline lineage, Shards, non-destructive
  Rewind, Seal, and a verified content-addressed artifact store.
- OpenAPI-generated Go and TypeScript clients, SQLite WAL persistence,
  idempotent mutations, optimistic concurrency, and ordered event streams.
- AES-256-GCM encrypted named secrets with purpose binding, Project
  authorization, private HTTPS clone grants, and native/structured harness
  environment references across Docker and Agent Sandbox.
- Thread-first Project sessions with durable encrypted provisioning intents,
  fresh Capsule and Timeline allocation, Ready-state promotion, restart
  recovery, and CLI, TUI, and browser entry points.
- Authenticated bounded workspace browsing and binary-safe review rendering
  through Docker and Agent Sandbox private supervisor clients.
- Safe local worktree sync with matching-origin verification, staged archive
  validation, protected Git metadata, conflict refusal, and explicit force
  mirroring.
- Durable explicitly approved Delivery resources for exact-ref HTTPS pushes and
  host-side idempotent GitHub pull requests, with CLI and browser Ship flows.
- Offline backup, restore, artifact verification and retention, transcript-key
  rotation, Prometheus metrics, optional OpenTelemetry tracing, and hardened
  release automation.
- Helm deployment, provider contract tests, deterministic Docker and Kind
  integration suites, SBOM/checksum/signing policy, and operational
  documentation.

### Security

- Capsules run non-root with bounded resources and never receive the host
  Docker socket, provider credentials, or control-plane state.
- Attach and preview access use short-lived scoped credentials; Capsule
  supervisor, transcript, and credential keys are kept outside SQLite.
- Structured transcripts are encrypted with per-Thread data keys and
  authenticated metadata. Ordinary Run prompts, PTY bytes, diffs, and secret
  values remain excluded from durable events and observability.
- Workspace archives reject traversal, link escapes, special files, expansion
  abuse, corruption, and unsafe replacement; restore and backup publication are
  atomic.
- Local sync never writes or deletes `.git` or `.meridian-prepared`; Delivery
  binds approval to exact reviewed Git objects and supplies purpose-scoped
  credentials only to one narrow operation.
- Telemetry is content-free and bounded. The unauthenticated API and Docker
  previews default to loopback.
- Docker and Agent Sandbox are not claimed as hostile multi-tenant isolation
  boundaries; hardened runtime deployment and external review remain required.

[Unreleased]: https://github.com/OrlojHQ/meridian/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/OrlojHQ/meridian/releases/tag/v0.1.0
