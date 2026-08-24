# Review UI, terminal, and previews

The responsive React review interface at `/ui/` consumes only the TypeScript
client generated from `api/openapi.yaml`; lifecycle transitions remain
server-owned.

## Development and production assets

Install the pinned Bun dependency graph and run the Vite development server:

```console
make bootstrap
go run ./cmd/meridiand --provider=fake --data-dir=/tmp/meridian-ui
cd frontend
bun run dev
```

Vite serves `/ui/` and proxies public API and WebSocket requests to
`127.0.0.1:8080`. Production assets are built and copied into the Go embed
package by `make ui-build`. A small unavailable page remains embedded so a Go
unit build never depends on pre-existing frontend output. Run `make ui-test`
for deterministic jsdom component tests.

The production routes are:

- `/ui/` — Capsule list across local projects plus a Project picker for
  starting a first prompt in a freshly provisioned Capsule and Timeline;
- `/ui/capsules/:id` and `/ui/capsules/:id/diff` — detail, bounded Git review,
  workspace file browsing, and an explicitly confirmed Ship panel;
- `/ui/threads` and `/ui/threads/:id` — retained structured Thread fleet,
  decrypted typed-block timeline, composer, permissions, and session actions;
- `/ui/runs/:id` and `/ui/runs/:id/terminal` — ordered activity and PTY;
- `/ui/moments/:id` — immutable filesystem Moment metadata; and
- `/ui/timelines/:id` — visual lineage with an accessible text alternative.

API routes remain at their existing root paths. Keeping browser routes beneath
`/ui/` prevents SPA fallback from shadowing scriptable API resources.

The project-level form posts the harness, optional Capsule name, and first
prompt once and displays the stable provisioning intent, Capsule, and Thread
references. It does not attempt to select or reuse an existing Ready Capsule.
Capsule-scoped Thread controls remain available on Capsule detail pages.

## Browser and content security

Static and SPA responses include a restrictive Content Security Policy,
clickjacking, MIME-sniffing, referrer, opener, and permissions headers. Diffs
are rendered as text in `<pre>` elements, never injected as HTML. The UI does
not write API bodies, diffs, terminal frames, prompts, or ticket values to
browser logs or storage.

Workspace file content uses base64 in JSON and is decoded only for text
rendering. Valid UTF-8 without binary control content is placed in `<pre>`;
binary, symlink, special, and over-1-MiB content is labeled unsupported. File
bytes and paths are not interpreted as HTML, written to browser storage, or
logged.

The Ship panel displays the exact inspected HEAD/tree and dirty state. It
requires the operator to type `ship`, a non-protected destination branch, a
commit message for dirty trees, and pull-request fields when requested. The
mutation carries the current Capsule resource version and exact reviewed Git
objects; repository or agent output cannot supply the confirmation.

Thread transcript routes replay a bounded ordered page and then follow SSE with
an `AbortController`, durable sequence cursor, ID deduplication, and bounded
exponential reconnect. Route changes abort the stream. Matching assistant
deltas collapse into one streaming message and disappear when the
authoritative final arrives. Message, tool, permission, unknown-block, and
error content is rendered as text; raw HTML is never interpreted.

The composer sends on `Enter` and inserts a newline on `Shift-Enter`. Pending
select/confirm/input requests are explicit controls; unknown blocks are visible
but non-executable. Cancel and archive require a reviewable confirmation, while
crypto-shred additionally requires typing `crypto-shred`. Every mutation uses
the current Thread resource version and refreshes authoritative state after a
conflict.

A locked transcript directs the operator to restore the matching installation
key. A corrupt transcript directs the operator to stop and restore from a
trusted backup. Neither view exposes key identifiers or ciphertext. Browser
storage, URLs, console output, and analytics never receive prompts, tool
results, or transcript bodies.

Encryption protects transcripts at rest, not from the trusted local control
plane: `meridiand` decrypts requested blocks and sends them to the local user.
Anyone who can access the unauthenticated loopback API or the daemon process has
the same practical visibility, so do not expose that API without an
operator-controlled authentication boundary.

The terminal requests a fresh 30-second, single-use, Run-scoped attach ticket.
The daemon keeps only its SHA-256 digest in bounded memory. The browser rejects
cross-origin and cross-Run WebSocket paths, sends binary-safe PTY input, tracks
output cursors and replay gaps, reconnects only while the Run is active, and
disposes its socket, resize observer, xterm listeners, and terminal instance on
unmount or detach. PTY bytes remain sensitive untrusted content.
The Terminal route remains a native Run/PTY fallback and is not a structured
Thread transport.

## Preview capability

Docker previews use a separate listener, `127.0.0.1:8081` by default. Configure
it with `--preview-listen`; only literal IPv4 or IPv6 loopback addresses are
accepted. The API listener does not change this restriction.

`GET /capsules/{id}/previews` asks authenticated `capsuled` for a bounded set of
listening ports in that Capsule. `POST
/capsules/{id}/previews/{port}/tickets` returns a two-minute URL. The URL is
reusable until expiry so page subresources and WebSockets can load. It is scoped
to exactly one Capsule and discovered port and is revoked by Delete, Seal, or a
daemon restart. The daemon stores only its SHA-256 digest and bounded in-memory
metadata.

Both proxy hops dial only Capsule loopback at the selected port. They do not use
`Host` for routing, accept destination hosts or IPs, perform DNS resolution, or
expose Docker. Hop-by-hop headers, authorization, cookies, and
`X-Meridian-*` headers are stripped. Requests, responses, headers, paths,
discovery, handshakes, frames, and time are bounded. WebSockets are supported
and close when the ticket expires or is revoked.

The fake provider reports preview unsupported and the UI shows that state.
Preview URLs are bearer secrets: do not log, persist, or share them. Application
cookies, redirects, absolute root paths, ports below 1024, and bodies over 8 MiB
are intentionally unsupported. Preview ingress remains part of the trusted
single-user Docker development profile, not an untrusted multi-tenant boundary.

The API and browser session are authenticated, but Meridian remains
single-principal and local-first. Keep `meridiand` on loopback unless an
operator supplies TLS and a reviewed network boundary. Docker remains a
trusted-development provider and is not an untrusted multi-tenant isolation
boundary.
