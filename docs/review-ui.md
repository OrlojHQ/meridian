# Application UI, terminal, and previews

The responsive React application at `/ui/` consumes only the TypeScript client
generated from `api/openapi.yaml`; lifecycle transitions remain server-owned.

## Application shell

The UI uses a full-height, Capsule-centered workspace:

- the persistent Activity sidebar groups Capsules by Project and shows each
  Capsule's harness and what it is doing and for how long (for example "Claude
  Code working", "Waiting for permission", or "Capsule failed") without
  requiring a dashboard round trip. The selected Capsule lists its sessions
  beneath it when it has more than one;
- the primary surface presents the Capsule's sessions as tabs (its native PTY
  and each structured Thread) under a one-line header whose actions menu holds
  Pause, Resume, Seal, and Delete, or a resource inspector; and
- the contextual tool pane provides capability-gated Changes, Preview, Files,
  Terminal, and Activity tabs for the selected Capsule.

The Activity view is an inbox of what needs the operator. Needs attention and
Ready for review come first as rows with direct actions: a session waiting for
permission or input links straight to that session, a failed Capsule or Run
shows its bounded failure reason and opens the Capsule, and finished work opens
the Capsule or, when the provider supports Git review, its diff page. Working
Capsules collapse into a compact list of links, and Idle, Paused, and Sealed
into one collapsed summary. When nothing needs the operator the view says it is
all caught up and still offers to start work in each Project or create a new
one. Status is derived only from Capsule, Run, and Thread summaries, including
the Thread's content-free `awaiting` flag. The working set never polls Git status, files,
or previews, because those count as Capsule activity and would defeat idle
pausing, and never fetches transcripts. `meridiand --runtime-refresh-interval`
keeps Run state and structured Thread output current while no client is
watching (ADR 0025).

A slim status bar runs along the bottom of the shell. It shows whether the
browser is online and whether the daemon API answers the capabilities request,
the provider version, and counts of Capsules that need attention, are working,
or are ready for review, each linking to the Activity view. When the provider is
`fake`, the bar turns to a warning that Capsules are simulated and no agents
run. It shows no resource use because the API exposes no resource metrics
(`capabilities.resourceMetrics` is false).

The Terminal tool appears only when the primary surface is not already showing
that native PTY, avoiding duplicate attachments while keeping PTY access beside
structured Threads and inspectors.

Run history and Moments live in the Activity tool instead of repeating beneath
the primary workspace. Run inspectors and historical terminal routes remain directly
addressable from that list. Every Run inspector and focused terminal provides a
direct return to its owning Capsule workspace.

Desktop layouts show all three surfaces. On narrower viewports the Activity
sidebar becomes a drawer and Capsule tools become an independently dismissible
full-screen surface above the status bar. Each pane owns its scrolling; the browser document does
not become an unbounded stack of Capsule panels.

The Changes tool parses the bounded unified diff into collapsible file sections,
old/new line-number gutters, hunk headers, and semantic addition/deletion rows.
Changes and Files share an extension-aware source renderer with line numbers.
Highlighting is loaded only when supported source opens and is skipped for large
or unknown files without preventing plain-text review. It uses Shiki's JavaScript
regular-expression engine rather than WebAssembly and maps the local GitHub
token palette to bundled CSS classes, preserving the UI's strict CSP. Repository
text is always rendered as React text nodes, never injected HTML.

Each diff line in the Changes tool has a comment affordance that appears on
hover and on keyboard focus. A comment is Markdown text anchored to the file
path, side, and line: deletions are anchored to the old side and additions and
context to the new side, and a comment may extend through later lines of the
same hunk. Pending comments render as text under their last line and in a
review summary with a count, where they can be edited or removed; a comment
whose line has left the current diff stays in the summary and is marked as not
in the diff. They are kept only in memory for each Capsule, shared by the
Capsule workspace and `/ui/capsules/:id/diff`, and are discarded on reload.
They are never written to browser storage or the server.

