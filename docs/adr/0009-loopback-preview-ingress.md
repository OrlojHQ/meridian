# ADR 0009: Add scoped loopback preview ingress

- Status: Accepted
- Date: 2026-08-24
- Supersedes: the preview deferral in ADR 0008

## Context

ADR 0008 correctly rejected previews while Meridian lacked authenticated
Capsule-owned port discovery and routing. The review UI needs local Docker
previews without allowing a browser to select a container address, host
destination, or provider control endpoint.

## Decision

`capsuled` discovers a bounded set of listening, loopback-reachable TCP ports
from its own network namespace. Its authenticated private protocol forwards
HTTP and WebSocket traffic only to `127.0.0.1` at one of those discovered ports.
The Docker adapter exposes this through an optional `PreviewRuntime`; lifecycle
providers and the fake provider do not acquire preview authority implicitly.

`meridiand` serves preview ingress on a separate listener configured with a
literal loopback IP. Startup rejects wildcard, named, or non-loopback preview
addresses. Public APIs discover ports and mint two-minute, reusable-until-expiry
URLs scoped to one Capsule and port. URL tokens have 256 bits of randomness;
only SHA-256 digests and bounded scope metadata remain in daemon memory.

Preview paths carry the token, Capsule ID, and port, and all three must match.
Routing never uses the request `Host`. Deletion and Seal revoke a Capsule's
tickets, restart loses all tickets, and active requests/WebSockets are canceled
at expiry or revocation. Both proxy hops strip hop-by-hop headers,
authorization, cookies, and Meridian-private headers and enforce path, header,
body, frame, discovery, and timeout limits. Neither hop follows redirects or
performs DNS-based target selection.

## Consequences

Docker previews are suitable only for the existing trusted, single-user local
development profile. The fake provider reports previews unsupported. Absolute
application URLs, redirects, cookies, responses over 8 MiB, ports below 1024,
and services not reachable through Capsule loopback are intentionally not
supported. Preview tokens are bearer secrets visible in browser URLs and must
not be logged, shared, or placed in durable storage.
