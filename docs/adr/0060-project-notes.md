# ADR 0060: Working notes and Project notes outlive a lead

- Status: Accepted; implemented, unreleased
- Date: 2026-11-20
- Amends: ADR 0059 section 4 (cross-tree notes are no longer deferred), ADR
  0029 (new mailbox requests), ADR 0057 (what may reach a scheduled Thread
  besides its prompt)
- Extends: ADRs 0012, 0016, 0039 and 0046

## Context

ADR 0059 lets a lead Thread last for weeks, and lets an operator pin several
leads in one Project, for example one for the API and one for the frontend.
What those leads learn stays in each lead's own conversation and in its
private `$HOME/.meridian/notes/` directory. Three things are lost as a result:

- **Subagents are deleted.** A finished subagent's Capsule is removed about
  an hour after it last did anything (ADR 0029, cleanup amendment). What it
  wrote in its own home goes with it. Only its report reaches the lead, and
  the next subagent the lead starts learns it again unless the lead retypes
  it.
- **Leads relearn the same facts.** The API lead and the frontend lead each
  find out, separately, how the Project's tests are run, which branch naming
  the operator wants, and that the staging database is read-only. The
  operator often has to say it twice.
- **New Threads start from zero.** A plain Thread started this morning knows
  nothing a lead found last week, unless the operator pastes it in.

ADR 0059's context describes how two other products handle this. In Cursor
Projects, a set of Project files, written by agents and the user, syncs to
every machine an agent uses and carries what was learned between agents.
Third-party descriptions say agents write those files directly, with no
review step. Cursor has not documented one either way, and this ADR relies
only on that description. A Grok Bot is a persistent agent with its own
memory. ADR 0059 records nothing about how a Bot's memory is written or
selected for a turn, and this ADR assumes nothing. Third-party analysis of
Cursor Projects argues that those files, not the coordinator's summarized
conversation, are the memory that lasts, because a summary loses detail.
ADR 0059 drew the same conclusion for one conversation: anything large or
durable should live in files the agent reads when it needs them.

ADR 0059 section 4 kept notes private to one lead tree on purpose. Text that
an agent writes and that later Threads read automatically is a way for prompt
injection to outlive the Capsule it arrived in. Meridian's other defenses
lean on fresh Capsules (ADR 0016): a hostile dependency that steers one
Thread is gone when that Capsule is deleted, except for what the repository
and the setup Moment carry. ADR 0057 calls this "no standing foothold".

Not every carrier is equally dangerous. Inside one lead tree, hostile text
already moves freely: the lead writes subagents' prompts, and subagents'
reports and patches go straight into the lead (ADR 0029). A note that only
that tree's members read adds persistence inside the tree, but no reach.
Text that crosses into a Thread that never saw where it came from is
different: a plain Thread started next week has no reason to doubt it, and
one poisoned change reaches every later Thread in the Project.

The repository is the obvious place for durable knowledge, and for much of
it the right one. It is not the right place for all of it. Some facts are
the operator's preferences, some are about this Station rather than the
code, and some are decisions still being made. In team mode (ADR 0046) a
Project is private to one member, and a commit is visible to the whole team.

## Decision

There are two tiers of notes, split by who will read them.

- **Working notes** belong to one lead tree: the lead and its subagents.
  Every member reads and writes them freely, with no review. They never
  reach anything outside that tree.
- **Project notes** belong to the Project. They reach other leads, plain
  Threads, future Threads and, when a routine opts in, scheduled Threads.
  Only the operator writes them, agents propose changes, and every change is
  reviewed (section 3 says why there is no auto-approve).

Both tiers are Markdown files stored by meridiand, encrypted, and delivered
to a Capsule as files outside `/workspace`. Every revision records who wrote
it and from which Thread.

### 1. Working notes

Working notes are the tree's shared memory. A subagent records what it
found, the next subagent reads it, and the lead reads it without asking for
another report.

- **Scope.** A grant's working notes are readable and writable by the
  grant's lead Thread and by each subagent whose link is open, including a
  promoted subagent that kept its link. A detached, cancelled, removed or
  denied subagent leaves the tree and receives nothing more. Other leads,
  plain Threads, routines and later lead trees never receive them, even in
  the same Project.
- **Storage.** meridiand stores them, not the lead Capsule. A subagent's
  findings then outlive its cleanup, the lead's notes survive a lead Capsule
  restart or deletion, subagents can write while the lead is idle-paused,
  and the operator can read them without starting any Capsule. Storing them
  in the lead Capsule was rejected for those reasons (see Alternatives).
