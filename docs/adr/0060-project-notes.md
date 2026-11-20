# ADR 0060: Project notes outlive a lead

- Status: Proposed
- Date: 2026-11-20
- Amends: ADR 0059 section 4 (cross-tree notes are no longer deferred), ADR
  0057 (what may reach a scheduled Thread besides its prompt)
- Extends: ADRs 0012, 0016, 0029, 0039 and 0046

## Context

ADR 0059 lets a lead Thread last for weeks, and lets an operator pin several
leads in one Project, for example one for the API and one for the frontend.
What those leads learn stays in each lead's own conversation and in its
private `$HOME/.meridian/notes/` directory. Three things are lost as a result:

- **Leads relearn the same facts.** The API lead and the frontend lead each
  find out, separately, how the Project's tests are run, which branch naming
  the operator wants, and that the staging database is read-only. The
  operator often has to say it twice.
- **New Threads start from zero.** A plain Thread started this morning knows
  nothing a lead found last week, unless the operator pastes it in.
- **Subagents are deleted.** A finished subagent's Capsule is removed about
  an hour after it last did anything (ADR 0029, cleanup amendment). What it
  wrote in its own home goes with it. Only its report reaches the lead.

ADR 0059's context describes how two other products handle this. In Cursor
Projects, a set of Project files, written by agents and the user, syncs to
every machine an agent uses and carries what was learned between agents. A
Grok Bot is a persistent agent with its own memory. ADR 0059 records nothing
more than that about either product: not whether an agent's write to those
files is reviewed, how a Bot's memory is selected for a turn, or how either
handles text that a hostile page planted. This ADR does not assume an
answer. Third-party analysis of Cursor Projects argues that those files,
not the coordinator's summarized conversation, are the memory that lasts,
because a summary loses detail. ADR 0059 drew the same conclusion for a
single conversation: anything large or durable should live in files the
agent reads when it needs them.

ADR 0059 section 4 kept notes private to one lead tree on purpose. Text that
an agent writes and that later Threads read automatically is a way for prompt
injection to outlive the Capsule it arrived in. Meridian's other defenses
lean on fresh Capsules (ADR 0016): a hostile dependency that steers one
Thread is gone when that Capsule is deleted, except for what the repository
and the setup Moment carry. ADR 0057 calls this "no standing foothold".
Shared agent-written notes would add a third carrier, and one that no
repository review sees.

The repository is the obvious place for durable knowledge, and for much of
it the right one. It is not the right place for all of it. Some facts are
the operator's preferences, some are about this Station rather than the
code, and some are decisions still being made. In team mode (ADR 0046) a
Project is private to one member, and a commit is visible to the whole team.

## Decision

A Project has a set of **Project notes**: Markdown files that only the
operator writes, that agents may propose changes to, and that Meridian copies
into each new session of the Project's Threads. Every change that reaches a
Capsule has been written or approved by the operator, and every change
records who wrote it, who approved it, and which Thread proposed it.

### 1. Who writes

- **The operator writes.** Only a user principal who owns the Project can
  create, edit, rename, delete or restore a note, through the API, CLI or web
  UI. The break-glass admin, the system, and every Capsule see the notes as
  not found, as they do for pins (ADR 0059 section 5).
- **Agents propose.** A lead or subagent may propose a change with a new
  `meridian.coord.v1` request, `propose-note`. A proposal names one note and
  gives its full new content, a new note, or a deletion, together with the
  revision it was written against. It is stored as a pending proposal, and
  nothing is delivered anywhere until the operator approves it. This is the
  same shape as `propose-delivery` (ADR 0029): the agent proposes, only the
  operator approves, and there is no auto-approve.
- **Where proposals can come from.** Only from a Capsule whose mailbox the
  daemon has configured, which today means a lead or a subagent under an
  active, not lapsed, grant. Both mailboxes relay `propose-note`, as both
  relay `propose-delivery`. A plain Thread has no mailbox and cannot
  propose. The operator can still copy text from any Thread's transcript into
  the note editor and save it (section 5); that is an operator write.
