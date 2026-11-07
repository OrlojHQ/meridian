# Structured harness adapters

`meridian-harness-adapter` is installed in the development and release Capsule
images. It never installs Pi, OpenCode, Claude Code, Codex, model providers, or credentials. The
thin `meridian-capsule` image stays supervisor-only. Optional official images
`FROM` that base and add one pinned harness binary. Project images may do the
same. Credentials remain external to repository YAML.

`make try-opencode` is the first-run path: it builds the OpenCode pack, starts
the local Docker daemon if needed, and opens the dashboard on New Project with
OpenCode selected. `make try-pi`, `make try-claude`, and `make try-codex` do
the same for those packs.

Official pack images start from the thin supervisor (`CAPSULE_BASE`) and add
one pinned harness binary plus trusted native PTY and structured profiles under
the pack name. Tag-only releases
publish and Cosign-sign `ghcr.io/orlojhq/meridian-capsule-<pack>:<tag>` from
the published thin Capsule image. Local make targets still build `:dev`.

Every pack builds with the shared `images/harness-pack/Dockerfile`. A pack is
a directory `images/capsule-<pack>/` holding:

- `pack.env`: the pinned release as data (never sourced by a shell): name,
  version, an HTTPS `.tar.gz` URL with `{version}` and `{arch}` placeholders,
  per-architecture tokens and SHA-256 checksums, and a `file` or `tree`
  layout naming the executable inside the archive;
- `project.yaml`: the trusted profiles, installed as
  `/etc/meridian/harnesses.d/<pack>.yaml`: a native profile whose executable
  is `/usr/local/bin/<pack>`, and a structured profile that runs that binary
  through `/usr/local/bin/meridian-harness-adapter`; and
- optionally a `<pack>` launcher script that sets harness environment before
  exec'ing the pinned binary. Without one, `/usr/local/bin/<pack>` is a symlink
  to the binary.

`images/harness-pack/install` validates the data, verifies the checksum, runs
the binary's `--version`, and installs all three. `install --check <dir>`
validates without network access. `make capsule-<pack>-image` and
`make try-<pack>` exist for every pack directory, and CI builds and releases
publish every one. `make list-packs` prints them.

| Pack | Image | Layout | Launcher |
| --- | --- | --- | --- |
| `claude` | `meridian-capsule-claude:dev` | file | Bun temp, updates disabled |
| `codex` | `meridian-capsule-codex:dev` | file | symlink |
| `opencode` | `meridian-capsule-opencode:dev` | file | Bun temp |
| `pi` | `meridian-capsule-pi:dev` | tree | Bun temp |

A new official pack also needs its name in `InstallationHarnessImages`; a test
fails until the catalog and the pack directories agree.

Each official pack installs a native and a structured profile under its name:

| Pack | Structured driver | Shipped adapter arguments |
| --- | --- | --- |
| `claude` | `claude-stream` | `claude-stream -- /usr/local/bin/claude` |
| `codex` | `codex-app-server` | `codex-app-server --approval-policy untrusted --sandbox danger-full-access -- /usr/local/bin/codex` |
| `opencode` | `opencode-server` | `opencode-server -- /usr/local/bin/opencode` |
| `pi` | `pi-rpc` | `pi-rpc --session-dir /home/capsule/.pi-sessions -- /usr/local/bin/pi` |

The terminal launcher picks the native profile, and Threads pick the
structured one. Because the image owns every mode of its names and a repository
cannot define profiles under them, the daemon knows a pack's modes before any
Capsule exists. It advertises them as `interactionModes: [native, structured]`
on the pack's entry in the capabilities `harnessImages` catalog. `mock` uses
the supervisor image, which installs no trusted profile, so its entry declares
no modes and the repository's `.meridian/project.yaml` decides. Project Thread
spawn rejects a pack whose catalog entry, matched by name and image, lacks
`structured` with `422 unsupported` before provisioning. Packs outside the
catalog, such as custom images, are not checked up front. A test keeps the
declared modes equal to the profiles in `images/capsule-<pack>/project.yaml`.

Structured sessions have no terminal to sign in with. Attach a provider
connection (see [personal harness setups](personal-harness-setups.md)) to the
Project and harness:

- for `claude`, `capsuled` sets `ANTHROPIC_BASE_URL` and a gateway lease in
  `ANTHROPIC_API_KEY`;
- for `codex`, it writes a `meridian` model provider into the setup home's
  `~/.codex/config.toml` that reads the lease from `MERIDIAN_PROVIDER_TOKEN`;
- for OpenCode and Pi, it writes their native provider configuration the same
  way.

Official structured profiles list no `secretReferences`, because listed
references are mandatory. Images that prefer named API keys can define their
own profile with Project-authorized `harness_env` secrets. A Claude Code turn
without credentials fails with `claude_turn_failed` ("Not logged in"), and a
Codex turn fails with `codex_turn_failed` after Codex's own retries.

