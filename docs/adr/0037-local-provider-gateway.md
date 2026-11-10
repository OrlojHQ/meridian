# ADR 0037: Local provider gateway for Docker installations

- Status: Proposed
- Date: 2026-11-09
- Extends: ADRs 0024 and 0027

## Context

Provider connections (ADRs 0024 and 0027) give a Capsule a 24-hour gateway
lease instead of an upstream credential. The gateway is enabled only by
`--provider-gateway-url`, an HTTPS origin that Capsules can reach and whose
certificate they trust. On Kubernetes that is a normal deployment
prerequisite. On a single-user Docker install, such as Homebrew's
`brew services start meridian`, it means running a reverse proxy with a
publicly trusted certificate or a tunnel. A tunnel such as cloudflared
publishes the whole Meridian API on the internet to give a container on the
same machine a route back to the host. That is the opposite of what the
gateway is for.

The default API listener is loopback-only, and Linux Capsules cannot reach
host loopback. Even where they can (Docker Desktop and OrbStack forward
`host-gateway` to host loopback), the harnesses insist on HTTPS, and a
private certificate fails unless every harness trusts it.

We checked how each official pack resolves TLS roots by pointing it at a
private-CA HTTPS server:

| Harness | Runtime | Trusts the CA with |
| --- | --- | --- |
| Codex 0.167 | Rust, rustls with bundled roots | `SSL_CERT_FILE` (or `CODEX_CA_CERTIFICATE`) |
| Claude Code 2.1 | Bun | `SSL_CERT_FILE` or `NODE_EXTRA_CA_CERTS` |
| OpenCode 2.2 | Bun | `SSL_CERT_FILE` or `NODE_EXTRA_CA_CERTS` |
| Pi 0.87 | Bun | `SSL_CERT_FILE` or `NODE_EXTRA_CA_CERTS` |
| DeepSeek Harness | Node 24 | `NODE_EXTRA_CA_CERTS` only |

No harness trusted the CA without a variable, and Codex and Node ignore the
system store. The Capsule root filesystem is read-only anyway, so a
system-store install was never an option at runtime.

## Decision

### A dedicated gateway listener when no URL is given

With `--provider=docker` and no `--provider-gateway-url`, `meridiand` starts a
second listener that serves TLS and only `/provider-gateway/` routes. Any
other path returns 404 before reaching the API, the UI, or authentication.
An explicit URL keeps today's behavior, and Kubernetes (Agent Sandbox) still
requires one. `--provider-gateway-listen=off` turns the local gateway off.

`meridiand` asks the Docker engine where to listen:

- Docker Desktop, or any engine whose daemon runs in a VM (macOS and
  Windows, including OrbStack and Colima): `127.0.0.1` on a random port.
  Capsules reach it through Docker's `host-gateway` alias.
- Native Linux Docker: the default `bridge` network's gateway address (often
  `172.17.0.1`) on a random port. Capsules reach it through the host's
  routing between bridges. Binding to the bridge address, not `0.0.0.0`,
  keeps the listener off LAN and public interfaces.
- A remote engine (`tcp://`, `ssh://`) or rootless Docker: no default. The
  daemon logs a warning and leaves the gateway off, as before.
  `--provider-gateway-listen=IP:PORT` sets the address explicitly.

The port is random so two daemons on separate data directories never
collide. Leases die with the daemon, so a new port after a restart changes
nothing for Capsules.

### Capsules name the gateway, not the host

Capsule containers get one extra hosts entry,
`provider-gateway.meridian.internal`, mapped to `host-gateway` or the bridge
address. The connection URL handed to `capsuled` is
`https://provider-gateway.meridian.internal:<port>/provider-gateway/<type>`.
The `.internal` top-level domain is reserved for private use, so the name
cannot collide with a public host. Containers created before this change have
no entry and need recreating to use the local gateway.

### An in-memory CA, rotated with lease lifetime in mind

`meridiand` generates an ECDSA P-256 CA when it starts. It is never written
to disk, so it is not in the data directory, backups, Moments, or Capsule
images, and a restart replaces it. Leases are also in memory, so a Capsule
that outlives the daemon loses its lease anyway; persisting the CA would buy
nothing and add a long-lived key to protect.

- The CA carries critical name constraints: it may sign only
  `provider-gateway.meridian.internal` and no IP address. If its certificate
  is copied out of a Capsule, or a Capsule process adds it to another trust
  store, it cannot vouch for any other site.
- The CA is valid for 30 days. Three days before it expires the daemon makes
  the next CA and hands out both in trust bundles. The leaf switches to the
  new CA 25 hours later, after every lease issued under the old bundle alone
  has expired (leases last 24 hours). If the clock jumps past the CA's
  expiry, as after a long sleep, a fresh CA replaces it at once; every lease
  is expired by then.
- The leaf is valid for 48 hours and is replaced when less than 24 hours
  remain, or when the CA changes. Its only name is the gateway name and it is
  only for server authentication.
- Only the certificates leave the daemon. `capsuled` receives them with the
  connection, next to the lease.

### `capsuled` trusts the CA through the harness environment

When a connection carries a CA, `capsuled` checks that it holds at most four
CA certificates in at most 16 KiB of PEM. It writes two files into the
Capsule's private setup home (tmpfs, outside the workspace):

- `.meridian/provider-ca.pem`: the gateway CAs;
- `.meridian/provider-ca-bundle.pem`: the image's system bundle (the
  inherited `SSL_CERT_FILE` or the first standard bundle path found, up to
  4 MiB) followed by the gateway CAs.

It sets `SSL_CERT_FILE` to the bundle and `NODE_EXTRA_CA_CERTS` to the CA
file in that harness process's environment. The bundle keeps public roots,
so tools that read `SSL_CERT_FILE` (Git over HTTPS, curl, other OpenSSL and
rustls clients) still reach public sites. A connection without a CA, from an
explicit `--provider-gateway-url`, changes neither variable.

## Consequences

- A Homebrew or `make` user can add an API key, a custom endpoint, or a
  ChatGPT plan connection without flags, a proxy, or a tunnel.
- Any process in any container on this Docker engine, not only Capsules, can
  open a connection to the listener. It serves only lease-authenticated
  gateway routes, the same surface the API listener already exposed with
  `--provider-gateway-url`; without a lease the gateway returns 401. On
  native Linux the bridge address is also reachable from the host itself.
- Host firewalls that drop container-to-host traffic (for example `ufw` with
  a default deny on `docker0` or `br-*`) block the listener. The
  troubleshooting guide covers allowing the port, or using
  `--provider-gateway-listen` or `--provider-gateway-url`.
- The trust variables override any `SSL_CERT_FILE` or `NODE_EXTRA_CA_CERTS`
  the image set, for harness processes that use a connection. The bundle
  starts from the image's `SSL_CERT_FILE`, so a derived image's corporate CA
  stays trusted through that variable; `NODE_EXTRA_CA_CERTS` from an image
  is replaced.
- TLS handshake errors on the listener are not logged, because any container
  can cause them.
- This does not make container isolation a multi-tenant boundary. A
  compromised Capsule can still spend against its own leased connection,
  exactly as before.
