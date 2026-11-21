# ADR 0059: Standing leads keep long conversations going

- Status: Accepted; implemented in v0.15.0
- Date: 2026-11-20
- Amends: ADR 0029 (coordination grant lifetime and lead wakes)
- Extends: ADRs 0016, 0019, 0025, 0028 and 0058

## Context

Two products now make one long conversation the place where work happens,
instead of many short sessions:

- **Cursor Projects** (beta since 2026-09-10, multiplayer since 2026-11-10).
  A coordinator agent in one long-lived chat plans work, delegates it to cloud
  subagents, and "maintains context over months of work". The coordinator does
  not write code. Subagents start with a clean context and a brief the
  coordinator writes, and the coordinator sees only their final summary. A set
  of Project files, written by agents and the user, syncs to every machine an
  agent uses and carries what was learned between agents. Inside a single chat,
  Cursor writes the full history to a file before summarizing it, and writes
  large shell and MCP output to files that the agent reads with `tail` and
  `grep` rather than receiving inline ("dynamic context discovery"). Cursor
  has not documented how compaction works inside a Project.
- **Grok Bot** (beta since 2026-08-11). Each Bot is a persistent agent with
  its own memory, tools, routines and cloud computer. A Bot is something you
  return to, not a list of chats. Bots hand work to each other through group
  threads, with text-only handoffs. xAI has not described how a Bot's
  conversation is compacted.

Neither product has a secret compaction technique. A long conversation stays
usable because:

1. the conversation that lasts is the one that does not do the heavy work;
2. workers start clean, get a written brief, and return a summary;
3. anything large or durable lives in files the agent reads when it needs
   them, not in the transcript; and
4. the harness compacts what is left.

Meridian already has most of (2): a lead Thread starts subagents in fresh
Capsules, the lead brief tells it to delegate and stay in the conversation,
reports are bounded, and idle leads are woken with reports (ADR 0029). ADRs
0019 and 0058 already let an operator come back to a paused Thread the next
day. A standing lead still cannot work like a Cursor coordinator, for these
reasons:

- **The grant has a fixed end.** A grant's deadline is at most 72 hours after
  `createdAt` and cannot be extended past that. The lead gets at most 32
  subagents in its lifetime. After that the conversation still exists but can
  no longer delegate, so the operator starts a new lead and loses the context.
- **Only a new Thread can become a lead.** The grant is created in the same
  transaction as the Thread. A plain Thread that has built up useful context
  cannot start subagents.
- **Coordination output goes inline into the lead.** A wake carries up to 8
  reports and 64 KiB. `patch` returns up to 4 MiB in the tool result. One
  integration pass can fill a large part of the lead's context, which pushes
  the harness to compact sooner and lose more.
- **Nothing is carried between subagents except the prompt.** The lead
  retypes shared conventions and findings into every spawn prompt.
- **Nothing marks a conversation as one you return to.** A Project is a
  template (ADR 0016), and its page lists separate Threads by recency. A lead
  you have used for two weeks sinks below this morning's one-off Threads.

Plain Threads stay. Not every conversation should delegate, and "Lead agent
with subagents" stays a choice that is checked by default when a Thread is
started (v0.14.0). Nothing in this decision makes a Thread a lead unless the
operator asks for it.

## Decision

### 1. Bound unattended time, not grant lifetime

Replace the absolute grant deadline with a lapse on unattended time.

- A grant records `lastOperatorAt`. It is updated when the operator sends the
  lead a message, approves a spawn, or patches the grant. It is not updated by
  wakes, reports, routines, or any coordination request.
- `unattendedLimit` (default 24 hours, 5 minutes to 72 hours) replaces
  `deadlineSeconds`. Once `now - lastOperatorAt` passes it, the mailbox
  refuses mutating requests as `coordination_lapsed`, and the existing
  deadline action is applied to running subagents. The grant is not revoked.
  The next operator message to the lead reopens it.
- `maxDescendants` becomes a count per rolling 24-hour window (default 8, at
  most 32) instead of a lifetime count. `maxLiveCapsules`, fan-out, depth and
  messages per link are unchanged.
- Revocation stays terminal. `RevokeLeadEnded` still ends the grant when the
  lead Thread ends for good, which does not include idle pause.

This keeps ADR 0029's reason for limits: a compromised or looping lead can
spend no more than one window's spawns and one `unattendedLimit` of time
before a person comes back. The bound moves from how long the conversation
has existed to how long it has run with nobody watching. This matches the
reasoning in ADR 0057, which bounds unattended spend rather than wall-clock
age.

