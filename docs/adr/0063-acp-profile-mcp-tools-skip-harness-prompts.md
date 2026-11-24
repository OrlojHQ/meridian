# ADR 0063: ACP profile MCP tools skip the harness permission prompt

- Status: Accepted; implemented in v0.17.0
- Date: 2026-11-24
- Amends: ADR 0045 (profile MCP tools skip the harness permission prompt),
  which left every ACP permission request with the operator
- Extends: ADRs 0030 and 0062

## Context

ADR 0045 let the Claude Code and Codex adapters allow tool calls for a
profile-registered MCP server (`--mcp-server`) without asking the operator.
It left ACP harnesses out: an ACP `session/request_permission` names its tool
only through a free-form `title`, so the adapter could not tell a profile
server's tool from a shell command whose title happened to look like one.

The `agy` pack (ADR 0062) made that gap visible. `agy acp` 1.5.14 asks
permission before every MCP tool call and offers only "allow once" and
"reject once", so an `agy` lead stopped for an operator click on every
`meridian_whoami`, `meridian_spawn`, `meridian_wait`, and the rest, with no
way to approve them for the session.

What `agy` sends, observed against the shipped 1.5.14 binary with a stub
model gateway and a stub `meridian-coord` server:

```json
{"method": "session/request_permission", "params": {"toolCall": {
  "toolCallId": "call_1", "name": "mcp_meridian-coord_meridian_whoami",
  "title": "Run mcp_meridian-coord_meridian_whoami", "kind": "other",
  "rawInput": {"toolSummary": "Check role"}},
  "options": [{"optionId": "allow", "kind": "allow_once"},
              {"optionId": "deny", "kind": "reject_once"}]}}
```

- Besides the title, `agy` sends a structured `name`, the registered tool
  name `mcp_<server>_<tool>`. A shell command is `run_command`; `agy`
  rejects a call to an unregistered tool before it asks.
- `agy` reads MCP servers from `session/new`, from
  `~/.gemini/config/mcp_config.json`, and from enabled plugins. All of them
  live in the runtime's home or come from the profile, never from the
  workspace, and a personal setup imports only `skills/` from `agy`'s home.
- `agy`'s own permission rules cannot do this instead. `agy acp` reads
  `~/.gemini/antigravity-cli/settings.json`, but a `permissions.allow` rule
  of `mcp(meridian-coord/*)`, `mcp(meridian-coord/meridian_whoami)`, or even
  `mcp(*)` (also under `permission`) still prompted.
  `--dangerously-skip-permissions` allows every tool, shell included.

## Decision

The `acp` driver answers a `session/request_permission` itself when the
request's own `toolCall.name` is `mcp_<server>_<tool>` for a server passed
with `--mcp-server`, and the request offers an allow option. It selects
`allow_once` when offered, otherwise `allow_always`, and does not relay the
request to the operator. The rules of ADR 0045 apply unchanged: the
coordination tools act only through the operator's grant, whose limits and
"Approve each spawn" setting the daemon enforces.

- Only the structured `name` counts. A request with no `name`, a `name` for
  any other server, or a title that merely reads like a profile tool still
  reaches the operator. The driver never matches titles.
- The name must come from the permission request itself, not from earlier
  `tool_call` updates for the same call.
- The decision is made before the "permissions disabled" check, as for
  Claude Code, so a profile with permissions off still runs its coordination
  tools instead of rejecting them.
- The call still appears in the transcript through the agent's own
  `tool_call` updates.
- `toolCall.name` is not part of ACP v1. An ACP agent that does not send
  it, as far as Meridian knows OpenCode and DeepSeek Harness, keeps
  prompting. Any ACP agent that sends it gets the same rule.

## Consequences

- An `agy` lead delegates without operator clicks unless the operator
  turned on "Approve each spawn".
- A process in the Capsule that writes an `agy` MCP server or plugin named
  `<profile-server>_<x>` into the runtime's home could have that server's
  tools allowed, because `mcp_<profile-server>_<x>_<tool>` matches. That
  gives it nothing new: the same process can already run any code in the
  Capsule and call `meridian-coord` directly, and the repository cannot
  register `agy` MCP servers. The Capsule stays the only isolation boundary.
- If a later `agy` release changes the name format or drops the field, its
  coordination calls prompt again. Nothing is allowed by accident.
