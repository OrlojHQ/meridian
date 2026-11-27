# Changelog

All notable changes to Meridian will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Meridian uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Deleting a lead Capsule now deletes the subagents it started, on every
  runtime (ADR 0029). The lead's deletion used to revoke its grant and only
  pause the subagents, leaving them behind. Each subagent Capsule, idle,
  working, paused or failed, is now deleted as a forced Capsule delete would
  delete it: its sessions end, its Capsule and workspace go, and its
  encrypted transcript is kept. Promoted subagents are kept. The daemon
  retries this every coordination tick, so a restart only delays it, and the
  delete confirmation for a lead says so.

### Fixed

- The Helm chart sets `podSecurityContext.fsGroupChangePolicy:
  OnRootMismatch`. Without it, Kubernetes re-applied the fsGroup on every
  control-plane Pod start, which opened the 0700 key and token directories
  and their 0600 files to the group, so `meridiand` refused to start
  ("API token directory permissions must be 0700") after any restart or
  `helm upgrade`. The chart schema now requires `OnRootMismatch`. An
  installation that is already crashlooping also needs its data directory
  modes repaired once; see "Repair data directory modes" in
  `docs/operations.md`.
- The chart's NetworkPolicy admits the control plane, and lets Capsules
  reach it, on the daemon's 8080 Pod port. It used `service.port`, so any
  other Service port cut off the control plane.
- The Kata validation script no longer fails on a cluster that is working
  correctly. Plain HTTP to `capsuled`'s TLS listener gets a 400, which no
  longer passes as a response. A CNI that rejects denied packets instead of
  dropping them (kube-router on k3s) passes, once Capsule B shows its own
  7778 listener is up. The script also stops its `kubectl port-forward` on
  exit, and a missing Capsule pod fails with a message instead of ending the
  run silently. While it waits on a Capsule, a failed API call is retried
  until the deadline rather than ending the run. If the port-forward itself
  has exited, which usually means the control-plane Pod restarted, the run
  fails and says so; it never reconnects behind the operator's back.
- The chart's `appVersion` follows the release (0.18.1), not `0.8.0-dev`.
- Git works in Agent Sandbox Capsules. Kubernetes applies the fsGroup only to
  the workspace volume root's group, so `/workspace` stayed owned by root and
  git refused the repository as "dubious ownership": in the harness and
  terminals, in Delivery, and silently in `capsuled`'s own HEAD and branch
  reads. `capsuled` now trusts exactly its workspace path as a git
  `safe.directory`, never `*`, and Delivery passes the same trust on its
  scrubbed git commands. A Capsule worked around with a global
  `safe.directory` needs nothing removed. Custom Capsule images need a
  `capsuled` from this release or later.

## [0.18.1] - 2026-11-26

### Changed

- An in-cluster Agent Sandbox control plane reaches `capsuled` on the pod IP
  over mutual TLS instead of Kubernetes port-forward (ADR 0065). Each
  Capsule gets its own server and client certificates in a `<sandbox>-tls`
  Secret; the Pod never receives the client key, and `meridiand` pins the
  Capsule's server certificate, so a reused pod IP fails the handshake. The
  chart's NetworkPolicy admits only the control plane on TCP 7778. Existing
  Sandboxes move over on their next official-pack image upgrade. Custom
  Capsule images need a `capsuled` from this release or later. A daemon
  outside the cluster keeps port-forward and refuses to start with
  `isolationClass: microvm`.

### Fixed

- Capsules on Kata (`isolationClass: microvm`) no longer depend on
  Kubernetes port-forward, which cannot reach a process inside a Kata guest:
  the first prepare call failed with `EOF` on every Kata hypervisor. The Kata
  profile stays marked not validated until the operator validation passes on
  a real Kata cluster with the new transport.
- `meridiand installation-token` prints the installation API token, so
  operators can copy it out of the distroless release image with
  `kubectl exec`; the image has no `cat`. The Kata and Kind Agent Sandbox
  validation scripts and the chart docs use it.

## [0.18.0] - 2026-11-25

### Added

- Settings > Appearance sets **Contrast** (Default or Strong), **Depth**
  (Flat, Soft, Glow, or Glass), the interface **Font** and **Code font**,
  and **Font size** (12 to 18px). **Light theme** and **Dark theme** each
  take a preset or a custom background, foreground, and accent, with a
  live preview and a warning when text contrast drops below 4.5:1. The
  rest of the palette is derived from those three colors. The terminal
  follows the code font and font size. Choices are remembered on this
  device.
- An official-pack Capsule upgrades to the release's image when it is
  paused or idle, so a Meridian update reaches a standing lead without
  recreating the Capsule (ADR 0064). `/home/capsule` is a persistent
  volume, so the harness session survives the swap and a Docker or Pod
  restart. Short-lived GitHub App tokens, wrappers, and clone helpers
  stay on an ephemeral `/run/meridian-session` mount. On Docker,
  `meridiand` pre-pulls the official pack images a Project already
  allows. A Capsule mid-turn, with a running subagent, an open terminal
  or preview, or unread subagent reports, is left until it is idle. The
  background pass also skips a Ready Capsule used in the last 15
  minutes. A message sent while an upgrade is in progress waits and is
  delivered when it finishes. The existing workspace stays mounted. If
  the new image does not become ready, the previous runtime is started
  again. A resumed session receives the current coordination brief.
  When the harness cannot resume the saved session, that Thread starts
  a fresh session once, the transcript notes "Upgraded Capsule from
  vX.Y.Z to vA.B.C — harness started a fresh session", and the prompts
  from the failed resume are sent again. The header shows **Upgrading…**
  while it runs. **Upgrade to vX.Y.Z**, or `meridian capsule upgrade`,
  starts one as soon as the Capsule is idle. Custom images, custom
  tags, and digest pins stay on the image they were created with. A
  Capsule created before this release starts one fresh harness session
  on its first upgrade. Migration
  `0043_capsule_upgrade_maintenance.sql`.

### Changed

- **Settings → Updates** shows the desktop app's version and how it was
  installed, the Station's `meridiand` version (flagged when it differs from
  the app), a link to the release notes, and a **Copy version info** button
  for bug reports. It shows what the background check already found, with
  when it last checked, instead of waiting for another click, and
  **Remind me later** can be undone. The fixed schedule rows and the Capsule
  runtime row are gone; runtime isolation stays under Advanced.
  `/capabilities` adds `build` (version, commit, build time), and the desktop
  bridge adds `desktop_app_info`.
- The Project page and the note review page are centered in the main
  area, so a wide window leaves the same space on both sides. The Project
  column is at most 48rem. The note review stays at most 56rem, wide
  enough for a note diff.
- While a subagent is working or starting, the **Agents** pill replaces
  its count with a spinner and **N working**. The button's name also
  says how many need you, as in "3 agents, 2 working, 1 need you". The
  panel's title and segmented tabs become one header row of underline
  tabs, **Subagents** and **Notes**, beside the grant state and its
  tools. A running subagent's spinner sits in its phase pill.
  **Reported** stands for one done report. A second report adds the
  count, and any other status is named on the detail line. On the Notes
  tab the budget line sits beside **New note**. Project notes keep
  their heading.

### Fixed

- System and Dark themes render the same dark palette. Dialogs outside
  the main layout no longer use slightly different colors under System.
- A failed update check in **Settings → Updates** no longer shows
  "Up to date".
- **Create GitHub App** reached GitHub with the setup state on the
  manifest's `redirect_url`, and GitHub rejected that URL because it had
  a query. The state now goes on the form action, and `redirect_url` has
  no query (ADR 0061). The missing-App warning stays one sentence.
- A single-user Station opened from another machine returned 401, and
  Home showed **Bring your setup** with no way to enter the installation
  token. Any 401 now shows the sign-in screen: that token form in
  single-user mode, which exchanges the token for the session cookie,
  and team sign-in in team mode. The form waits until the auth mode is
  known, so a team Station does not flash the token form. A signed-in
  browser on the Station's own origin can attach a terminal or a run.
  Preview ingress stays on loopback.

## [0.17.0] - 2026-11-24

### Added

- A lead chooses the model and harness for each subagent, and **Worker
  Model** on the lead's model menu sets the default for new ones (ADR 0031).
  `meridian-coord spawn` takes optional `--model` and `--harness`, and
  `meridian_spawn` takes the same fields. Omitting both uses the Worker
  Model. It starts as **Match**, the lead's current harness and model.
  **Custom** stores a model, and a harness when the lead allows more than
  its own, so the lead can stay on one model while new subagents use
  another. The choice is kept on the lead. A model or harness on a single
  spawn overrides it. A harness the lead is not allowed to use is refused
  (`harness_not_allowed`), and the error names the harnesses that are. A
  model a live session of that harness does not offer is refused
  (`model_unavailable`), and the error names the models it does offer. A
  model the lead or you named is applied before the subagent's first
  prompt, at that model's default effort: the prompt waits until the
  harness reports its models and switches, and the spawn fails if it
  cannot, instead of quietly using another model. **Match** is best effort.
  When the subagent's harness cannot run the lead's model, the subagent
  starts on that harness's default, and the Agents list and the lead see
  that model rather than the lead's. A spawn onto another harness with no
  model uses that harness's default model. New leads allow only their own
  harness. **Allowed harnesses** in Subagent settings adds others, and the
  harness new workers use stays selected. The lead brief, `meridian_whoami`,
  and the `meridian_spawn` description list the allowed harnesses, the
  models each has reported, and the current default. The Agents list and a
  subagent's Thread header show its harness and model. Migrations
  `0041_worker_model.sql` and `0042_worker_model_fallback.sql`.

### Changed

- Settings uses one layout on every tab. Each tab has its own heading and
  description, switches are the same control, and changing tabs starts at
  the top. Appearance picks System, Light, or Dark from a preview of each.
  Projects has a project menu and opens that project's editors at full
  width. Agents says which lead you are editing, or that the controls are
  the defaults until you open a lead Thread. Updates shows whether the
  desktop app is current and when it last checked. **Update & restart**
  shows download and install progress. **Remind me later** still hides that
  release for 24 hours. An update that is a command is shown with a button
  that copies it. The same tab lists the check schedule, the connected
  Station, and the Capsule runtime. In a browser it says that checks and
  one-click installs run in the desktop app.

### Fixed

- In the desktop app, links that open another site in a new tab, such as
  Open Google sign-in for Antigravity, did nothing. They now open in the
  system browser and the Meridian window stays where it was.
- Antigravity shows Google's full-color mark everywhere — the harness list,
  Capsules, Threads, and pickers — not only on its provider connection.
- An Antigravity (`agy`) lead no longer stops for an operator approval on
  every Meridian coordination tool call (`meridian_whoami`, `meridian_spawn`,
  `meridian_wait`, and the rest). The ACP adapter allows calls that `agy`
  names as tools of the profile's own MCP servers, as the Claude Code and
  Codex adapters already do. "Approve each spawn" still holds each new
  subagent for you. Other tool calls still ask (ADR 0063).
- Finishing a provider sign-in from the add-connection form opened that
  sign-in again, as if another one were required. A finished sign-in now
  offers **Sign in with another account**. Cancel still returns to the
  sign-in.

## [0.16.0] - 2026-11-24

### Added

- An official `agy` harness pack runs Google's Antigravity CLI 1.5.14 in
  native terminal sessions and in Threads through `agy acp`, with the
  coordination tools for leads.
