# ADR 0057: Start Project Threads on a schedule

- Status: Proposed
- Date: 2026-11-18
- Amends: ADR 0001 (product boundary), ADR 0016 (where a prompt may come from)
- Extends: ADR 0019 (idle pause and setup reuse), ADR 0020 (delivery binds the
  reviewed tree), ADR 0025 (Thread attention), ADR 0029 (installation Capsule
  cap), ADR 0046 (ownership), ADR 0052 (checks and rework)

## Context

Some coding work happens on a schedule: a weekly dependency bump, a nightly
flaky-test triage, a pass over new lint warnings every morning. Today the
operator either remembers to start that Thread or writes a crontab on the
host that calls the API with a bearer token. The crontab works. It also leaves
a standing installation or personal token in a file, cannot tell whether last
night's run is still going, does nothing about runs missed while the laptop
was asleep, and shows up in the audit log as the operator typing.

Other coding-agent products now ship scheduled agents (Cursor Automations,
Claude Code routines, Codex automations, Devin automations, Jules scheduled
tasks). Most of them also take event triggers and can chain or condition
runs. Meridian should not copy that part. ADR 0001 keeps Meridian away from
workflow engines, and an event trigger turns outside text into a prompt.

The building blocks already exist. `POST /projects/{id}/threads` records an
idempotent Thread intent and lets a reconciler start a fresh Capsule (ADR
0016). Setup Moments make that start cheaper (ADR 0019). Checks and remote CI
can send the agent back before anyone looks (ADRs 0052 and 0053). Delivery
already needs a human to approve one exact tree (ADR 0020). The thing that is
missing is a clock that may start the same intent, with limits on what it can
spend while nobody is watching.

## Decision

### Boundary

Amend ADR 0001: Meridian may start a Project Thread on a schedule the
operator wrote. A schedule is a clock, a fixed prompt, and limits. It is not
a workflow:

- one schedule starts one Thread per fire, and nothing else;
- the prompt is operator text stored when the schedule is saved. It has no
  template variables and takes no input from a previous run, the repository,
  or an outside system;
- there are no event triggers, conditions, chains, or fan-out. A scheduled
  Thread cannot hold a Coordination Grant (ADR 0029), so it cannot spawn
  subagents.

Event triggers (GitHub, chat, webhooks, error trackers) need a separate ADR.

### The schedule resource

A schedule belongs to one Project and has the Project's owner (ADR 0046). Its
fields:

- `name`, limited to `[a-z0-9-]{1,48}` and unique within the Project;
- `prompt`, at most 32 KiB, encrypted with the installation key. The AAD binds
  the schedule id, owner, Project, and resource version, so a ciphertext
  cannot be moved to another row;
- `harness`, which must be a structured harness on the Project's allowlist.
  Native PTY harnesses are refused, for the reason ADR 0052 gives: there is no
  structured channel to send the prompt or see a turn end;
- `model` and `effort`, both optional. These are option IDs in the form the
  harness reports them (ADRs 0031 and 0033). Leaving one empty uses the
  harness's default, the same as a Thread started by hand. Model names stay
  out of plaintext records, as ADR 0031 requires, so both are stored in the
  same encrypted envelope as the prompt. The schedule form offers the list
  that the most recent session of that harness in this Project reported, and
  also accepts a typed ID;
- `cadence`, a five-field cron expression evaluated in an explicit IANA time
  zone. Fires must be at least 15 minutes apart. A local time that does not
  exist because of a daylight-saving change is skipped, and a local time that
  happens twice fires once;
- `maxRunDuration`, from 5 minutes to 24 hours, default 2 hours;
- `catchUpWindow`, from 0 to 24 hours, default 1 hour (see below);
- `enabled`, plus a content-free `disabledReason` when Meridian turned it off.

The API is `GET` and `POST /projects/{projectId}/schedules`, `GET`, `PATCH`,
and `DELETE /schedules/{scheduleId}`, `POST /schedules/{scheduleId}/run` to
fire it now, and `GET /schedules/{scheduleId}/fires`. Optimistic concurrency
uses the schedule's resource version. The CLI gains `meridian schedule` with
the same verbs and `--json`. These are added to `api/openapi.yaml` when this
is implemented, not before.

Only the owner's authenticated API can create or change a schedule. A
Capsule has no way to do it: `meridian.coord.v1` does not get a schedule
verb, and no repository file defines or suggests a schedule.

### Firing

`meridiand` keeps `nextFireAt` for each enabled schedule. When it is due, the
daemon creates a Thread through the same service path as `POST
/projects/{id}/threads`, attributed to the owner, with an idempotency key of
`schedule:<id>:<scheduled time in RFC 3339 UTC>`. A daemon that restarts in
the middle of a fire uses the same key and gets back the same Thread. It does
not start a second one.

Amend ADR 0016's list of places a prompt may come from: an explicit client
mutation, a coordination request accepted under an active grant (ADR 0029),
and now the fire of an enabled schedule. The Thread's first message carries
encrypted `from_schedule` provenance with the schedule id. The UI labels it
as the schedule's prompt, not as something the operator typed just now.
Startup recovery still never invents a send.

