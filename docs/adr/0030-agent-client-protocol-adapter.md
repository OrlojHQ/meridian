# ADR 0030: Run Agent Client Protocol harnesses through one generic driver

- Status: Accepted
- Date: 2026-11-07
- Extends: ADRs 0013, 0026, and 0028
- Supersedes: the `opencode-server` driver

## Context

Every structured harness so far has its own driver in
`meridian-harness-adapter`: `pi-rpc`, `claude-stream`, `codex-app-server`, and
`opencode-server`. Each one translates a private protocol, so adding a harness
means new Go code, new fixtures, and a new review of how that harness's
records are bounded.

The Agent Client Protocol (ACP) is a published JSON-RPC protocol between an
editor ("client") and a coding agent over stdio. Version 1 covers what a
Meridian Thread needs: sessions in a working directory, prompts with stop
reasons, streamed message chunks, tool calls, plans, permission requests with
agent-defined options, available commands, cancellation, and resume or load.
OpenCode ships `opencode acp`, and more harnesses ship or plan ACP agents.

The `opencode-server` driver drove OpenCode's HTTP API and SSE stream. It used
a permission route OpenCode marks as legacy, needed reconnect and
deduplication logic for SSE, and ran commands through a separate blocking
endpoint.

ACP also defines client capabilities that let the agent read and write files
and run terminals through the client, and authentication methods the client
may be asked to perform.

## Decision

### One `acp` driver

`meridian-harness-adapter acp -- <agent> [args...]` launches the agent command
from the trusted profile directly, with stderr discarded and its own process
group, and maps ACP v1 onto `meridian.adapter.v1`:

- `initialize` must answer protocol version 1. The adapter advertises
  `fs.readTextFile: false`, `fs.writeTextFile: false`, and `terminal: false`.
- A Thread start calls `session/new` with the Capsule working directory and
  the profile's `--mcp-server` entries. Resume state is only
  `{"driver":"acp","sessionId":...}`. Resume uses `session/resume` when the
  agent offers it, else `session/load` with the replayed history dropped, else
  fails with `acp_resume_unsupported`.
- Each user message is one `session/prompt` with one text block. Messages sent
  during a turn queue, up to 32.
- Message chunks become deltas and one final message per ACP message ID; tool
  calls and updates become `tool_start` and `tool_result`; plans become a
  `plan` tool with a checklist; stop reasons other than `end_turn` and
  `cancelled` become errors. Thoughts, mode, model, usage, and session-info
  updates are dropped.
- `available_commands_update` becomes a `commands` frame of `prompt` commands.
  ACP agents expand `/name args` in an ordinary prompt, so the adapter has no
  actions.
- `session/request_permission` becomes a `permission_request` whose options
  are the names of the agent's options. The controller's answer is matched to
  one of those options by name or option ID; anything else selects the
  agent's reject option, or `cancelled` when it offered none. With
  `--permissions=false` the adapter rejects without asking.
- Cancel answers open permission requests with `cancelled`, sends
  `session/cancel`, and ends after the agent reports `cancelled` or two
  seconds pass.
- Every other agent-to-client request (`fs/*`, `terminal/*`, elicitation,
  unknown methods) is refused with "method not found". The agent works with
  its own tools inside the Capsule.

The adapter does not implement ACP authentication. Credentials come from the
agent's own configuration in the setup home: a personal setup or a provider
connection lease (ADR 0024). An `auth_required` answer fails the session with
`acp_auth_required`.

`--secret-env NAME` sets an environment variable to a fresh random 256-bit
value for each launch, for agents that protect a side channel with a password.
Names under `MERIDIAN_` and `LD_` and basic process variables are rejected.
These values are not credentials and never reach Meridian's stores.

### New harnesses are data

A harness with an ACP agent needs a pack image that installs a pinned,
checksummed build at image build time and a manifest whose structured profile
runs `acp -- /usr/local/bin/<pack> <its ACP arguments>`. Launching through
`npx` or any download at Capsule start is not supported. The pack must still
be added to `InstallationHarnessImages`.

### OpenCode moves to ACP

The OpenCode pack runs `opencode acp --hostname 127.0.0.1 --mdns=false` with
`--secret-env OPENCODE_SERVER_PASSWORD`. `opencode acp` also starts OpenCode's
HTTP server on loopback; the per-launch password keeps the same protection the
old driver gave `opencode serve`. The `opencode-server` driver and its
`opencode` adapter kind are removed. Provider connections for OpenCode are
unchanged: `capsuled` still writes OpenCode's native provider configuration
with a lease token read from `MERIDIAN_PROVIDER_TOKEN`.

Claude Code, Codex, and Pi keep their drivers. Pi's ACP bridge (`pi-acp`) was
reviewed and not adopted: it is a self-described MVP with expected breaking
changes, its published package uses caret dependency ranges, its documented
setup is `npx -y pi-acp`, and it runs `npm view` against the registry on
session start.

## Consequences

Adding an ACP harness is a pack directory and a catalog entry. The driver's
bounds and fixtures apply to every ACP agent, so a new harness does not add
new parsing code to review.

OpenCode's slash commands are `prompt` commands instead of `action` commands.
OpenCode runs `/name args` prompts as its own commands, so the behaviour is
the same; the `opencode_command_failed` error no longer exists.

The `adapterKind` value `opencode` becomes `acp` in the API, and any profile
running the `acp` driver reports it. A daemon from before this change does not
know the value. Official pack images share the daemon's release tag
(ADR 0021), so they stay in step.

ACP features that need client capabilities (file system, terminals,
elicitation) are not offered. An agent that requires them fails or degrades
in its own way. Adding any of them needs its own decision, because each one
would let the agent act through Meridian rather than only inside the Capsule.
