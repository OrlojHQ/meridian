# ADR 0029: Coordinate child Threads under an operator grant

- Status: Accepted
- Date: 2026-11-07
- Amends: ADR 0001
- Extends: ADRs 0012, 0013, 0016, 0019 and 0025

## Context

ADR 0001 excludes delegation and multi-agent orchestration. Users nonetheless
run many Capsules for one goal and track them by hand, and coding harnesses
can plan and split work themselves. The missing piece is not a planner but a
bounded, auditable way for one structured Thread's agent to start, message,
wait on and cancel other Threads, and receive their results, without giving a
hostile Capsule a credential, a host runtime API or control-plane authority.

Three ways to carry a request out of the Capsule were considered:

| Option | Verdict |
| --- | --- |
| Mount the installation bearer or a scoped API token in the Capsule | Rejected. Standing authority readable by any hostile process; the bearer is installation-wide (ADR 0015). |
| HTTPS callback from the Capsule to a host coordination gateway with a leased token | Rejected. Needs a Capsule-reachable host origin, adds Capsule-to-host ingress, puts an exfiltratable bearer in the environment and conflicts with deny-by-default egress. |
| New frame kinds in `meridian.adapter.v1` | Rejected. Harness tools cannot emit adapter frames, so the adapter would have to intercept tool calls, widening a deliberately closed protocol. |
| The daemon pulls requests from a `capsuled` mailbox | Chosen. No credential exists in the Capsule; the requester is the Capsule whose private transport the daemon dialed. |

## Decision

- Amend ADR 0001: Meridian may provide bounded parent/child Thread
  coordination. It still does not implement DAG or workflow engines, task
  routing, planning or an agent loop. The hosted harness decides what to
  delegate; Meridian enforces whether it may.
- An operator-created Coordination Grant, bound to one lead Thread and created
  in the same transaction as that Thread, is the only source of coordination
  authority. It fixes the Project, an allowed harness subset of the Project's
  structured allowlist (default: the lead's harness), depth (1), per-parent
  fan-out (default 4, at most 16), total children (at most 32), live child
  Capsules (at most 16), messages per link (default 50, at most 500), an
  absolute deadline (default 8 hours, 5 minutes to 72 hours) with a deadline
  action, and the spawn approval mode (`auto` or `operator`). Only the operator
  creates, updates or revokes a grant. The operator may loosen or tighten
  limits after creation. Switching `spawnApproval` to `auto` does not start
  spawns already waiting for approval.
- Capsules hold no coordination credential. `meridian-coord`, a CLI and stdio
  MCP server installed in Capsule images, writes requests in the closed,
  versioned, bounded `meridian.coord.v1` protocol to a `capsuled` mailbox on a
  private Unix socket (`$HOME/.meridian/coord.sock`, mode 0600 in a 0700
  directory). The mailbox refuses every request as `coordination_disabled`
  until the daemon configures it for a lead or a subagent and again once the
  grant ends.
  `meridiand` long-polls it over the existing authenticated
  private Capsule transport, identifies the requester by that channel and
  validates each request against the grant, Project policy and the
  requester's links. Capsules never call the public API and no Capsule-to-host
  route is added.
- The mailbox bounds pending requests (64, 2 MiB), request rate (30 a minute),
  record size (256 KiB, prompts 128 KiB) and the reply cache (128 entries),
  and rejects unknown fields. Every mutating request carries a Capsule-scoped
  idempotency key that the daemon records with a digest of the request before
  side effects; a reused key with different content is a conflict.
- A child is an ordinary Project Thread in its own fresh Capsule (ADR 0016),
  linked to its parent by a ThreadLink with an operator-visible name limited
  to `[a-z0-9-]{1,48}`. A Capsule may address only links whose parent is its
  own Thread. With depth fixed at 1, children receive `report`,
  `propose-delivery`, and `whoami`; they cannot spawn, send, wait, cancel,
  list, result, or patch.
- Every Capsule carries an immutable, content-free `role` of `main` or `sub`.
  Sub-Capsules record their lead Thread and link. The UI nests subagents under
  their lead and labels them; `role` is presentation only, is never read by an
  authorization check, and never suppresses attention. A subagent's permission
  and input requests are answered only by the operator.
- A prompt may cross into a Capsule from an explicit client mutation or from a
  coordination request accepted under an active grant. Delegated prompts carry
  encrypted `from_lead` provenance and are never shown as operator input.
  Startup recovery never invents a send; a still-pending accepted request may
  be replayed under its idempotency key.
- Child results return as bounded reports: the daemon derives a report from
  the child's own encrypted transcript (the last assistant message, at most
  16 KiB) when a turn ends, the session ends or the link closes, unless the
  child sent an explicit `report` (`done`, `blocked`, or `failed`) that
  overrides that default. The lead reads reports through `wait` or `result`;
  unread reports wake an idle lead with one coalesced `from_subagents`
  prompt, framed as untrusted data and re-encrypted under the lead's key.
  Delivery acknowledgements are content-free sequence numbers. `patch`
  returns a bounded `git format-patch` series plus the working-tree diff from
  the child's Capsule (default 1 MiB, max 4 MiB), produced with hooks
  disabled. Patch bytes are never stored. Link views carry only HEAD, base
  commit, and a content-free diffstat. Reports and patches are hostile
  input to the lead.