Existing grants migrate with `unattendedLimit` set to their remaining
deadline, capped at 72 hours.

### 2. Make an existing Thread a lead

This is for a plain Thread whose conversation turned out to be worth keeping
and splitting up. It is opt-in, and plain Threads otherwise stay plain.

`POST /threads/{threadId}/coordination-grant` is an operator-only API with
`expectedResourceVersion` and an `Idempotency-Key`. It creates a grant for an
active, structured, `role=main` Thread that has no grant, using the same
policy fields as Thread creation. No `meridian.coord.v1` verb is added for it.

A harness learns its role from Start and Resume metadata, so the brief is
delivered the same way ADR 0058 renews a session: when the Thread is idle,
nothing is waiting on the operator, and the transcript holds resume state,
the daemon stops the Run and resumes it with `{"coordination":"lead"}`. If
those conditions do not hold, the grant is recorded and the brief is
delivered at the next renewal point, before the next prompt. The `acp` driver
gains one change: it sends the `<meridian-context>` block on the first prompt
after a resume whose role differs from the previous session's role, in
addition to the first prompt of a new session.

Leads cannot be demoted. Revoking the grant leaves a plain Thread, as today.

Choices made in implementing this:

- **What can become a lead.** An `active` Thread (one with a live or
  starting session, so a paused Thread is resumed first) of a `role=main`
  Capsule with no lead or link, whose harness is a structured harness on the
  Project's allowlist, in a Project that accepts work. Subagents, promoted
  subagents that kept their link, and Capsules or Threads that already hold a
  grant in any state are refused. All Threads are structured, so a native or
  PTY session has no Thread to upgrade, and a pack that runs only in a
  terminal is refused by the allowlist check.
- **A revoked grant is not replaced.** Revocation stays the operator's
  terminal stop, `coordination_grants.root_thread_id` stays unique, and
  subagent names and links of the old grant are never shared with a new one.
  An operator who wants coordination again starts a new lead Thread. A
  Capsule holds at most one grant, so a later Thread of a Capsule whose
  earlier Thread led is refused too.
- **Who may call it.** Only a user principal. The break-glass admin, a
  Capsule, and the system see the Thread as not found, as for pins. The
  grant's `lastOperatorAt` starts at creation, which counts as operator
  presence. The created event is `coordination_grant.created` with the root
  Thread ID and `existingThread: true`, identifiers only. The Thread's
  resource version moves, so two requests at one version cannot both
  succeed; its `updatedAt` does not, since a grant is not activity. A replay
  with the same `Idempotency-Key` returns the same grant, and a reused key
  with a different body is a conflict.
- **When the role is stale.** ADR 0058's renewal now also runs when a live
  session's role is stale: the Thread holds an active grant (including a
  lapsed one) and the session was started without the lead role. For a
  session this daemon process started, the role it was given is held in
  memory with its lease. For one an earlier process started, the session is
  stale when its Run started before the grant was created. Both facts are
  stored or derived, so a daemon restart between the grant and the next
  prompt still renews. Only the upgrade counts; revocation restarts nothing.
- **Delivery.** The API configures the lead mailbox, then tries the
  renewal at once under ADR 0058's conditions (idle, nothing awaiting the
  operator, resume state present). It reports `restarted`. If the renewal
  cannot run, or fails, the grant still stands: the next prompt from any
  source renews first, and a stopped session resumes as a lead. No prompt is
  sent on the operator's behalf.
- **The `acp` brief.** The driver's resume state records the role whose
  brief is in the agent's history, set when a prompt carries it. State
  written before this change has no role, so an existing ACP lead or
  subagent is briefed once more on its next resume. The other structured
  drivers already re-send their system-prompt channel on resume.
- **Interfaces.** `POST /threads/{threadId}/coordination-grant` returns the
  grant, the Thread, and `restarted`. `meridian thread lead THREAD_ID` takes
  the lead limit flags and `--json`. The web UI's **Make this a lead** action
  offers the new-Thread dialog's lead choice (the same limits, and whether to
  approve each spawn).

### 3. Keep large coordination output out of the lead's transcript

Follow Cursor's file-first pattern. Content is not stored anywhere new; it is
written into the lead's own Capsule instead of into a tool result.

- **Wakes carry headlines.** A wake lists each pending report's subagent name,
  link ID, status, diffstat and the first 2 KiB of its summary, at most 16
  items and 16 KiB in total. The lead reads full reports with `result`.
  Reports are marked delivered only when `result` or `wait` returns them, so a
  headline is never mistaken for the full report. The web UI renders a wake as
  one line naming the subagents, with the wake prompt behind a disclosure,
  and leaves the link IDs out.
