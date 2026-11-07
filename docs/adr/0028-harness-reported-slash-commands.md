# ADR 0028: Report slash commands from the harness

- Status: Accepted
- Date: 2026-11-07
- Extends: ADRs 0013 and 0026

## Context

Users of Claude Code, Codex, Pi, and OpenCode expect `/` commands in the
Thread composer. Each harness defines its own commands, and repositories,
skills, plugins, and MCP servers add more. A list maintained in Meridian would
be wrong for every harness version and every repository.

The pinned harnesses differ in what they report and how they run a command
headlessly:

| Harness | Discovery | Headless execution |
| --- | --- | --- |
| Claude Code 2.1.283 | `initialize` response `commands[]` with name, description, argument hint, and aliases. `system/init` lists `terminal_slash_commands`. | `/name args` sent as the user message. |
| Codex 0.157.1 | No command list. `skills/list` reports skills. | `thread/compact/start`, `review/start`, and a `skill` input item on `turn/start`. A `/compact` text prompt is only text. |
| Pi 0.87.1 | `get_commands` returns extension commands, prompt templates, and skills. TUI built-ins are excluded upstream. | `prompt` expands `/name`. `compact` is a separate RPC. |
| OpenCode 1.18.32 | `GET /command` returns commands, MCP prompts, and skills. | `POST /session/{id}/command`. `prompt_async` does not expand `/name`. |

The list exists only after the harness starts, the same `/name` is a plain
prompt on one harness and a protocol call on another, and every entry is
repository or plugin data.

## Decision

### Protocol

- `meridian.adapter.v1` gains a `commands` capability and an adapter-to-
  controller `commands` frame. The adapter sends the frame only when both
  hellos set the capability. `capsuled` and the `generic` driver reject the
  frame otherwise.
- The frame carries the complete current list. A later frame replaces the
  earlier one. Each entry has `name`, `kind`, and optional `description`,
  `argumentHint`, and `aliases`.
- `kind` is `prompt` when the harness expands the text itself, or `action`
  when the adapter maps the name to a harness protocol call.
- The controller always sends `/name args` as an ordinary `user_message`. The
  adapter runs it as an action only when `name` is one of its own advertised
  actions. Every other text, including an unknown `/text`, is sent as a
  prompt, so there is no new controller frame and no way to invoke an action
  the adapter did not advertise.

### Bounds

The list is hostile input. `adapterproto` rejects a frame with more than 128
commands, a name longer than 64 bytes or outside
`[A-Za-z0-9][A-Za-z0-9._:-]*`, a description over 512 bytes, an argument hint
over 128 bytes, more than four aliases, duplicate names or aliases, or any C0
or C1 control character. Adapters fold whitespace, truncate on a rune
boundary, and drop entries that would fail, so a single bad entry (such as
Claude's internal `__remote-workflow`) does not end the session. Upstream
responses keep their own size limits; OpenCode's listing inlines every
template and gets a separate 4 MiB response bound.

### Per harness

- `claude-stream` reports the `initialize` commands as `prompt`, then
  re-reports without the names `system/init` marks terminal-only.
- `codex-app-server` reports `compact`, `review`, and each enabled skill with
  an absolute path, all as `action`. `/review` with no arguments reviews
  uncommitted changes, and with arguments uses them as custom instructions.
  Review runs as a nested turn, so the adapter ends the Meridian turn only when
  the outer turn completes.
- `pi-rpc` reports a `compact` action, then the `get_commands` entries as
  `prompt`.
- `opencode-server` reports every `GET /command` entry as `action` and runs it
  with `POST /session/{id}/command`. The call blocks until the reply exists, so
  it runs off the frame loop and its failure becomes an
  `opencode_command_failed` error.

Exclusions come from the harness, never from a Meridian list.

### Daemon, API, and UI

- The daemon persists the frame as an encrypted JSON status block like any
  other adapter event. `ThreadAdapterEvent` gains `type: commands` and a
  bounded `commands` array. There is no new endpoint and no migration.
- The composer opens a filterable menu when the text starts with `/` and
  contains no whitespace yet. It shows the latest reported list, matched by
  name and alias only, so Enter does not capture text the user meant as a
  prompt. Picking a command inserts `/name ` for the user to complete and
  send.
- Meridian controls appear in a separate group and never reach the harness:
  Stop while a turn runs, and New session when the Capsule has a structured
  profile.
- Model switching is not offered. A structured session's model comes from
  the Project's provider connection and harness setup (ADR 0024), and a
  per-Thread switch would bypass that. It needs its own decision. A harness
  that reports its own `/model`, as Claude Code does, keeps it, because the
  harness runs it inside the configuration Meridian gave it.

## Consequences

The menu shows what each harness reports for this repository and version, with
no Meridian list to keep in sync. A harness without discovery shows only the
actions its adapter maps, and a harness or older transcript that reports
nothing shows an explanatory empty state.

Command names and descriptions come from the repository. They are stored only
inside the encrypted transcript, never in plaintext lifecycle records, and the
UI renders them as text.

A daemon older than this change does not know the `commands` frame kind and
fails the session when it reads one. Official pack images share the daemon's
release tag (ADR 0021), so they stay in step. A custom image built from a
newer Capsule base than the daemon does not.

Actions add harness calls the adapter makes for the user: Codex compaction,
review, and skills, Pi compaction, and OpenCode commands. They run only from
a user message whose name the adapter advertised, with the same harness
permissions as a prompt. OpenCode commands can name a different agent or
model in their own definition; that is repository configuration the harness
already honours for the same command typed in its TUI.