- **Writing.** `meridian.coord.v1` gains `write-note {name, baseRevision,
  content | fromFile | delete}`, relayed by lead and subagent mailboxes. A
  write replaces the whole note. `baseRevision` is the revision the writer
  last read, 0 to create. A write against an older revision is refused as
  `conflict` with the current revision number and the writer's link name, no
  content. The mailbox then refreshes that note's local file, so the agent
  can read the current text and write again. `fromFile` reads a path under
  the Capsule's own `$HOME/.meridian/notes/` with the rules ADR 0059 section
  4 gives `contextFiles`. A successful write updates the writer's local file
  and returns the new revision.
- **Reading.** Notes are delivered as files (section 4) when each session in
  the tree starts or resumes. A new request, `sync-notes`, refreshes the
  directory during a session: the Capsule's mailbox asks meridiand for the
  current set, rewrites the files, and returns names, revisions and which
  changed, never content. `sync-notes` is a read, so it still answers when
  the grant has lapsed. The lead's next report wake lists, by name, the
  working notes that changed since the last wake and which link wrote them.
  A note write alone never wakes the lead.
- **The operator.** The operator can read, edit, delete and restore working
  notes from the lead's Agents panel, which gains a Notes tab. An operator
  edit is a revision like any other, recorded as the operator's. It does not
  count as operator presence for the grant, as a pin does not (ADR 0059
  section 5).
- **Bounds.** The note name and content rules in section 5 apply. A tree
  holds at most 32 live working notes and 128 KiB in total. Writes are
  limited to 30 per Capsule and 120 per tree in any rolling hour, on top of
  the mailbox's existing request rate. Each note keeps its 50 most recent
  revisions' content and every revision's provenance row. A write that would
  pass a limit is refused, never cut.
- **Provenance.** Each revision records the writer kind (`agent` or
  `operator`), the writing Capsule, Thread and link (or the lead itself),
  the base revision, the time, and the content digest and size.
- **Lapse and revocation.** While the grant is lapsed (ADR 0059 section 1),
  `write-note` is refused as `coordination_lapsed` like every other
  mutating request, and reads still answer. When the grant is revoked or
  ends with the lead, the notes become read-only: no Capsule can read or
  write them, and the operator can still read them and promote them
  (section 2). They are kept until the lead Thread is crypto-shredded or
  deleted, the Project is deleted, or the operator deletes them from the
  Agents panel.
- **Relation to private notes.** The private `$HOME/.meridian/notes/`
  directory stays as each Capsule's scratch space, and `contextFiles` stays
  unchanged. Scratch notes change on most turns, and sending every change
  through the mailbox would spend the write budget, crowd the history, and
  wake the lead's change list with noise. Keeping them local costs nothing
  and shares nothing. Working notes are the shared surface: the lead brief
  says to keep running notes in the private directory, to put what other
  members of the tree need into working notes, and to use `contextFiles` for
  material that only one spawn needs. `write-note --from-file` turns a
  private note into a working note without retyping it.

### 2. Project notes

Project notes are Markdown files that only the operator writes, that agents
may propose changes to, and that Meridian copies into each new session of
the Project's Threads.

- **The operator writes.** Only a user principal who owns the Project can
  create, edit, rename, delete or restore a Project note, through the API,
  CLI or web UI. The break-glass admin, the system and every Capsule see the
  notes as not found, as they do for pins.
- **Agents propose.** A lead or subagent may propose a change with
  `propose-note`, relayed by both mailboxes as both relay `propose-delivery`.
  A proposal names one note and gives its full new content, a new note, or a
  deletion, together with the Project-note revision it was written against.
  The content is `content` (at most 32 KiB), `fromFile` (a private note, as
  for `write-note`), or `fromWorkingNote` (a working note of the proposer's
  own tree, so the proposal records which working-note revision it came
  from). It is stored as a pending proposal, and nothing is delivered until
  it is approved. There is no separate "reason" field. The diff is the whole
  proposal, and an operator who wants the reasoning reads the proposing
  Thread. A plain Thread has no mailbox and cannot propose.
- **Promote from a working note.** A working note in the lead's Notes tab
  has "Promote to Project notes". It opens the same review page as an agent
  proposal, with the working note's current revision as the proposed content
  against the Project note of the same name, or as a new note. Approving it
  is an operator write that records the working-note revision and the
  Capsule that wrote it as its source. An agent can ask for the same thing
  with `propose-note --from-working-note`. Either way the text is reviewed
  before it leaves the tree.
