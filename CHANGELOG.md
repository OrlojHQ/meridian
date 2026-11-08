# Changelog

All notable changes to Meridian will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Meridian uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Lead Threads render subagent reports as cards (name, status, summary, and a
  link to the subagent Thread) instead of the raw coordination wake prompt, and
  pin a tray above the composer for subagents that need the operator. Report
  text is treated as hostile: control and bidi characters are stripped, length
  is capped, and link IDs stay out of the main view.

- Browser notifications, a tab badge, and a `G` then `N` shortcut for Capsules
  that need the operator. Notifications are opt-in from the status bar, fire
  only while the tab is hidden, skip the Capsule already open, and carry only a
  count plus "Meridian needs you" — never Thread titles, prompts, permission
  details, or transcript text. The document title shows `(N) Meridian`. The
  permission choice stays in the browser; no prompt drafts are written to
  browser storage, and there is no phone push.

- Subagent lifecycle. A finished subagent is paused and archived after its
  grant's `archiveAfter` (default 24 hours, 0 disables it; `--archive-after`,
  `archiveAfterSeconds`): its session ends and its Thread pauses, while the
  workspace and transcript stay. Subagents that need you, failed, or were
  promoted are never archived, and resuming the Thread brings a subagent back.
  `POST /capsules/{id}/promote` (`meridian capsule promote`, "Promote to
  main") makes a subagent a main Capsule that keeps its link to the lead
  unless `detach` is set; promotion is permanent and grants no coordination
  authority. The browser sidebar now lists main Capsules with each lead's
  subagent rollup, plus subagents that need you, and the board and Subagents
  panel fold archived subagents under "Archived (n)". Links gain `archivedAt`,
  `promotedAt` and the `detached` close reason; Capsules gain `promotedAt`;
  rollups gain `archived` (ADR 0029).

- A board of every Capsule by status. `GET /board`, `meridian board` and the
  browser's Board view group Capsules across Projects into needs you, working,
  PR open, ready for review, idle, paused and sealed, with the latest Delivery
  and, for leads, a subagent rollup. Subagents fold under their lead unless
  they need you; `expand=all` (`--all`, "Show all subagents") lists every
  subagent. The command palette gains "Next item needing me". When the
  operator messages a subagent directly, its lead's next report wake says so
  without the message text, and the message does not count against the lead's
  per-subagent limit; thread links gain `operatorMessages` (ADR 0029).

- A structured Thread can lead subagent Threads. Spawning it with a
  Coordination Grant (`meridian thread spawn --lead`, the browser's "Lead
  agent with subagents" option, or `coordination` on
  `POST /projects/{id}/threads`) lets its agent start subagents in their own sub-Capsules, message, wait on and
  cancel them, and read their reports, all within operator-set limits and a
  deadline. Agents use the in-Capsule `meridian-coord` CLI or MCP server,
  which writes to a `capsuled` mailbox that `meridiand` pulls; Capsules hold no
  credential. Unread reports wake an idle lead. New APIs expose grants
  (`GET /coordination-grants/{id}`, `POST .../revoke`) and links
  (`GET /threads/{id}/links`, `GET /thread-links/{id}`), Capsules gain a
  `role` of `main` or `sub`, and `meridiand --max-live-capsules` caps
  coordinated spawns. The browser nests subagents under their lead and labels
  delegated messages (ADR 0029).

- OpenCode and Pi Threads can lead subagents. The OpenCode pack registers
  `meridian-coord` as an MCP server through ACP `session/new`, like the
  Claude Code and Codex packs. Pi has no MCP client, so the Pi pack loads a
  bundled `meridian-coord` skill that teaches it the CLI. Packs may ship
  validated `skills/<name>/SKILL.md` files, installed under
  `/usr/local/share/meridian/<pack>/skills/`.

- Browser Threads work with every official pack. Each pack now installs a
  structured profile next to its native one under the same name. New
  `claude-stream` and `codex-app-server` adapter drivers run Claude Code's
  stream-json control protocol and the Codex app-server. They map streaming
  text, tool events, permission and question prompts, cancel, and resume to
  `meridian.adapter.v1`, and end every turn with `idle`. Both accept
  `--mcp-server` to register a stdio MCP server in the harness. Threads
  authenticate through provider connections (ADR 0026).

- Custom endpoint provider connections. `openai-compatible` and
  `anthropic-compatible` connections send gateway traffic to a self-hosted
  server such as vLLM, Ollama, or LiteLLM. The base URL, optional key, and
  default model are set on the connection and never reach a Capsule. Codex,
  OpenCode, and Pi use OpenAI-compatible endpoints; Claude Code uses
  Anthropic-compatible ones. Plaintext HTTP to private-network hosts needs
  `meridiand --provider-gateway-allow-private-http` and a per-connection
  opt-in. Use `meridian harness connect openai-compatible --base-url URL`
  or the Provider connections form (ADR 0027). For these connections, Codex
  turns off its `web_search` tool and `multi_agent` feature by default,
  because many compatible servers accept only function tools.

- Typing `/` in the Thread composer opens a filterable menu of the commands
  the harness reports for this session. Claude Code reports its commands at
  startup, Pi through `get_commands`, and OpenCode through ACP
  `available_commands_update`.
  Codex reports `compact`, `review`, and its skills, which the adapter runs
  through app-server calls, and Pi gains a `compact` action. Adapters send
  the list in a new negotiated, bounded `commands` frame of
  `meridian.adapter.v1`, and Thread blocks expose it as a `commands` event.
  Unknown `/text` is still sent as a prompt. Stop and New session appear as
  Meridian controls (ADR 0028).

- A new `acp` adapter driver runs any Agent Client Protocol agent as a
  structured harness. It maps prompts, streamed messages, tool calls, plans,
  permission requests, available commands, cancel, and resume or load onto
  `meridian.adapter.v1`, advertises no file-system or terminal capability,
  refuses other agent requests, and answers permissions only with options the
  agent offered. A new ACP harness needs only a pack image and a manifest
  (ADR 0030).

- A `dsh` pack runs DeepSeek Harness 0.2.2-rc.1 Threads over ACP
  (`dsh --profile acp`). The published package has no terminal UI, so the
  pack is Thread-only: its catalog entry declares only `structured`, and a
  terminal launch is refused before provisioning. Packs can now install from
  an npm lockfile (`PACK_LAYOUT=npm`, registry tarballs with `sha512`
  integrity, no install scripts) and require identifying `--help` text
  (`PACK_IDENTITY`).

- DeepSeek Harness Threads use a Project's `openai-compatible` or
  `anthropic-compatible` provider connection through the provider gateway.
  `capsuled` writes a `dsh` route that reads the gateway lease from
  `MERIDIAN_PROVIDER_TOKEN`, selects it for sessions and subagents, and
  disables DeepSeek's adapters, web search, and session-log upload; no raw
  key enters the Capsule. The task form now offers a provider connection for
  harnesses `capsuled` can configure. The gateway accepts Anthropic SDK
  clients' `?beta=true` marker and does not forward it.

- `meridian project apply-harness PROJECT_ID NAME=IMAGE` allowlists a custom
  harness image on an existing Project. `meridian thread follow --until-idle`
  exits when the turn that answers the latest prompt goes idle.

- Board and Activity rows that wait for permission or input can be peeked
  open to see which permission, tool or command is being asked for, and
  answered in place with the Thread view's card. The rows stay content-free;
  only an explicit peek reads that Thread's transcript tail, and nothing is
  cached or polled. Agent-supplied request text is shown as capped plain text
  with control and bidirectional characters removed.

### Changed

- A lead's Board rollup and its Subagents panel now derive from the same link
  phase. A subagent whose session failed, for example on a harness timeout,
  reads as failed in both, instead of "needs you" in the rollup and "Ended" in
  the panel. The panel words its limits as "3 of 4 started · up to 4 at a
  time · ends in 7h 52m".

- Provider gateway errors for rejected requests include the upstream HTTP
  status in `error.upstreamStatus` and the message, plus a short sanitized
  reason from the upstream error. URLs, the upstream host, and credential-like
  strings are redacted.
- Pi provider connections reference the gateway lease as
  `$MERIDIAN_PROVIDER_TOKEN`. Pi 0.87 treats a bare variable name in
  `models.json` as a literal key.

- The OpenCode pack's Threads run `opencode acp` through the `acp` driver,
  with a fresh password per launch on OpenCode's loopback server. The
  `opencode-server` driver is removed, and the `adapterKind` value `opencode`
  is now `acp`. Provider connections work as before.

- `meridian thread spawn --follow` exits when the first turn goes idle or the
  session ends instead of streaming until the connection closes.

- A harness name may carry one native and one structured profile. A
  repository can no longer define any profile under a name that a trusted
  image manifest defines.

- Official harness packs build from one shared
  `images/harness-pack/Dockerfile`. Each pack is now a `pack.env` data file
  with its pinned release and checksums, its trusted profile, and an optional
  launcher. Make targets, CI, and release publishing find packs from those
  directories, so adding a harness no longer needs its own Dockerfile.
- The browser Thread view is a chat. User messages are right-aligned bubbles
  and assistant replies render as markdown, with streamed deltas merged into
  one message. Status events no longer appear as messages; a "Working…"
  indicator shows while a turn runs. Tool calls are compact rows that expand
  to output, with consecutive calls grouped. Permission and input requests
  are inline cards that collapse once answered. The composer is a single
  rounded input pinned to the bottom, with a Stop button during a turn.
  Raw events stay available behind a toggle that is off by default.
  Markdown never renders raw HTML or loads remote images, and only absolute
  http, https, and mailto links are kept.
- Threads report a content-free `turn` (`working` or `idle`) while their
  session is live. The sidebar and session tabs show an idle agent as idle
  instead of working. Responses to permission and input requests are matched
  to the request named by `responseTo`, which the API now documents.

### Fixed

- A Thread whose session failed, stopped, or exited no longer shows "active"
  and "Reconnecting…" forever. It says "Session ended", shows the Run's
  failure reason as bounded plain text, and offers Restart session, Copy last
  prompt, and New session; the composer stays disabled until a new session
  starts, and the block stream stops reconnecting. Board rows for a failed
  Capsule or Run show the same reason as the Activity view, and the sidebar
  and session tabs label ended sessions "Ended" or "Stopped" instead of
  "Idle". After a restart, replies from an adapter that reuses message IDs
  across sessions no longer overwrite the previous session's reply.

### Security

- The dependency scan now runs on changes to `images/**/package*.json`, so
  the `dsh` pack's npm lockfile gates `main`. DeepSeek Harness 0.2.2-rc.1, the
  newest release, pins `undici` 8.11.2, which has four HIGH advisories fixed
  in 8.12.2 (CVE-2026-109383, CVE-2026-109829, CVE-2026-109869,
  CVE-2026-87018). Packs cannot override upstream pins, and the code runs only
  in the `dsh` Capsule, so `.trivyignore.yaml` accepts exactly these findings
  for that lockfile and version until 2027-01-07, to be removed once a `dsh`
  release ships `undici` 8.12.2 or later.

- `golang.org/x/net` is updated from v0.58.0 to v0.62.0, fixing
  CVE-2026-78661 (denial of service through crafted HTML in `x/net/html`) and
  CVE-2026-78669 (denial of service through excessive HTTP/2 SETTINGS
  frames). x/net v0.62.0 also raises its sibling modules: `x/sys` v0.49.0,
  `x/term` v0.47.0, `x/mod` v0.41.0, `x/sync` v0.24.0, `x/text` v0.43.0, and
  `x/tools` v0.51.0.

## [0.2.0] - 2026-09-27

### Added

- Command palette. ⌘K on macOS and Ctrl+K elsewhere (never taken from a focused
  terminal, where Ctrl+K is kill-line) opens a searchable list of Capsules,
  sessions, "New Capsule in" each Project, New project, Activity, Threads, and
  Harness settings, plus the current Capsule's full diff, lineage, and Pause or
  Resume, each gated on provider capabilities. Seal and Delete open the
  Capsule's existing confirmation instead of running. Matching is fuzzy, recent
  items come first and stay in page memory, and items show their shortcuts.
  `?` lists every keyboard shortcut. The palette uses only the working set and
  fetches nothing per Capsule.
- Diff review comments. In the Changes tool, including beside the full review
  page, a reviewer can comment on a diff line or a range of lines, then edit or
  remove each comment inline or in a review summary. "Send to agent" composes
  one message that quotes each commented excerpt with its file and line, shows
  it for review, and sends it to a chosen structured Thread (by default the
  Capsule's default session) through the existing Thread message API, then
  opens that session. Terminal-only Capsules get "Copy as prompt" instead;
  Meridian never types into a native PTY. Pending comments stay in browser
  memory for each Capsule and are discarded on reload.
- Change counts. The Changes tab label and the Changes pane header show how
  many files changed and how many lines were added and removed. They come from
  the diff the Changes tool already loaded and add no Git requests.

### Changed

- The Files tool is a collapsible tree. A directory is listed only when it is
  expanded, listings refresh only on request rather than on window focus, and
  arrow keys, Home, End, and Enter move through the tree and open files.
- Activity, Threads, and Harness settings moved from the sidebar footer to the
  top of the sidebar, and Harness settings has an icon.
- Harness settings is organized into Your setups, Import from this computer, and
  Provider connections, with shorter copy and no nested disclosures.
- New project preselects the harness chosen for the last Project created in
  this browser, when the installation still offers it, and otherwise the first
  pack in installation order other than `mock`.
- The standalone Thread page names the agent and links to its Capsule by name
  instead of showing internal identifiers in its heading.
- The New Capsule dialog starts with an agent picker. A Project with no agents
  lists the installed harness packs and adds one in place instead of sending
  the operator to the CLI. Choosing between a terminal and a written task is a
  secondary option under the picker.
- Capsules, sessions, and the agent picker identify each harness by its logo:
  Claude Code, OpenCode, and Pi from simple-icons, and OpenAI's Blossom for
  Codex exactly as OpenAI publishes it. Other harnesses show a monogram.
- Activity sidebar rows show the Capsule's harness, and the selected Capsule
  lists its sessions (native terminal and structured Threads) with their state.
  Idle and paused status dots are hollow; attention pulses.
- The Capsule workspace has a one-line header with the harness, state, Project,
  and an actions menu that holds Pause, Resume, Seal, and Delete. Sessions open
  as tabs in the primary surface, and a Capsule without sessions opens on the
  new-session form. Moments moved to the Activity tool.
- The Capsule tools toggle no longer overlaps the header's state badge.
- The Activity view is an inbox. Capsules that need attention or are ready for
  review come first with direct actions: open the waiting session, see a
  failure and open the Capsule, or open the diff when Git review is supported.
  Working Capsules collapse into a compact list and Idle, Paused, and Sealed
  into one summary. An "All caught up" state still offers to start work.
- A status bar along the bottom of the shell shows browser and daemon
  connectivity, the provider version, and attention, working, and review
  counts. With the fake provider it warns that Capsules are simulated and no
  agents run. It replaces the sidebar's connection indicator.

### Fixed

- Deleting a Capsule with a running agent no longer fails with "resource
  version conflict: cancel the active Run or Thread before deleting the
  Capsule". The Delete confirmation lists each running session with a Stop
  button and enables Delete once they have stopped. A running native terminal
  also has its own Stop control, with confirmation, in its session heading.
- Between 721 and 1040 pixels wide, where Capsule tools open as a drawer over
  the workspace, the Capsule header keeps Ship and the Capsule actions menu
  clear of the drawer. Before, the drawer covered them, so Pause, Seal, and
  Delete couldn't be reached without closing it.
- "Give it a task" is offered only for a harness pack that can run a
  structured session. No official pack can: Claude Code, Codex, OpenCode, and
  Pi install only native terminal profiles, and the New Capsule dialog now
  shows just the terminal launcher for them. Before, a task for one of these
  packs provisioned a full Capsule whose session failed at once with "Capsule
  runtime rejected structured session start". The daemon now refuses such a
  request with `422 unsupported` before provisioning anything. The
  capabilities `harnessImages` catalog carries a read-only `interactionModes`
  list for packs whose profiles the installation knows. `mock` and custom
  images declare none and are unchanged, because their profiles come from the
  repository.
- The Capsule workspace offers a new structured session only when the
  Capsule's own harness profiles include one. Capsules whose pack installs only
  native terminal profiles, which includes every official pack, no longer show
  a new-session tab that cannot start.
- `meridiand` no longer exits at startup when a Capsule recorded as Ready lost
  its container outside Meridian (for example after a Docker restart). Startup
  recovery fails that Capsule's Run, as ADR 0007 requires, instead of aborting.
- The Seal and Delete confirmation in the Capsule workspace is no longer
  clipped by the Capsule header. It receives focus on "Keep Capsule" and closes
  with Escape.

## [0.1.1] - 2026-09-27

### Changed

- `meridiand` defaults to `--provider=docker` and exits at startup with a clear
  error when it cannot reach Docker. `--provider=fake` simulates Capsules for
  tests and UI development only, must be requested explicitly, and logs a
  warning. The `meridiand` image's default command uses the Docker provider.
- The installation harness catalog lists the official agents first and `mock`
  last, so the browser and terminal dashboard preselect a real agent. The
  browser labels `mock` as a test harness with no agent.

## [0.1.0] - 2026-09-27

### Added

- Activity sidebar rows show what each Capsule is doing and for how long, and
  the Activity view groups Capsules into Needs attention, Working, Ready for
  review, Idle, Paused, and Sealed instead of listing internal resource fields.
- Threads report a content-free `awaiting` flag (`permission` or `input`) while
  an active structured session is blocked on the operator. It is derived in
  memory and never persisted in plaintext (ADR 0025).
- `meridiand --runtime-refresh-interval` (default 5s) refreshes live Run state
  and ingests structured Thread output while no client is watching.
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

### Fixed

- The Activity view reported each Capsule's oldest Run as its latest.
- Adapter heartbeats no longer count as Capsule activity for idle pausing.

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

[Unreleased]: https://github.com/OrlojHQ/meridian/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/OrlojHQ/meridian/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/OrlojHQ/meridian/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/OrlojHQ/meridian/releases/tag/v0.1.0
