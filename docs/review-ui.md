# Application UI, terminal, and previews

The responsive React application at `/ui/` consumes only the TypeScript client
generated from `api/openapi.yaml`; lifecycle transitions remain server-owned.

## Application shell

The UI uses a full-height, Capsule-centered workspace:

- the persistent Activity sidebar opens with links to Inbox (with a count
  of Capsules that need attention), Threads, and Harness settings under the
  brand and filter, then groups Capsules by Project and shows each
  Capsule's harness and what it is doing and for how long (for example "Claude
  Code working", "Waiting for permission", or "Capsule failed") without
  requiring a dashboard round trip. The selected Capsule lists its sessions
  beneath it when it has more than one;
- the primary surface presents the Capsule's session as a tab (the native PTY
  of a CLI Capsule, or the current Thread of a Thread Capsule) under a
  one-line header whose actions menu holds
  Pause, Resume, Seal, and Delete, or a resource inspector. meridiand refuses
  to delete a Capsule with an active Run, so the Delete confirmation lists
  running sessions with Stop buttons and enables Delete once they end; a
  running native terminal can also be stopped from its session heading.
  When the daemon accepts the delete, and while the Capsule is still
  Deleting, the workspace leaves that page with a history replace: the most
  recently updated remaining Capsule in the same Project, otherwise that
  Project's empty state, or the Inbox when the Project is not available. A
  direct visit to a Capsule that is already Deleted stays on its read-only
  page. The Capsule page, a Capsule opened from the sidebar, and the command
  palette all confirm through this menu; and
- the contextual tool pane provides capability-gated Changes, Preview, Files,
  Terminal, and Activity tabs for the selected Capsule.

Within a Project, pinned Threads come first, ordered by name, then the
other Capsules by recency, so a lead used for weeks does not sink below this
morning's one-off Threads (ADR 0059). A pinned row shows a pin beside its
name and keeps its subagent rollup, which still comes from the one board
read; pins ride on the Thread summaries the sidebar already loads, so they add
no request or polling. The pin button on a row (shown on hover or focus) and
Pin or Unpin in the Thread's actions menu set and clear it for the Capsule's
current live Thread in a main Capsule, lead or plain; subagents have no pin
until promoted. Any number of Threads can be pinned. A pinned Thread that
needs the operator is listed once, under Needs you, like any other.
Archiving or crypto-shredding the Thread, or deleting the Project, removes
the pin, and Start fresh moves it to the new Thread. A pin is presentation
only: it grants nothing and no authorization check reads it.