Session files live in the Capsule's tmpfs home. Resume survives adapter
restarts but not Capsule restarts; a missing session fails with
`claude_resume_failed` or `codex_resume_failed` rather than starting a new
conversation.

Repositories do not copy those profiles. Keep a thin Project default image and
allowlist a pack with `--harness-image=NAME=IMAGE`; `/new` selects it, the
daemon freezes that image and profile name on the Capsule, and the dashboard
hands the terminal to the harness when Ready. That layout is the contributor
example: keep the base image thin, add a Dockerfile and trusted manifest to
an official pack, and leave other harnesses out unless they are official.

The binary has six drivers:

- `generic -- <adapter> [args...]` validates and normalizes both directions of
  an existing `meridian.adapter.v1` child while suppressing child stderr and
  owning its process group. A profile can execute a trusted protocol-speaking
  adapter directly when this extra boundary is unnecessary.
- `pi-rpc -- <pi> [args...]` starts Pi with `--mode rpc`; `--session-name` and
  `--session-dir` are adapter flags. A user-message metadata object may select
  an upstream queue primitive explicitly:
  `{"pi":{"command":"prompt|steer|follow_up"}}`. The default is `prompt`.
- `opencode-server -- <opencode> [args...]` remains available for explicit
  structured API/CLI integrations. It starts `opencode serve` on a random
  `127.0.0.1` port with a fresh 256-bit Basic Auth password, mDNS disabled, no
  CORS origins, and no public bind. `--permissions=false` disables permission
  capability and rejects permission events.
- `claude-stream -- <claude> [args...]` runs Claude Code headless with
  `--print --input-format stream-json --output-format stream-json --verbose
  --include-partial-messages`. Claude Code queues messages sent during a turn.
  Tool permissions use the stdio control protocol (`--permission-prompt-tool
  stdio`) and become `permission_request` frames with `allow`/`deny` options;
  `AskUserQuestion` becomes one `input_request` per question.
  `--permissions=false` passes `--permission-prompts none` and advertises no
  permission or input capability. The adapter owns `--print`, the format
  flags, `--resume`, `--continue`, `--session-id`, `--mcp-config`, and the
  permission flags, and rejects them in profile arguments.
- `codex-app-server -- <codex> [args...]` runs `codex app-server --listen
  stdio://`; profile arguments such as `-c model="..."` follow it.
  `--approval-policy` (`untrusted`, `on-request`, or `never`) and `--sandbox`
  (`read-only`, `workspace-write`, or `danger-full-access`) are passed to
  `thread/start` and `thread/resume`. Command, file-change, and permission
  approvals, data-free MCP elicitations (Codex's MCP tool approvals), and
  `requestUserInput` become permission and input requests. Their options are
  Codex's own decisions (`accept`, `acceptForSession`, `decline`, `cancel`;
  permission grants use `allowTurn`, `allowSession`, or `deny`), and any other
  choice that is not an allow word declines. Codex runs one turn at a time, so
  messages sent during a turn queue as follow-up turns. User-message metadata
  `{"codex":{"delivery":"steer"}}` adds the message to the running turn with
  `turn/steer` instead.
- `mock` is deterministic and credential-free. `--fault` accepts `gap`,
  `duplicate`, `crash`, `crash-start`, `hang`, `oversize`, or `malformed`.

`claude-stream` and `codex-app-server` accept
`--mcp-server NAME=/absolute/executable[,argument...]`, repeatable, to register
a stdio MCP server in the harness's own configuration. For Claude Code that is
inline `--mcp-config` JSON; for Codex it is `-c mcp_servers.NAME.command` and
`.args`. Names match `[A-Za-z0-9_-]{1,64}`, and quotes, backslashes, and line
breaks are rejected. MCP tool calls still go through the harness's permission
prompts.

Both drivers end every turn with exactly one `status: idle` carrying resume
state. When the turn produced final text, an `assistant_message` precedes it;
Claude Code text that appears only in its `result` record is emitted as one.
A failed turn emits `error` before `idle`. Cancel interrupts the running turn
and ends the session.

Example profiles for custom images:

```yaml
version: v1
harnesses:
  - name: my-pi
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["pi-rpc", "--session-dir", "/home/capsule/.pi-sessions", "--", "/usr/local/bin/pi"]
    workingDirectory: .
    pty: false
    timeout: 2h

  - name: my-claude
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["claude-stream", "--mcp-server", "tools=/usr/local/bin/my-mcp,serve", "--", "/usr/local/bin/claude"]
    workingDirectory: .
    pty: false
    timeout: 2h

  - name: opencode-structured
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["opencode-server", "--", "/usr/local/bin/opencode"]
    workingDirectory: .
    pty: false
    timeout: 2h

  - name: custom
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["generic", "--", "/usr/local/bin/custom-meridian-adapter"]
    workingDirectory: .
    pty: false
    timeout: 2h

  - name: mock
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["mock"]
    workingDirectory: .
    pty: false
    timeout: 2m
```

