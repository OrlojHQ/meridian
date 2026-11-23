# ADR 0061: Capsule sessions can use the owner's GitHub connection

- Status: Accepted
- Date: 2026-11-23
- Amends: ADR 0051 (the connection token may enter a Capsule session), ADR
  0006 and ADR 0014 (a standing token in the session environment), and ADR
  0054 (an agent with this access can `git push` outside Delivery)
- Extends: ADR 0049 (what a Capsule inherits)

## Context

Connecting GitHub under Account stores one token on the Station. ADR 0051
uses it for clone, push, and pull requests, and keeps it out of the Capsule.
Clone gets a one-shot grant that is discarded. Push goes through the Station
Git gateway (ADR 0054). Pull requests are opened by `meridiand`. The Capsule
image has git and no `gh`. "What Capsules inherit" copies the git name and
email and not the token. A harness process therefore has no GitHub
credential. An agent that runs `gh` or calls the GitHub API is anonymous, and
`gh` is not installed, so a private repository looks missing.

The owner who connected GitHub expects an agent in a Capsule to use that
account: `gh`, the API, and git fetch and push.

## Decision

Capsule GitHub access is a switch on the owner's profile. It is on by
default, including for an owner who has not saved a profile. The owner turns
it off under What Capsules inherit. The next session follows the switch. It
is not pinned on the Capsule.

When the switch is on and the owner's GitHub connection is active,
`meridiand` opens the token and sends it with the session start. It is not a
named secret and does not replace one. A named `git_https`, `git_push`, or
`github_api` secret still wins for clone, Delivery push, and the pull request
`meridiand` opens. The session uses the connection.

`capsuled` places the token in the harness process environment as `GH_TOKEN`
and `GITHUB_TOKEN`, and in a workspace shell started with the token. It does
not place it in the Capsule process environment, so setup, preparation, and
project checks do not inherit it. A token inherited from the Capsule process
is dropped. The token is not written into git config, a credential file, a
command argument, a log, or the Start metadata.

Git config for the session gains a credential helper for `https://github.com`
and `https://gist.github.com`. The helper reads `GH_TOKEN` and answers only
for those hosts. Other hosts do not receive the token.

The Start and Resume metadata gains `github` with the login only, when the
login matches the GitHub login grammar. `systemBrief` tells every structured
harness, including a plain Thread, that `gh` and git can use the account, and
that curl against `https://api.github.com` is the fallback when `gh` is not
on `PATH`. The brief says not to print the token or put it in a file, a note,
or a message.

`gh` 2.105.0 is installed in the Capsule image. System packages belong in the
image. The prepared workspace is project dependencies and does not carry
`gh`.

Before run output or a structured frame is stored or shown, the session token
is replaced with `[redacted]`. A token split across reads is held until it is
complete. Logs, transcripts, and Raw events therefore do not contain it.
Redaction does not change files in the workspace.

Delivery push is unchanged. It still uses the gateway and a one-time
capability, and that path still does not send the connection token. An agent
with this access can also `git push` to github.com with the session token,
without a separate Delivery approval.

The same path is used for Docker and Agent Sandbox, and for the desktop app
and a server. Isolation class does not change it. Each owner has their own
connection and switch.

A connection that needs reconnect, a missing connection, or the switch off
sends nothing. The session still starts.

## Consequences

Hostile code in a Capsule can read the token from the process environment and
exfiltrate it for as long as the token remains valid. That is the reach ADR
0054 avoided for Delivery. The switch is how the owner accepts it, and it is
on because a connected account is what agents are expected to use. Turning it
off restores the ADR 0051 boundary for the next session.

A Capsule built from an older image does not have this supervisor or `gh`.
The owner starts a new Capsule from a current image. Reconnecting GitHub is
not required when the connection is already active. A shell that is already
running does not pick up the token until a new shell starts.

Losing the credential KEK still makes the connection unrecoverable. The token
is not persisted in a new place.