- **Content of a proposal.** The agent sends either `content` (at most
  32 KiB) or `fromFile`, a path under its own `$HOME/.meridian/notes/` that
  the lead Capsule's mailbox reads with the same rules as `contextFiles`
  (ADR 0059 section 4) and replaces with its content before relaying. A lead
  can therefore propose one of its private notes without retyping it. There
  is no separate "reason" field. The diff is the whole proposal, and an
  operator who wants the reasoning reads the proposing Thread.
- **Approval binds one exact change.** `approve` takes the proposal's
  content digest and the note's current revision. If the note has changed
  since the proposal's base revision, the proposal is `stale` and cannot be
  approved as it is. The operator can open it in the editor, merge it by
  hand, and save, which is an operator write that records the proposal as its
  source. The operator may also edit any proposal before approving it, and
  the revision records that it was approved with edits.
- **What an agent sees.** The `propose-note` response is content-free: the
  proposal ID and its state. A proposing Thread can read the states of its
  own proposals (`pending`, `approved`, `approved_with_edits`, `rejected`,
  `stale`, `expired`) through `whoami`, at most its 16 most recent, with no
  text. No agent sees another
  Thread's pending proposals, the content of any pending proposal, or a
  rejected one, including its own after it is rejected. An approved change
  reaches agents only through delivery (section 3), at a new session.
  Pending proposals are unreviewed agent text, so letting other agents read
  them would recreate the unreviewed channel this decision exists to avoid.
  A rejection carries no reason text into the Capsule. An operator who wants
  to explain it sends the Thread a message.
- **Bounds.** At most 16 pending proposals per Project and 4 per proposing
  Thread. A proposal that is pending for 7 days becomes `expired`. Each
  proposal counts against the mailbox's existing rate and size limits, and
  is idempotent under its Capsule-scoped key like other mutating requests.
  meridiand stores the proposal, so it outlives the proposing Capsule:
  subagent cleanup does not wait for review, and the proposing Thread's
  encrypted transcript stays readable for context.
- **Audit.** Writes, approvals, rejections, restores, deletions and shreds
  are audited as the operator, and each proposal as its Capsule, with
  identifiers and sizes only (ADR 0046).
- **No attention.** A pending proposal does not raise Thread attention (ADR
  0025) and does not wake or block the agent. It shows as a count on the
  Project's Notes tab and in the Inbox under "Notes to review". A proposing
  agent cannot use proposals to fill the operator's Needs you list.

### 2. What a note is

- **Names.** `[a-z0-9][a-z0-9-]{0,47}` plus a fixed `.md` suffix, unique
  within the Project. There are no directories. The name is stored in
  plaintext, like subagent and routine names, so it is bounded by the same
  grammar. Names that an agent proposes become plaintext only once a pending
  proposal is stored, and a rejected or expired proposal's row is removed
  after 7 days.
- **Content.** UTF-8 Markdown without NUL or control characters other than
  tab and newline. CRLF is stored as LF. At most 32 KiB per note, 32 live
  notes, and 128 KiB in total per Project. Writes and approvals that would
  pass a limit are refused, never cut.
- **Revisions.** Every write, approval, rename, deletion and restore creates
  an immutable revision with a sequence number per note. A note keeps the
  content of its 50 most recent revisions. Older revisions keep their
  content-free provenance row and lose their ciphertext.
- **Provenance.** Each revision records, in plaintext and content-free: the
  kind (`operator_write`, `approved_proposal`, `approved_with_edits`,
  `restore`, `delete`), the user who wrote or approved it, the time, the
  content digest and size, and, for a proposal, its ID and the proposing
  Thread, Capsule, link, and lead Thread. A note whose latest revision came
  from a proposal, with or without edits, is labelled as agent-proposed in
  the note list. Every revision's source is shown in the note's history
  (section 5).
- **Encryption.** Each Project has one notes data key (DEK), 256 random
  bits, created with its first note. Revision content and proposal content
  are encrypted with AES-256-GCM and a fresh 96-bit random nonce under that
  DEK. The DEK is wrapped with the installation transcript KEK, using ADR
  0012's construction. The revision AAD binds the Project, note ID, revision
  sequence, kind, proposal ID, and creation time. The proposal AAD binds the
  Project, proposal ID, base revision, and proposing Thread. The wrap AAD
  binds the Project, the owner, and the KEK's identity and version, so a
  wrapped key moved to another Project or owner fails to open. This follows
  ADR 0046's owner binding for envelopes. It is not a per-user key: ADR 0046
  keeps one KEK per installation and defers per-user data keys, and this ADR
  does not change that.