- **Approval binds one exact change.** `approve` takes the proposal's
  content digest and the Project note's current revision. If the note has
  changed since the proposal's base revision, the proposal is `stale` and
  cannot be approved as it is. The operator can open it in the editor,
  merge it by hand, and save. The operator may edit any proposal before
  approving it, and the revision records that.
- **What an agent sees.** The `propose-note` response is content-free: the
  proposal ID and its state. A proposing Thread can read the states of its
  own 16 most recent proposals (`pending`, `approved`, `approved_with_edits`,
  `rejected`, `stale`, `expired`) through `whoami`, with no
  text. No agent sees another Thread's pending proposals, the content of any
  pending or rejected proposal, or a rejection reason. Pending proposals are
  unreviewed agent text, so letting agents outside the proposer read them
  would recreate the unreviewed channel this tier exists to avoid. An
  operator who wants to explain a rejection sends the Thread a message.
- **Bounds.** At most 16 pending proposals per Project and 4 per proposing
  Thread. A proposal that is pending for 7 days becomes `expired`. Each
  proposal counts against the mailbox's rate and size limits and is
  idempotent under its Capsule-scoped key. meridiand stores the proposal, so
  it outlives the proposing Capsule: subagent cleanup does not wait for
  review, and the proposing Thread's transcript stays readable for context.
- **No attention.** A pending proposal does not raise Thread attention (ADR
  0025) and does not wake or block the agent. It shows as a count on the
  Project's Notes tab and in the Inbox under "Notes to review".

### 3. No auto-approve

Every Project-note revision is written or approved by the operator. An
earlier revision of this ADR let the operator turn on auto-approve for one
trusted lead. It is not part of this decision.

In its first form, an auto-approved revision reached every lead tree in the
Project before anyone had read it. One compromised lead could then put text
in front of every other lead without review, which is the cross-tree spread
the two tiers exist to prevent. Narrowed so that an unreviewed revision
reaches only the tree of the lead that proposed it, until the operator marks
it reviewed, it no longer spreads. It also no longer does anything that
working notes do not already do:

- **Reach.** Inside its own tree, an unreviewed revision is text the lead
  wrote that the lead and its subagents read. A working note is exactly
  that, with no grant flag to set.
- **Review.** For the text to leave the tree, the operator still reads a
  source diff and clicks once, "Mark reviewed" instead of "Promote to
  Project notes". The step is the same.
- **Volume.** The case for auto-approve was a busy lead proposing several
  facts an hour, where a click per fact teaches the operator to approve
  without reading. With working notes, those facts need no click at all.
  The operator promotes what should leave the tree when they choose, and
  can merge several working notes into one Project note, so the review
  count follows promotions, not facts.

Its only remaining benefit would be that the text already sits in the
Project-note slot, so the later review is "Mark reviewed" rather than
"Promote". That does not pay for a second unreviewed carrier, a grant policy
field, a reviewed and an unreviewed head per note, and a per-tree delivery
rule for Project notes. The lead brief instead says to keep frequent
findings in working notes and to propose a Project note only for a fact
that Threads outside the tree need.

### 4. Delivery

Three ways to get notes into a Capsule were compared:

| Option | Verdict |
| --- | --- |
| Read-only mount of a notes volume | Rejected. Docker would need a host path or a daemon-written volume, and ADR 0024 forbids host mounts. On Agent Sandbox it would put note plaintext into Kubernetes objects. A mount shared by live Capsules is the shared mutable state ADR 0016 rules out, and "read-only" only holds until the Capsule user changes a copy it owns anyway. |
| A block in the system prompt or first prompt | Rejected for content. Every note would be in every turn, whether used or not, and compaction would summarize it, losing exactly the detail notes are for. The system-prompt channel carries trusted adapter text, and putting agent-written text there would give it more weight than the user turn. |
| Files that `capsuled` writes outside `/workspace` | Chosen. The agent reads what it needs with its shell, which is ADR 0059's file-first pattern. Only a short, grammar-bounded index goes into the system-prompt channel. |

- **Where.** Project notes go to `$HOME/.meridian/project-notes/<name>.md`
  and working notes to `$HOME/.meridian/working-notes/<name>.md`, mode 0444
  in 0555 directories owned by the Capsule user. The modes tell the agent
  the files are not for editing. They are not a control, since the Capsule
  user can change them. `capsuled` writes each set into a temporary
  directory through an `os.Root` and renames it into place, refuses a target
  that is a symlink or lies inside `/workspace`, and removes files that are
  no longer in the set. The directories are outside the workspace, so a
  Delivery, local sync and workspace Moments never capture them. They are
  written after setup, so a setup Moment never captures them either (ADR
  0019).
