# ADR 0049: Workstation profiles

- Status: Proposed
- Date: 2026-11-14
- Amends: ADR 0024 (personal harness setups), ADR 0040 (harness-pack
  manifests), and ADR 0046 (per-user identity), for how a profile is owned
  and transferred
- Tracks: #83

## Context

Issue #83 asks that a Capsule feel like the user's laptop for every official
harness: Codex, Claude, OpenCode, Pi, and DeepSeek Harness. The pieces exist
but do not add up to one profile:

- **Harness setups** (ADR 0024). Encrypted, immutable revisions of a
  sanitized bundle, one per harness. A setup can be the owner's default for
  its harness (`harness_setups.default`). A Project can override it
  (`project_harness_setups`, a setup id or `clean`). Capsule creation pins
  one revision per harness in the same transaction (`pinHarnessSetups` in
  `internal/app/harness_setups.go`, the `capsule_harness_setups` table).
  `capsuled` writes the pinned revision into a private home at Run or Thread
  start (`prepareHarnessSetup` / `materializeSetupHome` in
  `internal/capsuleproto/harness_setup.go`).
- **Pack manifests** (ADR 0040). Each official pack's `manifest.yaml`,
  compiled into every binary, says which laptop directories and files a
  harness may import. The deny list stays in Go and is checked first.
- **Provider connections.** Owner-bound encrypted envelopes (ADR 0046). A
  Capsule gets a gateway lease only through a Project-and-harness grant
  (`project_provider_connections`), which the user approves on first launch.
- **Project fields.** `Setup` (the setup argv run into the prepared
  environment cache), `GitSecretName`, `HarnessSecretNames`, and
  `CommitAuthorName` / `CommitAuthorEmail`, which only Delivery reads.

Team mode (ADR 0046, v0.9.0) made every one of those rows owned by one user.
The harness setup default is now per owner, since `ListHarnessSetups` lists
only the caller's rows. So the "user profile" that #83 describes has a user
to belong to.

What is still missing:

- There is no single import. `meridian harness import` saves each harness
  on its own and shows no diff against what is already saved.
- Git identity is not read from the laptop. A Project without a commit
  author refuses a Delivery that has to commit.
- The import report (what was excluded and why) is printed and then lost.
  Revision metadata keeps only the file list and digest.
- Nothing answers "what will this Capsule inherit?" or "why is this Capsule
  different from my laptop?".
- There is no per-user default connection. Every new Project asks again.

## Decision

### A profile groups existing records

A workstation profile is a per-user grouping of records Meridian already
keeps, plus two small new pieces. It is not a second copy of any file.

| Item | Where it lives |
|---|---|
| Harness setup per harness | The owner's default setup for that harness (existing `harness_setups.default`). |
| Import report per revision | New. Sealed with the revision (below). |
| Git commit identity | New. `user.name` and `user.email` strings only. |
| Default provider connection per harness | New. A reference to one of the owner's own connections. |

A new `user_profiles` table has one row per user, keyed by `owner_user_id`.
It holds the git identity, the default connection id for each harness, a
resource version, and the update time. It has no file contents and no
credential values. A user with no row has an empty profile, which behaves
like today.

### Ownership under team mode

- **One owner.** `user_profiles.owner_user_id` is the primary key and
  `NOT NULL`. A profile belongs to exactly one user and is never
  installation-wide in team mode.
- **Default-private, and not addressable.** The API has `/profile` for the
  caller's own profile only. No route takes a user id. An admin cannot read
  another member's profile, and the break-glass bearer is refused, the same
  as on other object endpoints (ADR 0046).
- **References stay within one owner.** A profile names only the owner's own
  setups and connections. `internal/app` checks this, and store triggers
  refuse a default connection whose owner is different, as the
  `project_provider_connections` triggers already do.