- **Why the transcript KEK.** Notes are conversation content, not
  credentials. Crypto-shred (section 6) needs a data key to drop, which
  transcripts already have and the credential key's blob seal used for
  routine prompts does not. Backup custody and restore checks for the
  transcript KEK already exist. `meridian admin transcript-key rotate`
  re-wraps notes DEKs in the same transaction as Thread DEKs.
- **Never in plaintext records.** Note and proposal content stays out of
  SQLite plaintext, events, audit records, metrics, traces, logs,
  notifications and error messages. Responses that carry content are
  `Cache-Control: no-store`.

### 3. Delivery

Three ways to get approved notes into a Capsule were compared:

| Option | Verdict |
| --- | --- |
| Read-only mount of a notes volume | Rejected. Docker would need a host path or a daemon-written volume, and ADR 0024 forbids host mounts. On Agent Sandbox it would put note plaintext into Kubernetes objects. A mount shared by live Capsules is the shared mutable state ADR 0016 rules out, and "read-only" only holds until the Capsule user changes a copy it owns anyway. |
| A block in the system prompt or first prompt | Rejected for content. Every note would be in every turn, whether used or not, and compaction would summarize it, losing exactly the detail notes are for. The system-prompt channel carries trusted adapter text, and putting proposed text there would give it more weight than the user turn. |
| Files that `capsuled` writes outside `/workspace` | Chosen. The agent reads what it needs with its shell, which is ADR 0059's file-first pattern. Only a short, grammar-bounded index goes into the system-prompt channel. |

- **Where.** `$HOME/.meridian/project-notes/<name>.md`, mode 0444 in a 0555
  directory owned by the Capsule user. The modes tell the agent the files are
  not for editing. They are not a control, since the Capsule user can change
  them. `capsuled` writes the set into a temporary directory through an
  `os.Root` and renames it into place, refuses a target that is a symlink or
  lies inside `/workspace`, and removes files that are no longer in the set.
  The directory is outside the workspace, so a Delivery, local sync and
  workspace Moments never capture it. It is written after setup, so a setup
  Moment never captures it either (ADR 0019).
- **Edits never sync back.** Nothing reads the directory back. An agent that
  changes its copy changes only its own Capsule, until the next delivery
  replaces it. The way to change a note is `propose-note`.
- **When.** Snapshot at each session start, including resume after idle
  pause and ADR 0058 renewal, never while a turn is running. meridiand sends
  the current approved set over the private Capsule transport before it
  starts the adapter. A lead used for weeks is resumed often, so it picks up
  approvals within a day or so without a live channel into running Capsules.
  A live view was rejected: content would change under a running turn, and
  an approval would reach every running Capsule at once, including
  unattended ones. A session start is a clear point at which a change takes
  effect.
- **The index.** Start and Resume metadata gain `projectNotes`: the
  directory, and for each note its name, size, revision, and whether it
  changed since this Thread's previous session. The driver renders it with a
  fixed template in the harness's system-prompt channel, the same channels as
  the coordination brief. The template says that these are Project notes the
  operator wrote or approved, that they can be read with the shell, that they grant no
  permissions and do not change any setting, and that a change to them is
  proposed with `propose-note`. The index carries names and numbers only, at
  most 4 KiB. It is negotiated with a `projectNotes` capability in both
  hellos, like `contextUsage` (ADR 0059 section 6). An adapter that does not
  offer it still gets the files and no index, and the UI says that this
  harness is not told about them.
- **Who gets them.** A Project setting, `notesDelivery`: `all_threads`
  (default), `leads_only` (lead Threads and their subagents, not plain
  Threads), or `off`. The new-Thread dialog has an "Include
  Project notes" choice that defaults to the Project setting. A subagent
  follows the Project setting, not its lead's choice, and gets the set that
  is current when its session starts, which may be newer than its lead's.
  Delivery is on by default because a note exists to be read: a note the
  operator wrote that no Thread receives would be surprising, and every note
  that is delivered is operator text or operator-approved text.