- Sign in with Google for Antigravity (ADR 0062). A sign-in Capsule runs
  `agy`'s own login; Meridian keeps the resulting sign-in sealed and copies
  it into each `agy` Capsule of a Project that authorizes the connection,
  so no Capsule needs its own sign-in. The dashboard explains that code in
  those Capsules can read it, and links to Google's page that removes
  Antigravity's access everywhere. After copying the code on Google's page,
  one click on Paste from clipboard finishes the sign-in. The dashboard
  shows Antigravity's mark for the harness and the connection.
- Settings opens from a cog at the bottom of the sidebar, and from Ctrl+, or
  Cmd+,. The settings that were spread across the app — theme, harness and
  provider defaults, projects, routines, account, updates — are in that one
  window. A new setting is a registry entry. Switches and menus save
  immediately; a form with several fields keeps its Save button. Existing
  values carry over.
- A Station can register a GitHub App so a Capsule can use `gh` and git for
  one repository without the owner's login entering the Capsule (ADR 0061,
  #210). Create the App under Settings → Account → GitHub App, install it
  on the account or organization and include the repositories (or choose
  All repositories), then choose Refresh installations. Settings does not
  refresh on its own. Start a new Capsule from a current image; one already
  running keeps its previous supervisor and does not see the token. The App
  private key stays on the Station. A Capsule session receives a short-lived
  installation token for its one repository, refreshed before the hour is
  up. **Give Capsules a GitHub App token** under Settings → General stays on
  unless you turn it off, and the next session follows it. The Project page
  says when the App is missing or not installed. Clone and the Git gateway
  prefer that token when the App covers the repository. A public HTTPS
  Station can receive App webhooks that wake the existing CI poll. On a
  team Station only an administrator can create, remove, or refresh the
  App. Migration `0040_github_app.sql`.

### Changed

- Commands moves to the sidebar header, next to the name, with its Ctrl+K or
  Cmd+K hint. The theme cycle leaves the footer. Theme is changed in
  Settings, and the saved `meridian.theme` choice still applies.
- Harness settings, Account, and Routines in the sidebar open the matching
  Settings section. The old addresses open that section too.
- When a coordination tool call has an unknown argument, the error now lists
  the arguments that tool takes, such as `meridian_send takes: link
  (required), prompt (required), key.` A harness that guessed a field name
  without loading the schema can retry with the right one.
- The README shows each supported agent's mark under the tagline and beside
  each row of Supported agents.

### Security

- OpenTelemetry Go is 1.47.0. This resolves GO-2026-6974 (CVE-2026-128085),
  where a span or log attribute made of U+FFFD characters could bypass
  `AttributeValueLengthLimit` and be kept in full. Tracing stays off unless
  an OTLP endpoint is set, and Meridian does not set that limit.

## [0.15.2] - 2026-11-21

### Fixed

- A subagent's report no longer wakes its lead twice. The closing reply a
  subagent writes after `meridian_report` became a second `turn_ended`
  report with the same content, and the link lost its `done` status. That
  reply now counts as a second report only when the lead or the operator
  sent the subagent something after the report.
- A lead wake no longer lists a report as "still unread" when an earlier
  wake already showed the whole report.

### Changed

- The lead brief, the `meridian_send` description, and the Pi skill say a
  subagent that reported is idle and keeps its context, so a lead sends it a
  follow-up instead of spawning a new subagent.

## [0.15.1] - 2026-11-21

### Fixed

- The desktop app no longer gets stuck showing a Thread as working with the
  Send button disabled and the terminal stuck loading. The web UI reopened
  a Thread's live transcript stream for every streamed block, and the
  desktop webview (WebKit) sometimes kept an aborted stream's connection
  open. After a few turns those leaked streams used up WebKit's six
  connections to the Station, so every later request waited forever. The
  stream now stays open for the whole session, and live streams are
  fetched in a way WebKit closes when they are aborted.
- Delivery inspection for a Capsule whose Project has no repository answers
  that there is no repository (422) instead of failing as an internal error.
  The Capsule header stops polling once it gets that answer, and checks
  again when editing ends in case a repository was created.
- The desktop update prompt appears in a window that stays open. The app
  checks on a wall clock while a Station is open, after the computer wakes,
  and when the window is shown again, instead of only when the Station page
  loads. Remind me later still snoozes that version, and a check leaves an
  install already in progress alone. The rebuilt web UI bundle shipped with
  the desktop app includes this check.

## [0.15.0] - 2026-11-20

### Added

- Working notes and Project notes (ADR 0060). A lead and its subagents now
  share **working notes**: Markdown files any member writes with
  `write-note` (whole note, against the revision it read) and refreshes with
  `sync-notes`, which meridiand stores encrypted, so a finished subagent's
  findings outlive its Capsule. They never leave the lead's tree. You can
  read, edit, delete, restore and shred them from the lead's Agents panel,
  and the lead's next report wake lists which changed. **Project notes**
  reach every Thread of a Project, as files under
  `$HOME/.meridian/project-notes/`, at each session start. Only you write
  them: agents propose a change with `propose-note`, or you promote a working
  note, and you review each proposal as a source diff with hidden characters
  shown, approving one exact change at a time. `notesDelivery` chooses all
  Threads (the default), leads only, or none; a new Thread can opt out, and a
  routine delivers them only with `includeNotes`. Each harness is told about
  the files through a short index of names and numbers. Notes are bounded
  (32 KiB per note, 32 notes and 128 KiB per Project or tree), keep 50
  revisions, and are covered by key rotation, backup verification,
  crypto-shred, Project deletion and owner transfer. The API, `meridian
  notes`, `meridian grant notes`, the MCP tools, `meridian-coord` and the Pi
  skill gain the verbs.
- Leads can attach their notes to a subagent's task instead of retyping
  shared conventions, decisions and findings into every prompt. `spawn`
  takes `contextFiles` (`--context-file` in `meridian-coord`, repeatable):
  up to 8 files under `$HOME/.meridian/notes/` in the lead's Capsule. The
  lead's Capsule appends each one to the prompt as a fenced, labelled block,
  so Meridian sees an ordinary prompt with the same `from_lead` provenance
  and stores nothing new. The prompt with its notes must still fit 128 KiB;
  a spawn that does not fit is refused as `context_too_large` rather than
  cut. Paths outside the notes directory, symlinks out of it, and files that
  are not regular UTF-8 text are refused. The lead brief and the Pi skill
  mention it (ADR 0059).
- Make an existing Thread a lead. A plain Thread whose conversation turned
  out to be worth splitting up can now start subagents: choose **Make this a
  lead** in its actions menu, or run `meridian thread lead THREAD_ID` with
  the usual lead limit flags. The agent restarts its session to pick up the
  role, at once when it is idle and otherwise before your next message, and
  keeps its conversation. A running turn is never interrupted, and nothing
  is sent in your name. The Thread's Agents button appears as soon as the
  grant exists. Only you can do this; subagents, promoted subagents that
  kept their link, and Threads whose grant was revoked are refused. Plain
  Threads stay plain unless you ask. The API gains
  `POST /threads/{threadId}/coordination-grant`, and OpenCode and DeepSeek
  Harness leads are briefed again after a resume whose role changed
  (ADR 0059).
- Pin the Threads you come back to. A pinned Thread, usually a lead you keep
  for one part of a large repository, sits above the Project's other Threads
  in the sidebar, ordered by name rather than recency, and keeps its
  subagent rollup. Pin as many as you like, from the pin on the sidebar row
  or the Thread's actions menu. The New Capsule dialog offers "Send to" a
  pinned Thread next to starting a new one, and starts a new Thread unless
  you pick one. Sending is an ordinary message to that Thread, so it resumes
  a paused lead's grant. Plain Threads can be pinned too; subagents cannot
  until promoted. A pin is presentation only and grants nothing. Archiving
  or crypto-shredding the Thread, or deleting its Project, removes it. The
  API gains `POST /threads/{threadId}/pin` and `/unpin` and a `pinnedAt`
  field, and the CLI gains `meridian thread pin` and `meridian thread unpin`
  (ADR 0059).
