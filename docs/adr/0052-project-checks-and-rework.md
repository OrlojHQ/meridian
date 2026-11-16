# ADR 0052: Project checks and automatic rework

- Status: Accepted
- Date: 2026-11-16
- Extends: ADR 0007 (harness runs), ADR 0012 (encrypted transcripts), ADR 0020 (delivery binds the reviewed tree), ADR 0025 (live turn state), ADR 0050 (isolation class)

## Context

When an agent says it is finished, the operator is usually the first to learn that the work does not build, test, or lint. The operator then types "tests are failing, fix them" and waits, sometimes several times, before there is anything worth reviewing.

Meridian already runs a bounded argv inside the Capsule for Project setup, already knows when a structured Thread goes from working to idle, and already binds Delivery approval to an exact tree. Checks should use those pieces. They should not become a new Capsule lifecycle state, a new privilege, or a loop the operator cannot stop.

Remote CI status on a GitHub pull request is a later input. This decision keeps a place for it and does not read it.

## Decision

A Project may list required checks. Each check is an argv, the same shape as setup: Meridian does not start a shell. An empty list leaves the Project behaving as it does today.

The list is its own resource, `GET` and `PUT /projects/{projectId}/checks`. Saving checks does not bump the preparation generation and does not rebuild environments. Optimistic concurrency uses the Project resource version. The current Project list is what a Capsule runs; it is not frozen at Capsule creation.

When a structured Thread goes idle with nothing waiting on the operator, Meridian waits five seconds and then runs the checks inside the Capsule, one suite at a time, through `POST /v1/checks/run` on `capsuled`. The supervisor reads the worktree before and after. If the tree changes during the run, the result is `stale` and is not used as a gate. Each command has a timeout. The retained output is the tail, at most 64 KiB, with control characters stripped and likely secrets redacted. The tail is encrypted with the installation key before it is stored. Events, audit records, notifications, and the pull request body never include it.

If a check fails and attempts remain, Meridian sends the agent a message through the normal Thread path. The message is stored as a system message, labeled Meridian in the UI, and delivered to the harness as a user turn so the agent continues. The message names the check, the command, the exit code, and a bounded tail, and it says which attempt this is. `maxReworkAttempts` is the total number of tries, including the first. Zero means one run and no rework. The default is 3. The counter resets when the operator sends a new task.

The loop stops when the budget is used, when the operator sends a message, pauses, cancels, or rewinds, or when the operator turns off auto-fix for that Capsule. A cancelled run is not an attempt. A `running` row left behind by a daemon restart is marked `error` and queued once.

A native PTY run has no turn signal. Meridian runs checks when that run exits and only reports the result. It does not type into the terminal. Subagent Capsules do not run checks automatically.

An agent delivery proposal stays hidden until checks pass or the budget is used up. The operator can still ship. The Ship panel says whether checks passed, failed, or are out of date for the exact tree being approved. Passing checks do not enable shipping by themselves, and failing checks do not disable it. The pull request body gains a server-written Meridian checks section for that tree, with names, commands, and durations, and no log output.

Checks run as the same user in the same Capsule, without broker grants, provider credentials, or a Docker socket. They are a quality gate. The agent can change the commands it is asked to satisfy. The human approves the tree, including any test changes.

A repository file `.meridian/checks.yaml` may be offered as a suggestion. It is never applied by being read.

A later GitHub connection can record a remote status as another check result with the same summary, banner, and tree binding. This decision does not fetch one.

## Consequences

The operator adds one command, such as `make test`, and then sees work that passed, or a clear "Checks failing (n/n)" when the budget is used. Intermediate failures do not ask for attention.

Checks can still be defeated by the agent, can be slow, and do not start databases or browsers. A flaky check uses up an attempt; there is no separate flake retry. Output tails are untrusted text fed back to the agent, which is the same trust the agent already has when it runs the tests itself, and the message says so.

## Alternatives considered

- Folding checks into the environment resource. Rejected because every environment save invalidates prepared workspaces.
- Freezing the check list onto each Capsule at creation. Rejected for now so fixing a command does not require a new Capsule. The list can be frozen later if that becomes the surprise.
- Typing a rework prompt into a native PTY. Rejected. There is no structured channel, and inventing keystrokes would be brittle.
- Requiring a passing result before Ship is enabled. Rejected for this pre-v1 release. The banner is unambiguous and the operator decides.