"Send to agent" composes one message that lists each comment with its
`file:line` and a short quoted excerpt of the code it refers to, and shows the
whole message before anything is sent. Excerpts are control-stripped and
bounded, and paths and code sit in code spans and fences longer than any
backtick run they contain, so quoted repository text cannot close its fence and
read as the reviewer's instructions. The message goes to one active structured
Thread in the Capsule, by default the Capsule's default session, through the
existing Thread message API with the Thread's current resource version. A
conflict refreshes the Thread and keeps the batch so the operator can send
again; the batch is cleared only after the server accepts the message, and the
workspace then opens that session. The UI never types into or injects text into
a native PTY: when the Capsule has no structured session, and as an option
otherwise, "Copy as prompt" places the message on the clipboard for the
operator to paste, and the comments stay pending.

`New project` creates the top-level Project boundary and can apply one
installation-known harness pack without accepting a free image reference. Each
Project heading owns its own `+` launcher, so Capsule creation is already
scoped and does not ask for a Project again. The launcher starts with an agent
picker over the Project's applied packs; it can add another installation-known
pack to the Project in place, again without accepting a free image reference.
Under the picker the operator chooses between opening the agent's terminal
(native) and giving it a written task (structured, when the provider supports
it). Native launch lists only harness packs applied to that
Project, follows provisioning, and opens the first PTY Run. Structured launch
retains the encrypted Thread intent workflow. Keyboard shortcuts `n`, `/`,
`j`/`k`, and the arrow keys open Project creation, focus filtering, and move
through the Capsule working set only while focus is outside terminals and form
controls.

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

- `/ui/` — Activity view for the Capsule working set;
- `/ui/capsules/:id` — unified Capsule workspace, with native PTY or structured
  Thread controls in the primary surface and review tools beside it;
- `/ui/capsules/:id/diff` — exact-state Delivery approval in the primary
  surface while the contextual Changes tab shows the bounded Git diff;
- `/ui/threads` and `/ui/threads/:id` — retained structured Thread fleet,
  decrypted typed-block timeline, pinned composer, permissions, and session
  actions;
- `/ui/runs/:id` and `/ui/runs/:id/terminal` — ordered activity and focused PTY;
- `/ui/moments/:id` — immutable filesystem Moment metadata; and
- `/ui/timelines/:id` — visual lineage with an accessible text alternative.

API routes remain at their existing root paths. Keeping browser routes beneath
`/ui/` prevents SPA fallback from shadowing scriptable API resources.

The native launch mode lists only harness packs applied to the selected Project.
It creates a Capsule with the selected pack frozen, follows Capsule and Run
provisioning, and opens the existing PTY Terminal route when the daemon creates
the initial Run. Leaving the terminal closes only the browser attachment and
does not terminate the Run. The Capsule workspace presents the active launcher
Run, while Activity retains links to historical Runs. A terminal Run remains
bounded by its harness profile timeout. Official native
packs use the schema's 24-hour maximum; when a Run exits, fails, or times out,
the workspace shows its bounded failure reason and requires an explicit
`Start <harness>` action instead of silently restarting a process.

The separate structured form posts the harness, optional Capsule name, and
first prompt once and displays the stable provisioning intent, Capsule, and
Thread references. It does not attempt to select or reuse an existing Ready
Capsule. Capsule-scoped Thread controls remain available on Capsule detail
pages.

## Browser and content security

Static and SPA responses include a restrictive Content Security Policy,
clickjacking, MIME-sniffing, referrer, opener, and permissions headers. Diffs
are rendered as text in `<pre>` elements, never injected as HTML. The UI does
not write API bodies, diffs, terminal frames, prompts, or ticket values to
browser logs or storage. Review comments live only in page memory, and the
review prompt reaches the clipboard only through an explicit "Copy as prompt".

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
unmount. Replay cursors remain internal unless a gap must be diagnosed. The
browser prefers xterm.js WebGL rendering so block and
box-drawing glyphs used by native harness TUIs remain cell-aligned, with the DOM
renderer as a compatibility fallback. `capsuled` supplies conservative
`xterm-256color`, true-color, and UTF-8 defaults when the Capsule image does not
set terminal environment values. PTY bytes remain sensitive untrusted content.
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