- **Scheduled Threads.** A routine (ADR 0057) delivers notes only when the
  routine sets `includeNotes: true`, which defaults to false. Amend ADR
  0057: a routine's prompt still takes no input from a previous run, and a
  routine that opts in also gets the Project notes as files. Notes are
  operator-approved, but a scheduled run has no witness, so the operator
  chooses per routine.
- **Record.** Each session records the content-free set of note revisions it
  was given. The Thread's details show it, and the transcript gets a quiet,
  content-free divider ("Project notes updated: 2 changed") when a resumed
  session receives a different set. That divider is stored like the
  compaction marker in ADR 0059 section 6.

### 4. The repository alternative

The repository stays the better answer for anything that is true of the code
and that other people should see:

- build, test and lint commands, code conventions, architecture, and
  decisions already made belong in `AGENTS.md`, `docs/`, or an ADR. They are
  versioned with the code they describe, reviewed through a Delivery, seen by
  teammates and CI, and read by every harness on any machine;
- anything a teammate's Threads should know. In team mode Project notes are
  private to one owner, as everything else is (ADR 0046).

Project notes are for what does not belong in a commit:

- **operator preferences**: how this operator likes pull requests written,
  which checks they run locally, how much to ask before acting;
- **in-flight decisions**: "we are moving off the v1 client; do not add new
  calls to it" while the migration is still being argued about;
- **Station-specific facts**: this Station's slow Docker builds, which local
  services exist, which provider connection or isolation class the Project
  uses, facts about another repository the operator works in;
- **how the leads divide the work**: "the API lead owns `pkg/api`; the
  frontend lead owns `frontend/`; ask before touching the other's tree".

A "promote to repository" action is not part of this decision. Meridian
never writes to a workspace or creates a commit outside a Capsule and a
reviewed Delivery, and a promote action would need one or the other. The
existing path is short: the note page has "Copy as Markdown", and the
operator asks a lead to add the text to the repository, which arrives as an
ordinary Delivery for review. The reverse never happens automatically. A
repository file cannot create or change a note, for the same reason a
repository cannot define a routine (ADR 0057).

### 5. UI

- **Notes tab.** Each Project page gets a Notes tab beside its Threads. It
  lists notes by name with size, last change, who made it, and a label when
  the live text came from an agent proposal. It shows the Project's total
  against the 128 KiB limit, with a rough token count for an agent that reads
  every note. The `notesDelivery` setting is on this tab.
- **Editor.** Notes are edited as Markdown source in a plain text editor with
  a rendered preview. Saving takes the note's current revision, so two tabs
  cannot overwrite each other. The preview renders no raw HTML, loads no
  remote images, and does not follow links on its own.
- **Proposals.** A pending proposal opens to a source diff against its base
  revision, not a rendered preview, with the preview as a second tab.
  Control characters, bidirectional overrides, and zero-width characters are
  shown as visible escapes, and long lines wrap, so text cannot hide inside
  the diff. The page shows which Thread proposed it and links to that
  Thread. Approve, Edit, and Reject act on one proposal at a time. There is
  no "approve all". Inbox lines and notifications name the Project, the note
  and the proposing Thread only. They never quote proposal text, so a
  proposal cannot put a message in front of the operator before the diff
  page frames it as agent text.
- **History.** Each note has a history of its revisions with provenance and
  a diff between any two retained revisions. Restore creates a new revision.
  Deleted notes stay in the history until purged (section 6).
- **From a transcript.** A message's menu has "Add to Project notes", which
  opens the editor with the selected text. Nothing is saved until the
  operator saves it, and the revision records the source Thread.
- **CLI and API.** `meridian notes list|show|edit|history|restore|delete`
  and `meridian notes proposals|approve|reject`, all with `--json`. The API
  is `GET /projects/{projectId}/notes`, `GET` and `PUT
  /projects/{projectId}/notes/{name}`, `POST .../notes/{name}/delete`,
  `GET .../notes/{name}/revisions`, `POST .../notes/{name}/restore`,
  `GET /projects/{projectId}/note-proposals`, `POST
  /note-proposals/{id}/approve` and `/reject`, and `POST
  /projects/{projectId}/notes/shred`. Mutations take an expected revision or
  resource version and an `Idempotency-Key`.

