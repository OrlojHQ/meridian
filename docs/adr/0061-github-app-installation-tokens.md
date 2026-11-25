# ADR 0061: GitHub App installation tokens for Capsules

- Status: Accepted
- Date: 2026-11-23
- Extends: ADR 0051 (one GitHub connection), ADR 0053 (remote CI), ADR 0054 (Station Git gateway)
- Tracks: #210

## Context

ADR 0051 stores one owner-bound GitHub login on the Station. Clone, push, and pull requests use it when a Project does not name a purpose-scoped secret. The login is often the owner's whole GitHub account. ADR 0054 keeps that credential out of a Delivery push: the Capsule pushes to the Station Git gateway with a one-time capability, and the Station adds the credential.

An agent working inside a Capsule still needs `gh` and the GitHub API while the session runs: pull request status, checks, reviews, issues, comments, and git fetch or push to github.com. Putting the owner's long-lived login in the Capsule would let anything running there, including repository code, use that login against every repository the owner can reach.

A Station may also be shared by a team. Members should not pass personal tokens around for Capsule access.

## Decision

Each Station can register one GitHub App through GitHub's manifest flow. The App's private key, webhook secret, and client secret are sealed with the installation credential KEK and stay on the Station. Capsules receive only a short-lived installation access token for the Capsule's one repository.

### Creating the App

Settings offers **Create GitHub App**. The Station returns a same-origin handoff page. That page is the only document allowed to post the manifest, and it may post only to `https://github.com`. The Station's other pages cannot post outside themselves. The desktop app opens the handoff in the system browser, because the webview does not submit that form. The handoff is authorized by a one-time ticket, not the session cookie. GitHub redirects back to the Station with a one-time code. The Station exchanges that code and stores the credentials. The code is authorized by a state value the Station issued to the signed-in user who started the flow, because the redirect does not carry the session cookie. The redirect origin must match the request host or the Station's configured public URL. The state value rides on the manifest form's action URL (`…/settings/apps/new?state=…`), not in `redirect_url`: GitHub rejects a `redirect_url` that has a query, and appends `code` and `state` to the redirect itself.

In team mode only an administrator can create, remove, or refresh the App. Any signed-in user can see that it exists and which accounts it is installed on. Settings does not refresh installations by itself.

The user installs the App on their account or organization and can choose **All repositories**. No per-repository secret is required for a private repository the installation includes.

The manifest asks for:

| Permission | Access | Used for |
| --- | --- | --- |
| Contents | write | clone, fetch, push, and reading files |
| Pull requests | write | pull requests, reviews, and review comments |
| Issues | write | issues and issue comments |
| Checks | read | `gh pr checks` and CI |
| Statuses | read | commit statuses |
| Metadata | read | repository metadata, which GitHub requires |

### What a Capsule receives

When Capsule GitHub access is on (the default, under What Capsules inherit) and the App is installed on the repository, a harness session and a new workspace shell receive an installation token:

- `GH_TOKEN` and `GITHUB_TOKEN` in that process environment
- a git credential helper for `github.com` that reads a token file
- a `gh` wrapper on `PATH` that reads the same file

The helper, the wrapper, and the git config do not contain the token. Setup, preparation, and project checks do not receive it.

GitHub expires an installation token after one hour. The Station mints a new one when ten minutes remain and writes it into the Capsule. `gh` and git read the file, so a long session keeps working. The App private key never leaves the Station.

Run output and structured frames replace the token with `[redacted]` before they are stored or shown. The harness brief tells the agent the access exists, that it is limited to this repository, and not to print the token. A pull request the agent opens with `gh` is opened by the App. A pull request Meridian opens from an approved Delivery still uses the owner's GitHub login when one is connected, so that pull request stays authored as the user.

### When the App does not apply

If the App is not created, not installed on the repository, or the switch is off, the Capsule gets no GitHub token. The Project page says which of those it is. Clone, push, the Delivery pull request, and remote CI keep using a named secret or the owner's GitHub login, as ADR 0051 describes.

When the App does cover the repository:

- Clone uses an installation token instead of the owner's login, so the login is not sent into the Capsule for that clone. A named `git_https` secret still wins.
- The Git gateway uses an installation token for the push when minting succeeds. A named `git_push` secret still wins. If minting fails, the gateway falls back to the owner's login, which still never enters the Capsule.
- Remote CI prefers an installation token. A failure of that token does not mark the owner's login as needing reconnect.

### Webhooks

If the Station was given a public HTTPS address, the manifest subscribes to check, pull request, and installation events and points the webhook at the Station. The Station verifies the signature with the webhook secret and wakes the existing CI poll. It does not replace the poll. A Station without a public HTTPS address does not register a webhook. ADR 0053's poll remains the source of the stored observation.

## Consequences

- A process inside a Capsule can read the installation token from the environment, the token file, or a `gh` process. The token can act on one repository, with the permissions above, until it expires. It cannot act as the owner's other repositories. The App private key is not in the Capsule.
- Losing the installation KEK makes the App unrecoverable, as with other sealed credentials. The user creates the App again.
- An existing Capsule keeps running its old supervisor until it is replaced. The token and `gh` apply to a Capsule started from a current image.
- The repository picker still lists repositories the owner's GitHub login can see. The Project page says when the App does not cover that repository.

## Alternatives considered

- Put the owner's GitHub login in the Capsule. Rejected. That login usually reaches every repository the owner can, and anything in the Capsule can read it.
- Proxy every `gh` and API call through the Station so no token enters the Capsule. Rejected for this change. The agent runs `gh` and git itself; a proxy would have to speak both protocols and still reveal a credential to those processes at request time. A one-repository token that expires is the narrower credential those processes need.
- User-to-server tokens so Capsule pull requests are authored as the user. Not this change. Delivery pull requests stay on the owner's login. A later change can add user-to-server without putting that token in the Capsule.
