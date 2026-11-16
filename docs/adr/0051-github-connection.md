# ADR 0051: One owner-bound GitHub connection

- Status: Accepted
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

- On a loopback request to a single-user Station, the owner can confirm "Use
  this computer's GitHub login." Only that request runs `gh auth token`. The
  station does not read it at startup, on a timer, or for a remote request.
  Team mode never reads the host login: that login belongs to the machine,
  not the member.
- Otherwise the station runs GitHub's device flow. The browser shows the
  user code and opens `https://github.com/login/device`. The device code
  stays in station memory. The public OAuth client id is
  `--github-oauth-client-id` or `MERIDIAN_GITHUB_OAUTH_CLIENT_ID`. It is not
  a secret. An empty id disables the device flow. There is no client secret.
- `meridian github connect` on the owner's computer can read that computer's
  `gh` login after the same confirmation and send the token to the station.
  The station still does not read `gh` itself in that case.
- `meridian github connect --stdin` is the fallback when neither `gh` nor a
  client id is available.

The requested scope is `repo` plus `read:user`. `repo` is the classic scope
that covers private clone, push, and pull requests.

### Resolution

For each of clone, push, and pull request:

1. A Project field that names a secret (`gitSecretName`, `gitPushSecretName`,
   `githubAPISecretName`) uses that secret. A missing or wrong-purpose secret
   fails closed. The connection is not a fallback for a named secret.
2. An empty name uses the owner's GitHub connection when its status is
   `active`.
3. With neither, clone stays anonymous. Push and pull request are refused.

A clone that cannot authenticate is terminal. That includes a missing
credential for a private or inaccessible repository, HTTP 401, 403, or 404
from git or the GitHub API, and the "could not read Username",
"Authentication failed", and "Repository not found" families. The reconciler
does not retry it. The stored failure code is `repository_access` and the
text is "Meridian couldn't access owner/repo. It may be private." Git stderr
is not stored. Creating a Project on github.com with no named `git_https`
secret probes that repository first and refuses with the same error when
GitHub says it cannot be read. A probe that cannot reach GitHub does not
block creation; the clone is still classified if it fails.

Clone and push still receive one one-shot HTTPS grant for that git process.
The pull request is still opened by `meridiand`. The connection token is not
placed in a Capsule environment, a command argument, a log, or an API
response.

A rejected login (`401`, or `403` with a SAML/SSO signal) is stored as
`needs_reconnect` with reason `invalid`, `expired`, or `missing_access`. The
UI shows Reconnect GitHub. A connection in that state is not used for a
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