- Revocation, deadline and cancellation are daemon-side state changes that
  take effect on the next request without Capsule cooperation. Closing a link
  by cancellation or revocation cancels the child's run and then pauses its
  Capsule. Closing a link never deletes a child Capsule or transcript. An
  optional installation cap (`--max-live-capsules`) refuses coordinated
  spawns when the installation already runs that many Capsules.
- Coordination is advertised only where the provider implements structured
  transport and the mailbox end to end (Docker and Agent Sandbox). Audit
  events for grants, links, forwarded messages, reports and rejections carry
  identifiers, codes and sizes only. Metrics and traces remain content-free.
- Amendment, board and operator notices: `GET /board` lists every Capsule's
  status from Capsule, Thread, Run, Delivery and link rows plus the existing
  in-memory Thread attention (ADR 0025). It reads no transcript text beyond
  that derivation, polls no Git state, and grants nothing; subagents that need
  the operator are listed under their own name. When the operator messages a
  subagent directly, its active link counts the message (`operatorMessages`,
  a plaintext counter) outside `maxMessagesPerLink`, and the lead's next
  report wake says that the operator messaged that subagent and how many times,
  never what was said, although the subagent's own next report may repeat it.
  A notice never wakes a lead on its own. Messages to a
  subagent whose link has closed are not counted and leave the link untouched,
  so resuming a stopped subagent does not restart its stop window.
- Not in this decision, and left for later ADR amendments: depth above 1
  and queueing spawns over capacity. Auto-archive and promotion are covered
  by the subagent lifecycle amendment below. Operator spawn approval and grant
  updates are covered by the amendment after that. Explicit reports and
  bounded patches are covered by the amendment after that. Proposed Deliveries
  are covered by the amendment after that. Delivery approval under ADR 0020 is
  unchanged: an agent may propose a Delivery, and only the operator can approve
  it.

## Consequences

Users can hand a goal to one lead and supervise the resulting tree, with the
lead as their point of contact and every child remaining a normal reviewable
Thread. Revocation needs no token rotation, and coordination works under
deny-by-default egress.

Any process in a granted Capsule can spend the grant's allowance and send
hostile prompts to its children, and hostile child reports can prompt-inject
the lead. Grants bound but do not prevent cost amplification. Reports copy
child content into the lead transcript, so crypto-shredding a child does not
erase copies already delivered to its lead. Pull-based delivery adds one
long-poll per coordinating Capsule and some latency. This ADR does not claim
untrusted multi-tenant isolation.

## Amendment: OpenCode and Pi leads

Every official pack except `dsh` now exposes `meridian-coord` to its
structured profile, so OpenCode and Pi Threads can lead. (`dsh` followed in
the amendment below.)