- Plain Threads show how full the agent's context window is. Once it is half
  full, the Thread toolbar shows a small meter ("62% of 200k"), so you can
  compact with the harness's own command in the `/` menu or start fresh.
  Leads show no meter, so a long lead conversation reads as one continuous
  conversation. The figure is in Thread details for both. Where the harness
  compacted the conversation, the transcript shows a quiet divider ("Earlier
  conversation was summarized by Claude Code"), so an agent that forgets
  something earlier has a visible reason. Claude Code, Codex, OpenCode,
  DeepSeek Harness and Pi report the numbers. Claude Code, Codex and Pi also
  report compactions. A harness that reports nothing shows no meter. The
  figure is the input size of the agent's most recent model call, not what
  the session has spent in total. Meridian keeps only the latest figure, in
  memory, and does not compact anything itself. Adapters gain a negotiated
  `context_usage` frame, and the Thread API gains `contextUsage` and a
  `context_usage` transcript event for compactions (ADR 0059).

### Changed

- A lead no longer stops coordinating 72 hours after it started. Its grant
  now pauses after a stretch without you (24 hours by default, 5 minutes to
  3 days), and your next message to the lead resumes it. While paused, the
  lead cannot start or message subagents, running subagents are cancelled
  and paused as at the old deadline (or keep running with `stop_new_work`),
  and the lead is not woken with reports. Only your own actions count:
  messaging the lead, approving a spawn, or changing the grant. The subagent
  limits count spawns in any 24 hours instead of over the lead's lifetime.
  The lead's Agents panel shows spawns in the last 24 hours and when it
  pauses, and lets you change how long it runs without you.
  `meridian thread spawn --lead` and `meridian grant update` take
  `--unattended-limit`; `--lead-deadline` still works as a deprecated alias.
  The API replaces `deadlineSeconds` and `deadlineAt` with
  `unattendedLimitSeconds`, `lastOperatorAt`, `lapsesAt`, and
  `spawnsInWindow`, adds the `lapsed` grant state, and refuses
  `deadlineSeconds`. Existing leads keep the time they had left (ADR 0059).
- A lead's transcript no longer repeats subagent reports in a card. Where
  Meridian woke the lead, one quiet line names the subagents, each linked
  to its Thread ("↳ Reports from subagent-1, subagent-2 failed"), shows a
  status only when a report did not finish normally, and expands to the
  exact wake prompt the lead received. The lead's reply, the subagent's
  Thread and the Agents panel already carry the rest.
- Subagent reports no longer fill up a lead's conversation. When reports
  arrive, the lead is woken with short headlines (name, link ID, status,
  diffstat and the first 2 KiB of each report, at most 16 reports and 16 KiB)
  instead of full reports, and reads a full report with `result` when it
  needs it. `send`, `cancel`, `status`, `patch`, `result` and `wait` take a
  subagent's name as well as its link ID, resolved only among the lead's own
  subagents. A
  report counts as read only when the lead reads it, and a headline the lead
  has not read does not wake it again. `patch` and `result` take `toFile`
  (`--to-file` in `meridian-coord`), which writes the patch or report to a
  private file under `$HOME/.meridian/coord/` in the lead's Capsule, outside
  `/workspace`, and returns its path, size, HEAD, base commit and diffstat.
  These files are capped at 16 MiB per lead, oldest removed first. The lead
  brief now tells leads to check diffstats first, read large output from
  files, hand large reviews to a subagent, and keep running notes in
  `$HOME/.meridian/notes/` (ADR 0059).
- The Routines editor picks a time zone from a searchable list. Each zone
  shows its current offset and local time, your zone is listed first, and
  the schedule line shows the next run in the routine's zone and in yours
  when they differ.
- The desktop update button shows a spinner beside "Downloading…" while an
  update is in flight, and a percentage when the shell reports download
  progress. Once the download has finished and the app is installing, the
  same spinner sits beside "Restarting…". The button stays disabled, keeps
  its width, and is marked busy for assistive technology. With reduced
  motion, the spinner stays a static indicator.

### Fixed

- The Thread composer's effort button keeps the level the session is running
  with after the first message and after a model switch. A level the new
  model does not offer is clamped to one it does. When no level is known at
  all, the button says Default instead of Effort.

## [0.14.0] - 2026-11-19

### Added

- Lead Threads delegate on their own. When a session starts or resumes,
  Meridian tells the harness whether it is a lead or a subagent, through each
  harness's own system-prompt channel. A lead hands independent or
  long-running work to subagents by default, then tells you and ends its turn
  instead of blocking; a subagent works on its own and finishes with a report.
  "Lead agent with subagents" is now checked by default when you start a
  Thread, and unchecking it still starts a plain Thread.

### Changed

- A Thread started without a name is named from its first message, so the
  sidebar, Needs you, Recent, and notifications show the same title as its
  header instead of `thread-<id>`. The web UI renames the Capsule once, only
  while it still has a generated name; `meridian thread spawn` without
  `--name` does the same from the prompt. The name is plaintext metadata,
  unlike the encrypted transcript.
- Session tabs and sidebar rows say what a session is, Terminal or Thread,
  with the harness as the icon, instead of repeating the harness name.
- A Capsule stays the kind you chose when you made it. A CLI Capsule shows
  its terminal and no longer offers a Thread; a Thread Capsule shows one
  Thread. The **+** session tab is gone.
- A Thread Capsule runs one Thread session at a time. Earlier sessions sit
  above the current transcript, folded until you open them, with a divider
  saying the agent starts without them. Start fresh replaces New session: it
  stops a live session after asking, then starts a new Thread in the same
  Capsule with the same harness.
- The Thread toolbar no longer shows **Live** while updates are arriving. Its
  badge appears only when the transcript may be out of date: **Reconnecting…**
  while the stream retries, and **Disconnected** (formerly Closed) once it
  gives up. **Session ended** is unchanged.
- Claude Code's request retries show in the Thread as one notice that updates
  with each attempt, such as "Model request failed: the model provider could
  not be reached. Retrying, attempt 3 of 10." This needs a Capsule built from
  the new image.
- Capsule tools start closed, so a Thread or Capsule opens on the conversation
  at full width. The tools toggle and the Changes, Preview, Files, and
  Terminal buttons still open them. Full review and Open terminal still open
  the tool they name. A reload or a new Thread starts closed again. A panel
  width you dragged is still remembered.

### Fixed

- A failing GitHub CI check that was due for rework could be skipped for a
  poll: its next poll time was stored in a form that sorts wrongly as text
  when its fraction of a second ends in zero.
- A Thread you come back to after a day, or after Meridian restarts, takes
  your next prompt instead of sitting on Working. Before delivering it,
  Meridian resumes the idle harness's session with a fresh provider gateway
  lease, certificate, and address (ADR 0058). You no longer have to stop and
  resume the session, or start a new Capsule.
- The Terminal tool uses a prompt icon, in the tool tabs and on the collapsed
  tool rail, at the same size and weight as Changes, Preview, and Files.

## [0.13.0] - 2026-11-19

### Added

- Routines start a fresh Capsule and Thread with a fixed prompt on a cron
  clock (ADR 0057). Pick the harness, model, effort, prompt, and time zone on
  the new Routines page or with `meridian routine`. A run with no changes deletes
  its Capsule and keeps the transcript. A run with changes, a question, a
  failure, or a timeout is kept and shows under Needs you. Runs never
  overlap, stop at a time limit, catch up once after downtime, and skip while
  too many results wait for you. A routine never ships: Delivery still
  needs your approval. `--max-live-capsules` now also skips routine runs.

### Changed

- The Shell panel terminal is compact: 13px monospace, tighter line height,
  and shorter Shell and Connected rows. Harness output is a small button.
  An 8px inset stays outside the fitted columns and rows, in dark and light.

### Fixed

- The session tab row stays fully visible between the thread header and
  Thread details. A short window no longer tucks that row under the details
  bar. The transcript is still the only vertical scroller.

## [0.12.0] - 2026-11-18

### Added

- Subagents can open as tabs on the lead Capsule's page. Open tabs survive
  reload and stay open when you switch sessions. Closing a tab leaves the
  subagent running. Changes, Preview, and Files follow the selected
  subagent's Capsule and name whose tools they show; the page header and
  pull request controls stay with the lead. Each subagent still links to
  its own Capsule page.
- Saved Stations keep a name and an address, with no cap. The launch screen
  and the tray switch by those names. The sidebar names the open Station and
  opens that picker; ⌘1–⌘9 (Ctrl+1–9 elsewhere) switch to the first nine. A
  Station's page never receives your other Stations' names, addresses, or
  status. An unsent Thread message asks before a switch reloads the window.
  A host and port is enough; HTTPS is assumed. Status dots in the picker come
  from one check at a time, with a few workers and a three-second limit; a
  Station it cannot reach in time shows no dot.
- The Thread composer can attach files and images. A **+** button, drag and
  drop, and pasting an image add chips above the message. Images are sent as
  image content when the harness says it can take them. Other files are
  copied into the Capsule workspace and named by path. Size and type limits
  fail with a short explanation, and image attach stays hidden when the
  harness cannot take images. The transcript keeps the file's name and path,
  not its bytes, and shows a chip after reload. Attachments stay out of
  `git status` and are never included in a delivery commit.

### Changed

- The Thread composer focus state is a slightly lighter 1px border, in dark
  and light. The thick offset ring and outer glow are gone, and the message
  field does not draw a second outline. Text fields that used that ring,
  including the command palette, the activity filter, and settings inputs,
  use the same border.
- Stations you used before are kept and named by their host, instead of
  remembering only the last five addresses.
- "Check this computer for setup changes" is in Harness settings. It stays
  on unless you turn it off, and nothing is uploaded until you click Save.

### Fixed

- The Thread column scrolls in one place. The transcript is the only
  vertical scroller, the composer stays pinned under it, and a long tool
  line no longer opens a second pair of scrollbars around the column. A
  code block still scrolls sideways inside itself. The Terminal fills the
  same column without scrolling the page. On a short window the transcript
  keeps some height, and a long draft scrolls inside the message box.
- A release whose macOS desktop job is cancelled before a runner accepts it
  is retried automatically, up to three attempts. The retry runs only when
  that job never started and its annotation says it was not acquired. A job
  that received a runner and then failed stays failed, and the desktop
  manifest job runs again with the retried build.
- The Capsule header no longer asks to update a setup when the pinned
  revision and commit identity already match the profile. Files an import
  left out, or kept with a warning, stay listed in Setup details and no
  longer keep that banner up.
- The Capsule header shows Create PR only when a delivery would publish
  something: commits ahead of the default branch's remote head, or
  uncommitted project files. A clean checkout, a branch with no commits of
  its own, and files the supervisor writes into the worktree no longer show
  the button. Push update stays only when an existing pull request has
  commits or file changes that are not on the remote branch yet, and a
  delivery push records the pushed commit so the button clears once the
  push succeeds. A project `.gitignore` cannot pull supervisor files back
  into a delivery commit. Inspection
  reports `aheadCount`, `unpushedCount`, and `hasDeliverableChanges`, and
  the header reads them again while a turn is writing and once the turn
  finishes.
- The model picker hides a `meridian/` provider prefix on a reported model
  label, the same way it hides `meridian · `. Other providers keep their
  prefix, and a list from several providers still groups by provider. A
  slash inside a model's own name, such as `Model X (4bit/8bit)`, does not
  make a group. Search still matches the full id and label.

## [0.11.5] - 2026-11-18

### Security

- Listing a saved connection's models sends its saved key only to its saved
  endpoint. A different base URL needs its own key, and private-network HTTP
  needs the installation opt-in on this path too.
- An automatic `thread-` Capsule name is no longer replaced with text from
  the first message. The name is plaintext and the transcript is encrypted, so
  the Station does not copy one into the other. The Capsule page still shows a
  title from the first message without saving it; click it to save a name.
  Reading a Capsule, a Capsule list, or the board no longer writes to it.
- A session error from the harness is shown only when it does not look like
  it carries a credential, such as a token, key assignment, bearer header, or
  long token string.

### Fixed

- A stop the harness never answers returns 409 `harness_unresponsive` with
  the explanation, instead of a 500 with no message. The session is still
  ended.
- A forced Capsule delete checks the version and that the Capsule can be
  deleted before it stops any session. A replay does not stop sessions again.
- The workspace shell no longer drops output for a viewer that reads slowly.
  A viewer that falls more than 1 MiB behind is disconnected instead.
- Opening, attaching to, and typing in the workspace shell count as Capsule
  activity, so the idle scan does not pause a Capsule in use from its
  Terminal.
- The GitHub CLI login check uses the active github.com account, so a stale
  second account or another host no longer hides a working login. A `gh`
  that does not answer is reported as such, not as signed out or missing. A
  login-shell PATH lookup that fails is retried after a minute instead of
  never.
- `make opencode-binary` explains that the pinned OpenCode build is
  Linux-only, and checks the archive with `shasum` when `sha256sum` is
  missing.
- OpenCode Capsules start again. Generated model variants are written as the
  list OpenCode 2.2.6 expects, and a saved connection with the old variants
  map is converted when it is imported. Before, OpenCode dropped the whole
  provider and every new session failed with "No models are available".
- When the harness cannot start a session, its message is saved on the run
  and shown in the thread instead of a generic adapter error.
- Resuming a thread whose first session never finished starting opens a new
  session instead of failing with "Thread has no adapter resume state".
- Harness marks in the New Capsule picker stay crisp and readable in dark
  mode. The selected-item check no longer restyles the last icon in the row.

## [0.11.4] - 2026-11-17

### Fixed

- An imported OpenCode provider whose key comes from an environment variable, a file, or is missing is no longer copied into the Capsule. When its endpoint matches a provider connection, its models run through the gateway under that provider's own name, in one group. Otherwise it is left out of the model list, and the import report says it needs a connection. A turn that fails because a provider has no key names that provider and asks to connect it in Settings. A connection's default model is chosen from the endpoint's model list, with imported names, instead of a typed id. Reasoning effort is detected from the endpoint and the imported setup: a model that reasons offers the standard levels, starting at medium, and a rejected reasoning parameter is retried once without it. A turn or model change that gets no reply ends with an error. Stopping a session ends it even when the harness ignores the cancel, and the stop shows its progress and any error. Deleting a Capsule can force-stop a session that is still running and then delete.
- The Capsule header names only the setup for the harness that Capsule runs, with the revision time in local time. The note that Settings edits apply to the next Capsule appears only after that setup changes.
- The Terminal tab opens a shell in the Capsule workspace instead of retrying a harness terminal that does not exist. A harness terminal remains available as Harness output when that run has one. A permanent attach failure is shown once, not retried.

### Changed

- The Capsule header uses one row: a title, the branch, the saved setup for
  the harness this Capsule runs, and a status only
  when the session is working, needs input, or failed. An automatic
  `thread-` id is shown as a short title from the first message. Click the
  title to rename the Capsule (`POST /capsules/{capsuleId}/name`).
- Setup comparison moves to the Capsule actions menu as Setup details. When
  the Capsule differs from the current profile, the header offers to update it.
- Create PR replaces Ship. The sheet summarizes the change, fills the branch,
  title, and description from the thread, and confirms in one click. The
  reviewed HEAD and tree are still sent, and stay in a collapsed row. After a
  pull request exists, the header links to it and can push an update.
- A Capsule is started from its Project. The sidebar's main button creates a
  Project. Each Project row and the Project page open a New Capsule dialog
  already scoped to that Project. Ctrl+N (⌘N on macOS) does the same for the
  open Project, and asks which Project only when none is open. The dialog
  shows a harness dropdown, Thread or CLI, and an optional lead agent.
- An existing Capsule whose name is still an automatic `thread-` id is given
  a short title from its first message the next time it is read. A name the
  operator set is left as they wrote it. The title is stored on the Capsule,
  so the sidebar and Inbox show it without reading the transcript again.

## [0.11.3] - 2026-11-17

### Fixed

- A private GitHub clone with an active connection failed as
  `repository_access`. The Station did pass the login, but the clone helper
  was written on the Capsule's `noexec` `/tmp`, so git never received it.
  The helper is now written under the Capsule home directory, which is
  executable. A connection that cannot be opened is not treated as an
  anonymous clone.
- A Capsule that failed because it could not read a repository is retried
  once when its owner connects or replaces GitHub. The failure notice offers
  Retry once GitHub is connected, and Connect GitHub stays on the page.
  Creating a Project keeps what was typed after connecting.

## [0.11.2] - 2026-11-17

### Fixed

- Settings and `meridian github connect` reuse a GitHub CLI login when the
  Station was started from the desktop app or a service whose `PATH` does
  not include the usual install location. The card says whether `gh` is
  signed in, not signed in, unable to read its token, or missing, including
  the path that was searched. A device code is not offered unless an OAuth
  client id is configured. `MERIDIAN_GH_PATH` or `--gh-path` selects the
  binary. The token is not shown.

## [0.11.1] - 2026-11-16

### Upgrade notes

- v0.11.0 was tagged but never published: its release check found that the
  embedded web UI had not been rebuilt for these changes. 0.11.1 is the
  first release with the changes below; upgrade from 0.10.1 directly to it.
- **Back up the data directory first.** Migrations 0031 (GitHub
  connections), 0032 (the `repository_access` failure code), 0033 (Project
  checks), and 0034 (remote CI) run on first start. 0.10.1 refuses a data
  directory that 0.11.1 has migrated, so rolling back means restoring the
  backup. Existing Projects behave as before until you connect GitHub or
  save checks.
- **Pushes now go through the Station Git gateway** (ADR 0054). The push
  credential stays on the Station; the Capsule pushes with a one-time
  capability for the approved commit and branch. Docker installs get this
  from the local gateway listener with no change. Behind
  `--provider-gateway-url`, route `/git-gateway/` to `meridiand` as you
  route `/provider-gateway/`, and on Kubernetes allow the control plane to
  reach your Git host. An installation with no gateway (a remote or rootless
  Docker engine without `--provider-gateway-url`) refuses pushes until one
  is configured, or until `--delivery-push=capsule` restores the in-Capsule
  push.
- A Project that names no `git_https`, `git_push`, or `github_api` secret now
  uses its owner's GitHub connection for clone, push, and pull requests, but
  only for an HTTPS `github.com` repository. A Project on another host still
  needs named secrets, and named secrets still win.
- Remote CI sends failures back to the agent only for a Project that has
  saved its checks settings with automatic rework on. Other Projects show the
  CI result without rework.
- API additions: `GET`/`DELETE /github/connection`,
  `POST /github/connection/check`, `GET`/`POST /github/connection/host`,
  `POST /github/connection/token`, `POST`/`GET`/`DELETE
  /github/connection/device`, and `GET /github/repos`;
  `GET`/`PUT /projects/{projectId}/checks` and
  `GET /projects/{projectId}/checks/suggestion`;
  `GET`/`POST /capsules/{capsuleId}/check-runs`,
  `POST /capsules/{capsuleId}/checks/auto-rework`,
  `GET /check-runs/{checkRunId}`, and `POST /check-runs/{checkRunId}/cancel`;
  `GET /capsules/{capsuleId}/remote-ci`; and
  `DELETE /provider-connections/{connectionId}`. `pkg/client` is
  regenerated.
- New `meridiand` flags: `--github-oauth-client-id` (or
  `MERIDIAN_GITHUB_OAUTH_CLIENT_ID`), needed only for the device-code
  fallback when `gh` is missing or signed out, with no client id built in;
  and `--delivery-push`.
- New commands: `meridian github connect`, `status`, and `disconnect`;
  `meridian project checks show` and `set`; `meridian capsule checks`; and
  `meridian harness disconnect`.
- Keep `meridian` and `meridian-desktop` on the same release, as before.

### Added

- Connect GitHub once (ADR 0051, #197). Home, Settings, and
  `meridian github connect` store one owner-bound login. Clone, push, and
  pull requests use it when a Project does not name its own `git_https`,
  `git_push`, or `github_api` secret. Those secrets still win when they are
  set. When `gh` is signed in, Connect GitHub asks once: "Use your GitHub
  login @user?" That token is stored unchanged, so organization SSO and
  approved-app grants that already apply to `gh` still apply. If it later
  expires or `gh` re-authenticates, a local check refreshes it or shows
  Reconnect GitHub. The device code is only the fallback when `gh` is
  missing or signed out. New
  Project searches repositories, including private ones, and keeps "Use a
  token instead." An invalid, expired, or under-scoped login shows
  Reconnect GitHub. The device flow needs
  `--github-oauth-client-id` or `MERIDIAN_GITHUB_OAUTH_CLIENT_ID`. No client
  id is shipped. Without a named secret the login is used only for an
  HTTPS `github.com` repository (#203).
- A private or inaccessible GitHub repository with no usable credential
  fails once, as `repository_access`, instead of retrying checkout.
  The Capsule and the Thread say which repository could not be read and
  offer Connect GitHub or a token. Creating that Project checks access
  first. Git output is not stored or shown. Migration 0032 allows the
  new failure code (#203).
- After Meridian opens or updates a pull request, it reads that commit's
  GitHub checks and shows them beside the local ones. A failure can be sent
  back to the agent. The fix is not pushed until you review it. See
  [docs/checks.md](docs/checks.md) and ADR 0053. Migration
  `0034_remote_ci.sql` stores the summary. Automatic rework from CI is
  opt-in through the Project's saved checks settings (#206).
- A Project can list required checks, such as `make test`. When a structured
  Thread finishes a turn, Meridian runs them inside the Capsule. A failure is
  sent back to the agent up to the attempt limit, then the Capsule shows
  **Checks failing (n/n)** or **Checks passed**. See [docs/checks.md](docs/checks.md)
  and ADR 0052. Migration `0033_project_checks.sql` adds the tables
  (#198, #205).

### Changed

- An approved push no longer sends the push credential into the Capsule.
  `capsuled` pushes to the Station Git gateway with a one-time capability
  bound to one Delivery's commit, branch, and expected old value.
  `meridiand` checks the push command, adds the credential, and streams the
  pack to the source host unparsed. The Station, not the Capsule, reads the
  remote branch before the push, and the source host checks the expected old
  value atomically. With the GitHub connection of ADR 0051, this keeps the
  owner's whole GitHub login out of every Capsule (#207).
- Tests seed each SQLite database from one migrated template, and CI runs
  test and lint, the race test, and the snapshot release as parallel jobs.
  The race test takes about 4 minutes instead of 15 to 18, and the required
  check is still "Build, test, and lint" (#208).

### Fixed

- The web UI embedded in `meridiand` is rebuilt, so the GitHub connection,
  checks, remote CI, and connection-removal screens ship with this release.
  CI now fails a pull request whose committed `internal/webui/dist` differs
  from a fresh `make ui-build`, instead of the release workflow finding it
  at tag time.
- An imported OpenCode setup whose `enabled_providers` list left out
  `meridian` ignored an `openai-compatible` connection. OpenCode 2.2.6 turns
  that list into a deny-all provider policy. Attaching the connection now
  allows `meridian`, writes the provider in the shape that version loads
  (`providers`, with `package` and `settings`), and keeps that model selected
  in the terminal and in Threads. `meridian import` warns when the imported
  default model still depends on a host environment variable or a server the
  Capsule cannot reach (#202).
- A revoked provider connection can be removed. Revoke still keeps the row,
  so a ChatGPT sign-out can say when the provider did not confirm it.
  **Remove** in Provider connections, `DELETE /provider-connections/{id}`,
  and `meridian harness disconnect ID` then delete a revoked connection,
  its Project grants, and a workstation-profile default that names it. A
  connection that is still signed in stays. A Capsule that is starting,
  ready, or paused, and whose Project still grants it, must be stopped
  first. Another member's connection is not found (#202).

## [0.10.1] - 2026-11-15

### Upgrade notes

- v0.10.0 was tagged but never published: its release build's race tests
  hit Go's default 10-minute limit. 0.10.1 is the first release with the
  changes below; upgrade from 0.9.0 directly to it.
- **Back up the data directory first.** Migration 0029 adds workstation
  profiles and migration 0030 adds `projects.required_isolation`. 0.9.0
  refuses a data directory that 0.10.1 has migrated, so rolling back means
  restoring the backup. Existing Projects read back with no isolation
  requirement, and nobody has a profile until they import one.
- API additions: `GET`/`PUT /profile`, `POST /profile/imports`,
  `GET /capsules/{capsuleId}/profile-diagnostic`, and
  `POST /capsules/{capsuleId}/profile-update`; a required `isolation` field
  on `GET /capabilities`; and `requiredIsolation` on Projects.
  `pkg/client` is regenerated.
- The Helm chart gains `requireIsolation`, `agentSandbox.isolationClass`,
  `agentSandbox.isolationHandlers`, `agentSandbox.startTimeout`, and
  `networkPolicy.kubernetesAPIServerPorts`. Setting `isolationClass` adds one
  ClusterRole that can `get` only the configured RuntimeClass; leaving it
  empty keeps the install namespace-scoped.
- `agentSandbox.startTimeout` (default `10m`) now bounds the Sandbox Ready
  wait during create, which used to share `operationTimeout` (`2m`). The TTL
  must exceed operation plus start plus setup timeouts; raise `ttl` if you
  had set it close to the old sum.
- The desktop app and web UI call a running `meridiand` a Station, and the
  bundled one is "Local". Only text changed: identifiers, bridge commands,
  `gateway.json`, and the API are the same, and older apps and daemons keep
  working with newer ones.
- Keep `meridian` and `meridian-desktop` on the same release, as before.

### Added

- Workstation profiles (ADR 0049, #83). One import brings a computer's
  harness setups, git identity, and default model connections into a
  profile that every new Capsule inherits (#174–#182):
  - `meridian import` reviews every harness on this computer and the git
    commit identity, lists what it keeps and leaves out, and saves them in
    one atomic request (#180).
  - In the desktop app, **Review this computer's setup** on first run, or
    **Import this computer's setup…** in the tray, does the same in a window
    without a terminal. The app checks for setup changes at launch, on
    wake, and every 30 minutes, and offers to re-import (#182).
  - Home shows first-run steps (import, connect a model, create a Project)
    until you have a Project. Harness settings gains "What Capsules
    inherit": the default setup and connection per harness, the git
    identity, and each Project's overrides (#181).
  - A Capsule pins the profile it started with. Its **Profile…** drawer
    compares that pin with your current profile and can update the Capsule
    when no session is running (#178, #181).
  - Commits an agent makes inside a Capsule carry your pinned git identity
    (#179).
  - A default connection is preselected, never granted: each Project still
    asks once (#176).
- Provider isolation classes (ADR 0050, #86). Every provider reports the
  class it verified: `container`, `sandbox` (gVisor), or `microvm` (Kata).
  Docker is always `container`. The Agent Sandbox provider reports `sandbox`
  or `microvm` only when `--agentsandbox-isolation-class` is declared and
  the configured RuntimeClass's handler is a known handler for that class;
  an unknown or never-isolating handler, a missing RuntimeClass, or missing
  RBAC stops startup. `kata-fc` is not accepted (#186–#188).
- `meridiand --require-isolation` (Helm `requireIsolation`) refuses to start
  on a weaker provider (#187, #193).
- Projects can require an isolation class at creation (`requiredIsolation`,
  `meridian project create --require-isolation`, and the New Project
  dialog). Capsule creation refuses an unmet requirement before any side
  effect, and the reconciler checks it again before each provider create
  (#189–#191).
- A separate Agent Sandbox start timeout (`--agentsandbox-start-timeout`)
  for image pull and VM boot. A timeout names the last startup phase and is
  recorded once rather than retried, and readiness failures report only the
  condition's short reason, never controller or kubelet text (#192).
- `make agentsandbox-kata-validation`, an operator-run validation of the
  Kata microVM profile on KVM nodes, documented with the profile in
  `docs/kubernetes-agentsandbox.md` (#193).

### Changed

- A running `meridiand` is a Station in the desktop app and web UI ("Switch
  Station", "Recent Stations"), so "gateway" now means only the provider
  gateway. The bundled Station is "Local" (#185).
- The README is reorganised around workstation profiles and team
  installations, with new screenshots (#184).

### Fixed

- The release workflow's race tests use the same 20-minute package limit
  and 35-minute job limit as CI, so a tag build no longer fails where PR CI
  passed.
- The Helm chart's control-plane API egress was fixed at TCP 443. On
  clusters whose CNI applies NetworkPolicy after Service translation and
  whose API server listens on 6443 (kubeadm), the control plane could not
  reach the API with NetworkPolicy on. Set
  `networkPolicy.kubernetesAPIServerPorts=[443,6443]` there (#193).

## [0.9.0] - 2026-11-14

### Upgrade notes

- **Back up the data directory first.** Migrations 0023–0028 add per-user
  ownership, audit records, team-mode sessions, personal access tokens, and
  departed-member transfer. Every existing Project, Capsule, secret, provider
  connection, and harness setup is backfilled to the bootstrap owner.
- On first start, the daemon re-wraps secret, provider connection, and
  harness setup envelopes so each one binds its owner. It logs how many it
  re-wrapped, and counts and leaves alone any it cannot open. 0.8.0 cannot
  open the re-wrapped envelopes, and there is no downgrade: rolling back
  means restoring the backup.
- Single-user installations behave as before. The only visible change is the
  new Account page under Settings. Team mode stays off unless you configure an
  OIDC issuer and claim the installation.
- API additions: `/auth/*` (mode, OIDC sign-in, claim, sessions, sign-out,
  device login, desktop sign-in handoff), personal access tokens,
  `/members/*` membership administration, `/audit-records`, and break-glass
  transfer, delete, and identity recovery for members. `pkg/client` is
  regenerated.
- The Helm chart gains `publicUrl`, `auditRetention`, and `oidc.*` values.
  The OIDC client secret comes from an existing Kubernetes Secret, never from
  values. Empty values keep single-user mode.
- Keep `meridian` and `meridian-desktop` on the same release, as before.
  Desktop installs on 0.8.0 show "Meridian 0.9.0 is available" in the sidebar
  update card.

### Added

- Team mode (ADR 0046). Several people share one installation, each signing
  in through your OIDC identity provider, with their own Projects, Capsules,
  secrets, provider logins, and harness setups that no one else can see. No
  role can read another member's work. Configure it with `--oidc-issuer`,
  `--oidc-client-id`, `--oidc-client-secret-file`, and `--public-url`, plus
  `--oidc-required-group` and `--oidc-groups-claim` to restrict who may join.
  Turn it on once, irreversibly, by signing in as the owner and confirming
  with the installation token (#155–#162).
- Sign-in hardening. The authorization code flow uses PKCE and a nonce, and
  each sign-in is bound to the browser that started it, so a callback link
  from someone else's sign-in cannot sign you in as them. Sessions end after
  12 hours idle or 7 days (#160).
- Credentials are bound to their owner. Secret, provider connection, and
  harness setup envelopes include the owner in their authenticated data, so
  ciphertext moved to another member fails to open (#158, #165).
- Audit log. Sign-ins and sign-outs, the claim, secret and provider
  connection changes, Project grants, Deliveries, coordination approvals,
  token and membership changes, refused cross-owner access, and break-glass
  actions are recorded without secret values. Read it with `meridian audit` or
  `GET /audit-records`, and limit how long records are kept with
  `--audit-retention` (#159).
- Personal access tokens for the CLI and TUI. Create them on the Account page
  with a name, an expiry of 30 days to a year, and an optional read-only
  scope. They start with `mrd_pat_`, are shown once, and work with
  `--token-file` and `MERIDIAN_TOKEN_FILE` (#161).
- `meridian login` signs in to a team installation from the terminal with
  the device grant and saves a personal access token (#164).
- Membership administration for admins: `meridian member list`, `set-role`,
  `disable`, `enable`, and `revoke-credentials`, and a Members section on the
  Account page. Disabling a member signs them out everywhere, revokes their
  tokens, and stops their Capsules' model traffic. The last enabled admin
  cannot be demoted or disabled (#162, #168).
- Departed members. With the installation token, `meridian member transfer`
  moves a disabled member's work to another member, re-sealing their
  secrets and setup revisions for the new owner, and
  `meridian member delete-objects` deletes it. Provider logins never move.
  A secret or setup revision that no longer opens moves unchanged rather
  than blocking the transfer (#163, #165).
- Bootstrap owner recovery. `meridian member rebind-identity` moves the
  owner to a new identity-provider account using the installation token. It
  revokes the owner's sessions, tokens, and personal Claude and ChatGPT
  logins (#166).
- The desktop app signs in to a team gateway through your system browser and
  hands the session back to the app over a one-time loopback ticket
  (ADR 0047) (#169).
- The desktop app shows which gateway it is connected to in the window title
  and the status bar, with the signed-in member in team mode. The picker and
  the tray's Switch gateway menu list up to five recent gateways (ADR 0048)
  (#170).
- The web UI shows a Sign in screen, a status-bar Sign in link, and an
  Account sign-in panel when a team-mode session has ended (#168).
- Helm values for team-mode sign-in (#167).
- Resize the Capsule tools panel by dragging its left edge, or by focusing
  the edge and using the arrow keys (Shift for bigger steps, Home and End for
  the narrowest and widest). Double-click the edge to return to the default
  width. The width is remembered in this browser and kept when you hide and
  show the panel. The Thread column keeps its minimum width, and windows
  narrow enough to show the panel as a drawer do not offer resizing (#153).

### Fixed

- The Changes tab of a Capsule whose Project has no repository says the
  workspace has no Git repository, and how to start tracking changes,
  instead of "workspace git command failed: capsuled git_failed". The API
  reports this as `no_repository`; other git failures stay `git_failed`.
- The Changes, Files, and Preview buttons on the collapsed Capsule tools rail
  open that tool. Before, all three opened Changes.
- A long Capsule name in the Thread header is shortened with an ellipsis
  when the Thread column is narrow, instead of pushing the activity pill
  under the Ship and actions buttons.
- A CLI Capsule's terminal sends the PTY one resize after its size settles,
  rather than one per layout change.

## [0.8.0] - 2026-11-13

### Upgrade notes

- No database migration.
- API additions: `POST /thread-links/{linkId}/remove` and a `capsuleRemoved`
  flag on thread links. A lead's `childRollup` no longer counts subagents
  whose Capsule was removed. New grants default to `deadlineSeconds` 86400
  (was 28800) and `archiveAfterSeconds` 3600 (was 86400). `pkg/client` is
  regenerated.
- Existing leads keep their stored deadline and archive period; only leads
  started after the upgrade get the new defaults.
- Subagents that are already archived under a lead with auto-archive on, and
  whose last report changed no files (or whose Delivery succeeded), have
  their Capsules deleted on the first sweep after upgrading. Their
  transcripts are kept.
- Claude Code and Codex leads stop asking you to approve Meridian's own
  coordination tool calls (ADR 0045). "Approve each spawn" still holds each
  new subagent for you when it is on.
- Desktop installs on 0.7.2 show "Meridian 0.8.0 is available" in the
  sidebar update card. Keep `meridian` and `meridian-desktop` on the same
  release, as before.

### Added

- Stop and remove one subagent from a lead's Agents panel. A small stop icon
  on each row (shown on hover) asks for a one-line confirmation, then closes
  that subagent's link as cancelled, ends its session and deletes its
  Capsule. The lead keeps its grant and can start new subagents in the freed
  slot; the subagent's Thread history is kept, and removed subagents move to a
  Removed fold. Promoted subagents are not offered it. The API is
  `POST /thread-links/{id}/remove` (#149).
- Finished subagents are cleaned up. After a subagent has been idle for its
  lead's cleanup window (default 1 hour, was 24 hours), it is archived and,
  if nothing in its workspace is unsaved, its Capsule is deleted; its
  transcript stays. A subagent whose last report changed files that were not
  delivered is kept until you remove it. Change the window per lead under
  "Clean up finished subagents" in the Agents panel settings, or set it to
  Never. Capsules you start yourself still only pause (#149).

### Changed

- A lead's Agents panel has a header with a status pill, a settings button
  and a quiet Stop action. Each subagent is a row with its harness mark and a
  phase pill, and a running subagent shows a small spinner at the left of its
  row. The settings button holds "Run at once" (how many subagent Capsules
  the lead can have at a time, 1 to 16), "Clean up finished subagents", the
  spawn-approval switch, and a line with the lifetime total and time left;
  the started-of-total meter is gone. The header says "Approving spawns" when
  that switch is on. An empty panel says how to get the lead to start
  subagents (#147, #149).
- A lead's sidebar and board rows show how many subagents it has now, such
  as "2 subagents", instead of "0/1 done, 1 idle". Archived and removed
  subagents are not counted, so removing one takes the count down by one
  (#149).
- A lead's default deadline is 24 hours instead of 8, in the web UI, the
  launcher and `meridian thread spawn --lead` (#149).
- The desktop app's update notice is now a compact card at the bottom of
  the sidebar instead of a wide banner over the top of the window. Close
  it to be reminded later (#147).
- Provider connections are listed as rows with the provider's mark, what
  each connection is, and a status pill, with their actions as quiet links.
  The add form sits under them with spaced fields (#147).
- Dropdowns use Meridian's own chevron instead of the platform stepper, and
  the provider and harness pickers show the chosen mark inside the field
  (#147).

### Fixed

- The web UI and desktop app keep talking to the gateway when the browser
  or webview reports that it has gone offline. Before, an `offline` event
  paused every request: a Thread's Send button stayed disabled with nothing
  reaching the daemon, and newly opened panels such as a lead's Agents panel
  never loaded, while the page still showed its last data (#150).
- A Claude Code or Codex lead no longer stops for an operator approval on
  every Meridian coordination tool call (`meridian_whoami`, `meridian_spawn`,
  `meridian_wait`, and the rest). The adapters allow tool calls for the
  profile's own MCP servers, whose authority is the Coordination Grant.
  "Approve each spawn" still holds each new subagent for you. Calls to other
  MCP servers still ask (ADR 0045, #148).

## [0.7.2] - 2026-11-13

### Upgrade notes

- No database migration and no API change.
- This is the first release a desktop app can update to from inside the
  app. A copy installed with the curl installer or the app zip shows
  "Meridian 0.7.2 is available" with Update & restart; the update is
  verified against the signing key built into 0.7.1. A Homebrew cask
  install shows `brew upgrade --cask meridian-desktop` instead, and a
  Linux package shows the curl installer command.
- Keep `meridian` and `meridian-desktop` on the same release, as before.

### Security

- The desktop app's bridge (alerts, tray attention, Gateway control, update
  check and install) is granted only to the gateway origin you chose, and
  only while it is the active gateway. It replaces a static grant to every
  HTTPS origin and every loopback port (#137).

### Fixed

- The web UI refreshes the project list every 5 seconds, so a Project
  created with the CLI or in another tab appears without a reload, and its
  Capsules count under Needs you and raise desktop alerts (#132).
- In a CLI Capsule, the Run badge, Stop, and the stop confirmation sit in a
  toolbar above the terminal instead of covering its top-right output
  (#133).
- The desktop app's Gateway control in the sidebar matches the items around
  it, with an icon, instead of small centred text (#134).
- The desktop gateway picker says "Docker isn't running" when the bundled
  gateway exits because it cannot reach Docker, with meridiand's log below
  (#135).
- Stopping meridiand during startup (Ctrl-C, `brew services stop`, or the
  desktop app quitting while its bundled gateway starts) now shuts down
  cleanly instead of reporting "transaction has already been committed or
  rolled back" (#144).
- The release workflow keyless-signs and attests `install-desktop.sh` and
  lists it in `checksums.txt`, like every other release file. It is
  attached by the desktop checksums job, so prerelease tags, which skip the
  desktop jobs, no longer carry it (#138).

## [0.7.1] - 2026-11-13

### Upgrade notes

- v0.7.0 was tagged but never published: its universal macOS desktop build
  could not find a universal gateway sidecar. 0.7.1 is the first release
  with the changes below; upgrade from 0.6.0 directly to it.
- No database migration and no API change. The `meridian` formula, the CLI
  tarballs, and the container images upgrade as before.
- The desktop app is new and optional. Install it with
  `brew install --cask orlojhq/tap/meridian-desktop` or the curl installer
  below. It is not code-signed or notarized; both install paths clear the
  quarantine flag, and a hand-copied app needs right-click Open once.
- The desktop app reuses a meridiand already answering at
  `127.0.0.1:8080` and starts its bundled one only when nothing is there,
  on the same data directory. Keep `meridian` and `meridian-desktop` on the
  same release (`brew upgrade meridian && brew upgrade --cask
  meridian-desktop`). A meridiand older than 0.7.1 serves a UI without
  desktop alerts, the Gateway control, or update notices, and the app shows
  a banner saying so. See "Desktop app beside an existing meridiand" in
  `docs/operations.md`.
- Containers, volumes, and networks left behind by deleting a Capsule whose
  setup failed in an earlier version are not cleaned up by this release;
  remove them by hand (see Fixed).

### Added

- A desktop shell for macOS and Linux (ADR 0043) loads the same `/ui/` bundle
  the daemon serves. Bundled local starts or reuses `meridiand`; another
  gateway uses the existing browser session and does not start the sidecar.
  Closing the window hides it to the tray. Quit stops only a sidecar this
  app started. Notifications and the tray go through a runtime bridge the
  browser ignores. Windows is a later follow-up. The bundle identifier is
  `io.orloj.meridian`.
- Tag releases attach an unsigned universal macOS disk image and app zip,
  plus an amd64 Linux `.deb`, with checksums, SPDX SBOMs, and Sigstore
  bundles. `brew install --cask orlojhq/tap/meridian-desktop` installs the
  disk image and clears the quarantine flag. Without Homebrew,
  `curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh`
  downloads that release, checks `checksums.txt`, and installs the desktop
  app (or the CLI binaries with `--cli`). Apple signing stays off until its
  secrets are set. The `meridian` formula is unchanged. The Linux package
  is `meridian-desktop` (`sudo apt remove meridian-desktop`) and does not
  install `meridian` or `meridiand`.
- The desktop window checks for a newer release on launch and every four
  hours. It shows "Meridian X.Y.Z is available" with Update & restart or
  Later, and Later hides that version for a day. Homebrew installs are told
  to run `brew upgrade --cask meridian-desktop`. Linux packages are told to
  run the curl installer. A signed macOS self-update stops a sidecar this
  window started before it restarts. Every stable release publishes
  `latest.json` for macOS and Linux, so the check works without the updater
  signing key. That key is optional; without it the release still ships and
  self-update stays off. Release candidates do not build desktop artifacts.
- The desktop app explains running beside another meridiand. It shows a
  banner when the daemon it reuses is older than the app, and the gateway
  picker says when the bundled gateway stopped because another Meridian holds
  the data directory or the database is newer than it supports. The cask's
  caveats and `docs/operations.md` say to keep `meridian` and
  `meridian-desktop` on the same release.

### Fixed

- A Capsule whose container stopped because Docker Desktop or OrbStack quit,
  restarted, or updated is started again instead of being marked Failed. The
  container, workspace, and image are reused, and the Capsule stays Ready.
  Meridian notices at startup, on the live refresh, or when you next start
  work, and sets no Docker restart policy. A container that keeps stopping
  (more than 3 restarts in 10 minutes) or cannot start still fails the
  Capsule, with the exit code (#118, ADR 0044).
- A Run whose Capsule runtime stopped no longer shows as Working forever. The
  live refresh fails it with "Capsule runtime stopped while this Run was
  active" and pauses its Thread with the transcript kept; resume the Thread
  to start a new session in the restarted Capsule.
- A Paused Capsule whose container an engine restart stopped stays Paused
  instead of failing, and resumes normally.
- On Agent Sandbox, Kubernetes still restarts the Pod itself. A Ready
  Capsule whose Pod is coming back stays Ready, and its active Runs fail the
  same way.
- Deleting a Capsule whose setup failed, such as a checkout that could not
  clone the repository, now removes its Docker container, `-workspace`
  volume, and `-network` network, or its Agent Sandbox. They used to keep
  running after the Capsule showed Deleted. Containers left by earlier
  versions still need `docker rm -f`, `docker volume rm`, and
  `docker network rm`.

## [0.6.0] - 2026-11-11

### Upgrade notes

- No database migration. Capsules created before the upgrade keep the pack
  image they were created with, so their composer keeps the harness's own
  model labels until you start a new Capsule; new Capsules use this
  release's packs, which report the model and effort that actually run.
- `POST /projects/{projectId}/threads` now accepts a request without
  `prompt`. Existing clients that send one are unaffected.

### Added

- A New Capsule dialog replaces the home-page composer. It asks only for
  Thread or CLI, the Project, the harness, and a provider connection when
  more than one fits, and opens from the sidebar, the home page, and each
  Project's `+`. The home page lists recent work.
- A Thread started from the dialog opens idle, without a first message, so
  you can pick the harness's model and effort before the first turn.
  `meridian thread spawn` still requires a prompt.
- The dialog can start a Thread as a lead agent with subagents, optionally
  approving each spawn. It asks for no limits: a lead may create up to 16
  subagents, at most 4 running at once, within the default 8-hour deadline.
  A lead also starts idle.
- Project settings can add installation harness packs to a Project, so
  future Capsules can use more than the initial harness. The per-Project
  launcher shares these controls.

### Changed

- The Thread composer's model button names the model that runs, such as
  "Opus 5.5" rather than "Default (recommended)", and the effort button shows
  the level in effect from the start. Claude Code sessions ask Claude Code
  for both before the first turn (ADR 0042). Adapters and `capsuled`
  negotiate a new `resolvedModel` capability for the `models` frame's
  `resolved` name, exposed as `ThreadModels.resolved`.
- While a new Capsule provisions, its page shows "Starting the session"
  instead of offering a second way to start one.

### Fixed

- CLI mode no longer requires a provider connection; it opens the harness
  for interactive sign-in. Only Thread mode needs one, and it says so.
- The Capsule header no longer draws a pinned harness setup note over the
  title, branch, and activity pill.
- The Thread composer wraps its controls below the message box when its own
  column is narrow, such as with the Capsule tools open, not only on narrow
  windows.
- The per-Project launcher's "Subagents" limit is no longer silently capped
  at 8 by the default tree total.

## [0.5.0] - 2026-11-11

### Upgrade notes

- Startup applies forward-only migration 0022. It removes any saved harness
  allowlist entry named `mock` and does not report an error. Create and
  verify a backup first; rolling back requires that backup and the old
  binary. An allowlist written after the upgrade, including a test
  configuration, is left as stored.

### Changed

- Meridian has a logo in the sidebar and browser tab, and the README shows
  the browser workspace, Thread review, and Harness settings.
- Official harness packs follow the installation release (ADR 0041). A
  Project stores the pack reference it was given, such as
  `meridian-capsule-codex:v0.4.0`, but new Capsules now use the pack the
  running `meridiand` advertises, so after an upgrade they get the new
  release's harness and `capsuled` without editing each Project. The first
  Capsule per harness pulls the new image and reruns Project setup once.
  Existing Capsules keep the image they were created with; the Capsule page
  marks one built on an older release, and the Capsule API returns
  `imageReference`. Custom images, custom tags, and digest-pinned references
  are used as stored.
- Importing a harness setup in the browser is laid out as two steps: choose
  the folder, then review and save. The folder path is shown as a hint with a
  Copy button, since a web page cannot open a path itself, and Review import
  is visible but disabled until something is chosen. Harness settings leads
  with the one-step terminal import, `meridian harness import <harness>`,
  with a Copy button and `--server` filled in when the page is not on this
  computer. Repeated hints are merged.
- A saved harness setup leads with two actions, Edit files and skills and
  Update from this computer, which jumps to the import steps. Rename, the
  default switch, and delete are quiet text actions, and rename opens a name
  field only when chosen. The editor lists files relative to the harness
  folder and opens the add-a-skill and add-a-file forms on demand, with
  Review changes and Cancel at the end. After an import is saved, the import
  steps stay open with a confirmation, so more files or a skills folder can
  be added without starting over.
- The sidebar lists Projects as a tree, with each Project's Capsules nested
  under it and the most recently active Project first. It replaces the "All
  projects" dropdown and the Recent list. A new Project appears right away,
  with "No Capsules yet · Start one". Hovering or focusing a Project shows
  New Capsule, Project settings, and Delete; hovering a Capsule shows Delete.
  Both deletes open the existing confirmations, and on touch screens the
  actions are always shown. Capsules that need you stay listed once, under
  Needs you, and their Project shows how many. A failed Project delete is
  reported on its row. Collapsed Projects are remembered in this browser.
- The `mock` harness is test-only. It is no longer in the installation
  catalog, so a real install's API, CLI, TUI, and web UI do not list or
  offer it. On upgrade, migration 0022 drops that name from harness
  allowlists already saved on Projects. Tests and local end-to-end
  configuration that allowlist it after startup still keep it. The adapter
  `mock` subcommand remains for those runs.

### Fixed

- The phone sidebar opens again. The menu button set it open, but the shell
  closed it on the next render.

## [0.4.1] - 2026-11-10

### Upgrade notes

- Upgrade from 0.4.0 promptly. 0.4.0 was built with Go 1.26.10 and is
  affected by GO-2026-6859, an HTTP/1 server crash in `net/http` (see
  Security). Homebrew installs upgrade with `brew upgrade meridian`, then
  `brew services restart meridian`.
- No database migrations. A 0.4.0 data directory starts unchanged, and
  rolling back to 0.4.0 needs only the old binary.

### Fixed

- `meridiand` starts on a fresh install again. Since 0.3.0 it exited with
  `acquire data directory: inspect data directory: ... no such file or
  directory` when its data directory did not exist yet, which broke the
  first `brew services start meridian` and the README's release install. It
  now creates a missing data directory with mode `0700` before taking the
  lease, as 0.2.x did. An existing directory keeps its mode, and a symlink
  or non-directory is still refused.
- Harness settings lists each harness with its logo instead of a one-letter
  placeholder, and shows what a new Capsule would use: the default setup and
  the provider connection, or that the sign-in expired. Each row has one
  Set up or Manage button, and on a phone the status wraps instead of being
  cut off. DeepSeek Harness now has its own mark everywhere harnesses are
  shown, from simple-icons 16.33.0 (CC0-1.0), like Claude Code, OpenCode,
  and Pi.
- Connect a provider no longer breaks the page. The settings drawer's
  backdrop turned opaque under the cursor, hiding everything behind it;
  Close and Escape did not stick, because the drawer reopened from the link
  on the next render; and the whole shell shifted up because the phone menu
  button was laid out below the status bar on desktop. The link now opens a
  drawer with only provider connections, Close and Escape close it, and the
  page stays in place.

### Security

- Go toolchain bumped from 1.26.10 to 1.26.11 in `go.mod`, CI, release, and
  every Go build image (`golang:1.26.11-bookworm`, pinned by digest). This
  resolves GO-2026-6859, an HTTP/1 server crash caused by a `MaxBytesReader`
  header race in `net/http`, which `govulncheck` reports as reachable from
  the `meridiand` API, the provider gateway, and `capsuled`. 0.4.0 was built
  with 1.26.10 and is affected.

## [0.4.0] - 2026-11-10

### Upgrade notes

- Stop the 0.3.x `meridiand` before starting this release on the same data
  directory. Both hold the data-directory lease, so the second daemon refuses
  to start.
- Startup applies forward-only migrations 0020 and 0021 (Project lifecycle
  state for deletion, and bounded Capsule startup progress). Create and
  verify a backup with the 0.3.x binary first; rolling back requires that
  backup and the old binary.
- `--provider=fake` is removed. A daemon started with it exits with
  `unsupported provider`. Use `docker` (the default) or `agentsandbox`.
- Provider connections on Docker no longer need `--provider-gateway-url`;
  `meridiand` starts a local gateway listener (ADR 0037). Capsules created
  before upgrading need recreating to use a connection. With the local
  gateway, the API listener no longer serves `/provider-gateway/`.
  Installations that set `--provider-gateway-url` keep that route.
- Official harness pack images move with the release tag. The pinned harness
  versions are unchanged from 0.3.0.
- The web UI home screen is now a Capsule list and start composer instead of
  the card Inbox. Existing `/ui/` links still open the same pages.

### Added

- Delete a Project and what it owns (ADR 0039).
  `POST /projects/{projectId}/delete`, `meridian project delete`, the web UI,
  and the TUI (`/delete-project`) remove its Capsules through the provider,
  then its Threads, transcripts, Moments and unreferenced artifacts,
  environment, and the ciphertext of named secrets no other Project uses.
  Each client shows what will be removed and requires the Project name (or
  `--yes` in the CLI). A container that cannot be removed leaves the Project
  `failed` with the reason, and deleting again resumes. Projects now carry
  `state` and `failure`. Backups, snapshots, and free pages are not erased.
- Capsule startup shows progress. Docker's image pull is read as byte counts
  and a fixed set of phases (pulling image, creating Capsule, starting
  harness, connecting), and the Capsule page shows a step tracker, a pull
  bar, and a specific reason with Retry when a pull fails (registry
  unreachable, authentication required, or image not found). Registry text
  is never stored.
- A Capsule page shows its pinned harness setup, the revision time, and that
  edits in Settings apply to the next Capsule. Capsule responses include
  `pinnedHarnessSetups`; a clean Capsule omits it.
- First run is guided. The empty sidebar offers Import setup, Connect a
  provider, and Create a Project. The new-Project form accepts `owner/repo`
  and GitHub SSH URLs, stores them as credential-free HTTPS, and asks for a
  GitHub token for a private repository, saved as a `git_https` secret.
- The terminal and Thread start forms use what you already have. With no
  saved setup, the launcher says the Capsule will not have your local
  settings and offers Import my setup. When exactly one live connection
  supports the harness, both forms preselect it and show its default model.
  A Thread cannot start without a usable connection; a terminal Capsule can
  still sign in inside the harness.
- Each official harness pack declares what `meridian harness import` reads
  in `images/capsule-<name>/manifest.yaml` (ADR 0040): the configuration
  directory and its override variable, importable paths, which files are
  parsed as configuration, home-level and shared files, and the provider
  connection types the harness accepts. The CLI, browser review, `meridiand`,
  and `capsuled` read the copy compiled into each binary, instead of Go
  switches in three packages. What each harness imports is unchanged.
  `capsuled` now also sets `DSH_HOME` in every setup home.
- Provider connections work out of the box on Docker (ADR 0037). Without
  `--provider-gateway-url`, `meridiand` serves the provider gateway on a
  second TLS listener that answers only `/provider-gateway/` routes, bound to
  host loopback on Docker Desktop, OrbStack, and other VM-backed engines, or
  to the default bridge address on native Linux. Its certificate chains to an
  in-memory CA, name-constrained to `provider-gateway.meridian.internal`, that
  is never written to disk and rotates without breaking live leases. Capsules
  get a hosts entry for that name, and `capsuled` makes each harness trust the
  CA through `SSL_CERT_FILE` and `NODE_EXTRA_CA_CERTS`, so Codex, Claude Code,
  OpenCode, Pi, and DeepSeek Harness all reach it without a reverse proxy or
  tunnel. `--provider-gateway-listen` sets the address (or `off`); remote and
  rootless engines have no default and still need one of the two flags.
  Capsules created before upgrading need recreating to use connections. With
  a local gateway, the API listener no longer serves `/provider-gateway/`.
- Claude login stays on the control plane (ADR 0036). Harness settings sign
  in to Claude from the page: Meridian runs the OAuth login, stores the
  access and refresh tokens as authenticated ciphertext, and does not show
  them again. Pasting an API key or a setup token remains a fallback. The
  provider gateway attaches the current access token to `api.anthropic.com`.
  A Capsule receives only a gateway lease. Codex, OpenCode, Pi, and DeepSeek
  Harness credential setup is unchanged.

- Homebrew install: `brew install orlojhq/tap/meridian` installs `meridian`
  and `meridiand`, and `brew services start meridian` runs the daemon in the
  background. The tap is updated only after a release is published;
  prereleases are skipped.
- `make vulncheck` runs `govulncheck` (v1.9.0) in CI and in the release
  verify job, failing on known vulnerabilities in code Meridian reaches.
- Sign in with ChatGPT (ADR 0038): sign in once from Harness settings or
  `meridian harness connect chatgpt`, and Codex uses your ChatGPT plan in
  every Capsule, for terminals and Threads, without signing in again. The
  daemon runs the OAuth flow, keeps the token set encrypted, and refreshes it;
  Capsules get only their 24-hour gateway lease. The gateway allows only
  `POST /v1/responses` for these connections and requires `stream: true` and
  `store: false`. Signing out revokes the token at OpenAI. Uses the provider
  gateway, which local Docker installs now start on their own. Claude login
  is the separate control-plane sign-in above.

### Changed

- The web UI is reorganized around Capsules. A project switcher and a Capsule
  list (Needs you, then recent) replace the card Inbox, and New Capsule opens
  a composer that starts a Thread or the harness CLI. A CLI session gives the
  terminal the width, with Changes, Files, and Preview on a collapsible rail.
  Project settings is a short page, and Harness settings opens import and
  provider connections in a slide-over.
- The web UI uses one design system: shared color, type, spacing, and motion
  tokens, first-class light and dark themes that follow the system or a
  theme switch in the shell, and shared controls. Motion is removed when reduced motion
  is requested.
- ChatGPT sign-in leads with `meridian harness connect chatgpt` when the page
  is not on the same computer as `meridiand`, before the paste box.

### Removed

- The simulated Capsule provider is gone. `meridiand` no longer accepts
  `--provider=fake`, no longer logs a startup warning for it, and the web UI
  no longer warns that Capsules are simulated. Docker and Agent Sandbox are
  the providers. Tests use an in-memory lifecycle double that is not linked
  into the daemon.

### Fixed

- Deleting a Capsule leaves its page. The UI moves to the Project's most
  recently updated Capsule, the Project page, or the Inbox, and Back does not
  return to the deleted Capsule.
- A Thread offers Open terminal only when the harness has a native profile,
  so structured-only packs such as DeepSeek Harness no longer link to an
  empty terminal.
- `make release-docker-validate` builds `meridian-coord` and stops on the
  first error. Published images were not affected.
- The Thread composer lists models and reasoning effort in separate menus, so
  a long model list no longer scrolls inside a second scroll area. The effort
  button is hidden when the harness reports no choice, and both controls stay
  inside the message box on a narrow screen.

### Security

- Go toolchain bumped from 1.26.5 to 1.26.10 in `go.mod`, CI, release, and
  every Go build image (`golang:1.26.10-bookworm`, pinned by digest). This
  resolves the 20 standard library advisories `govulncheck` reported in code
  paths Meridian's binaries reach, including `crypto/x509` distrust of
  compromised intermediates (GO-2026-6809), `crypto/tls` (GO-2026-6607),
  `net/textproto` (GO-2026-6608), and several `net/http` issues
  (GO-2026-6605, GO-2026-6609 to GO-2026-6613, and GO-2026-6617).

## [0.3.0] - 2026-11-08

### Upgrade notes

- Stop the old `meridiand` before starting 0.3.0 on the same data directory.
  0.3.0 holds the directory with a lease and refuses to start beside another
  0.3.0 daemon or maintenance command, but a 0.2.x daemon writes no lease and
  is not detected. Never share one data directory between two daemons,
  including a host daemon and a container through a bind mount.
- Startup applies forward-only migrations 0014 to 0019 (coordination,
  operator notices, subagent lifecycle, spawn approval, subagent results, and
  Delivery proposals). Create and verify a backup with the 0.2.x binary first;
  rolling back requires that backup and the old binary.
- Official harness pack images move with the release tag. The packs pin
  Claude Code 2.1.319, Codex 0.167.0, OpenCode 2.2.6, Pi 0.87.1, and DeepSeek
  Harness 0.2.2-rc.1 (a developer preview).
- New provider connection features (custom endpoints, reasoning effort
  levels) still need the provider gateway, as in 0.2.x: an HTTPS
  `--provider-gateway-url` that Capsules can reach.

### Added

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

- DeepSeek Harness (`dsh`) Threads can lead subagents: the `dsh` structured
  profile registers `meridian-coord` through ACP `session/new`, like
  OpenCode (ADR 0029).

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

- Subagents can send an explicit `report` (`done`, `blocked`, or `failed`
  plus a ≤16 KiB summary) to their lead, and a lead can `patch` a bounded
  `git format-patch` plus working-tree diff from the child's Capsule
  (default 1 MiB, max 4 MiB, hooks disabled). Explicit reports override the
  turn-end default until a later assistant message; ending the child's
  session does not replace them. They are persisted only in the child's
  encrypted transcript. Patch bytes are never stored; link views carry HEAD, base
  commit, and a content-free diffstat. Subagent mailboxes expose `report`,
  `propose-delivery`, and `whoami`. Reports and patches are untrusted data
  (ADR 0029).

- Lead Threads render subagent reports as cards (name, status, summary, and a
  link to the subagent Thread) instead of the raw coordination wake prompt, and
  pin a tray above the composer for subagents that need the operator. Report
  text is treated as hostile: control and bidi characters are stripped, length
  is capped, and link IDs stay out of the main view.

- Coordinated Threads can `propose-delivery` (`title`, `body`, `branch`) for
  the operator to approve. The proposal is persisted as an unapproved
  Delivery (`proposedBy: agent`) and never ships on its own.
  `POST /deliveries/{id}/approve` binds the reviewed HEAD and tree and then
  follows the existing execute path; `POST /deliveries/{id}/reject` is
  terminal. The board shows `needs_you` / `proposed_delivery`. Title and body
  are treated as hostile. Capsules cannot approve or reject (ADR 0029).

- Operator spawn approvals and grant updates. A Coordination Grant may set
  `spawnApproval: operator` (`--spawn-approval operator`, "Approve each
  subagent spawn") so each lead spawn waits on the board until
  `POST /coordination-requests/{id}/approve` or `/deny` (`meridian approvals`).
  Pending children keep their encrypted prompt, are not provisioned, do not
  count as live, and cannot be promoted. `PATCH /coordination-grants/{id}`
  (`meridian grant update`) lets the operator loosen or tighten limits,
  harnesses, remaining deadline, archiveAfter and spawn approval; switching
  to `auto` leaves already queued spawns queued (ADR 0029).

- A lead Thread's Agents list shows the model and reasoning effort each live
  subagent last reported (`ThreadLink.model` and `ThreadLink.effort`). The
  daemon derives them in memory from the subagent's transcript and never
  stores them.

- Browser Threads work with every official pack. Each pack now installs a
  structured profile next to its native one under the same name. New
  `claude-stream` and `codex-app-server` adapter drivers run Claude Code's
  stream-json control protocol and the Codex app-server. They map streaming
  text, tool events, permission and question prompts, cancel, and resume to
  `meridian.adapter.v1`, and end every turn with `idle`. Both accept
  `--mcp-server` to register a stdio MCP server in the harness. Threads
  authenticate through provider connections (ADR 0026).

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

- Harness settings and `meridian harness import` can import a DeepSeek Harness
  (`dsh`) setup from the real local home (`~/.dsh` / `$DSH_HOME`), including
  `settings.yaml`, `cordis.patch.yml`, `skills/`, and `profiles/`. Credentials
  and `!!js` YAML tags are excluded. Capsule images are unchanged.

- ACP agents can ask the operator questions. The `acp` driver accepts
  form-mode `elicitation/create` when permissions are enabled and asks each
  field in turn as a button question or a typed answer, checks every answer
  against the agent's schema, and declines URL-mode and unsupported forms
  (ADR 0032).

- `meridian project apply-harness PROJECT_ID NAME=IMAGE` allowlists a custom
  harness image on an existing Project. `meridian thread follow --until-idle`
  exits when the turn that answers the latest prompt goes idle.

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

- The Thread composer shows the session's current model inside the message
  box and switches it from a menu of the models the harness reports
  (`POST /threads/{threadId}/model`). Claude Code, Codex, Pi, OpenCode, and
  DeepSeek Harness report their models; the switch applies from the next turn,
  and a model the harness does not offer is refused without ending the session
  (ADR 0031). Codex also lists the model the Thread opened with, such as a
  provider connection's default, when its own catalog lacks it.

- The Thread composer's model picker also shows and changes reasoning effort
  when the harness reports levels for its current model ("Sonnet · High").
  Claude Code, Codex, Pi, and ACP agents such as DeepSeek Harness report their
  levels; a change applies from the next turn
  (`POST /threads/{threadId}/effort`, ADR 0033).

- Custom endpoint connections can declare the reasoning effort levels their
  default model accepts (`--effort low,high,max`, or checkboxes in Harness
  settings). Pi, DeepSeek Harness, OpenCode, and Codex then offer those levels
  in the composer's effort picker and send them upstream (ADR 0034).

- The model picker has a search field when the harness lists more than eight
  models; it matches labels, IDs, and descriptions, and Enter picks the first
  match.

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

- A board of every Capsule by status. `GET /board`, `meridian board` and the
  browser's Board view group Capsules across Projects into needs you, working,
  PR open, ready for review, idle, paused and sealed, with the latest Delivery
  and, for leads, a subagent rollup. Subagents fold under their lead unless
  they need you; `expand=all` (`--all`, "Show all subagents") lists every
  subagent. The command palette gains "Next item needing me". When the
  operator messages a subagent directly, its lead's next report wake says so
  without the message text, and the message does not count against the lead's
  per-subagent limit; thread links gain `operatorMessages` (ADR 0029).

- Board and Activity rows that wait for permission or input can be peeked
  open to see which permission, tool or command is being asked for, and
  answered in place with the Thread view's card. The rows stay content-free;
  only an explicit peek reads that Thread's transcript tail, and nothing is
  cached or polled. Agent-supplied request text is shown as capped plain text
  with control and bidirectional characters removed.

- Browser notifications, a tab badge, and a `G` then `N` shortcut for Capsules
  that need the operator. Notifications are opt-in from the status bar, fire
  only while the tab is hidden, skip the Capsule already open, and carry only a
  count plus "Meridian needs you" — never Thread titles, prompts, permission
  details, or transcript text. The document title shows `(N) Meridian`. The
  permission choice stays in the browser; no prompt drafts are written to
  browser storage, and there is no phone push.

- `meridiand` and the mutating `admin` commands hold the data directory with
  an exclusive lock and a heartbeat lease file, and refuse to start while
  another daemon or maintenance command holds it, including one across a
  Docker Desktop bind mount, where two daemons could corrupt the SQLite
  database (ADR 0035).

### Changed

- The Claude Code pack pins Claude Code 2.1.319 (was 2.1.283) and the Codex
  pack pins Codex 0.167.0 (was 0.157.1). Both adapters' upstream contracts
  are unchanged; Pi stays on 0.87.1.

- Activity and Board are one Inbox at `/ui/`. The page keeps Board status
  lanes (needs you, working, PR open, ready for review, idle, paused, sealed)
  and the Activity headline ("N need attention", or "All caught up"), with
  peek-and-respond and subagent rollups on the same rows, so operators no
  longer visit two pages for the same blockers. The sidebar and command
  palette expose Inbox instead of separate Activity and Board links;
  `/ui/board` redirects to `/ui/`. `meridian board` is unchanged.

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
  with a fresh password per launch on OpenCode's loopback server. The pack
  pins OpenCode 2.2.6. The `opencode-server` driver is removed, and the
  `adapterKind` value `opencode` is now `acp`. Provider connections work as
  before. ACP does not collect credentials; sign in in a native terminal
  session or attach a provider connection.

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

- Starting a Capsule or task from the New Capsule dialog closes the dialog as
  soon as `createCapsule` or `createProjectThread` succeeds, then opens the
  workspace. The dialog stays open on validation or API errors so the
  operator can retry. Previously it only closed on a later navigation, so a
  successful start could leave the operator stuck behind the modal.

- Helm NetworkPolicy allows Capsule egress to the in-namespace provider
  gateway (control-plane TCP 8080) and control-plane HTTPS egress to
  operator-selected model-provider CIDRs (`api.openai.com:443`,
  `api.anthropic.com:443`, and HTTPS custom endpoints). Capsules still do
  not receive provider or API-server CIDRs, default internet egress, or a
  host Docker socket.

- A failed Pi prompt now settles the Thread to idle. Before, the adapter
  emitted `error` and skipped `idle`, so the session stayed "working" with
  the composer disabled even though the turn had ended.

- A Thread whose session failed, stopped, or exited no longer shows "active"
  and "Reconnecting…" forever. It says "Session ended", shows the Run's
  failure reason as bounded plain text, and offers Restart session, Copy last
  prompt, and New session; the composer stays disabled until a new session
  starts, and the block stream stops reconnecting. Board rows for a failed
  Capsule or Run show the same reason as the Activity view, and the sidebar
  and session tabs label ended sessions "Ended" or "Stopped" instead of
  "Idle". After a restart, replies from an adapter that reuses message IDs
  across sessions no longer overwrite the previous session's reply.

- Starting a structured session now persists the capsuled protocol error
  (`structured_interaction_unavailable`, and other supervisor codes) on the
  Run instead of the generic "Capsule runtime rejected structured session
  start". `createThread` with `start` refuses a native-only catalog pack
  before it creates a Thread, the same way Project Thread spawn does.

- Capsule git status, diff, and patch failures (`git_failed`) no longer map
  to "illegal lifecycle transition". Changes shows a workspace git failure.

### Security

- Harness adapters pin a session working directory to a real path under
  `/workspace`. They refuse a host `Getwd()` path, a path that escapes the
  Capsule workspace, or a symlink in that chain, and start child processes
  with that directory instead of inheriting the host cwd.

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

[Unreleased]: https://github.com/OrlojHQ/meridian/compare/v0.18.1...HEAD
[0.18.1]: https://github.com/OrlojHQ/meridian/compare/v0.18.0...v0.18.1
[0.18.0]: https://github.com/OrlojHQ/meridian/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/OrlojHQ/meridian/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/OrlojHQ/meridian/compare/v0.15.2...v0.16.0
[0.15.2]: https://github.com/OrlojHQ/meridian/compare/v0.15.1...v0.15.2
[0.15.1]: https://github.com/OrlojHQ/meridian/compare/v0.15.0...v0.15.1
[0.15.0]: https://github.com/OrlojHQ/meridian/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/OrlojHQ/meridian/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/OrlojHQ/meridian/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/OrlojHQ/meridian/compare/v0.11.5...v0.12.0
[0.11.5]: https://github.com/OrlojHQ/meridian/compare/v0.11.4...v0.11.5
[0.11.4]: https://github.com/OrlojHQ/meridian/compare/v0.11.3...v0.11.4
[0.11.3]: https://github.com/OrlojHQ/meridian/compare/v0.11.2...v0.11.3
[0.11.2]: https://github.com/OrlojHQ/meridian/compare/v0.11.1...v0.11.2
[0.11.1]: https://github.com/OrlojHQ/meridian/compare/v0.10.1...v0.11.1
[0.10.1]: https://github.com/OrlojHQ/meridian/compare/v0.9.0...v0.10.1
[0.9.0]: https://github.com/OrlojHQ/meridian/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/OrlojHQ/meridian/compare/v0.7.2...v0.8.0
[0.7.2]: https://github.com/OrlojHQ/meridian/compare/v0.7.1...v0.7.2
[0.7.1]: https://github.com/OrlojHQ/meridian/compare/v0.6.0...v0.7.1
[0.6.0]: https://github.com/OrlojHQ/meridian/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/OrlojHQ/meridian/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/OrlojHQ/meridian/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/OrlojHQ/meridian/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/OrlojHQ/meridian/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/OrlojHQ/meridian/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/OrlojHQ/meridian/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/OrlojHQ/meridian/releases/tag/v0.1.0
