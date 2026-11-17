# Project checks

A Project can list the commands that must pass before the work is worth reviewing. Add one command, such as `make test`. When a structured Thread finishes a turn, Meridian runs the commands inside the Capsule. If one fails, Meridian sends the failure back to the agent and tries again, up to the attempt limit. You are asked to look when the checks pass, or when the attempts are used up.

An empty list changes nothing.

## What you configure

Each check is an executable and its arguments. Meridian does not start a shell. A pipeline is an explicit `sh -c` argv, which you can type in the command field as `sh -c 'go test ./...'`.

- Up to 16 checks.
- Each argument is at most 4096 bytes.
- The timeout defaults to 10 minutes and cannot exceed 60 minutes. A timeout counts as a failure.
- **Automatically send failures back to the agent** is on when checks exist.
- **Attempts** defaults to 3, which includes the first run. `0` runs the checks once and does not send the agent back.

The desktop Project page, `meridian project checks set`, and `PUT /projects/{id}/checks` edit the same list. Saving checks does not rebuild the Capsule environment.

If a Ready Capsule contains `.meridian/checks.yaml`, the Project page can offer those commands. Choosing them only fills the form. The file never changes what runs until you save.

```yaml
checks:
  - name: test
    argv: ["make", "test"]
    timeoutSeconds: 600
```

## What you see

The Capsule shows the current attempt:

- **Running checks · attempt 1 of 3** while the commands run.
- **Reworking · attempt 2 of 3** after a failure that was sent back. The Thread shows the message as from Meridian, not from you.
- **Checks passed** when the suite passed on the current tree.
- **Checks failing (3/3)** when the budget is used up. Review is still allowed.

Only the pass and the used-up budget ask for you. A failure that still has attempts left does not.

**Stop auto-fix** ends the loop for that Capsule. Checks can still be run by hand. Sending a message, pausing, cancelling, or rewinding also stops the current loop, and the attempt counter starts over on your next task.

`meridian capsule checks CAPSULE --run --follow` runs the suite from the terminal. `meridian capsule get` prints the summary.

## Review

The Ship panel says whether the checks passed, failed, or are out of date for the exact tree you are approving. Editing the tree after the checks makes them out of date and offers **Run checks**. Shipping stays available either way: you decide.

The pull request body gains a Meridian checks section written by the server from the stored result for that tree. It lists the command names and durations. It does not include command output. The pull request still points at the commit and tree you reviewed.

A proposal from the agent stays hidden until the checks pass or the attempts run out.

## What the commands can do

Checks run as the same user, in the same Capsule, with the same isolation as the agent. They do not receive broker grants, provider credentials, or a Docker socket. They are not a security boundary. The agent can change a Makefile. You approve the tree, including test changes.

Checks have to be self-contained. Meridian does not start a database or a browser for them.

A native terminal session has no turn signal. Meridian runs the checks when that session exits and reports the result. It does not type into the terminal. Subagent Capsules do not run checks on their own.

Output is kept as a short tail, encrypted at rest, and shown to you and to the agent. It is not written into events, audit records, notifications, or the pull request.

## GitHub CI

After Meridian opens a pull request, or pushes a new commit to one it already opened, it reads that commit's GitHub checks. You see them beside the local commands:

- **Waiting on CI** while GitHub is still running.
- **CI passed** when the checks succeeded.
- **CI failing (n/n)** when they failed. The count is the same attempt budget as the local commands. **Stop auto-fix**, a new message, pause, cancel, and rewind stop the loop the same way. A failure is sent to the agent only when the Project has saved its checks settings with automatic rework on, and only between turns: if the agent is working or waiting on you, the message waits for the next idle turn.
- **Can't read CI: Reconnect GitHub** when the login itself was rejected.
- **Connect GitHub to read CI** when nothing is connected. Local checks still run. This is a prompt, not a failed check.
- **Can't read CI for this repository** when GitHub refuses that one repository. The login stays connected.

A failure can be sent back to the agent. Meridian does not push the fix. The pull request updates only after you review the new commit and ship it, the same as any other push.

The station polls GitHub. It does not need a webhook. A watch that cannot read CI (no login, a login to reconnect, or a repository GitHub refuses) polls more slowly as it ages, up to once an hour. A watch stops when its Capsule fails, is sealed, or is deleted, and after seven days. The tail you and the agent see is a short redacted excerpt. The pull request body lists names and conclusions only.