- **Edits never sync back.** Nothing reads the directories back. An agent
  that changes its copy changes only its own Capsule, until the next
  delivery replaces it. The way to change a working note is `write-note`,
  and the way to change a Project note is `propose-note`.
- **When.** Both sets are written at each session start, including resume
  after idle pause and ADR 0058 renewal, never while a turn is running.
  Working notes can also be refreshed with `sync-notes` (section 1). Project
  notes are a snapshot per session: a lead used for weeks is resumed often,
  so it picks up approvals within a day or so. A live view of Project notes
  was rejected: content would change under a running turn, and an approval
  would reach every running Capsule in the Project at once, including
  unattended ones.
- **The index.** Start and Resume metadata gain `notes`: the two directories,
  and for each note its tier, name, size, revision, and whether it changed
  since this Thread's previous session. The driver renders it with a fixed
  template in the harness's system-prompt channel, the same channels as the
  coordination brief. The template says that Project notes were written or
  approved by the operator; that working notes are written by the lead and its
  subagents and are as trustworthy as their reports; that neither grants a
  permission or changes a setting; and how each tier is changed. The index
  carries names and numbers only, at most 8 KiB. It is negotiated with a
  `notes` capability in both hellos, like `contextUsage` (ADR 0059 section 6).
  An adapter that does not offer it still gets the files and no index, and the
  UI says that this harness is not told about them.
- **Who gets Project notes.** A Project setting, `notesDelivery`:
  `all_threads` (default), `leads_only` (lead Threads and their subagents), or
  `off`. The new-Thread dialog has an "Include Project notes" choice that
  defaults to the Project setting. Every Thread that receives Project notes
  receives the same set: each note's current revision, which the operator
  wrote or approved. A subagent follows the Project setting, not its lead's
  choice, and gets the set that is current when its session starts.
- **Why the default is `all_threads`.** Every Project-note revision is
  operator text or text the operator approved, and agent text that has not
  been reviewed stays in its tree as working notes. A note the operator
  wrote, which no Thread receives, would be surprising. `leads_only` was
  considered as the default and rejected: it would leave plain Threads,
  which are most Threads, starting from zero, to guard against unreviewed
  text that never reaches Project notes.
- **Scheduled Threads.** A routine (ADR 0057) delivers Project notes only when
  the routine sets `includeNotes: true`, which defaults to false. A scheduled
  Thread holds no grant, so it never has working notes. Amend ADR 0057: a
  routine's prompt still takes no input from a previous run, and a routine
  that opts in also gets the Project notes as files. A scheduled run has no
  witness, so the operator chooses per routine.
- **Record.** Each session records the content-free set of note revisions it
  was given. The Thread's details show it, and the transcript gets a quiet,
  content-free divider ("Project notes updated: 2 changed") when a resumed
  session receives a different Project-note set. That divider is stored like
  the compaction marker in ADR 0059 section 6. Working-note refreshes add no
  divider, since they change often and the lead's wake already lists them.

### 5. What a note is

These rules apply to both tiers unless stated.

- **Names.** `[a-z0-9][a-z0-9-]{0,47}` plus a fixed `.md` suffix, unique
  within the Project's notes or within one tree's working notes. There are
  no directories. The name is stored in plaintext, like subagent and routine
  names, so it is bounded by the same grammar.
- **Content.** UTF-8 Markdown without NUL or control characters other than
  tab and newline. CRLF is stored as LF. At most 32 KiB per note. Project
  notes are limited to 32 live notes and 128 KiB in total per Project, and
  working notes to the same per tree (section 1).
- **Revisions.** Every write, approval, rename, deletion and restore creates
  an immutable revision with a sequence number per note. A note keeps the
  content of its 50 most recent revisions. Older revisions keep their
  content-free provenance row and lose their ciphertext.
- **Project-note provenance.** Each revision records, in plaintext and
  content-free: the kind (`operator_write`, `approved_proposal`,
  `approved_with_edits`, `restore`, `delete`), the user who wrote or
  approved it, the time, the content digest
  and size, and for a proposal or promotion its ID, the proposing or writing
  Thread, Capsule, link and lead, and the working-note revision it came
  from. A note whose latest revision came from an agent is labelled
  agent-proposed in the note list.
