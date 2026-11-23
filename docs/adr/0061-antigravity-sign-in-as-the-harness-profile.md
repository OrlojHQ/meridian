# ADR 0061: Keep the Antigravity sign-in as the harness's own profile

- Status: Proposed
- Date: 2026-11-22
- Amends: ADR 0024 (subscription token caches), for Antigravity only
- Extends: ADRs 0006, 0014, 0026, 0030, 0040, and 0046

## Context

Antigravity CLI (`agy`) is Google's coding agent. Its `agy acp` command is an
Agent Client Protocol v1 server, so Meridian's existing `acp` driver
(ADR 0030) can run it as a structured harness. A Google AI Pro or Ultra plan
is used by signing in with a Google account. `agy` runs that OAuth flow with
its own client and keeps the result in its own profile directory.

Every other subscription login Meridian supports stays off the Capsule:

- Claude (ADR 0036): the daemon runs Claude Code's OAuth flow, and the
  provider gateway attaches the login to each request.
- ChatGPT (ADR 0038): the daemon runs OpenAI's published third-party flow,
  and the gateway attaches the token.

Neither works for Antigravity. Google's Antigravity Additional Terms
(Section 6) call using third-party software, tools, or services to access
the Service a breach, with OpenClaw using the Antigravity OAuth login as the
example. The FAQ repeats this for Claude Code, OpenCode, and OpenClaw and
recommends an API key for third-party agents. Forum replies written in
Google's voice (not marked as staff) say two things. Running the
**unmodified** `antigravity-acp` binary with its own sign-in inside an ACP
client such as T3 Code or Zed is permitted for single-user development.
Tools that **extract or proxy** the OAuth token are what get suspended.
Suspensions are real: a wave of `403 TOS_VIOLATION` errors from
`cloudcode-pa.googleapis.com` was reported on 2026-11-05 and 06.

`agy` 1.5.14 has a `--cloud_code_endpoint` flag, so the gateway could
technically sit in front of `cloudcode-pa`. That would be exactly the
proxying the terms forbid, so it is rejected.

Keeping the sign-in inside one Capsule is allowed, but a Capsule's home is a
tmpfs. The login would be lost with every new Capsule and every container
restart (ADR 0044). That is the per-Capsule sign-in that ADR 0038 removed
for Codex, and it is not acceptable UX.

An experiment on 2026-11-22 with `agy` 1.5.14 in the `meridian-capsule:dev`
image (read-only root, tmpfs home, no D-Bus) found:

- `agy acp --login` prints a Google authorization URL. The user signs in,
  `https://antigravity.google/oauth-callback` shows a code, and `agy` reads
  the code from stdin and writes its profile.
- Without a D-Bus session bus, `agy` stores the token in a file and needs no
  environment workaround.
- Two files are enough to sign in elsewhere: `antigravity-oauth-token`
  (581 bytes of JSON: an access token, refresh token, expiry, an
  `auth_method`, and the account email) and `cache/acp-auth-state.json` (the
  sign-in method). Copied into containers with other hostnames, they served
  ACP turns with no sign-in. Nothing in them is tied to the machine.
- `agy` refreshed an expired access token itself, and the refresh token did
  not change.
- The login's scopes are `cloud-platform`, `userinfo.email`,
  `userinfo.profile`, `cclog`, `experimentsandconfigs`, `aicode`, and
  `openid`.
- `agy acp` started a stdio MCP server given in `session/new`, ran its tool
  after one `session/request_permission`, and did the same after
  `session/resume` in a second container.

## Decision

### An official `agy` pack

`images/capsule-agy/` pins the Linux `agy` release tarball from
`storage.googleapis.com/antigravity-public`, per architecture, with SHA-256
checksums computed from the files Google's release manifest signs with
SHA-512. The pack installs:

- a native profile running the `agy` terminal UI;
- a structured profile:
  `acp --mcp-server meridian-coord=/usr/local/bin/meridian-coord,mcp -- /usr/local/bin/agy acp --disable-telemetry`.
  `agy` keeps its data in `$HOME/.gemini/antigravity-cli/`, under the
  runtime's home and never under the workspace, so the profile follows a
  personal setup's home (ADR 0024) with no directory flag;
- a launcher that sets `AGY_CLI_DISABLE_AUTO_UPDATE=1`, because the image
  pins the binary on a read-only root.

The structured profile never uses `agy -p`, which the FAQ says is not
supported inside third-party applications on consumer plans. Leads get the
coordination tools and brief through the `acp` driver with no new driver
code.

### An `antigravity` connection holds the harness's own profile

`antigravity` is a new provider connection type. It is not a gateway
connection: no gateway route, lease, or upstream exists for it. It holds one
sealed **profile**: the two files above, at their paths under `agy`'s
application data directory.

- **Meridian never uses the token.** The daemon does not refresh it, send
  it upstream, attach it to a request, put it on the gateway, or derive
  anything from it. Only `agy` reads it, in a Capsule, and only `agy` talks
  to Google.