- OpenCode registers it as a stdio MCP server in the `mcpServers` list the
  ACP adapter sends on `session/new` and on resume, from the profile's
  `--mcp-server` entry, like the Claude Code and Codex profiles. The
  alternative, an `opencode.json` or `OPENCODE_CONFIG` file, would be
  written into or merged with repository-controlled configuration and would
  outlive the session; the ACP list is per session and comes only from the
  trusted image profile.
- Pi 0.87 has no MCP client. The Pi pack ships a `meridian-coord` Agent Skill
  that describes the CLI, and the structured profile loads it with `--skill`.
  The agent runs the CLI with its bash tool. The skill is instructions only;
  it adds no executable and no authority.
- Pack skills are trusted image content. The pack installer accepts only
  `skills/<name>/SKILL.md` regular files of at most 16 KiB with matching
  `name` and a `description`, and installs them read-only under
  `/usr/local/share/meridian/<pack>/skills/`.

Registration changes how the agent discovers the tool, not what any process
in a Capsule may do: `meridian-coord` was already on every Capsule's `PATH`
and the mailbox still refuses everything outside an active grant.

## Amendment: DeepSeek Harness leads

The `dsh` structured profile registers `meridian-coord` the same way as
OpenCode: one `--mcp-server` entry in the trusted image profile, sent in the
`mcpServers` list of ACP `session/new` and of the resume call. Every official
pack now exposes the tool, so a `dsh` Thread can lead. Nothing is written
into `dsh`'s home or the workspace for it, and the Cordis patch that
`capsuled` writes for a provider connection is unchanged. A `dsh` child
needs a usable provider connection like any other `dsh` Thread.

## Amendment: subagent lifecycle

Finished subagents are archived automatically, and the operator can promote a
subagent to a main Capsule.

- **Auto-archive.** A grant's `archiveAfter` (default 24 hours; 0 disables
  it; otherwise 1 minute to 30 days in whole seconds) can be changed later by
  the operator, like the other grant limits. A subagent is archived once it has been idle for that long
  after it finished: its link is active and its last report is `turn_ended`
  or `done`, or its link closed as cancelled, ended or revoked. Subagents that need the
  operator, are working, have a delivery in flight, failed (report, Run or
  Capsule), or were promoted are never archived; failed subagents stay until the
  operator acts. A daemon sweep every 30 seconds marks the link `archivedAt`,
  moves an idle Thread to `paused` and cancels its session, and leaves the
  Capsule to ADR 0019 idle pause. `paused` rather than `archived` keeps the
  subagent resumable. Archiving never sends a message, never deletes a
  workspace or transcript, and never resumes a stopped Capsule. The link is
  marked before the session is cancelled, and the report scan skips archived
  links, so ending the session is not reported to the lead as the subagent
  ending. Resuming the subagent's Thread clears `archivedAt`. While archived,
  the lead's `send` is refused as unavailable; only the operator brings a
  subagent back.
- **Promotion.** `POST /capsules/{id}/promote` turns a sub-Capsule into a main
  Capsule (`promotedAt`). By default the link stays open, so the lead can
  still message the subagent and still receives its reports. `detach` also
  closes the link as `detached`; a promoted Capsule that kept its link can be
  detached later. A detached Capsule leaves the tree's live-Capsule count and
  counts only toward installation limits. Promotion is permanent; there is no
  demote. A promoted subagent is never auto-archived, and closing its link by
  cancellation or revocation no longer stops it, because the operator has
  taken it over. Promotion grants no coordination authority: the Capsule
  gets no grant of its own and its agent's requests are still refused.
- **Store rules.** Triggers allow a Capsule's role to change only from `sub`
  to `main` with `promotedAt` set, and its lead and link to be cleared only
  together, on a promoted Capsule. A link's `promotedAt` is permanent, a link
  can close as `detached` only once promoted, and a link is never archived
  and promoted at once. Promotion and archive events carry identifiers only.