- **Subagents by name.** `send`, `cancel`, `status`, `patch`, `result` and
  `wait` take a subagent's name wherever they take a link ID. A name resolves
  only among the requesting lead's own links, where names are unique, open or
  closed, and an ID wins over a name. meridiand resolves names for the
  requests it serves and the lead Capsule's mailbox for those it serves
  itself; a relayed request keeps the name as written, so its idempotency
  digest does not depend on which links the mailbox has been told about.
- **Patches and full reports can go to files.** `patch` and `result` accept
  `toFile: true`. `capsuled` in the lead Capsule writes the bytes to
  `$HOME/.meridian/coord/<link>/<seq>.{patch,md}` (mode 0600, outside
  `/workspace`, so they never reach a Delivery). The tool returns the path,
  size, HEAD, base commit and diffstat. Files are capped at 16 MiB per lead
  Capsule, oldest removed first. They are hostile data, as the inline forms
  already are.
- **The lead brief changes** to say: read diffstats before patches, write
  patches to files and read them with the shell, spawn a reviewer subagent
  instead of reading large diffs yourself, and keep a running notes file in
  `$HOME/.meridian/notes/` for decisions and open work, which survives
  compaction because it is a file.

### 4. Attach notes to a spawn

`spawn` accepts `contextFiles`, at most 8 paths under `$HOME/.meridian/notes/`
in the lead Capsule. `meridian-coord` reads them in the lead Capsule and
appends them to the prompt as a fenced, labelled block. The block counts
toward the 128 KiB prompt limit and is encrypted with the prompt under
`from_lead` provenance. Meridian stores nothing new: notes live in the lead's
Capsule, and the copy a subagent gets is part of its prompt.

Choices made in implementing this:

- **Path grammar.** 1 to 8 distinct slash-separated relative paths of at most
  255 bytes, with no empty, `.` or `..` element, backslash, or control
  character, checked by `meridian.coord.v1` itself. Subdirectories of the
  notes directory are allowed. Symlinks inside it are followed only as far
  as an `os.Root` on it allows, so none reaches outside. The notes directory
  itself must be a real directory, not a symlink, and `capsuled` disables
  `contextFiles` when it would lie inside `/workspace`, as for `toFile`.
  Only regular UTF-8 files without NUL are read, opened non-blocking so a
  FIFO cannot stall the mailbox. Nothing creates the directory; the brief
  tells the lead to.
- **Block format.** After the prompt, a `## Notes attached by the lead`
  heading and one line saying the fences are not part of the files, then
  per file `### <path> (<n> bytes)` and a `text` fence of backticks one
  longer than the longest backtick run in the file (at least three). Under
  CommonMark a closing fence must be at least as long as the opening one, so
  no line of a note can close its block early.
- **Refusals.** A spawn whose combined prompt exceeds 128 KiB is refused with
  the new code `context_too_large`; nothing is cut. A missing file or notes
  directory is `not_found`, any other unreadable path is `invalid_request`,
  and a Capsule without a notes directory answers `unavailable`. Messages
  name the path and the limit, never content. meridiand refuses a spawn that
  still carries `contextFiles` as `invalid_request`.
- **Retries.** The mailbox reads the notes again on every retry and the
  idempotency digest covers the combined prompt, so retrying with the same
  key after a note changed is a `conflict` rather than a second, different
  spawn.

Notes shared across separate lead trees or future Threads (Cursor's synced
Project files) are not part of this decision. Content an agent writes that
later Threads read automatically is a new way to carry prompt injection
forward. That needs its own ADR, with operator review of what is kept.

### 5. Pin the leads you come back to

An operator may pin any number of lead Threads in a Project, for example one
for the API, one for the frontend, and one for a migration. Pinned leads sit
above the Project's other Threads in the sidebar, by name, not by recency.
The Project page's composer offers to send to a pinned lead or to start a new
Thread, and defaults to starting a new Thread. Pinning is set and cleared only
by the owner and is cleared when the Thread is archived or deleted. It is a
presentation choice: it grants nothing and is never read by an authorization
check.

- **What can be pinned.** Any live (active or paused) Thread of a main
  Capsule, lead or plain. A plain Thread the operator keeps coming back to
  has the same problem of sinking by recency, and a pin grants nothing, so
  limiting it to leads would only add a rule to explain. A lead keeps its
  pin when its grant is revoked, since the conversation is still worth
  returning to. A subagent Thread cannot be pinned until it is promoted: it
  is listed under its lead.
