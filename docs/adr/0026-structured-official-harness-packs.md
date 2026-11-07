# ADR 0026: Ship structured profiles in the official harness packs

- Status: Accepted
- Date: 2026-11-07
- Extends: ADRs 0021, 0022, 0023, and 0024
- Amends: ADR 0022 (duplicate profile names)

## Context

A Project Thread names one harness, and that name is the Project
`harnessImages` allowlist key, the Capsule's frozen harness, and the `capsuled`
profile name. ADR 0022 made every profile name unique, so an official pack
could ship only its native PTY profile. Threads therefore could not use any
official pack, including the Pi and OpenCode packs whose adapters already
existed, and Claude Code and Codex had no structured adapter at all.

Structured sessions also have no terminal, so a harness cannot be signed in
interactively the way the native TUI is.

## Decision

### One name, one profile per interaction mode

- A harness configuration may define a name at most twice: once `native` and
  once `structured`. Every lookup selects by name and interaction mode. The
  native launcher, TUI, and browser match only the mode they start.
- The trusted image owns every mode of every name it defines. If
  `/etc/meridian/harnesses.d` defines a name, `/workspace/.meridian/project.yaml`
  cannot define that name in any mode. A repository cannot attach its own
  adapter, executable, or `secretReferences` to an official name.
- Official packs install a native and a structured profile under the pack name.
  The catalog advertises `interactionModes: [native, structured]` for them.

### Claude Code adapter (`claude-stream`)

- The adapter starts `claude --print --input-format stream-json
  --output-format stream-json --verbose --include-partial-messages` and owns
  the session, resume, permission, and MCP flags. Profile arguments cannot set
  them.
- Permissions use the stdio control protocol (`--permission-prompt-tool
  stdio`). `can_use_tool` becomes `permission_request`, and `AskUserQuestion`
  becomes one `input_request` per question. Any choice other than an explicit
  allow word denies. `--permissions=false` passes `--permission-prompts none`
  instead.
- Records that carry a subagent's `parent_tool_use_id` are dropped. The
  parent's tool events already describe that work.
- Resume state holds only the session UUID.

### Codex adapter (`codex-app-server`)

- The adapter starts `codex app-server --listen stdio://` and speaks its v2
  JSON-RPC with `experimentalApi` off.
- Command, file-change, and permission approvals become `permission_request`.
  `requestUserInput` becomes `input_request`.
- MCP elicitations that collect no data, which Codex uses to approve MCP tool
  calls, become `permission_request`. Elicitations that collect data, and URL
  elicitations, are declined.
- Dynamic client tools, ChatGPT token refresh, attestation, and legacy v1
  approvals are refused.
- Codex runs one turn at a time. Messages sent during a turn queue as follow-up
  turns, and `{"codex":{"delivery":"steer"}}` metadata uses `turn/steer`
  instead.
- The shipped profile uses `--sandbox danger-full-access
  --approval-policy untrusted`. Codex's own sandbox needs user namespaces, which
  a Capsule never grants: it drops all capabilities and runs with
  `no-new-privileges`. The Capsule stays the boundary, with the isolation limits
  in the threat model, and every command Codex does not consider read-only waits
  for the operator.

### Both adapters

- Each turn ends with exactly one `status: idle`, preceded by an
  `assistant_message` whenever the turn produced final text. Failed turns emit
  `error` and then `idle`.
- A missing resume session fails closed with `*_resume_failed` and does not
  silently start a new conversation. Harness session files live in the
  Capsule's tmpfs home, so a resume works across adapter restarts but not
  across Capsule restarts.

### Authentication

Structured sessions authenticate non-interactively through provider
connections (ADR 0024):

- For Claude Code, `capsuled` sets `ANTHROPIC_BASE_URL` and a 24-hour gateway
  lease in `ANTHROPIC_API_KEY`.
- For Codex, it writes a `meridian` model provider into the setup home's
  `~/.codex/config.toml` that reads the lease from `MERIDIAN_PROVIDER_TOKEN`.

Official structured profiles list no `secretReferences`, because a listed
reference is mandatory and would block users who rely on a connection. A
custom image can still use Project-authorized `harness_env` secrets. Native
login state inside the same Capsule is neither relied on nor managed.

### MCP registration

A profile may pass `--mcp-server NAME=/absolute/executable[,argument...]`, one
or more times, to `claude-stream` or `codex-app-server`. The adapter registers
each stdio server in that harness's own configuration channel:

- for Claude Code, inline `--mcp-config` JSON;
- for Codex, `-c mcp_servers.NAME.*` overrides.

Names are limited to `[A-Za-z0-9_-]{1,64}`. Values may not contain quotes,
backslashes, NUL, CR, or LF, and the profile parser already rejects inline
secret markers. MCP tool calls still go through the harness's permission
prompts.

## Consequences

Official packs work in both the terminal and browser Threads, and the README
table reflects that.

A repository that previously defined a structured profile under an official
pack name now fails closed when that pack's image is used. It must choose
another name.

The adapters translate published protocols only and never parse terminal
output. Both CLIs change quickly, so the adapters reject malformed or oversized
records instead of guessing, and their contract fixtures pin the record shapes
of Claude Code 2.1.283 and Codex 0.157.1.

MCP servers that a profile registers run with the harness's authority inside
the Capsule. A Thread holds a gateway lease for as long as the Capsule session
does, as ADR 0024 already accepts for native sessions.