- **Presentation.** The sidebar and Inbox list main Capsules
  (`role=main`) plus subagents that need the operator or whose lead is gone,
  and take every lead's rollup from one board read, so hidden subagents add no
  per-Capsule polling. Rollups count archived subagents (also counted as done),
  and the board and the lead's Subagents panel fold them under "Archived (n)".
  A promoted Capsule is its own row, labelled as promoted from its lead, and
  is badged in the lead's fold while it keeps its link.

## Amendment: terminology

A lead Thread's children are called subagents, pairing with the lead agent
that starts them; earlier revisions of this ADR called them helpers. The
rename covers the API, CLI, browser, `meridian-coord` and documentation.
Two stored names keep the old word: migration `0016_helper_lifecycle.sql`,
whose file name is recorded in `schema_migrations`, and the coordination
origin kind `from_helpers` inside transcripts encrypted before the rename,
which reads back as `from_subagents`. Neither changes authority.

## Amendment: operator spawn approvals and grant updates

The operator can require a click before each spawn starts, and can change an
active grant's limits.

- **Spawn approval.** `spawnApproval: auto` (the default) starts each accepted
  spawn. `operator` records the child Capsule, encrypted prompt and ThreadLink
  as `pending_approval` and does not provision it until
  `POST /coordination-requests/{id}/approve`. Deny closes the link as `denied`
  without deleting the Capsule; the spawn still counts toward
  `descendantsCreated`. Pending children do not count as live, are skipped by
  recover and intent lookup, cannot be promoted, and surface on the board as
  `needs_you` / `spawn_approval`. Switching a grant to `auto` leaves already
  queued spawns queued. The lead may cancel a pending spawn. Capsules cannot
  approve or deny their own requests.
- **Grant updates.** `PATCH /coordination-grants/{id}` lets the operator loosen
  or tighten spawn approval, fan-out, descendants, live Capsules, messages per
  link, allowed harnesses, remaining deadline, deadline action and
  `archiveAfter`. Identity (project, lead, depth, createdAt) stays fixed.
  Deadline is remaining time from now, between 5 minutes and 72 hours, and
  cannot pass `createdAt+72h`. Only an active grant can be patched. Every
  mutation uses `expectedResourceVersion` and `Idempotency-Key`.

## Amendment: subagent reports and patches

A subagent can send an explicit result to its lead, and the lead can fetch a
bounded patch of the subagent's git work. Report text and patch bytes are
hostile.

- **Explicit report.** `report {status: done|blocked|failed, summary}` is a
  mutating, Capsule-scoped request a subagent mailbox will relay. The summary
  (at most 16 KiB) is persisted as an assistant response on the child's
  encrypted transcript and is not delivered to the child's harness. It
  becomes `ThreadLink.last_report_status` and overrides the turn-end default
  until a later assistant message is recorded. Ending the child's session
  without a new assistant message does not replace `done`, `blocked`, or
  `failed`. `done` is an archive-eligible finish, like `turn_ended`.
  `blocked` and `failed` stay until the operator or a later report. Leads
  cannot report.
- **Mailbox for subagents.** The daemon enables the mailbox on a Ready
  sub-Capsule with `role=sub` and `depth=1`. Subagents see `report`,
  `propose-delivery`, and `whoami`. The lead still cannot approve a subagent's permission or
  input requests. meridiand polls Ready children the same way it polls the
  lead: pull-only, no Capsule credential, not counted as activity.
- **Patch.** `patch {link, maxBytes}` is a lead-only remote read. `capsuled`
  runs `git format-patch --stdout` from the child's stored `base_commit`
  (HEAD when the Capsule became Ready) plus `git diff HEAD`, with
  `core.hooksPath=/dev/null` and no shell. Default 1 MiB, maximum 4 MiB;
  oversized output is truncated and flagged. The response includes HEAD,
  base commit, and a content-free diffstat. Patch bytes are never written to
  SQLite; only those counts and SHAs are. The board still does not poll Git.