- **Envelopes bind the owner.** Setup revision envelopes already use the id
  `owner:<owner_user_id>:<revision_id>` in their AAD. The import report is
  sealed in its own envelope with the id
  `owner:<owner_user_id>:<revision_id>:report`. A report ciphertext moved to
  another user's row, or swapped into a bundle slot, fails to open. The git
  identity and connection ids are not credentials and are stored as
  metadata, like the Project's commit author today.
- **No cross-user sharing.** There is no API to copy, export, or grant a
  profile to another user. Making one is out of scope (ADR 0046) and would
  need its own ADR.
- **Departed members.** Break-glass transfer does not move the profile row:
  a git identity, like a personal login, stays with the person it names.
  Setups still move, as today, but arrive with `default` cleared, so the
  recipient's profile does not change without their action. Today a moved
  default can leave the recipient with two defaults for one harness. Deleting
  a departed member deletes their profile row.
- **Single-user mode.** The bootstrap owner has the only profile. On a
  server that is not loopback, the profile page says that anyone holding the
  installation token uses this profile.

### Three layers

The layers stack in this order: **Capsule pin, then Project, then user
profile**. The first layer that sets an item wins.

| Item | User profile | Project | Capsule pin |
|---|---|---|---|
| Harness setup | Default setup per harness | Setup id or `clean` per harness (existing) | Revision pinned at create (existing), or `clean` |
| Provider connection | Default connection per harness: a suggestion, never a grant | Grant per harness (existing). The gateway checks only this. | None. The lease comes from the Project grant at session start (existing). |
| Git commit identity | Name and email | `commitAuthorName` / `commitAuthorEmail` (existing) | Identity copied at create |
| Private clone | None | `gitSecretName` (existing) | None |
| Setup argv | None | `setup` (existing) | Prepared environment cache (existing) |
| Harness env secrets | None | `harnessSecretNames` plus the pack profile (existing) | None |

How each existing mechanism fits:

- **Harness setups and pins.** `pinHarnessSetups` already resolves Project
  override, then the owner's default, in the Capsule creation transaction.
  That does not change. The profile adds no per-harness state beyond the
  default flag that already exists.
- **ADR 0040 manifests.** The profile adds no manifest field. `meridian
  import` detects harnesses by checking which manifest `root`, `homeFiles`,
  and `shared` paths exist, and it imports with the same allowlist and deny
  list. The connection overlay stays per-harness Go. Describing it in the
  manifest is still an open question and is not part of this decision.
- **`internal/capsuleproto/harness_setup.go`.** Materialization still writes
  one pinned revision into `.meridian-setup-<revision>`. The one addition is
  the pinned git identity, sent with the runtime setup. `capsuled` writes it
  as a generated `.gitconfig` in that private home, with only a `[user]`
  section. The laptop's `~/.gitconfig` is never copied, since it routinely
  holds `insteadOf` rewrites, credential helpers, and `core.sshCommand`.
  An identity that contains control characters, quotes, backslashes, or
  `<` / `>` is refused when it is saved and again by `capsuled`, rather than
  escaped. A repository's own
  `.git/config` can still override it, as git normally allows.
- **Provider connections.** A profile default is never an authorization. The
  gateway keeps checking only the Project grant. A default becomes a grant
  only through an explicit owner action: creating a Project with "Use my
  default connections" (on by default in the UI), or the launcher's existing
  first-launch grant, which preselects the default. Each grant goes through
  the existing `GrantProviderConnection` path and writes the existing
  content-free audit record. Changing a default never changes the grants on
  existing Projects. A grant is consent to give that repository's code a
  lease that spends the user's login, so it is given once per Project.
- **Project setup scripts.** The profile never adds to the Project setup
  argv, the prepared environment cache key, or the cache contents. Personal
  files are still applied after preparation, outside the cache, and outside
  Moments (ADR 0024). Setting up one Project never puts a user's profile into
  a shared cache.
- **Delivery commit author.** Delivery uses the Project's commit author if
  it has one. Otherwise it uses the identity pinned on the Capsule.
  Otherwise it refuses a commit, as today. This settles the open question in
  #83: the identity is set once in the profile, a Project that commits as a
  bot keeps its override, and the Project row does not get a silent copy.

