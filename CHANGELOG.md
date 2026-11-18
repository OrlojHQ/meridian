# Changelog

All notable changes to Meridian will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Meridian uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- A release whose macOS desktop job is cancelled before a runner accepts it
  is retried automatically, up to three attempts. The retry runs only when
  that job never started and its annotation says it was not acquired. A job
  that received a runner and then failed stays failed, and the desktop
  manifest job runs again with the retried build.
- The model picker hides a `meridian/` provider prefix on a reported model
  label, the same way it hides `meridian · `. Other providers keep their
  prefix, and a list from several providers still groups by provider. Search
  still matches the full id and label.

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

[Unreleased]: https://github.com/OrlojHQ/meridian/compare/v0.11.5...HEAD
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
