# ADR 0045: Profile MCP tools skip the harness permission prompt

- Status: Accepted
- Date: 2026-11-13
- Amends: ADR 0029 (bounded Thread coordination) and the threat model's
  statement that profile-registered MCP tool calls pass through harness
  permission prompts

## Context

Official structured profiles register Meridian's coordination server with
`--mcp-server meridian-coord=/usr/local/bin/meridian-coord,mcp`. Claude Code
treats MCP tools as needing permission, and in a structured Thread the
adapter relays each of those requests to the operator. A lead therefore
stopped for an operator click on every `meridian_whoami`, `meridian_spawn`,
`meridian_wait`, `meridian_send` and `meridian_patch`. One small delegated
task took four or five approvals, and a `meridian_wait` prompt blocked the
lead while its subagent was already running. Operators who left "Approve
each spawn" off reasonably read this as Meridian ignoring that choice.
Codex raises the same kind of request as an MCP elicitation with
`codex_approval_kind: mcp_tool_call`.

The prompt adds no protection here:

- The coordination tools act only through the operator's Coordination Grant.
  The daemon enforces the grant's fan-out, descendant, live-Capsule, message
  and deadline limits, and the "Approve each spawn" setting. A prompt in the
  harness changes none of those.
- Any process in a granted Capsule can already spend the grant without a
  prompt. The Pi pack does exactly that through its bash tool (ADR 0029 and
  the threat model).
- The servers come from trusted profile metadata. A repository cannot define
  or change an official profile (ADR 0026), and `ParseMCPServer` takes only
  an absolute executable path.

## Decision

The Claude Code and Codex adapters answer a permission request for a
profile-registered MCP server's tool themselves, with allow, and do not
relay it to the operator:

- **Claude Code:** a `can_use_tool` request whose tool name is
  `mcp__<server>__<tool>` for a server passed with `--mcp-server`.
- **Codex:** an `mcpServer/elicitation/request` with
  `codex_approval_kind: mcp_tool_call` whose `serverName` is such a server.
  Other elicitations, and tool calls for any other server, still prompt.
- **ACP harnesses** (OpenCode, DeepSeek Harness) are unchanged. An ACP
  permission request names its tool only through agent-written titles, so
  the adapter cannot tell a profile server's tool from any other, and it
  keeps relaying them.
- **Pi** already runs the coordination CLI without prompts.

Native (terminal) sessions are unaffected. Each harness asks there as it
always has.

The operator's controls over subagents are the grant and its settings: lead
mode at creation, the limits, "Approve each spawn", and Stop subagents.
Subagents still send their own permission and input requests to the
operator, not the lead.

## Consequences

- A lead delegates without operator clicks unless the operator turned on
  "Approve each spawn". Spawn approval then happens once per subagent on the
  board, not once per tool call in the transcript.
- A hostile repository that loads its own MCP server under a profile
  server's name, if the harness lets it, would also have that server's tool
  calls allowed. That gives it nothing new: the same repository can already
  allow any tool through its own `.claude/settings.json` permissions, and
  any process in the Capsule can already call `meridian-coord` directly. The
  Capsule stays the only isolation boundary.
- A future profile that registers an MCP server must accept that its tools
  run without a harness prompt. A server whose individual calls need operator
  approval must enforce that approval itself, as the coordination server does
  through the grant, or must not be registered with `--mcp-server`.
