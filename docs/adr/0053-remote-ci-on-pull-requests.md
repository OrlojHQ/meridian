# ADR 0053: Read GitHub CI on a Meridian pull request

- Status: Accepted; implemented in v0.11.1
- Date: 2026-11-16
- Extends: ADR 0020 (delivery binds the reviewed tree), ADR 0051 (one GitHub connection), ADR 0052 (Project checks)

## Context

After Meridian opens or updates a pull request, the commands that ran inside the Capsule are no longer the whole story. GitHub runs its own checks on that commit. The operator should see those results next to the local ones, for the same commit, and a failure should be able to go back to the agent the same way a local failure does.

A single-user Station should not need an inbound webhook to learn this. A later GitHub App can replace the poll without a second storage shape.

## Decision

When a Delivery push or pull request succeeds, Meridian records a watch for that commit and pull request. `meridiand` polls check runs and commit statuses. The first wait is five seconds. The wait doubles until it is one minute. A commit with no checks stays "Waiting on CI" for eight polls, then "No CI reported for this commit".

The credential is the same resolution as clone, push, and pull request:

1. A Project that names `githubAPISecretName` uses that secret. A missing secret fails closed. The connection is not a fallback.
2. An empty name uses the owner's active GitHub connection.
3. With neither, local checks still run. CI says "Connect GitHub to read CI". That is a prompt, not a failed check.

A `401` on the connection, or a connection already marked `needs_reconnect`, says "Can't read CI: Reconnect GitHub" and uses the same reconnect reasons as ADR 0051. A `403` or `404` on one repository, including organization SSO, does not retire the login. It says "Can't read CI for this repository" and keeps polling slowly. A named secret that GitHub rejects does not retire the connection either.

The watch stores the check name, conclusion, link, and a short tail. The tail is annotations or the check summary, at most 4 KiB, with control characters stripped and likely secrets redacted, then encrypted with the installation key. Events, audit records, and the pull request body do not include it. The token never enters a Capsule environment, a log, or an error string.

`ApplyRemoteCI` is the only write of an observation. The poller calls it. A later webhook or GitHub App can call it with the same snapshot and retire the poll.

Remote CI rework is opt-in: it runs only for a Project that has saved its checks settings with automatic rework on. Other Projects still see the CI result. A CI failure uses the Project's attempt budget, Stop auto-fix, and the epoch that an operator message, pause, cancel, or rewind advances. When attempts remain, Meridian sends the agent a system message that names the failed checks and a short tail, and says the text is CI output. The message is sent only while the Thread is between turns and not waiting on the operator; otherwise it waits for the next idle turn. The pull request is not updated by that message. ADR 0020 still requires a reviewed push of one exact commit. This decision does not push. The Capsule says to review the fix before it can be pushed. A later push uses the existing Delivery path, and the new commit gets its own watch.

The summary and the server-written pull request section list CI names and conclusions beside the local commands. They do not list tails.

## Consequences

The operator sees "Waiting on CI", "CI passed", "CI failing (n/n)", or a plain prompt to connect or reconnect. A failure can send the agent back without shipping the fix. Polling costs one API read per open watch, at most once a minute after the first minute. A repository that never reports checks stops after the quiet polls. A watch that cannot read CI waits a tenth of its age between polls, from one minute to one hour. A watch stops when its Capsule is no longer creating, preparing, ready, or paused, and seven days after it was recorded.

## Alternatives considered

- An inbound webhook as the only source. Rejected for a local Station, which may not have a public address. The observation function is ready for one later.
- Pushing the agent's fix as soon as CI fails. Rejected. ADR 0020 binds a push to a reviewed commit, and a new fix is a new commit.
- Treating a repository `403` as a dead login. Rejected. It matches ADR 0051: one organization can refuse a token that still works elsewhere.