The Inbox at `/ui/` is the operator's one attention-first list. It uses the
Board's status lanes (needs you, working, PR open, ready for review, idle,
paused, sealed) and the Activity headline ("N need attention", or "All caught
up"). A session waiting for permission or input can be peeked and answered in
place, a failed Capsule or Run shows its bounded failure reason, finished work
opens the Capsule or, when the provider supports Git review, its diff page, and
subagents fold under their lead unless they need you. When nothing needs the
operator the view still offers to start work in each Project or create a new
one. `/ui/board` redirects here. Status is derived from the content-free Board
and working-set summaries, including the Thread's `awaiting` flag. The working
set never polls Git status, files, or previews, because those count as Capsule
activity and would defeat idle pausing, and never fetches transcripts.
`meridiand --runtime-refresh-interval` keeps Run state and structured Thread
output current while no client is watching (ADR 0025).

Inbox rows that wait for permission or input have a "Peek"
disclosure that names the blocker and lets the operator answer without opening
the Thread. Rows stay content-free: only an explicit expand reads that one
Thread's transcript, limited to the trailing 256 messages the daemon uses for
`awaiting`, and the result lives in the row's own state. It is never prefetched,
polled, or kept in the query cache, and collapsing the row discards it. The peek
renders the Thread view's permission or input card. The request kind and
summary are agent-authored, so they are shown as one plain-text line with
control characters and bidirectional controls removed, capped at 64 and 600
characters; option labels are capped at 48 characters, while the answer still
names the option exactly as the agent offered it. Answers are the same
`POST /threads/{threadId}/responses` request the Thread view sends, with the
current resource version and a fresh idempotency key, so they pass the same
authentication and checks; a version conflict reloads the request. After an
answer, focus moves to the next row's peek. A lead's Board rollup and its
Subagents panel derive from the same link phase, so a subagent whose session
failed reads as failed in both.

A slim status bar runs along the bottom of the shell. It shows whether the
browser is online and whether the daemon API answers the capabilities request,
the provider version, and counts of Capsules that need attention, are working,
or are ready for review, each linking to the Inbox. It shows no resource use
because the API exposes no resource metrics
(`capabilities.resourceMetrics` is false). "Notify me" asks this browser for
the Notification permission. Granted alerts fire only while the tab is hidden
and only when a Capsule the operator is not already viewing enters needs-you;
the payload is a count plus "Meridian needs you", never a Thread title, prompt,
permission details, or transcript text. The document title shows `(N) Meridian`
for the same count. The permission lives in the browser; Meridian does not
write it, or any prompt draft, to browser storage, and there is no phone push.

When a Project has checks, the Capsule header and the Inbox use that result
instead of treating every idle agent as ready for review. A retry stays in
Working. **Checks passed** and **Checks failing (n/n)** need you. The Ship
panel states whether those checks match the exact tree being approved, and
the pull request body gains a server-written summary with no command output.
See [checks](checks.md).

The Terminal tool appears only when the primary surface is not already showing
that native PTY, avoiding duplicate attachments while keeping PTY access beside
structured Threads and inspectors.

Run history and Moments live in the Activity tool instead of repeating beneath
the primary workspace. Run inspectors and historical terminal routes remain directly
addressable from that list. Every Run inspector and focused terminal provides a
direct return to its owning Capsule workspace.

Capsule tools start closed on every width, so opening a Thread or Capsule
shows the conversation at full width. The tools toggle and the tool buttons
open them. A control that asks for a tool, such as Full review or Open
terminal, opens that tool. Open and closed are not stored, so a reload or a
new Thread starts closed again. On narrower viewports the Activity sidebar
becomes a drawer and Capsule tools become an independently dismissible
full-screen surface above the status bar. On phone widths (720px and below)
the tool labels collapse to icons. Each pane owns its scrolling; the browser
document does not become an unbounded stack of Capsule panels.

The Changes tool parses the bounded unified diff into collapsible file sections,
old/new line-number gutters, hunk headers, and semantic addition/deletion rows.
The Changes tab label and the Changes pane header show how many files changed
and how many lines were added and removed. Both are derived from the diff the
Changes tool has already loaded, so they add no polling or Git request; until
that diff loads, and while the Capsule is not Ready, no counts are shown. A
truncated diff marks the file count as a lower bound.

The Files tool is a collapsible tree with `role="tree"`. It lists the workspace
root when opened and lists a directory only when the operator expands it; it
never prefetches, because reading the workspace counts as Capsule activity.
Listings and file content refresh only through the tool's Refresh action, not
on window focus or reconnect. Arrow keys move through and expand the tree, Home
and End jump to its ends, and Enter opens a file. Symlinks, special files, and
files over 1 MiB are listed but cannot be opened.

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
installation-known harness pack without accepting a free image reference. It
preselects the harness chosen when the operator last created a Project, if the
installation still offers it, and otherwise the first pack in installation
order. Only the harness name is remembered, in the browser's
`meridian.newProjectHarness` storage entry, and only after the Project is
created; the dialog works unchanged when browser storage is unavailable.

The New Capsule dialog opens from the sidebar's New Capsule button, the home
page, and each Project heading's `+`, which preselects that Project. It asks
only for Thread or CLI, the Project, the harness, and a provider connection
when more than one fits. A Thread starts without a first message: the harness
starts idle and reports its models, so the operator picks the model and effort
in the Thread before the first turn. When the provider supports coordination,
a Thread can start as a lead agent with subagents, optionally approving each
spawn. The dialog asks for no limits: it allows 16 subagents in any 24 hours
and leaves the concurrent subagent Capsule limit (4 by default) and the
unattended limit (24 hours) at the daemon's defaults. Both can be changed from
the Agents panel's settings button while the lead runs. The panel shows how
many subagents started in the last 24 hours and "pauses after N without you".
A lead left that long without a message from you is shown as Paused: it can
no longer start or message subagents until you send it a message. A lead also starts idle and is
told to split the work in its first message.

A plain Thread that turned out to be worth splitting up can become a lead
later: **Make this a lead** in the Thread's actions menu offers the same
choice as starting a lead (approve each spawn, with the same limits) and
says that the agent restarts its session to pick up the role. The daemon
restarts it at once when the Thread is idle and waiting on nothing, and
otherwise before the next message; the dialog says which happened. The
Thread's Agents button appears as soon as the grant exists. The action is
offered only on an active Thread of a main Capsule that has no grant, when
the provider supports coordination; nothing makes a Thread a lead unless
the operator asks (ADR 0059).

Where Meridian woke a lead with subagent reports, its transcript shows one
quiet line, drawn like the compaction divider and never as an operator
message: "↳ Reports from subagent-1, subagent-2 failed · Wake prompt ▸". Each
name links to that subagent's Thread; at most three are named, then "+N
more". A report that ended its turn or said it was done shows just the name,
and any other status (failed, blocked) follows it. The line repeats no report
text: the lead's reply summarizes the reports, each subagent's Thread holds
its full output, and the Agents panel shows each status. **Wake prompt**
expands the exact text the lead received, with link IDs left out, including
any note that the operator messaged a subagent directly. The line is a group
labelled with the subagents' names and statuses, and the wake keeps its
`from_subagents` provenance (ADR 0029).

Thread or CLI is offered only when the provider supports structured sessions
and the selected pack might run one: a pack whose installation catalog entry,
matched by name and image, declares `interactionModes` without `structured`
offers only CLI, and one that declares `structured` without `native` offers
only Thread. The official Claude Code, Codex, OpenCode, and Pi packs declare
both; DeepSeek Harness declares only `structured`, because its published
package has no terminal UI. A pack outside the catalog, such as a custom
image, declares nothing, so it keeps the choice and the Capsule's profiles
decide. Harnesses are added to a Project in its settings, from the packs the
installation knows, never from a free image reference.

When the Project has pinned Threads, the New Capsule dialog adds "Send to",
which starts a new Thread unless the operator picks a pinned one ("API lead
(lead)"). The options for a new Capsule then give way to a message box, and
the message goes to that Thread through the ordinary Thread message API with
its current resource version, after resuming it if it is paused. Because it
is an operator message, it renews a lead's grant and reopens one that paused
without the operator.

Keyboard shortcuts `n`, Ctrl+N (⌘N on macOS), `/`, `j`/`k`, the arrow keys,
`g` then `n`, and `?` open Project creation, start a Capsule in the open
Project (or ask which Project when none is open), focus filtering, move
through the Capsule working set, jump to the next Capsule that needs the
operator, and list every shortcut, only while focus is outside terminals and
form controls. Ctrl+N is not taken from a focused terminal. `g` then `n` is the same
command as the palette's "Next item needing me": it cycles oldest-first through
Inbox items that need you. The command
palette opens with ⌘K on macOS and Ctrl+K elsewhere, or from "Commands" in the
sidebar. ⌘K works from anywhere, including a focused terminal. Ctrl+K is the
shell's kill-line, so the palette never takes it from a terminal. The palette
is a modal dialog with a combobox over a grouped listbox: Capsules, sessions of
Capsules with more than one, "New Capsule in" each Project, New project,
Inbox, Threads, Harness settings, the keyboard shortcut list, and, for the
current Capsule, the full diff (when the provider supports Git), lineage, Pause
or Resume (when it supports pausing), Seal (when it supports snapshots), and
Delete. Matching is fuzzy over names, Projects, harnesses, and activity labels,
and each item shows its shortcut where it has one. With an empty query the most
recently run items come first; they are kept in page memory only and never
written to browser storage.

The palette builds its items only from the working set and the capabilities
response the shell already holds and issues no request of its own for any
Capsule, for the same idle-pausing reason the working set never polls Git
status, files, or previews. Lifecycle items go through the Capsule workspace's
actions menu, which owns those mutations: Pause and Resume run there with the
Capsule's current resource version, while Seal and Delete only open its
existing confirmation and never run from the palette. A delete the daemon
accepts then leaves the Capsule, as described above. The palette replaces the
current history entry when it asks the menu to act on the Capsule already on
screen, so Back does not return to a Capsule that was deleted.

## Notes

Project notes and working notes (ADR 0060) have their own surfaces, and note
or proposal text appears only on pages that label it:

- **Project Notes tab** (`/ui/projects/:id?tab=notes`) lists each note with
  its size, last change, who wrote it and an Agent-proposed label, deleted
  notes separately, the total against 128 KiB with a rough token count, the
  note count, the `notesDelivery` select, and the pending proposals, each
  linking to its review page. It offers Copy as Markdown, delete, delete with
  history, and crypto-shred, which asks you to type the word to confirm.
- **Notes tab in the lead's Agents panel** lists the tree's working notes
  with size, last change and the member that wrote each, with history, edit,
  delete, shred and Promote to Project notes. While the grant is not active
  the tab is read-only: no edit, restore or plain delete, but promotion,
  delete with history and shred remain.
- **Editor.** A plain textarea with a Preview tab. Saving sends the revision
  the editor opened, so two tabs cannot overwrite each other; a conflict
  keeps your text and shows a diff against the new head. The preview renders
  no raw HTML, loads no image (an image shows as `[image: alt (url)]`), and
  never navigates: a link is inert text with its target beside it. Hidden
  characters are shown as escapes.
- **Review page** (`/ui/note-proposals/:proposalId`) opens on a source line
  diff against the note's current revision, with the rendered preview as a
  second tab. Control, bidirectional, zero-width and tag characters are shown
  as `\u{XXXX}` escapes and long lines wrap. It names the proposing Thread and
  its lead, with links, shows the state, and warns about credential-shaped
  content and hidden characters. Approve sends the proposal's digest and the
  note's current revision; Edit approves your edited text as
  `approved_with_edits`; Reject closes it. There is one proposal per page and
  no approve-all. A stale proposal cannot be approved; Open in editor merges
  it by hand against the current head.
- **Notes to review** in the sidebar, under Needs you, appears only when a
  proposal is pending and names only the note, the Project and the Thread.
  No notification quotes note or proposal text, and proposals raise no
  Thread attention.
- **History** lists each revision with its provenance, diffs any two retained
  revisions, and restores one as a new revision.
- **Add to Project notes** in a finished message's actions menu opens the
  editor with your selection (or the whole message), as a new note or
  appended to an existing one. Nothing is saved until you save, and the
  revision records the source Thread.
- The new-Thread dialog has an **Include Project notes** checkbox that
  follows `notesDelivery` and is hidden when delivery is off; the routine
  form has an Include Project notes switch, off by default.
- A later session of a Thread, resumed or restarted, that received a
  different Project-note set than the previous one gets a quiet
  "Project notes updated: N changed" divider, drawn like the compaction
  divider. Thread details list the delivered note set and say when the
  harness was not told about the files.

## Development and production assets

Install the pinned Bun dependency graph and run the Vite development server
against a local Docker daemon:

```console
make bootstrap
go run ./cmd/meridiand --provider=docker --data-dir=/tmp/meridian-ui
cd frontend
bun run dev
```

Vite serves `/ui/` and proxies public API and WebSocket requests to
`127.0.0.1:8080`. Production assets are built and copied into the Go embed
package by `make ui-build`. A small unavailable page remains embedded so a Go
unit build never depends on pre-existing frontend output. Run `make ui-test`
for deterministic jsdom component tests.

The production routes are:

- `/ui/` — Inbox of every Capsule by status (needs you first); `/ui/board`
  redirects here;
- `/ui/capsules/:id` — unified Capsule workspace, with native PTY or structured
  Thread controls in the primary surface and review tools beside it;
- `/ui/projects/:id` — empty Project, offering to start work in that Project.
  An unknown Project redirects to the Inbox;
- `/ui/capsules/:id/diff` — exact-state Delivery approval in the primary
  surface while the contextual Changes tab shows the bounded Git diff;
- `/ui/threads` and `/ui/threads/:id` — retained structured Thread fleet,
  decrypted typed-block timeline, pinned composer, permissions, and session
  actions. The fleet and Inbox title a Thread by its Capsule name, with the
  agent, Project, role (lead, or subagent of a named lead) and a status chip
  as secondary text, all from plaintext metadata; no transcript is
  decrypted to label a list row. The Capsule name is plaintext, so the
  Station never derives it from an encrypted transcript. For an automatic
  `thread-` id, the Capsule page shows a short title from the first operator
  message, read through the Thread API and not saved or written to browser
  storage. Clicking the title on the Capsule page saves a Capsule name the
  operator chose.
  The list puts Threads that need you first, then sorts by recency. The
  Thread ID appears only in Thread details;
- `/ui/settings/harnesses` — Harness settings, in three sections: Your setups,
  Import from this computer, and Provider connections;
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
Thread references. The daemon refuses a pack whose catalog entry lacks
`structured` with `422 unsupported` before creating a Capsule, so a CLI, TUI,
or stale browser request cannot provision a Capsule whose session could never
start. It does not attempt to select or reuse an existing Ready
Capsule. Capsule-scoped Thread controls remain available on Capsule detail
pages.

## Browser and content security

Static and SPA responses include a restrictive Content Security Policy,
clickjacking, MIME-sniffing, referrer, opener, and permissions headers. Diffs
are rendered as text in `<pre>` elements, never injected as HTML. The UI does
not write API bodies, diffs, terminal frames, prompts, or ticket values to
browser logs or storage; browser storage holds only the harness names the New
project and New Capsule dialogs remember. Review comments live only in page
memory, and the review prompt reaches the clipboard only through an explicit
"Copy as prompt".

Workspace file content uses base64 in JSON and is decoded only for text
rendering. Valid UTF-8 without binary control content is placed in `<pre>`;
binary, symlink, special, and over-1-MiB content is labeled unsupported. File
bytes and paths are not interpreted as HTML, written to browser storage, or
logged.

The pull request sheet summarizes files, line counts, and whether the action
will create a commit. It fills the branch, title, and description from the
open Thread. The operator confirms with one click. Push branch only sends a
push and omits pull-request fields. A dirty tree is committed with the title
(or the commit message, when only pushing); a clean tree is not given a
commit message. The exact HEAD and tree stay in a collapsed row and are sent
as `expectedHead` and `expectedTree` with the current Capsule resource version.

Thread transcript routes replay a bounded ordered page and then follow SSE with
an `AbortController`, durable sequence cursor, ID deduplication, and bounded
exponential reconnect. Route changes abort the stream. Matching assistant
deltas collapse into one streaming message and disappear when the
authoritative final arrives. Message, tool, permission, unknown-block, and
error content is rendered as text; raw HTML is never interpreted.

The composer sends on `Enter` and inserts a newline on `Shift-Enter`. Pending
select/confirm/input requests are explicit controls; unknown blocks are visible
but non-executable. Cancel and archive require a reviewable confirmation, while
crypto-shred additionally requires typing `crypto-shred`. Deleting a Project
requires typing that Project's name. The dialog lists how many Capsules,
threads, and secrets the delete will remove, and says that transcripts and
artifacts are deleted with the Project. After the daemon accepts it, the page
replaces the current history entry with the next Project, or with the Inbox
when none remains. Every mutation uses
the current Thread resource version and refreshes authoritative state after a
conflict.

A Capsule stays the kind chosen in the New Capsule dialog. A CLI Capsule runs
its harness's terminal and never offers a Thread. A Thread Capsule runs one
Thread session at a time, so its page shows one Thread tab: the active Thread,
or else the most recent one kept. Neither offers a tab to start another
session beside it. Earlier Threads of
the same Capsule appear above its transcript as folded "Show earlier session"
rows, each with its date, harness, message count, and an Open link to its own
Thread page, where its actions (archive, crypto-shred) stay. An earlier
transcript is read only when it is unfolded, and nothing in it can be answered.
A divider before the current transcript says when its session started and that
the agent starts without the conversation above. A link to an earlier Thread
opens the Capsule on the current one with that session unfolded.

Start fresh replaces the current session with a new Thread in the same Thread
Capsule,
with the same harness, keeping the files and changes. It is in the Thread
actions menu, the session-ended banner, the composer's `/` menu, and next to
Resume on a paused Thread. A live session is stopped first, after a
confirmation; Meridian waits for it to pause, archives a Thread that never
started a session, then creates and starts the new Thread. Each step is an
ordinary Thread API call, so a failure part way leaves a stopped Thread to
resume or start fresh from again. A lead and its subagents do not offer Start
fresh, so coordination is never ended by it, and neither does a Thread in a CLI
Capsule. The start-a-session form appears only in a Thread Capsule with no
Thread left.

When an active Thread's current Run is terminal (failed, stopped, or exited),
the Thread says so instead of looking live: the toolbar reads "Session ended",
the page badge reads "failed" or "ended", and a banner after the transcript
shows when the session ended and, for a failure, the Run's `failure` text. That
text can carry adapter or runtime output, so the Thread and Inbox
views render it as one plain-text line capped at 280 characters, with control
characters and bidirectional overrides removed; adapter stderr is never shown.
The banner offers Restart session (a new Run in the same Thread), Copy last
prompt (the operator's own latest message, written to the clipboard only on
click; subagent reports are never offered), and Start fresh. The composer stays
disabled until a new session starts, because the daemon rejects messages to a
Thread without an active session. The block stream stops reconnecting once the
daemon has closed it for the ended session and follows again when a new
session starts. The failed Run is read once, only after the session ends.

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

A provider that does not implement preview reports it unsupported, and the UI shows that state.
Preview URLs are bearer secrets: do not log, persist, or share them. Application
cookies, redirects, absolute root paths, ports below 1024, and bodies over 8 MiB
are intentionally unsupported. Preview ingress remains part of the trusted
single-user Docker development profile, not an untrusted multi-tenant boundary.

The API and browser session are authenticated, but Meridian remains
single-principal and local-first. Keep `meridiand` on loopback unless an
operator supplies TLS and a reviewed network boundary. Docker remains a
trusted-development provider and is not an untrusted multi-tenant isolation
boundary.
