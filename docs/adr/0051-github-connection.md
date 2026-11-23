# ADR 0051: One owner-bound GitHub connection

- Status: Accepted; implemented in v0.11.1. Amended by ADR 0061 for Capsule sessions.
- Date: 2026-11-16
- Extends: ADR 0014 (purpose-scoped secrets), ADR 0020 (delivery), ADR 0046
  (per-user ownership), and ADR 0049 (the first-run steps)
- Tracks: #197

## Context

Private clone, push, and pull requests each needed their own pasted token:
`git_https` on the Project, then `git_push` and `github_api` from the CLI.
The station never offered a single sign-in, and the first-run path stopped
at "paste a token" for a private repository.

A GitHub user token can perform all three jobs. ADR 0014 still forbids
treating one stored secret as three purposes. Capsules must not receive a
standing token. Pull requests stay on the station.

## Decision

Store one GitHub connection per owner. The token is an envelope under the
credential KEK. The authenticated context binds the connection id, owner, and
login, and is not a named-secret context. Login, status, scopes, and source
are metadata. The row is not moved when a departed member's work is
transferred.

### How a login is obtained

The default path is this computer's `gh` login. When `gh` is installed and
signed in, Connect GitHub is one confirmation: "Use your GitHub CLI login
(@user)?" That confirmation is what runs `gh auth token`. The station stores
that token unchanged, so organization SSO grants and approved-app
authorizations that already apply to `gh` still apply. It is not exchanged
for a new OAuth grant.

The station finds `gh` on `PATH`, then at `/opt/homebrew/bin/gh`,
`/usr/local/bin/gh`, `/home/linuxbrew/.linuxbrew/bin/gh`, `~/.local/bin/gh`,
and `/usr/bin/gh`, then on the login shell's `PATH`. `MERIDIAN_GH_PATH` or
`--gh-path` names one absolute binary and is not searched past. A desktop
app started from the graphical session inherits
`PATH=/usr/bin:/bin:/usr/sbin:/sbin`; before it starts the bundled station
it prepends the login shell's `PATH` and those install locations.
`gh auth status` and `gh auth token` run with that environment, and with
`HOME` filled in when the session left it empty, so a keyring token stored
for the user can be read. The status probe does not keep command output
and does not take the token. The settings card and `meridian github connect`
say which case it is: signed in, not signed in, signed in but the token
could not be read, or not found, and the path that was searched.

The station does not read `gh` at startup, for a remote request, or in team
mode. That login belongs to the machine, not a team member. After the owner
has confirmed a host login, a later connection check on loopback may read
`gh auth token` again. If `gh` has re-authenticated, the stored token is
replaced. If `gh` is signed out or the token is rejected, the connection
becomes `needs_reconnect` and the UI shows Reconnect GitHub.

The device flow is only the fallback when `gh` is missing, signed out, or
unable to read its token. The page says which of those it is. It shows a
device code only when a public OAuth client id is configured; otherwise it
does not offer one. The device code stays in station memory. The public
OAuth client id is `--github-oauth-client-id` or
`MERIDIAN_GITHUB_OAUTH_CLIENT_ID`. It is not a secret. An empty id disables
the device flow. There is no client secret. A device-flow token is a
different grant from `gh` and may still need organization approval.

`meridian github connect` asks the same question before using `gh`.
`--device` skips `gh`. `--stdin` reads a token when neither `gh` nor a
client id is available. Declining the `gh` question does not start the
device flow.

The requested scope is `repo` plus `read:user`. `repo` is the classic scope
that covers private clone, push, and pull requests.

### Resolution

For each of clone, push, and pull request:

1. A Project field that names a secret (`gitSecretName`, `gitPushSecretName`,
   `githubAPISecretName`) uses that secret. A missing or wrong-purpose secret
   fails closed. The connection is not a fallback for a named secret.
2. An empty name uses the owner's GitHub connection when its status is
   `active` and the repository is an HTTPS `github.com` URL on the default
   port. Any other host never receives the connection's token.
3. With neither, clone stays anonymous. Push and pull request are refused.

A GitHub rate limit (429, or 403 with `X-RateLimit-Remaining: 0`,
`Retry-After`, or a rate-limit message) is not an access answer. The access
probe treats it as "could not check", like a network error.

A clone that cannot authenticate is terminal for that attempt. That includes
a missing credential for a private or inaccessible repository, HTTP 401,
403, or 404 from git or the GitHub API, and the "could not read Username",
"Authentication failed", and "Repository not found" families. The reconciler
does not retry it on its own. Creating or replacing an active GitHub
connection retries each Failed Capsule with that failure once. If the clone
fails for access again, the Capsule stays failed until the connection
changes or the owner retries. The stored failure code is `repository_access`
and the text is "Meridian couldn't access owner/repo. It may be private."
Git stderr is not stored. Creating a Project on github.com with no named `git_https`
secret probes that repository first and refuses with the same error when
GitHub says it cannot be read. A probe that cannot reach GitHub does not
block creation; the clone is still classified if it fails.

Clone and push still receive one one-shot HTTPS grant for that git process.
The pull request is still opened by `meridiand`. The connection token is not
placed in a Capsule environment, a command argument, a log, or an API
response.

A rejected login (`401`, or `403` from the user lookup with a SAML/SSO
signal) is stored as `needs_reconnect` with reason `invalid`, `expired`, or
`missing_access`. The UI shows Reconnect GitHub. A `403` on one repository
or pull request, including organization SSO, does not retire the login: the
token can still be approved for other organizations, which is the usual
state of a `gh` token. A connection in `needs_reconnect` is not used for a
grant, so a public clone still works. A non-empty scope list that does not
include `repo` is `missing_access` immediately. An empty scope list is kept,
because some tokens do not report classic scopes.

### Out of scope

A team GitHub App, installation tokens, and other forges are a later change.
The desktop app uses the same pages as the web UI. It does not gain a bridge
command that can read `gh` and hand the token to a page.

## Consequences

Existing purpose-scoped secrets keep working and take precedence. A new
Project created from the repository picker stores no git secret; clone, push,
and pull request use the connection. Losing the credential KEK makes the
connection unrecoverable, as with other secrets. A person who can read the
data directory and the KEK can still open the token.