When a schedule names a model or effort, the prompt is held back until the
session reports its models. The daemon then sends `set_model` and, if
needed, `set_effort` through the ADR 0031 path, and delivers the prompt only
after the harness reports the chosen values as current. A harness that did
not negotiate `models` or `effort`, an ID missing from the reported list, a
refused switch, or no report within 2 minutes all end the fire as `failed`
with a content-free reason, and the prompt is never sent. Meridian does not
fall back to the default model. A scheduled run on a model the operator did
not pick could cost more or behave differently, and nobody would be there to
notice. These failures count toward the auto-disable below.

Each fire starts a fresh Capsule. Setup Moment reuse applies as it would for
any new Thread. No transcript, worktree, or session state carries over from
the previous fire.

A fire is skipped, and the skip is recorded, when:

- the previous fire's Thread is still creating, preparing, or working. Runs of
  one schedule never overlap;
- the installation's `--max-live-capsules` cap is reached. ADR 0029 applies
  that cap to coordinated spawns only; this ADR applies it to scheduled fires
  too. It still does not limit Capsules the operator starts;
- the schedule already holds its maximum number of kept Capsules (see
  below);
- the owner is disabled or has departed (team mode), or the Project is being
  deleted.

`POST /schedules/{id}/run` follows the same rules. It is an operator action,
so it is audited as the operator.

### Missed fires

The daemon has to be running for a schedule to fire. Meridian does not wake a
sleeping host. When `meridiand` starts, or the host wakes, each schedule fires
at most once for missed times, and only if the most recent missed time is
inside `catchUpWindow`. Every other missed time is recorded as `missed`. A
week-long shutdown does not start a week of runs.

### The run

A scheduled Thread has exactly the authority an operator-started Thread in
that Project has: the same image allowlist, isolation class (ADR 0050),
egress policy, provider connection and gateway lease, checks, and harness
permission settings. A schedule cannot turn on a more permissive harness
mode, add trusted MCP tools, or widen egress.

A permission or input request waits for the operator as usual and raises
Thread attention (ADR 0025). Nothing answers it automatically. If the run is
still going when `maxRunDuration` runs out, Meridian cancels the run and
pauses the Capsule, the same deadline action as a coordination grant. Pause
does not delete anything.

Project checks and remote CI rework run as configured for the Project, with
the same attempt budget. The agent may propose a Delivery. Only the operator
can approve it, for one exact tree (ADR 0020). A schedule never ships.

When a fire's Thread goes idle, Meridian records a content-free outcome
for the fire: `proposed` (a delivery proposal exists), `changed`,
`no_changes`, `waiting` (on the operator), `timed_out`, or `failed`.

`no_changes` is decided once, by `meridiand`, when the Thread first goes
idle with nothing waiting on the operator and the last check suite, if any,
is not running. All of these must hold, read through `capsuled` with hooks
disabled:

- HEAD is the base commit the Capsule was cloned at, and no other local
  branch or tag was created;
- `git status --porcelain --ignored=no` is empty, so there are no tracked
  changes and no untracked files outside `.gitignore`;
- there is no delivery proposal and no outstanding permission or input
  request.

This read does not count as Capsule activity (ADR 0019). If any condition
fails, or the read itself fails, the outcome is `changed`. Doubt keeps the
Capsule.

A `no_changes` fire ends the session, archives the Thread, and then deletes
the Capsule through the existing Capsule delete path. Deleting a Capsule
removes its runtime and workspace and keeps the encrypted Thread transcript,
so the operator can still read what the agent looked at and why it found
nothing to do. The setup Moment cache is not affected, and no Moment is
captured first. The fire row records that the Capsule was deleted. A
`no_changes` fire raises no attention and leaves no compute or workspace
behind.

Every other outcome keeps the Capsule. `proposed`, `changed`, and `waiting`
raise attention, and their Capsules pause when idle as usual. `timed_out`
and `failed` raise attention too, and stay paused so the operator can see
what went wrong. The operator deletes these Capsules when they are done
with them, the same as any other Capsule.

A schedule holds at most 5 kept Capsules at once (configurable from 1 to
20). Once it holds that many, the next fire is skipped with reason
`backlog` and attention is raised once. Fires start again as soon as the
operator deletes one. Unreviewed results cannot pile up on disk while
nobody is looking.

### Turning a schedule off

Meridian disables a schedule, and says why, after three fires in a row that
fail before the agent's first turn (provisioning, clone, setup, credential,
or model selection errors). It also disables it when the provider connection it needs
is `needs_reconnect`, or when the Project no longer allows its harness. A
disabled schedule raises attention once and fires again only after the owner
turns it back on.

In team mode, an admin transfer of a departed member's Project (ADR 0046)
moves its schedules but leaves them disabled. The new owner reads the prompt
and turns each one on. Admins cannot read prompts. Deleting a Project (ADR
0039) deletes its schedules and fire history.

