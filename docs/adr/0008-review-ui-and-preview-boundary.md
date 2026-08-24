# ADR 0008: Embed the review UI and fail previews closed

- Status: Accepted; preview deferral superseded by ADR 0009
- Date: 2026-08-23

## Context

The browser review client needs same-origin access to the public Meridian API,
ordered Run activity, and scoped terminal attachment. Browser routes cannot
collide with existing root-level API resources. Preview routing would require
trusted discovery of a Capsule-owned port and a provider-owned connection path;
neither exists in the current public or private protocol.

Routing to container addresses, trusting request Host headers, or exposing the
Docker API would violate the provider boundary and create SSRF, cross-Capsule,
and host-control risks.

## Decision

Production Vite assets are embedded in `meridiand` and served beneath `/ui/`
with SPA fallback and restrictive security headers. Existing API paths keep
their behavior. Development Vite proxies only the public API and attach
WebSocket.

The browser obtains terminal authority only through the existing short-lived,
single-use Run ticket. Ticket registries store SHA-256 digests rather than
plaintext values.

Preview is an effective daemon capability, not merely a provider claim. It is
reported unsupported until a narrow provider/runtime interface and authenticated
`capsuled` protocol support bounded Capsule port discovery and connection. No
preview listener or proxy is started in this decision.

## Consequences

Go builds remain independent of the frontend toolchain through an embedded
fallback page; `make ui-build` replaces it with production assets. UI routes are
visibly namespaced and static security policy can remain separate from API
responses.

Local browser review and terminal attachment are available without exposing
private supervisor URLs. Preview URLs are unavailable rather than insecure or
misleading. Adding previews later requires a separate loopback-only listener,
scoped digest-only tokens, expiry/revocation, strict destination validation,
bounded HTTP/WebSocket proxying, and threat-model and integration-test updates.