### 6. Lifecycle

- **Delete a note.** Creates a `delete` revision and stops delivering the
  note at the next session start. Its history stays, so it can be restored.
  "Delete with history" also removes every revision's ciphertext and its
  pending proposals. That is a row delete, not media erasure.
- **Crypto-shred.** `POST /projects/{projectId}/notes/shred` clears the
  Project's wrapped notes DEK. Every revision and proposal becomes unreadable
  ciphertext, the live set is empty, and a new DEK is created with the next
  note. Content-free provenance rows stay. Use it when the operator finds a
  poisoned note and wants no copy left that Meridian can read.
- **Copies elsewhere.** A note delivered to a Capsule is plaintext in that
  Capsule's home until the Capsule is deleted or the next delivery replaces
  it. An agent that read a note may have quoted it into its transcript, a
  report, a spawn prompt, or a commit. Shredding notes does not reach those
  copies, as shredding a subagent does not reach reports its lead already
  holds (ADR 0029).
- **Project deletion.** ADR 0039's cascade also removes notes, revisions,
  proposals and the wrapped DEK. Capsules are deleted first, which removes
  their copies.
- **Owner transfer in team mode.** The break-glass transfer of a departed
  member's Project (ADR 0046) re-wraps the notes DEK with the new owner in
  the AAD in the same transaction, rejects pending proposals as
  `owner_changed`, and sets `notesDelivery` to `off`. The new owner reads the
  notes and turns delivery back on, as they turn on transferred routines
  (ADR 0057). The admin who transfers cannot read notes.
- **Backups.** Backups hold notes ciphertext and wrapped DEKs and list the
  transcript KEK they need, exactly as for Threads (ADR 0012). Deep
  verification authenticates notes envelopes too. A backup taken before a
  shred or deletion still holds readable notes for anyone with that backup
  and the KEK.

### 7. Interaction with ADR 0059

- **Private notes stay.** A lead's `$HOME/.meridian/notes/` is still its
  scratch space for running notes, decisions and open work. It is not shared
  and never delivered elsewhere except through `contextFiles` into one
  spawn's prompt. Project notes live in a separate directory, so an agent
  cannot confuse the two and an edit to private notes changes nothing shared.
- **From private to shared.** `propose-note` with `fromFile` turns a private
  note into a proposal. The lead brief gains one line: when you learn a
  durable fact about the Project or the operator's preferences that other
  Threads need, propose it as a Project note, and never put a credential in
  one.
- **`contextFiles`.** Unchanged, and still limited to the private notes
  directory. A subagent gets Project notes on its own when delivery is on,
  so the lead does not need to attach them. A lead that wants a subagent to
  have a Project note when delivery is off attaches its own copy, which is
  then a `from_lead` prompt with the trust ADR 0059 already gives one.
- **Context budget.** Files cost nothing until read. The index is at most
  4 KiB in the system-prompt channel. An agent that reads every note reads at
  most 128 KiB, about the size of one spawn prompt. The context meter (ADR
  0059 section 6) counts the index and whatever the agent actually reads, as
  it counts any file.
  The limits are set so that a Project's notes are a reference shelf, not a
  second transcript.

## Not in this decision

- Sharing notes with another member, or across Projects. ADR 0046 has no
  sharing, and this does not add any.
- A promote-to-repository action (section 4).
- Live delivery into a running session.
- Proposals from plain Threads, which would need a mailbox outside a grant.
- Semantic retrieval or summarization of notes by Meridian.

## Threat model changes

`docs/threat-model.md` gains a "Project notes" section that covers:

- **A deliberate carrier across fresh Capsules.** Notes are the one channel
  by which text from one Capsule reaches later ones without a repository
  commit, which narrows "no standing foothold" (ADR 0057). Only operator
  text and operator-approved text crosses, each revision records where it
  came from, and history, restore, and shred undo a mistake.