- **Store and API.** The pin is a nullable `pinned_at` on the Thread row,
  which already carries `owner_user_id`. `POST /threads/{threadId}/pin` and
  `/unpin` take `expectedResourceVersion` and an `Idempotency-Key` like other
  operator mutations, and only a user principal may call them: the
  break-glass admin, a Capsule, and the system see the Thread as not found.
  No `meridian.coord.v1` verb reads or sets it. A pin does not move
  `updatedAt`, count as Capsule activity, or count as operator presence for
  the grant. Archive and crypto-shred clear it, Project deletion removes the
  row (ADR 0039), and a schema trigger refuses a pin on any other Thread.
- **Sending to a pinned lead** is the normal operator message to that Thread,
  after resuming it if it is paused. The choice is a "Send to" select in the
  New Capsule dialog, which the Project page's New Capsule button and the
  sidebar's + both open. It therefore updates `lastOperatorAt`
  and reopens a lapsed grant (section 1).
- **Start fresh** on a pinned plain Thread moves the pin to the new Thread,
  so the Capsule keeps its place.
- A pinned Thread that needs the operator is still listed once, under Needs
  you, until it is answered.

A single standing lead per Project was considered and rejected. Large
repositories have parts that are worked on separately, and one lead for all of
them would carry unrelated context into every turn, which is the problem this
decision exists to avoid.

### 6. Show context use

Harnesses already report token counts in their structured streams, and the
adapters used to drop them (for example, `acp.go` discarded ACP usage
updates). Each driver reads the counts its harness reports. These were checked
on 2026-11-20 against the pinned harness in each official pack image: the
Claude Code 2.1.319 binary's stream-json schema, the
`codex app-server generate-json-schema` output of Codex 0.167.0, the
`opencode acp` code of OpenCode 2.2.6, the `dsh-acp` code of DeepSeek Harness
0.2.2-rc.1 and its bundled ACP SDK 1.4.0, and the RPC docs shipped with Pi
0.87.1:

| Driver | Fill and window | Compaction |
| --- | --- | --- |
| `claude-stream` | `message.usage` on each main-loop `assistant` record (`input_tokens` + `cache_creation_input_tokens` + `cache_read_input_tokens`), and `contextWindow` of the main model's entry in `result`'s `modelUsage`, which is keyed by model, so the window is known from the first turn's end | `system` record with subtype `compact_boundary`; `compact_metadata.post_tokens`, when present, is the new size |
| `codex-app-server` | `thread/tokenUsage/updated`: `tokenUsage.last.inputTokens`, which already counts cached input, and `tokenUsage.modelContextWindow`, which may be null | a completed `contextCompaction` item; the size is unknown until the next usage update |
| `acp` | the `usage_update` session update's `used` and `size`. OpenCode sends it at the end of each turn, and DeepSeek Harness after each assistant message. Each harness computes `used` itself. | none: `compaction_update` is an unstable ACP extension sent only to clients that advertise it, so the adapter does not |
| `pi-rpc` | `get_session_stats` `contextUsage` (`tokens`, `contextWindow`), asked after each settled turn, at startup, and after a compaction; `tokens` is null right after a compaction | `compaction_end` with a `result`, or a successful `compact` command, counted once |

Context fill is the input size of the most recent model call (for Claude,
input plus cache-read plus cache-creation tokens), not the session's total
spend. It falls after compaction. The adapter sends a content-free
`context_usage` frame with tokens used, window size, and a `compacted` flag.
This is a new frame kind in the closed adapter protocol (ADR 0013), so it
follows that protocol's version rules. Both hellos must set a `contextUsage`
capability. `capsuled` fails a Run whose adapter sends the frame without
negotiating it, and an adapter that is not offered it sends nothing. Older
adapters and daemons therefore keep working. Counts are integers from 0 to
2^40. A frame that is not a compaction must carry a non-zero count and
window. A compaction may carry zeros when the harness does not yet know the
new size. The frame has no model field, and unknown fields are rejected. A
harness that reports nothing sends no frame, and the meter is hidden.

`meridiand` keeps only the latest value per Run in memory, like Thread
attention (ADR 0025), and puts it on the Thread view. What the operator sees
depends on whether they can do anything about it:

- **Plain Threads** show a small meter in the toolbar ("62% of 200k") once the
  context is half full. Here the operator decides what to do: compact with the
  harness's own command in the `/` menu (ADR 0028), or Start fresh.