### Pins: a snapshot, not a live mount

- Creating a Capsule copies the current profile into the pin: one setup
  revision per harness (existing) and the git identity. `clean` pins neither.
- In this version a Capsule does not follow the profile on its own. Doing so
  would rewrite the home of a harness that keeps session state there.
  Instead, the owner can choose "Update this Capsule to my current profile".
  That re-pins the setups and identity in one transaction. It is refused
  while a Run or Thread session is active on the Capsule. The next session
  start materializes the new pin through the existing path.
- Existing Capsules keep their pins. Edits made inside a Capsule stay there
  and are never written back to the profile. A Capsule cannot change the
  profile or its own pin: the supervisor token, gateway leases, and
  coordination mailbox have no route to these endpoints.

### `meridian import`

`meridian import` is a new top-level command. `meridian harness import`
stays and does the same per-harness import.

1. **Detect.** Check which manifest roots exist, honoring each root's
   override variable. Read the global git config (`$GIT_CONFIG_GLOBAL`, or
   `~/.gitconfig` and `$XDG_CONFIG_HOME/git/config`) with a small parser
   that reads only `[user] name` and `email`. It does not run `git`, does
   not follow `include` or `includeIf`, and reports when it skipped them.
   Nothing else under `$HOME` is walked.
2. **Preview.** Build the existing preview for each detected harness: files
   kept, files excluded and why, portability warnings, and pinned npm
   packages.
3. **Diff.** Fetch the caller's current default revisions through the
   existing `GET /harness-setups/{id}/contents` (authenticated, `no-store`).
   Show files added, changed, and removed. Keep the fetched contents in
   memory only. Show secret-shaped values only as "excluded", never as
   bytes. Add one line per harness for login: "covered by connection
   `<name>`", or "no connection: Threads will fail until you connect one."
