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
  action, and the spawn approval mode. Only the operator creates or revokes a
  grant. Grants cannot be widened after creation, and the only approval mode
  is `auto`, which the operator chose by creating the grant.
- Capsules hold no coordination credential. `meridian-coord`, a CLI and stdio
  MCP server installed in Capsule images, writes requests in the closed,
  versioned, bounded `meridian.coord.v1` protocol to a `capsuled` mailbox on a
  private Unix socket (`$HOME/.meridian/coord.sock`, mode 0600 in a 0700
  directory). The mailbox refuses every request as `coordination_disabled`
  until the daemon configures it for a lead and again once the grant ends.
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
  own Thread. With depth fixed at 1, children receive no coordination
  authority.
- Every Capsule carries an immutable, content-free `role` of `main` or `sub`.
  Sub-Capsules record their lead Thread and link. The UI nests helpers under
  their lead and labels them; `role` is presentation only, is never read by an
  authorization check, and never suppresses attention. A helper's permission
  and input requests are answered only by the operator.
- A prompt may cross into a Capsule from an explicit client mutation or from a
  coordination request accepted under an active grant. Delegated prompts carry
  encrypted `from_lead` provenance and are never shown as operator input.
  Startup recovery never invents a send; a still-pending accepted request may
  be replayed under its idempotency key.
- Child results return as bounded reports: the daemon derives a report from
  the child's own encrypted transcript (the last assistant message, at most
  16 KiB) when a turn ends, the session ends or the link closes. The lead
  reads reports through `wait` or `result`; unread reports wake an idle lead
  with one coalesced `from_helpers` prompt, framed as untrusted data and
  re-encrypted under the lead's key. Delivery acknowledgements are
  content-free sequence numbers.
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
- Not in this decision, and left for later ADR amendments: operator spawn
  approval, widening a grant, depth above 1, returned patch series, proposed
  Deliveries, auto-archive of finished children, promoting or detaching a
  sub-Capsule, and queueing spawns over capacity. Delivery approval under
  ADR 0020 is unchanged; agents cannot propose or approve a Delivery.

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
structured profile, so OpenCode and Pi Threads can lead.

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