- **Store.** Migration 0018 widens `last_report_status` with `done` and
  `blocked`, records `base_commit`/`head` (lowercase hex, 7–64) and
  non-negative diffstat counts, and allows coordination request kind
  `report`. `base_commit` is immutable once set. Report summaries stay in
  transcript ciphertext.

## Amendment: proposed Deliveries

A coordinated Thread may propose a Delivery of its own Capsule for the
operator to approve. The agent never ships.

- **`propose-delivery {title, body, branch}`.** Mutating and Capsule-scoped.
  Lead and subagent mailboxes both relay it. meridiand inspects git, refuses
  a protected or default branch, and persists a Delivery in state `proposed`
  with `proposedBy: agent`. Title (≤512) and body (≤64 KiB) are hostile
  plaintext. They are stored as bounded PR metadata the same way operator
  ship already stores them. The mailbox response is content-free: id, state,
  and branch. Expected HEAD, tree, Capsule version, and `approvedAt` stay
  empty until the operator approves. The proposal does not acquire the
  capture lock, does not check delivery secrets, and is never executed.
  Recovery skips `proposed` and `rejected`. One proposed or in-flight
  Delivery at a time per Capsule.
- **Operator approval.** `POST /deliveries/{id}/approve` binds
  `expectedResourceVersion` (the Delivery), `expectedCapsuleVersion`,
  `expectedHead`, and `expectedTree`, optionally edits title/body/branch, and
  then follows the existing queued execute path (ADR 0020).
  `POST /deliveries/{id}/reject` is terminal. Both are installation-bearer
  operator APIs; a Capsule cannot call them. There is no auto-approve.
- **Board.** A proposed Delivery is `needs_you` / `proposed_delivery`. The
  UI sanitizes title and body before display and requires the operator to
  type `approve` and bind the freshly inspected HEAD and tree. Auto-archive
  still skips a Capsule that needs the operator or has a Delivery in flight.
- **Store.** Migration 0019 rebuilds `deliveries`: states `proposed` and
  `rejected`, `proposed_by` agent|operator, `approved` 0|1, nullable approval
  facts until approve, and identity that keeps `proposed_by` immutable.
  Approval fields may change only from `proposed`.

## Amendment: operator removes one subagent

The operator can stop and remove a single subagent without revoking its
lead's grant. Grant-wide revocation stays the only way to stop the lead from
spawning.

- **`POST /thread-links/{id}/remove`.** An installation-bearer operator API
  with `expectedResourceVersion` and an idempotency key; there is no
  coordination request kind for it, so no Capsule can call it. It closes that
  one link as `cancelled` with a failed/cancelled report, the same record the
  lead's own `cancel` writes, so the lead hears that the subagent was
  cancelled. It then cancels the subagent's session and deletes its Capsule
  once no Run is active there. The grant, its limits, and sibling links are
  unchanged; the deleted Capsule leaves the live Capsule count, so the lead
  can spawn into the freed slot.
- **This is the one path where closing a link deletes a Capsule.** It is the
  operator's explicit Capsule delete, scoped to a subagent: the workspace goes
  with the Capsule and encrypted Thread history is kept, exactly as
  `POST /capsules/{id}/delete`. Lead cancel, revocation, the deadline, and
  auto-archive still only cancel and pause.
- **Refusals.** A promoted subagent is a main Capsule and is refused; the
  operator deletes it from its Capsule. A Capsule that is not running while a
  session is still live is refused before anything changes, because ending a
  session needs the Capsule running and deletion waits for the session.
- **Ordering and crashes.** The link closes first, in one transaction with
  the replay record. Deletion is attempted in the request and retried by the
  coordination tick while the session finishes cancelling. The pending
  deletion is held in memory only: if the daemon restarts, or the session does
  not end within the cancel grace, the closed link falls back to the existing
  cancel-and-pause sweep, and the operator can remove it again. Nothing is
  left running in either case.
- **Visibility.** Link views carry a derived, content-free `capsuleRemoved`
  flag. The lead's Agents panel moves removed subagents to a Removed fold and
  offers the per-row stop only on subagents that are neither promoted nor
  removed.