- **Encryption.** Each Project has one Project-notes data key (DEK) and each
  grant one working-notes DEK, 256 random bits, created with the first note.
  Revision and proposal content is encrypted with AES-256-GCM and a fresh
  96-bit random nonce under its DEK. Each DEK is wrapped with the
  installation transcript KEK, using ADR 0012's construction. Revision AAD
  binds the Project, the grant for a working note, the note ID, revision
  sequence, kind, and the proposal or writing Capsule. Proposal AAD binds the
  Project, proposal ID, base revision and proposing Thread. Wrap AAD binds
  the Project, the grant for working notes, the owner, and the KEK's
  identity and version, so a wrapped key moved to another Project, tree or
  owner fails to open. This follows ADR 0046's owner binding for envelopes.
  It is not a per-user key: ADR 0046 keeps one KEK per installation and
  defers per-user data keys, and this ADR does not change that.
- **Why the transcript KEK.** Notes are conversation content, not
  credentials. Crypto-shred (section 7) needs a data key to drop, which
  transcripts already have and the credential key's blob seal used for
  routine prompts does not. Backup custody and restore checks for the
  transcript KEK already exist. `meridian admin transcript-key rotate`
  re-wraps notes DEKs in the same transaction as Thread DEKs.
- **Never in plaintext records.** Note and proposal content stays out of
  SQLite plaintext, events, audit records, metrics, traces, logs,
  notifications and error messages. Responses that carry content are
  `Cache-Control: no-store`. Writes, approvals, rejections, restores,
  deletions and shreds are audited with identifiers
  and sizes only (ADR 0046).

### 6. The repository alternative

The repository stays the better answer for anything that is true of the code
and that other people should see:

- build, test and lint commands, code conventions, architecture, and
  decisions already made belong in `AGENTS.md`, `docs/`, or an ADR. They are
  versioned with the code they describe, reviewed through a Delivery, seen by
  teammates and CI, and read by every harness on any machine;
- anything a teammate's Threads should know. In team mode notes are private
  to one owner, as everything else is (ADR 0046).

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

Working notes are for the current piece of work: what a subagent found, what
was tried and failed, which files matter, what is left.

A "promote to repository" action is not part of this decision. Meridian
never writes to a workspace or creates a commit outside a Capsule and a
reviewed Delivery, and a promote action would need one or the other. The
existing path is short: a note has "Copy as Markdown", and the operator asks
a lead to add the text to the repository, which arrives as an ordinary
Delivery for review. The reverse never happens automatically. A repository
file cannot create or change a note, for the same reason a repository cannot
define a routine (ADR 0057).

### 7. UI

- **Notes tab on the Project.** It lists Project notes by name with size,
  last change, who made it, and an agent-proposed label. It
  shows the total against the 128 KiB limit with a rough token count for an
  agent that reads every note, the `notesDelivery` setting, and the pending
  proposals.
- **Notes tab in the lead's Agents panel.** It lists the tree's working
  notes with size, last change and which member wrote it, with history,
  edit, delete and "Promote to Project notes". A revoked lead's tab is
  read-only and still offers promotion.
- **Editor.** Notes are edited as Markdown source in a plain text editor with
  a rendered preview. Saving takes the note's current revision, so two tabs
  cannot overwrite each other. The preview renders no raw HTML, loads no
  remote images, and does not follow links on its own.
- **Review.** A proposal and a promotion both open to a source diff
  against the note's current revision, not a
  rendered preview, with the preview as a second tab. Control characters,
  bidirectional overrides and zero-width characters are shown as visible
  escapes, and long lines wrap, so text cannot hide inside the diff. The
  page shows which Thread proposed or wrote it, links to that Thread, and
  flags content that the credential-shaped checks of ADR 0024 match. Approve,
  Edit and Reject act on one change at a time. There is no
  "approve all". Inbox lines and notifications name the Project, the note
  and the Thread only. They never quote note or proposal text, so agent text
  cannot reach the operator before a page frames it as agent text.
- **History.** Each note has a history of its revisions with provenance and
  a diff between any two retained revisions. Restore creates a new revision.
  Deleted notes stay in the history until purged (section 8).
- **From a transcript.** A message's menu has "Add to Project notes", which
  opens the editor with the selected text. Nothing is saved until the
  operator saves it, and the revision records the source Thread.