### Records

Schedules and fires are stored with `owner_user_id` like other child rows.
A fire row keeps the scheduled time, the actual time, the outcome or skip
reason, and the Thread id. Events, metrics, traces, and audit records carry
identifiers, cadence, and outcome codes. They never carry the prompt. The
audit log records schedule create, change, enable, disable, delete, and
run-now as the operator, and each clock fire as `schedule:<id>` acting for
the owner.

## Threat model changes

When this is implemented, `docs/threat-model.md` gains a "Scheduled Threads"
section that covers:

- **Unattended spend.** A schedule spends compute and model calls with nobody
  watching. Bounds: at least 15 minutes between fires, no overlap, one
  catch-up, `maxRunDuration`, the installation live-Capsule cap, and
  auto-disable after repeated start failures. A schedule with a slow cadence
  and a long duration can still use up a provider quota or plan allowance
  before the operator looks.
- **No standing foothold.** A fresh Capsule per fire means a process left
  behind by a hostile dependency does not survive into the next run, except
  through what the repository and the setup Moment already carry. ADR 0019
  keeps post-setup work out of setup Moments.
- **Prompt injection without a witness.** A hostile repository or dependency
  can steer a scheduled agent the same way it steers an attended one. The run
  gets no extra authority, permission requests still wait for the operator,
  and Delivery still needs approval. Anything the harness does without
  asking, within allowed egress and trusted tools, happens with nobody
  watching for up to `maxRunDuration`. This is a new residual risk.
- **Credentials used while the owner is away.** A fire uses the owner's
  provider connection and GitHub connection without the owner being present.
  No new credential is stored, and the schedule holds no token. Revoking the
  connection or disabling the member stops future fires. Every fire is
  audited.
- **Deleting empty runs.** A `no_changes` Capsule is deleted, so any
  process a hostile dependency left in it is gone along with whatever it
  wrote outside the repository. The encrypted transcript is the only record
  of that run. An agent can make a run look empty by reverting its work, but
  that only deletes the Capsule faster, and nothing leaves it. An agent can
  also keep a Capsule alive by leaving a change, but that run then raises
  attention and counts toward the kept-Capsule limit. Both directions fail
  toward the operator seeing it. A failed state read counts as `changed`.
- **Only the owner sets the clock.** Capsules, repository files, and outside
  systems cannot create, change, or trigger a schedule.

## Consequences

The operator writes a prompt and a cadence once and reviews results as they
arrive. Runs with nothing to show do not ask for attention and do not leave
a Capsule behind. Runs that do have something to show stay until the
operator is done with them, and a schedule stops firing while its backlog is
full. Meridian stays
the thing that runs the environment, not the thing that decides what to do:
the harness does the work and the operator ships it.

The daemon gains a durable clock loop, two tables, and a new prompt source
to reason about. A laptop Station only runs schedules while it is awake, and
the catch-up rule means a nightly job on a closed laptop runs once at the next
wake or not at all. Unattended runs use the owner's model and GitHub
quotas.

The external-crontab approach keeps working for anyone who wants it.

## Alternatives considered

- **Wake an existing Thread on a timer**, keeping its Capsule and transcript.
  This is useful for "check on this PR every hour". Rejected for this
  decision: it keeps one mutable Capsule alive indefinitely, grows the
  transcript without bound, and lets a hostile process from one run see the
  next one. The CI watch (ADR 0053) already covers the PR case. A later ADR
  can add a bounded heartbeat for one Thread.
- **Only document the external crontab.** Rejected as the only answer. It
  needs a standing token on the host, cannot skip an overlapping run or bound
  a missed-fire backlog, and is audited as the operator.
- **Event triggers in the same change.** Rejected. A local Station may not
  have a public address (the reasoning in ADR 0053), and an event payload is
  outside text that would become a prompt. This needs its own threat model.
- **Prompt templates, conditions, or chaining runs.** Rejected under ADR 0001.
  That is a workflow engine.
- **Auto-approve the Delivery of a scheduled run**, for example dependency
  bumps that pass CI. Rejected under ADR 0020. A new commit needs a reviewed
  approval.
- **Keep empty runs paused, or archive them, instead of deleting.** Rejected.
  A paused Capsule holds a workspace, and with Docker a stopped container,
  until someone deletes it by hand. A nightly schedule would leave hundreds
  of them. The transcript is kept, and that is what an operator actually
  needs from an empty run.
- **Delete the Capsules of shipped runs automatically.** Deferred. A pushed
  commit may still get review comments or CI failures that the agent should
  fix in the same Capsule. A later decision can delete after the pull request
  closes.
- **A permissive harness mode for scheduled runs**, so they do not stop on
  permission prompts. Rejected. A schedule should not be a way to get more
  authority than starting the same Thread by hand. Projects that want fewer
  prompts use the existing harness settings and trusted MCP tools (ADR 0045),
  which apply to every Thread.
