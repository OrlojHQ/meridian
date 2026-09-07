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
one pinned harness binary plus a trusted native PTY profile. Tag-only releases
publish and Cosign-sign `ghcr.io/orlojhq/meridian-capsule-<pack>:<tag>` from
the published thin Capsule image. Local make targets still build `:dev`.

| Make target | Image | Profile |
| --- | --- | --- |
| `capsule-opencode-image` | `meridian-capsule-opencode:dev` | `/etc/meridian/harnesses.d/opencode.yaml` |
| `capsule-pi-image` | `meridian-capsule-pi:dev` | `/etc/meridian/harnesses.d/pi.yaml` |
| `capsule-claude-image` | `meridian-capsule-claude:dev` | `/etc/meridian/harnesses.d/claude.yaml` |
| `capsule-codex-image` | `meridian-capsule-codex:dev` | `/etc/meridian/harnesses.d/codex.yaml` |

Repositories do not copy those profiles. Keep a thin Project default image and
allowlist a pack with `--harness-image=NAME=IMAGE`; `/new` selects it, the
daemon freezes that image and profile name on the Capsule, and the dashboard
hands the terminal to the harness when Ready. That layout is the contributor
example: keep the base image thin, add a Dockerfile and trusted manifest to
an official pack, and leave other harnesses out unless they are official.

The binary has four drivers:

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
- `mock` is deterministic and credential-free. `--fault` accepts `gap`,
  `duplicate`, `crash`, `crash-start`, `hang`, `oversize`, or `malformed`.

Example profiles:

```yaml
version: v1
harnesses:
  - name: pi
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["pi-rpc", "--session-dir", "/home/capsule/.pi-sessions", "--", "/usr/local/bin/pi"]
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
Neither driver parses terminal/TUI output.

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
  server/SSE fixtures, reconnect cursors, and deduplication;
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
capability fail-closed behavior. Real Pi/OpenCode smokes require both the
corresponding `MERIDIAN_*_SMOKE=1` flag and a short-lived configured prompt with
working provider credentials. Otherwise they skip, while credential-free
contract fixtures still run.