- **CLI and API.** `meridian notes list|show|edit|history|restore|delete`,
  `meridian notes proposals|approve|reject`, and `meridian grant
  notes GRANT_ID` for working notes, all with `--json`. The API is
  `GET /projects/{projectId}/notes`, `GET` and `PUT
  /projects/{projectId}/notes/{name}`, `POST .../notes/{name}/delete`,
  and `.../restore`, `GET .../notes/{name}/revisions`,
  `GET /projects/{projectId}/note-proposals`, `POST
  /note-proposals/{id}/approve` and `/reject`, `POST
  /projects/{projectId}/notes/shred`, and the same read, write, delete,
  restore and revision endpoints under `/coordination-grants/{grantId}/notes`
  plus `POST /coordination-grants/{grantId}/notes/{name}/promote`, which
  creates the proposal for review. Mutations take an expected revision or
  resource version and an `Idempotency-Key`.

### 8. Lifecycle

- **Delete a note.** Creates a `delete` revision and stops delivering the
  note at the next session start, or the next `sync-notes` for a working
  note. Its history stays, so it can be restored. "Delete with history" also
  removes every revision's ciphertext and, for a Project note, its pending
  proposals. That is a row delete, not media erasure.
- **Crypto-shred.** `POST /projects/{projectId}/notes/shred` clears the
  Project-notes DEK. Every revision and proposal becomes unreadable, the
  live set is empty, and a new DEK is created with the next note.
  Content-free provenance rows stay. Use it when the operator finds a
  poisoned note and wants no copy left that Meridian can read. Working notes
  are shredded with their lead: crypto-shredding the lead Thread (ADR 0012)
  also clears its grant's working-notes DEK, and the Agents panel offers the
  same shred on its own.
- **When the lead ends.** Revocation or the lead ending makes working notes
  read-only (section 1). Deleting the lead's Capsule keeps them, as it keeps
  the lead's transcript. Crypto-shredding the lead Thread or deleting the
  Project removes them.
- **Copies elsewhere.** A delivered note is plaintext in that Capsule's home
  until the Capsule is deleted or the next delivery replaces it. An agent
  that read a note may have quoted it into its transcript, a report, a spawn
  prompt, a proposal, or a commit. Shredding notes does not reach those
  copies, as shredding a subagent does not reach reports its lead already
  holds (ADR 0029).
- **Project deletion.** ADR 0039's cascade also removes both tiers, their
  revisions, proposals and wrapped DEKs. Capsules are deleted first, which
  removes their copies.
- **Owner transfer in team mode.** The break-glass transfer of a departed
  member's Project (ADR 0046) re-wraps every notes DEK with the new owner in
  the AAD in the same transaction, rejects pending proposals as
  `owner_changed`, and sets
  `notesDelivery` to `off`. The new owner reads the notes and turns delivery
  back on, as they turn on transferred routines (ADR 0057). The admin who
  transfers cannot read notes.
- **Backups.** Backups hold notes ciphertext and wrapped DEKs and list the
  transcript KEK they need, exactly as for Threads (ADR 0012). Deep
  verification authenticates notes envelopes too. A backup taken before a
  shred or deletion still holds readable notes for anyone with that backup
  and the KEK.

### 9. Interaction with ADR 0059

- **Private notes and `contextFiles`** stay as ADR 0059 section 4 has them:
  per-Capsule scratch, attached to one spawn's prompt when the lead asks.
  Section 1 says why working notes do not replace them.
- **The lead brief** gains three lines: keep shared findings in working
  notes with `write-note` and run `sync-notes` before relying on them;
  propose a Project note when a fact will matter to Threads outside this
  tree; never put a credential in either. The subagent brief gains the first
  and last.
- **Fewer retyped prompts.** A subagent receives its tree's working notes on
  its own, so the lead no longer attaches shared conventions to every spawn.
  `contextFiles` stays for material only that spawn needs.
- **Context budget.** Files cost nothing until read. The index is at most
  8 KiB in the system-prompt channel. An agent that reads every note of both
  tiers reads at most 256 KiB, about two spawn prompts. The context meter
  (ADR 0059 section 6) counts the index and whatever the agent actually
  reads, as it counts any file. The limits keep notes a reference shelf, not
  a second transcript.

### 10. Choices made in implementation

Where this decision was silent, the implementation took the smallest safe
choice:

- **`off` overrides a Thread's choice.** With `notesDelivery` set to `off`,
  no Thread receives Project notes, including one whose "Include Project
  notes" choice was on. A Thread's choice can otherwise turn delivery on
  under `leads_only` or off under `all_threads`. A subagent always follows
  the Project setting, and a routine's Thread follows `includeNotes` (still
  subject to `off`). After an owner transfer, then, nothing reaches any
  Thread until the new owner turns delivery back on.