Do not put API keys, server passwords, or bearer tokens in these arguments.
Use Project-authorized `harness_env` named secrets and profile
`secretReferences` for process-start environment values. Meridian does not
perform ambient provider credential discovery, and it never injects
`git_https`, `git_push`, or `github_api` values into adapters.

## Tested upstream contracts

The Pi driver follows the authoritative `badlogic/pi-mono`
`packages/coding-agent/docs/rpc.md` and `rpc-types.ts` contract retrieved
2026-08-24: strict LF JSONL, correlated `response` records, delta-only
`message_update`, authoritative `message_end`, tool execution events,
`extension_ui_request`/`extension_ui_response`, `get_state`,
`switch_session`, and `abort`. Resume state contains only the bounded Pi session
path/id/name. Unknown events, malformed records, correlation mismatches, and
oversized records terminate the adapter.

The OpenCode driver follows the authoritative server documentation and OpenAPI
surface retrieved 2026-08-24: authenticated `opencode serve`, `/global/health`,
`POST /session`, `GET /session/{id}`, `POST
/session/{id}/prompt_async`, `GET /event`, message/part/session events, `POST
/session/{id}/abort`, message reconciliation, and the documented legacy `POST
/session/{id}/permissions/{permissionID}` endpoint. SSE reconnects use
`Last-Event-ID` when supplied and bounded hash deduplication otherwise.

OpenCode's permission route is explicitly legacy and has changed in development
versions. Disable it with `--permissions=false` when the installed version does
not publish that exact endpoint; the driver does not guess newer permission
schemas. Question/input APIs and arbitrary URL proxying are not supported.

The Claude Code driver follows the stream-json control protocol of Claude Code
2.1.283, recorded against the real binary on 2026-11-07: an `initialize`
control request, `user` input records, `stream_event` partial messages whose
text deltas share an ID with the authoritative per-block `assistant` records,
`user` tool results, `result` turn ends (including `is_error` results such as
"Not logged in"), `can_use_tool` control requests answered with `allow` plus
`updatedInput` or `deny`, `control_cancel_request`, and `interrupt`. Records
from subagents (non-null `parent_tool_use_id`) are dropped.

The Codex driver follows the `codex app-server generate-json-schema` output of
Codex 0.157.1: `initialize` and `initialized`, `thread/start`,
`thread/resume`, `turn/start`, `turn/steer`, `turn/interrupt`, `turn/*` and
`item/*` notifications, `item/agentMessage/delta`, `error` with `willRetry`,
`serverRequest/resolved`, and the v2 approval, user-input, and elicitation
server requests. Resume state contains only the thread UUID. Correlation
mismatches and malformed or oversized records end the adapter.

No driver parses terminal/TUI output.

Harness adapters own upstream session, compaction, tool, permission, and model
semantics. Meridian validates and transports typed frames, persists an encrypted
Thread transcript (including bounded frame metadata inside ciphertext), and
presents explicit user controls; it does not implement, infer, or recover an
agent loop. The TUI launcher uses native PTY profiles and does not translate
harness commands into this protocol. Daemon recovery never auto-sends transcript input. Clients retry
ambiguous mutations with the same idempotency key; durable content-free
delivery acknowledgement prevents successful replay from resending.

## Verification

The deterministic coverage is intentionally composed rather than hidden in one
brittle external script:

- adapter contract tests cover mock multi-turn, tool events, permission/input
  round trips, resume, cancellation, Pi RPC fixtures, authenticated OpenCode
  server/SSE fixtures, reconnect cursors, and deduplication. Claude Code and
  Codex fixtures replay recorded upstream records for streaming, final
  messages, tools, permissions, questions, MCP approval, queueing and steering,
  withdrawn prompts, failed turns, resume, and MCP registration;
- supervisor tests cover strict negotiation, malformed/oversized/deep JSON,
  LF framing (including U+2028/U+2029 content), concurrent writes, replay gaps,
  crash/hang/process-group cleanup, stale auth/version, and bounded consumers;
- Docker integration builds the development Capsule image, verifies the
  packaged adapter through `mock-structured`, exercises disconnect/replay,
  native PTY fallback, Moments, Shard, Rewind, Seal, and owned cleanup;
- application, API, maintenance, TUI, and web suites cover encrypted
  multi-turn Thread persistence, idempotency/restart behavior, SSE reconnect,
  lifecycle conflicts, offline key rotation, backup/deep verify/restore,
  crypto-shred, and client presentation.

`make agentsandbox-integration` is an opt-in live Kind check; deterministic fake
client tests always verify the same structured port-forward transport and
capability fail-closed behavior. Real Pi, OpenCode, Claude Code
(`MERIDIAN_CLAUDE_SMOKE`), and Codex (`MERIDIAN_CODEX_SMOKE`) smokes require both the
corresponding `MERIDIAN_*_SMOKE=1` flag and a short-lived configured prompt with
working provider credentials. Otherwise they skip, while credential-free
contract fixtures still run.