- **The daemon checks the profile's shape only.** It accepts exactly the
  two paths: regular files, each at most 16 KiB, valid JSON, with the
  expected keys and string types, and an `auth_method` of `consumer`. It
  keeps the account email as the connection's display label. It never logs
  or returns file contents.
- **Storage:** the profile is sealed with the credential-KEK connection
  envelope (ADR 0014). The envelope's authenticated context binds the
  connection ID, the type, the owner (ADR 0046), and a credential generation
  counter.
- **Grants:** a Project must authorize the connection for the `agy`
  harness, as with every connection. In team mode a connection is its
  owner's alone and can be granted only to that user's Projects.

### Sign-in runs `agy` itself, in a sign-in Capsule

"Sign in with Google" in Harness settings starts a **sign-in Capsule**. It
uses the `agy` pack image and has no repository, no Project secrets, no git
credentials, no harness setup, and no provider connection. The only program
it runs is the pinned `agy` binary, so repository code can never forge the
profile or swap in another Google account.

1. `capsuled` runs `agy acp --login`, with no shell, and returns the
   authorization URL `agy` prints. Meridian checks that it is an
   `https://accounts.google.com/` URL with the pack's client ID and
   redirect, and shows it in the dashboard.
2. The user signs in in their own browser and pastes the code from
   `antigravity.google/oauth-callback` into the dashboard. The daemon passes
   it to `capsuled`, which writes it to `agy`'s stdin. Neither stores it.
3. When `agy` exits successfully, `capsuled` reads the two files with rooted
   operations and returns them. The daemon checks and seals them, then
   deletes the sign-in Capsule.
4. The ceremony lives in daemon memory, belongs to the user who started it,
   expires after ten minutes, and writes only that user's connection.
   Deleting the Capsule ends an abandoned attempt.

Reauthorization runs the same ceremony and replaces the sealed profile in
place.

### Every `agy` Capsule gets the profile when its harness starts

When a native or structured `agy` runtime starts with an authorized
connection, the daemon opens the profile and sends it to `capsuled` with the
runtime start, the same way personal setups reach the home (ADR 0024).
`capsuled` writes it to `.gemini/antigravity-cli/` in the runtime's home
with mode 0600, using rooted operations, before it starts `agy`. Native and structured
sessions read the same profile.

- The home stays outside Moments, archives, and workspace capture, so the
  profile never reaches a Moment, an artifact, or another Capsule.
- Nothing is copied back. If `agy` ever rotates the refresh token, the
  daemon's copy goes stale and the next sign-in replaces it. Write-back is
  left out until Google is seen rotating tokens.
- When Google rejects the login, `agy` answers `auth_required` and the
  `acp` driver reports `acp_auth_required`. The daemon then marks the
  connection `needsSignIn`. The Thread links to Harness settings, and one
  new sign-in serves every Capsule again.

### Sign-out

Removing the connection deletes the sealed profile and stops new Capsules
from receiving it. Capsules already running keep their copy until they stop,
because Meridian cannot reach into them to revoke it. The settings page says
so and links to the Google Account page where the user can remove
Antigravity's access. That revokes the refresh token everywhere, including
the user's own `agy` installs, since they share Google's client.

### API-key use is the separate, preferred route for everyone else

A Gemini API key fits the provider gateway as it is: `agy`'s enterprise
model gateway settings (`AGY_LLM_GATEWAY_URL` and related) can point it at
a lease, so the key never enters a Capsule. A `gemini` gateway connection
is a separate decision and not part of this ADR.

## Consequences

- One sign-in per installation (per user in team mode) replaces one per
  Capsule for `agy`, in both terminals and Threads.
- **This is the first subscription login that enters a Capsule.** Any code
  in an `agy` Capsule can read the refresh token and keep using it until the
  user revokes it at Google. Because the login carries `cloud-platform`,
  that can reach the user's Google Cloud projects, not only Antigravity.
  The settings page states this before sign-in and recommends a dedicated
  Google account. This is the exposure ADR 0024 and ADR 0038 refused. It is
  accepted here only because every way of keeping the token out of the
  Capsule breaks Google's terms and risks the user's account.
- Compromise of the control plane and the credential KEK reveals the
  refresh token, as for the other OAuth connections.
- Google's enforcement is behavioral and unpublished. Meridian runs only
  the unmodified binary with its own sign-in, which is the use Google's
  replies call permitted. But Google has not addressed containers or
  remote hosts, so a user's account could still be suspended. The
  documentation says so and says the installation must be the account
  holder's alone.
- Plan models are what `agy acp` offers the account. Google gives ACP
  clients only the plan's Gemini models.
- The profile format is `agy`'s private format. A release that changes it
  fails the shape check. Sign-in then reports the change, and existing
  profiles keep working until `agy` rejects them.

## Open questions

- Whether the native terminal UI reads the same token file as `agy acp`.
  Both default to `$HOME/.gemini/antigravity-cli/`, but the experiment ran
  only `agy acp`.
- Whether Google rotates Antigravity refresh tokens on any schedule.
- Whether Google will publish a sanctioned third-party route, as OpenAI did.
  That would allow moving the login back onto the control plane.