- **A revoked tree is read-only, not frozen.** Once the grant is revoked or
  ended, the operator can still read, promote, delete (with or without
  history) and shred its working notes, but not edit or restore them.
- **Only the latest session's set is kept.** The content-free set a session
  received is stored per Thread for its latest session only; the transcript's
  "Project notes updated" divider marks each change between sessions. A
  first session never gets a divider.
- **A failed delivery is skipped, not fatal.** If `capsuled` cannot write a
  set, for example because the agent replaced its directory with a symlink,
  the session starts without that set and the index leaves it out. Refusing
  the session would let a Capsule process deny its own Thread service for no
  gain. Leftover files the agent added under an old set are moved aside and
  removed on a best-effort basis.
- **Rename keeps the history.** An operator rename is an `operator_write`
  revision of the same note under the new name, refused if any note,
  deleted or live, already has that name. Pending proposals for the old name
  become stale.
- **Shredded proposals.** A crypto-shred also closes the Project's pending
  proposals as `rejected` with the closed reason `shredded`, and a working
  notes shred closes pending promotions from that tree the same way. "Delete
  with history" closes the note's pending proposals with the reason
  `purged`. Proposal reasons are a closed set; none is free text.
- **Conflicts carry content to the mailbox only.** A `write-note` against an
  old revision is refused with the current revision and who wrote it, and
  meridiand sends the note's current text to the Capsule's mailbox, which
  writes the refreshed file and strips the text before it answers the tool.
- **Rates.** The write limits count agent revisions in the store over the
  last hour, so they survive a daemon restart; they apply on top of the
  mailbox's own request rate.
- **The Inbox list.** `GET /note-proposals` lists every pending proposal of
  the caller's Projects for "Notes to review", content-free.
- **No reason field, and approve binds the digest.** The CLI's `approve`
  requires the digest `meridian notes proposal` printed, so a script cannot
  approve text it never showed.

## Not in this decision

- Sharing notes with another member, or across Projects. ADR 0046 has no
  sharing, and this does not add any.
- A promote-to-repository action (section 6).
- Live delivery of Project notes into a running session.
- Working notes for plain Threads, and proposals from them, which would need
  a mailbox outside a grant.
- Auto-approve of Project-note proposals, for any lead. Confined to the
  proposing lead's tree it duplicates working notes, and allowed past that
  tree it spreads unreviewed text to other leads (section 3).
- Semantic retrieval or summarization of notes by Meridian.

## Threat model changes

`docs/threat-model.md` gains a "Project notes and working notes" section that
covers:

- **Working notes are an unreviewed carrier, scoped to one tree.** Any
  process in any member Capsule can write text that every other member reads,
  and that text outlives the Capsule that wrote it for as long as the grant
  lives. Members already exchange hostile prompts and reports, so working
  notes add persistence, not reach. Only the lead, its open-link subagents,
  and the operator can read them. Nothing outside the tree receives them
  except through an operator-reviewed promotion or proposal, or a Delivery
  the operator approves. Trusting one lead therefore affects only that
  lead's tree until the operator reviews something it wrote.
- **A deliberate carrier across fresh Capsules.** Project notes are the one
  way text from one Capsule reaches later Capsules without a repository
  commit, which narrows "no standing foothold" (ADR 0057). Every revision is
  operator-written or operator-approved. History, restore and shred undo a
  mistake for later sessions.