4. **Git identity.** Show it as its own line: "Use `Ada Lovelace
   <ada@example.com>` from `~/.gitconfig` as your commit identity."
5. **Confirm once.** `--yes --accept-exclusions` stays the non-interactive
   form, and it prints how many exclusions it accepted. A harness over the
   bundle limit (256 files, 128 KiB each, 512 KiB total) is listed as "not
   imported: over the limit" and is not saved. The others can still be.
6. **Save atomically.** One idempotent `POST /profile/imports` carries every
   bundle, its report, and the git identity choice. The server validates
   every bundle with the existing `Bundle.Validate`. It saves each one as a
   new default revision, seals each report, and updates the profile row, all
   in one transaction. A failure saves nothing.

Remote daemons work as they do today: `meridian --server URL import`. The
browser still cannot scan the laptop (ADR 0024), and this ADR adds no local
helper process. The browser folder picker stays the path for people who
will not use the CLI, one harness at a time, and it saves its report the
same way.

The report comes from the client, so the server treats it as display data
from the owner. Its shape is checked and bounded (at most 512 entries,
bounded path and reason lengths), and the UI renders it only as text.

### The "why this differs from your laptop" diagnostic

`GET /capsules/{capsuleId}/profile-diagnostic` is computed in
`internal/app` from control-plane records only. It never asks the Capsule,
whose reports are hostile. It returns typed rows:

- pinned revision vs the profile's current revision per harness, with both
  save times ("pinned to Tuesday's import; your profile changed Thursday")
- files in the pin vs files in the current revision, from plaintext revision
  metadata
- exclusions and warnings from the pinned revision's sealed report:
  credential fields stripped, MCP servers dropped (local binaries, unpinned
  `npx`), absolute paths kept
- whether the Project has a connection grant for each pinned harness, and
  whether that connection is revoked or needs sign-in
- where the commit identity comes from: Project, pin, or none
- `clean`, when the Capsule was created without a profile

The response carries no file contents and no credential values. It is
owner-only and `no-store`, because the report contains config key paths and
laptop paths. The response says "matches your profile" only when the pin is
current and no warning or exclusion is outstanding. It never says the
Capsule matches the laptop, since the control plane has not seen the laptop
since the last import.

Repository files such as `AGENTS.md`, `CLAUDE.md`, and `.mcp.json` remain the
clone's business. Meridian does not merge them with the profile. The
diagnostic does not inspect the workspace to report on them.

### API, events, and audit

- `GET /profile` and `PUT /profile` (git identity and default connections,
  with the resource version), `POST /profile/imports`,
  `GET /capsules/{capsuleId}/profile-diagnostic`, and
  `POST /capsules/{capsuleId}/profile-update`. All of them use the existing
  idempotency keys, which are scoped to the caller (ADR 0046).
- One content-free, owner-filtered `profile.updated` event.
- New audit actions: profile import, a change to a default connection, and a
  Capsule profile update. They record the object ids only, never the
  identity strings or file paths.

### UI

- **First run.** Three steps: import this computer, connect a model, and
  create a Project. The page says "we found Codex and Claude" only after a
  `meridian import` has saved a profile. Until then the step says "Run
  `meridian import` on this computer" and offers the folder picker. It moves
  on when the `profile.updated` event arrives. The connect step is skipped
  when a connection already covers each imported harness. The Project form
  accepts a GitHub SSH URL and stores the HTTPS form, so the existing clone
  path works.
- **"What Capsules inherit".** A settings page with one row per harness:
  files, the default connection, and exclusions from the last import. It also
  shows the git identity, then each Project's overrides. Hooks, plugins, and
  MCP servers carry one sentence: they are code that runs inside the
  Capsule. Saving shows which Capsules are still pinned to older revisions.
- **Capsule panel.** It shows the diagnostic, with "Update this Capsule to
  my current profile" while no session is active, and "Start a new Capsule
  from my profile". It has no action that brings back an excluded file.

### Out of scope

- A Capsule that follows the profile automatically
- Saving a Capsule's edits back to the profile, or any export from a Capsule
- A local helper process the browser talks to (it would need its own ADR,
  per ADR 0024)
- The manifest connection overlay
- SSH keys, `~/.ssh`, and `core.sshCommand`. Private clone stays the
  `git_https` grant (ADR 0014).
- Dotfile managers, shell rc files, editor settings, and `~/.config/*`
  directories without a manifest
- Raising the bundle limit, or storing skills as artifact blobs
- Checking whether tools a setup expects exist in the image
- Sharing profiles between users

## Consequences

- Existing setups, Project overrides, and Capsule pins keep working. A user
  with no `user_profiles` row behaves exactly as before.
- The schema change is forward-only (ADR 0011): `user_profiles`, a
  per-revision report envelope table, and an identity column on Capsule
  pins, with owner triggers that follow migration 0023.
- Commit identity is self-asserted. Meridian does not check it against the
  member's OIDC email, and Delivery commits are not signed. A member can
  commit under any name, as they already can through the Project fields.
- The import report is the importing client's account of what it excluded.
  A modified CLI can save a misleading report about the owner's own setup.
  The bundle itself is still validated on the server.
- A default connection makes it easier to grant a login to a new Project. A
  grant still needs one owner action per Project, and it is audited.
- The threat model gains a "Workstation profiles" section, an invariant, and
  a residual-risk paragraph.

## Implementation order

Each step is its own PR, as #84 was (#155 to #167):

1. Store: migration, `user_profiles`, report envelopes, the identity on
   pins, owner triggers, and the departed-transfer change.
2. App: profile get and update, atomic import, the identity on pins,
   Delivery author resolution, Capsule profile update, and the diagnostic.
3. API: OpenAPI paths, handlers, and `make generate`.
4. `capsuled`: the generated `.gitconfig` in the setup home.
5. CLI: `meridian import` with detection, diff, and git identity.
6. UI: the first-run import step, "What Capsules inherit", and the Capsule
   diagnostic panel.
