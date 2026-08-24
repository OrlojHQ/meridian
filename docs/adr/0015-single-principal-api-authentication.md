# ADR 0015: Authenticate the API as one installation principal

- Status: Accepted
- Date: 2026-08-24

## Context

Meridian's first product remains a trusted single-user installation, but a
loopback bind or an operator-provided reverse proxy is not an API
authentication mechanism. REST requests, event streams, and ticket minting can
read source-bearing data or cause privileged lifecycle and delivery effects.
Introducing users, roles, organizations, or tenant isolation would add a
different product and security model.

## Decision

- Authenticate the public API with one high-entropy installation bearer token.
  It represents the installation's single trusted operator and grants the same
  principal access to every Meridian object; there is no RBAC, user directory,
  organization model, or multi-tenant authorization boundary.
- Require `Authorization: Bearer` authentication for REST, SSE, and every
  endpoint that mints a PTY or preview ticket. Reject tokens in query strings,
  cookies, or request bodies. Authentication failures are content-free and
  bearer values are never logged.
- Leave only liveness and readiness probes unauthenticated. Their responses
  remain content-free and expose no object, provider, version, or configuration
  details beyond process health.
- Keep PTY and preview redemption on their existing short-lived, narrowly
  scoped tickets. The installation token does not replace a Run-scoped PTY
  ticket or Capsule-and-port-scoped preview ticket and is not forwarded to a
  Capsule or preview application.
- Keep the installation token outside SQLite, artifacts, Moments, backups, and
  Capsule mounts. Provisioning, file permissions, rotation, and reverse-proxy
  transport protection are operator responsibilities; compare presented
  values without data-dependent timing and fail startup when configured token
  custody is unsafe.
- Apply normal object validation, optimistic concurrency, idempotency, and
  policy checks after authentication. A single principal removes cross-user
  access decisions; it does not turn possession of an identifier or event
  cursor into authorization.

## Consequences

Possession of the installation token gives full control-plane authority, so
its compromise has installation-wide impact. TLS or an equivalently protected
local transport remains required wherever bearer traffic can be observed.
Ticket scoping limits exposure at terminal and preview ingress without
creating a second user identity system.

This is target architecture. Until it is implemented, the current public API
must be treated as unauthenticated and confined to a trusted local or
independently authenticated boundary.