- **The operator approves a poisoned diff.** A plausible change ("skip the
  flaky tests", "run this bootstrap script first", "the token is in
  `.env.example`") steers every later Thread. Controls: a source diff with
  hidden characters escaped, one change at a time, a label on agent-proposed
  notes, the index saying notes grant nothing, and per-routine opt-in. A note
  cannot change harness permission settings, egress, trusted MCP tools, a
  grant, or Delivery approval. This is a residual risk.
- **No auto-approve.** An unreviewed Project-note revision delivered to
  every lead tree would let one compromised lead reach every other lead.
  Confined to its own tree, it would add nothing over working notes. Neither
  form ships (section 3).
- **Social engineering.** Note and proposal text never appears in
  notifications, Inbox lines or titles. There is no free-text reason field.
  Text is shown only on pages that label it as agent text from a named
  Thread. Proposals and note writes raise no attention.
- **Size and cost.** Per-note, per-tree and per-Project limits, write rates,
  proposal limits and expiry, retained revisions, and an 8 KiB index.
- **What a compromised Capsule can do**, by tier, and what it cannot: write or
  approve a Project note, see pending proposals, change delivery, reach
  another tree's working notes or another Capsule's copies, raise attention,
  or plant notes through the repository.
- **Secrets in notes.** Every process in every Capsule that receives notes
  can read them. Review pages flag credential-shaped content. Credentials
  belong in named secrets.

## Consequences

A lead and its subagents share what they learn without a click, and a finished
subagent's findings outlive its Capsule. An operator with several leads in one
Project writes or approves a fact once, and every later session in that
Project has it as a file. The operator reviews only what leaves a tree, when
they choose to promote it. The costs are a review step for anything that
leaves a tree, an unreviewed carrier inside each tree for as long as its grant
lives, and a channel that a careless approval can poison until the operator
restores or shreds it.

The store gains Project-note, working-note, revision and proposal tables, a
per-Project and per-grant wrapped DEK. `meridian.coord.v1` gains `write-note`,
`sync-notes`, `propose-note` and proposal states in `whoami`. `capsuled` gains
notes delivery. Start and Resume metadata gain `notes`, the adapter hellos
gain a capability for it, and every structured driver renders the index in its
system-prompt channel. A new structured harness driver must do the same, as it
must deliver the coordination brief. Transcript key rotation, backup
verification, Thread crypto-shred, Project deletion and owner transfer each
cover notes.

## Alternatives considered

- **One reviewed tier only**, the first draft of this ADR. Every change,
  including a subagent's finding meant for its sibling, waited for an
  operator click. Rejected. Inside a tree, review guards a boundary that
  hostile text already crosses through prompts and reports, so it buys
  little there. It would cost a click per finding, which pushes agents back
  to retyping in prompts and pushes the operator toward approving without
  reading, which weakens review where it matters.
- **Cursor's model, as described**: Project files that agents and the user
  both write, and that reach every agent, with no review step according to
  third-party descriptions. Adopted inside one lead tree, where it adds no
  reach. Rejected for the Project as a whole. One successful injection in
  any Thread would reach every later Thread, which is the risk ADR 0059
  section 4 deferred and ADR 0057's fresh Capsule per run exists to avoid.
- **Working notes stored in the lead Capsule.** Rejected. Subagents could
  not read or write while the lead is idle-paused, which is most of a
  weeks-long lead's life. The notes would be lost with the lead Capsule, and
  the operator could not read them without starting it.
- **Working notes replace the private notes directory.** Rejected in
  section 1: scratch changes too often to send through the mailbox.
- **Auto-approve for one trusted lead, delivered to every lead tree.** The
  previous revision of this ADR. Rejected. An unreviewed revision would put
  one lead's text in front of every other lead in the Project, so a
  subagent that poisoned its lead's working notes, or a lead steered by a
  hostile repository, would reach trees the operator never pointed at it.
  That is the cross-tree spread the two tiers exist to prevent.
- **Auto-approve confined to the proposing lead's tree until reviewed.**
  Rejected in section 3. It reaches no further than a working note and
  needs the same review click to leave the tree. Its only gain, skipping a
  promotion step, does not pay for a second unreviewed carrier.
- **`leads_only` as the delivery default.** Rejected in section 4.
- **Repository only.** Rejected as the only answer, and recommended for
  everything in the first list of section 6. It makes every preference a
  commit and a Delivery, puts Station facts and half-made decisions into a
  team's history, and is visible to teammates who should not see a member's
  private Project.
- **A vector or semantic memory store**, from which Meridian retrieves
  relevant passages for each turn. Rejected. Retrieval would decide what the
  agent sees on each turn, so the operator could no longer review what is
  delivered. Embedding text needs a model call from the daemon, or a local
  model, which adds egress, cost and a new component. Poisoned text can be
  written to rank well. Meridian does not run the agent loop (ADRs 0001 and
  0013), and a harness that wants retrieval can grep a small set of files.
- **Per-lead notes only**, as ADR 0059 leaves it. Rejected. It is the
  problem in the context: subagents' findings go with their Capsules, leads
  relearn, and new Threads start from zero.
- **Notes in the system prompt.** Rejected in section 4: cost on every turn,
  loss to compaction, and agent-written text in a trusted channel.
- **Write into each harness's own memory file** (`~/.claude/CLAUDE.md`,
  `~/.codex/AGENTS.md`). Rejected. Those files belong to the personal
  harness setup that `capsuled` installs (ADR 0024), the format and loading
  rules differ per harness, and the harness would load the text as trusted
  instructions with no index saying where it came from.
- **One shared, live directory for every Capsule in the Project.** Rejected
  under ADR 0016. Any Capsule could change what every other Capsule reads.