- **The operator approves a poisoned diff.** A plausible change ("the test
  suite is flaky; skip it", "run `scripts/bootstrap.sh` first", "the
  staging token is in `.env.example`") steers every later Thread. Controls:
  diffs shown as source with hidden characters escaped, one proposal at a
  time, the agent-proposed label on the note, the index saying notes grant
  nothing, and per-routine opt-in. A note cannot change harness permission
  settings, egress, trusted MCP tools, a grant, or Delivery approval, so it
  can steer only what an agent could already do without asking. This is a
  residual risk.
- **Social engineering in proposals.** Proposal text never appears in
  notifications or Inbox lines, there is no free-text reason field, and the
  only place it is shown is a page that labels it as agent text from a named
  Thread.
- **Size and cost.** 32 KiB per note, 32 notes, 128 KiB per Project, 16
  pending proposals per Project and 4 per Thread, 7-day expiry, 50 retained
  revisions per note, and a 4 KiB index.
- **What a compromised Capsule can do.** It can propose changes within the
  bounds above, read and exfiltrate the notes delivered to it within its
  egress, change its own copy, and read the states of its own proposals. It
  cannot write or approve a note, read pending proposals or another Thread's
  proposals, change delivery, reach another Capsule's copy, raise attention,
  or plant notes through the repository.
- **Secrets in notes.** Every process in every Capsule that receives notes
  can read them. Proposals are scanned with the credential-shaped checks ADR
  0024 uses, and matches are flagged on the review page. That is defense in
  depth. Credentials belong in named secrets.

## Consequences

An operator with several leads in one Project writes a fact once, or approves
it once, and every later session of every Thread in that Project has it as a
file. A finished subagent's finding can outlive its Capsule if the operator
approves it. The cost is a review step for every agent-proposed change, and a
new channel that a careless approval can poison for every later Thread until
the operator restores or shreds it.

The store gains notes, revision, and proposal tables and a per-Project
wrapped DEK. `meridian.coord.v1` gains `propose-note` and proposal states in
`whoami`. `capsuled` gains a notes delivery request. Start and Resume
metadata gain `projectNotes`, the adapter hellos gain a capability for it,
and every structured driver renders the index in its system-prompt channel.
A new structured harness driver must do the same, as it must deliver the
coordination brief. Transcript key rotation, backup verification, Project
deletion and owner transfer each cover notes.

## Alternatives considered

- **Agent-writable notes without review**, as ADR 0059 describes Cursor's
  Project files, which agents and the user both write. Rejected. One
  successful injection in any Thread would reach every later Thread in the
  Project, with nothing in between. This is the risk ADR 0059 section 4
  deferred, and ADR 0057's fresh Capsule per run exists to avoid it.
- **Repository only.** Rejected as the only answer, and recommended for
  everything in the first list of section 4. It makes every preference a
  commit and a Delivery, puts Station facts and half-made decisions into a
  team's history, and is visible to teammates who should not see a member's
  private Project.
- **A vector or semantic memory store**, from which Meridian retrieves
  relevant passages for each turn. Rejected. Retrieval would decide what the
  agent sees on each turn, so the operator could no longer review what is
  delivered. Embedding text needs a model call from the daemon, or a local
  model, which adds egress, cost, and a new component. Poisoned text can be
  written to rank well. Meridian does not run the agent loop (ADRs 0001 and
  0013), and a harness that wants retrieval can grep a small set of files.
- **Per-lead notes only**, as ADR 0059 leaves it. Rejected. It is the
  problem in the context: leads relearn, new Threads start from zero, and a
  subagent's findings go with its Capsule.
- **Notes in the system prompt.** Rejected in section 3: cost on every turn,
  loss to compaction, and proposed text in a trusted channel.
- **Write into each harness's own memory file** (`~/.claude/CLAUDE.md`,
  `~/.codex/AGENTS.md`). Rejected. Those files belong to the personal
  harness setup that `capsuled` installs (ADR 0024), the format and the
  loading rules differ per harness, and the harness would load the text as
  trusted instructions with no index saying where it came from.
- **One shared, live directory for every Capsule in the Project.** Rejected
  under ADR 0016. Any Capsule could change what every other Capsule reads.