- **Leads** show no meter. A lead is meant to feel like one continuous
  conversation, as Cursor Projects and Grok Bot coordinators do. Those products
  show no context figure and summarize on their own when the window fills.
  Sections 3 and 4 keep a lead's context small, and the harness compacts the
  rest. The number is in the Thread's details for anyone who looks.
- **Both** get a quiet divider in the transcript where the harness compacted
  ("Earlier conversation was summarized by Claude Code"). Meridian does not
  own the agent loop, and compaction quality differs by harness. If an agent
  forgets something, the divider shows why.

Fill is held only in memory, but a divider has to survive a reload and a
daemon restart. It also has to stay in place among the messages around it.
So a `context_usage` frame with `compacted` is the one usage frame written to
the transcript. It is written as an encrypted status block, like the
`commands` and `models` blocks, holding only the event type and the flag. The
counts are dropped before the write. Frames that only update fill are never
written. The marker is therefore content-free and adds nothing to SQLite or
backups except, inside ciphertext, the fact that a compaction happened at that
point. A compaction whose new size is unknown clears the in-memory value, so
the meter hides until the harness reports again. A daemon restart also clears
it.

Meridian still does not compact or summarize transcripts itself (ADR 0013).
The numbers are counts with no model name, so they do not touch ADR 0031's
rule that model names stay out of plaintext records.

## Not in this decision

- Routines or events that wake a standing lead. ADR 0057 rejected waking an
  existing Thread on a timer. A standing lead makes that more useful, but it
  needs its own decision.
- Depth above 1. Cursor does not nest subagents either.
- Notes shared between lead trees or carried into future Threads (see 4).
- Meridian-side summarization or compaction.

## Threat model changes

- **Longer-lived authority.** A grant can now last as long as the operator
  keeps talking to the lead. The bound on unattended spend is one
  `unattendedLimit` and one window of `maxDescendants`, the same order as
  today's grant. A lead that is in active use can spawn more over its
  lifetime than the old cap of 32. That is intended, and each window is still
  bounded.
- **Operator presence is the renewal signal.** Only operator API calls reset
  `lastOperatorAt`. A Capsule cannot reset it, because coordination requests,
  reports and wakes never count as operator activity.
- **Upgrading a Thread with history.** Turning a plain Thread into a lead
  gives coordination authority to a conversation that may already contain
  hostile repository or tool output. The operator chooses this explicitly,
  and the authority is the same as a new lead's.
- **Coordination files in the lead Capsule.** Patch and report files are the
  same hostile bytes the lead already receives inline. They stay inside the
  Capsule, outside `/workspace`, and are never written to the daemon's store.
- **Notes in spawn prompts.** These are lead-authored text in a prompt, with
  the same trust as the rest of a `from_lead` prompt. A compromised lead could
  already write anything into a spawn prompt.
- **Pins.** A pin is presentation only. No authorization check, wake,
  routine, or coordination limit reads it, and only the owner's own API calls
  set it, so a Capsule cannot raise its own prominence in the operator's
  sidebar.
- **Harness-reported context counts.** A hostile adapter or harness can
  misreport fill. That makes the meter wrong and misleads only the operator's
  choice to compact or start fresh. The counts are bounded integers with no
  text. They are held in memory and never logged. Only a count-free
  compaction marker is written, inside the encrypted transcript.

## Consequences

An operator can keep a lead conversation, or several for different parts of a
Project, going for weeks, and can still start plain Threads for quick work. The lead hands
out the heavy work, keeps its context small, and is woken with short
headlines. Pause and renewal (ADRs 0019 and 0058) make returning to it cheap.
How well a very long conversation holds up still depends on the harness's
compaction, which Meridian makes visible but does not replace.

Grant rows gain `lastOperatorAt` and `unattendedLimit` and a rolling spawn
window. The `acp` driver gains a role-change brief. The lead brief and
`meridian-coord` gain file outputs and `contextFiles`. Adapters gain an
optional `context_usage` frame.

## Alternatives considered

- **Raise the 72-hour deadline cap.** Rejected. It moves the cliff without
  removing it, and it bounds age rather than unattended time.
- **Let the lead extend its own grant.** Rejected. Authority would then
  renew itself, which ADR 0029 forbids.
- **Meridian summarizes the lead's transcript.** Rejected under ADR 0013.
  Harnesses own compaction and know their own tool and cache semantics.
- **Synced Project files now.** Deferred; see 4.
- **Share one Capsule between the lead and its subagents** (Cursor's default
  shared checkout). Rejected under ADR 0016. Cursor's own docs warn that
  parallel subagents can overwrite each other's edits in a shared checkout.
