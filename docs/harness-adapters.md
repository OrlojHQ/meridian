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
  version, and a layout. The `file` and `tree` layouts take an HTTPS
  `.tar.gz` URL with `{version}` and `{arch}` placeholders, per-architecture
  tokens and SHA-256 checksums, and the executable inside the archive. The
  `npm` layout takes a package name and an entry file inside it instead (see
  below). `PACK_IDENTITY`, when set, is text the binary's `--help` must
  contain;
- `project.yaml`: the trusted profiles, installed as
  `/etc/meridian/harnesses.d/<pack>.yaml`: a native profile whose executable
  is `/usr/local/bin/<pack>`, and a structured profile that runs that binary
  through `/usr/local/bin/meridian-harness-adapter`. A pack whose harness has
  no terminal UI ships only the structured profile;
- optionally a `<pack>` launcher script that sets harness environment before
  exec'ing the pinned binary. Without one, `/usr/local/bin/<pack>` is a symlink
  to the binary. An `npm` pack requires one, since its entry runs under the
  image's Node; and
- optionally `skills/<name>/SKILL.md` Agent Skills, installed read-only under
  `/usr/local/share/meridian/<pack>/skills/` for a profile to load. Each skill
  is one regular file of at most 16 KiB with `name` and `description`
  frontmatter; names match `[a-z0-9-]`, and symlinks or other files fail the
  build.

`images/harness-pack/install` validates the data, verifies the checksum, runs
the binary's `--version` (and `--help` against `PACK_IDENTITY`), and installs
the binary, profiles, launcher, and skills. `install --check <dir>` validates
without network access.
`make capsule-<pack>-image` and `make try-<pack>` exist for every pack
directory, and CI builds and releases publish every one. `make list-packs`
prints them.

The `npm` layout is for harnesses published only as npm packages. The pack
directory also holds a `package.json` that must be exactly
`{"name":"meridian-capsule-<pack>","private":true,"dependencies":{"<package>":"<version>"}}`
(npm's two-space formatting), with an exact version, and the
`package-lock.json` npm generated from it. The check requires lockfile
version 3, the pinned version at `node_modules/<package>`, no `link` entries,
and a `registry.npmjs.org` tarball with a `sha512` integrity for every
package. The build runs `npm ci --ignore-scripts --omit=dev` with the image's
Node into `/usr/local/libexec/<pack>`, so every file comes from the lockfile
and no package script runs. Nothing is downloaded when a Capsule starts.

| Pack | Image | Layout | Launcher |
| --- | --- | --- | --- |
| `claude` | `meridian-capsule-claude:dev` | file | Bun temp, updates disabled |
| `codex` | `meridian-capsule-codex:dev` | file | symlink |
| `opencode` | `meridian-capsule-opencode:dev` | file | Bun temp |
| `pi` | `meridian-capsule-pi:dev` | tree | Bun temp |
| `dsh` | `meridian-capsule-dsh:dev` | npm | Node, telemetry disabled, native addons loaded in place |

A new official pack also needs its name in `InstallationHarnessImages`; a test
fails until the catalog and the pack directories agree.

Each official pack installs a structured profile under its name, and all but
`dsh` a native one:

| Pack | Structured driver | Shipped adapter arguments |
| --- | --- | --- |
| `claude` | `claude-stream` | `claude-stream -- /usr/local/bin/claude` |
| `codex` | `codex-app-server` | `codex-app-server --approval-policy untrusted --sandbox danger-full-access -- /usr/local/bin/codex` |
| `opencode` | `acp` | `acp --mcp-server meridian-coord=/usr/local/bin/meridian-coord,mcp -- /usr/local/bin/opencode acp` |
| `pi` | `pi-rpc` | `pi-rpc --session-dir /home/capsule/.pi-sessions -- /usr/local/bin/pi --skill /usr/local/share/meridian/pi/skills/meridian-coord` |
| `dsh` | `acp` | `acp --mcp-server meridian-coord=/usr/local/bin/meridian-coord,mcp -- /usr/local/bin/dsh --profile acp` |

A lead Thread reaches the in-Capsule `meridian-coord` tool (ADR 0029) through
its structured profile. Claude Code and Codex register it as a stdio MCP
server with `--mcp-server` (shown in the profile files). OpenCode and `dsh`
receive it in the `mcpServers` list of ACP `session/new` and of the resume
call, so the registration is per session and Meridian writes no OpenCode or
DeepSeek Harness configuration into the workspace; OpenCode names the tools `meridian-coord_meridian_spawn`
and so on. Pi has no MCP client, so the Pi pack ships a `meridian-coord`
skill that teaches the agent the CLI through its bash tool, and the profile
loads it with `--skill` (Pi loads explicit `--skill` paths even when skill
discovery is off). A subagent Capsule lists `meridian_report`,
`meridian_propose_delivery`, and `meridian_whoami`. In a Capsule that is
neither a lead nor a subagent,
the MCP server lists no tools and the CLI answers `coordination_disabled`.

The terminal launcher picks the native profile, and Threads pick the
structured one. Because the image owns every mode of its names and a repository
cannot define profiles under them, the daemon knows a pack's modes before any
Capsule exists. It advertises them as `interactionModes` (`[native,
structured]`, or `[structured]` for `dsh`) on the pack's entry in the
capabilities `harnessImages` catalog. `mock` uses the supervisor image, which
installs no trusted profile, so its entry declares no modes and the
repository's `.meridian/project.yaml` decides. Project Thread spawn rejects a
pack whose catalog entry, matched by name and image, lacks `structured`, and
creating a Capsule with a terminal launcher rejects one that lacks `native`,
both with `422 unsupported` before provisioning. The dashboard offers only
the modes a catalog pack declares. Packs outside the catalog, such as custom
images, are not checked up front. A test keeps the declared modes equal to the
profiles in `images/capsule-<pack>/project.yaml`.

### DeepSeek Harness

`dsh` installs `@deepseek-ai/dsh` (DeepSeek Harness, a developer preview)
from its lockfile. Debian and Ubuntu ship an unrelated `dsh` (distributed
shell) whose `--version` also succeeds, so the pack sets `PACK_IDENTITY` to
`boot a DeepSeek Harness profile` and the build fails on any other binary.
The published package ships the `acp`, `web`, `headless`, and `sdk` profiles
but no terminal UI; DeepSeek's TUI is a plugin that `dsh` installs from npm at
run time, so the pack has no native profile rather than download code in a
Capsule. The launcher sets `DSH_TELEMETRY_DISABLED=1`, because `dsh`
otherwise posts product telemetry to DeepSeek, and `NARB_DISABLE_NATIVE_CACHE=1`,
because its native-addon loader otherwise copies addons into the Capsule's
`noexec` `/tmp` and fails to load them.

The dependency scan (`.github/workflows/trivy.yml`) covers the pack
lockfiles, and an upstream pin can only change with a new upstream release:
the canonical `package.json` leaves no room for npm `overrides`. DeepSeek
Harness 0.2.2-rc.1 pins `undici` to exactly 8.11.2 in `dsh-http-proxy` and
`dsh-web-fetch-http`, which has four HIGH advisories fixed in 8.12.2
(CVE-2026-109383, CVE-2026-109829, CVE-2026-109869, CVE-2026-87018). The
code runs only inside the `dsh` Capsule. `.trivyignore.yaml` accepts those
four findings for that lockfile and `undici` version only, until
2027-01-07; remove the entries when the pack moves to a `dsh` release with
`undici` 8.12.2 or later.

DeepSeek Harness reports no `available_commands_update`, so its `/` menu
lists only Meridian's own commands. It asks permission when a shell command
requests more than its default workspace-write sandbox, and offers "Allow
once" and "Reject".

`dsh` reaches models only through a Meridian provider connection. Attach an
`openai-compatible` or `anthropic-compatible` connection that has a default
model to the Project and `dsh` from the task form. Official `openai` and
`anthropic` connections are refused for `dsh`, because its route must name a
model. Without a connection, a dsh Thread's turns fail; Meridian does not
pass a raw provider key into the Capsule instead.

Besides its DeepSeek adapters, `dsh` mounts `@deepseek-ai/dsh-llm-pi-ai` in
every shipped profile (`dsh-base`), with no routes until settings declare
them. Personal setups import from `~/.dsh` (`$DSH_HOME`) into that same
tree. For a connection, `capsuled` sets `DSH_HOME` to `.dsh` in the
Meridian-owned setup home, puts the gateway lease in
`MERIDIAN_PROVIDER_TOKEN`, and rewrites `$DSH_HOME/cordis.patch.yml` on every
launch. The patch is JSON, which `dsh`'s YAML loader reads without evaluating
`!!js` tags, and the model name has passed Meridian's model validation:

```yaml
- id: llm-pi-ai
  config:
    providers:
      meridian:
        api: openai-completions        # anthropic-messages for anthropic-compatible
        baseURL: <gateway URL>/v1      # <gateway URL> for anthropic-compatible
        apiKeyEnv: MERIDIAN_PROVIDER_TOKEN
        models:
          - id: <connection model>
- id: acp
  config: {provider: meridian, model: <connection model>}
- id: agent-default-model
  config: {provider: meridian, model: <connection model>}
- id: tool-web
  config: {search: false, fetch: true}
- {id: llm-deepseek, disabled: true}
- {id: llm-deepseek-account, disabled: true}
- {id: session-log-deepseek, disabled: true}
- {id: web-search-deepseek, disabled: true}
- {id: web-deepseek-api, disabled: true}
```

Each entry replaces that entry's whole configuration, so the profile's own
defaults for these entries do not survive. `dsh` then sends
`POST <gateway URL>/v1/chat/completions` with the lease as
`Authorization: Bearer`, or `POST <gateway URL>/v1/messages?beta=true` with
it as `x-api-key`. The gateway swaps the lease for the upstream key and
forwards the request without the query; `beta=true` is the only query it
accepts, and only on Anthropic routes. `agent-default-model` matters because
subagents otherwise default to `deepseek-official`, and the model picker
offers only the `meridian` route.

The patch also closes `dsh`'s other outbound model paths:

- The DeepSeek adapters (`llm-deepseek`, `llm-deepseek-account`) are the
  only users of DeepSeek's Files API and the only senders of the
  `dsh_session_log` request field, which carries a copy of the session's
  events. With them and the `session-log-deepseek` plugin disabled, no
  request carries a session log; the pi-ai route sends images inline.
- DeepSeek web search reads its own `DEEPSEEK_API_KEY` and is disabled, and
  the `web_search` tool is not registered. `web_fetch` stays; it fetches from
  the Capsule under the Project's egress policy, like other harnesses' fetch
  tools.
- `dsh` lists models (`GET /models`) only from configuration pages of its
  `web` profile, never in ACP, and the gateway accepts only `POST`.

`dsh` itself accepts `http://` routes; the gateway origin is HTTPS because
`meridiand` requires it for every harness. `dsh` runs on the pack's Node 24,
which trusts its bundled public roots, so a gateway origin with a publicly
trusted certificate needs no Capsule setup. For a private CA, build a derived
image that adds the CA and either sets `NODE_EXTRA_CA_CERTS` to its path or
installs it with `update-ca-certificates` and sets `NODE_USE_SYSTEM_CA=1`.
Node ignores the system store without that variable, and the turn fails with
"Connection error."

A repository's `.env` ranks below the launch environment in `dsh` and cannot
set `DSH_*` variables, so it cannot replace the lease or move `DSH_HOME`. A
process in the Capsule can still edit the patch or save a credential in
`$DSH_HOME`, which outranks the environment, but it already holds the lease,
which is valid only at the gateway for that Capsule.

Structured sessions have no terminal to sign in with, and ACP does not
collect credentials. Sign in in a native terminal session (`opencode auth
login`, `claude auth login`, `codex login`, or the Pi TUI), or attach a
provider connection (see [personal harness setups](personal-harness-setups.md))
to the Project and harness:

- for `claude`, `capsuled` sets `ANTHROPIC_BASE_URL` and a gateway lease in
  `ANTHROPIC_API_KEY`. The Claude login itself stays encrypted on the control
  plane. Sign in from Harness settings; Meridian runs the OAuth login and the
  provider gateway attaches an API key as `X-Api-Key`, or the OAuth access
  token (or a setup token) as `Authorization: Bearer`. The login is not
  written into the Capsule environment, files, volume, image, or prompt;
- for `codex`, it writes a `meridian` model provider into the setup home's
  `~/.codex/config.toml` that reads the lease from `MERIDIAN_PROVIDER_TOKEN`;
- for OpenCode and Pi, it writes their native provider configuration the same
  way;
- for `dsh`, it writes the route patch above and sets `DSH_HOME`.

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

The binary has six drivers. `acp` is the generic one: any harness that speaks
the Agent Client Protocol needs only a pack image and a manifest (see
[Adding an ACP harness](#adding-an-acp-harness)). The others are
harness-specific:

- `generic -- <adapter> [args...]` validates and normalizes both directions of
  an existing `meridian.adapter.v1` child while suppressing child stderr and
  owning its process group. A profile can execute a trusted protocol-speaking
  adapter directly when this extra boundary is unnecessary.
- `pi-rpc -- <pi> [args...]` starts Pi with `--mode rpc`; `--session-name` and
  `--session-dir` are adapter flags. A user-message metadata object may select
  an upstream queue primitive explicitly:
  `{"pi":{"command":"prompt|steer|follow_up"}}`. The default is `prompt`.
  The Thread composer's Steer control sends `steer`; queued follow-ups are
  held in the UI until the turn is idle, then sent as ordinary prompts.
- `acp -- <agent> [args...]` runs an Agent Client Protocol (ACP v1) agent
  over stdio: `initialize`, `session/new` in the Capsule working directory
  (a real path under `/workspace`, never a host cwd or a symlink escape),
  `session/prompt` per message, and the `session/cancel` notification. The
  command and its arguments come only from the trusted profile; the agent is
  started directly, never through a shell, `npx`, or a package manager.
  `--permissions=false` answers every `session/request_permission` with the
  agent's own reject option, declines every elicitation, and advertises no
  permission, input, or elicitation capability.
  `--secret-env NAME`, repeatable, sets `NAME` to a fresh random 256-bit value
  for each launch, for an agent that protects a side channel with a password
  it does not generate itself; no official pack uses it. `--mcp-server`
  registers stdio MCP servers through `session/new`, with the syntax below.
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
  `turn/steer` instead. The Thread composer's Steer control sets that
  metadata; queued follow-ups wait in the UI until the turn is idle.
- `mock` is deterministic and credential-free. `--fault` accepts `gap`,
  `duplicate`, `crash`, `crash-start`, `hang`, `oversize`, or `malformed`.

`claude-stream`, `codex-app-server`, and `acp` accept
`--mcp-server NAME=/absolute/executable[,argument...]`, repeatable, to register
a stdio MCP server in the harness's own configuration. For Claude Code that is
inline `--mcp-config` JSON; for Codex it is `-c mcp_servers.NAME.command` and
`.args`; for ACP it is the `mcpServers` list of `session/new` and
`session/resume`. Names match `[A-Za-z0-9_-]{1,64}`, and quotes, backslashes, and line
breaks are rejected. MCP tool calls still go through the harness's permission
prompts.

These drivers end every turn with exactly one `status: idle` carrying resume
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

  - name: my-acp-agent
    interactionMode: structured
    adapter:
      protocol: meridian.adapter.v1
      executable: /usr/local/bin/meridian-harness-adapter
      arguments: ["acp", "--", "/usr/local/bin/my-agent", "acp"]
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

## Slash commands

Adapters report the slash commands their harness supports in a `commands`
frame, sent only when both hellos negotiate the `commands` capability
(ADR 0028). The browser composer shows the latest report when the user types
`/`. Meridian keeps no command list of its own.

| Driver | Source | Kind | What runs |
| --- | --- | --- | --- |
| `claude-stream` | `initialize` `commands[]`, minus `system/init` `terminal_slash_commands` | `prompt` | Claude Code expands `/name args` itself. |
| `codex-app-server` | `compact`, `review`, and enabled skills from `skills/list` | `action` | `thread/compact/start`; `review/start` on uncommitted changes, or with the arguments as custom instructions; `turn/start` with a `skill` item. |
| `pi-rpc` | `compact`, then `get_commands` | `compact` is an `action`, the rest `prompt` | `compact` with the arguments as custom instructions; Pi's `prompt` expands the rest. |
| `acp` | `available_commands_update` | `prompt` | The agent expands `/name args` sent as an ordinary `session/prompt`, as ACP specifies. |
| `generic` | the child adapter | as reported | Validated and forwarded. |

The controller sends `/name args` as an ordinary `user_message`. An adapter
runs it as an action only when `name` is one of its own advertised actions;
anything else, including unknown `/text`, goes to the harness as a prompt.
Frames hold at most 128 commands. Names match
`[A-Za-z0-9][A-Za-z0-9._:-]*` up to 64 bytes and are unique with their aliases
(at most four each); descriptions are at most 512 bytes and argument hints 128,
with no control characters. Adapters fold, truncate, and drop entries to fit,
and report the list again when it changes.

Stop and New session are Meridian controls in the same menu. They call the
Thread API and are never sent to the harness.

## Models

Adapters report the harness's current model and the models it offers in a
`models` frame, and switch on a `set_model` frame, when both hellos negotiate
the `models` capability (ADR 0031). The browser composer shows the current
model inside the message box and switches from its menu
(`POST /threads/{threadId}/model`). The switch applies from the next turn.

| Driver | List | Current | Switch |
| --- | --- | --- | --- |
| `claude-stream` | `initialize` `models[]` | `default` until a switch succeeds | control request `set_model` |
| `codex-app-server` | `model/list`, without hidden models, plus the Thread's opening model first when the catalog lacks it | `thread/start` or `thread/resume` `model` | `model` on the next `turn/start`, which Codex keeps |
| `pi-rpc` | `get_available_models`, as `provider/id` | `get_state` `model` | `set_model` with provider and model ID |
| `acp` | the `select` config option with category `model`, flattened | its `currentValue` | `session/set_config_option` |
| `generic` | the child adapter | as reported | forwarded |

Each list is what the harness's configuration already lets it use: Pi lists
providers with credentials, the `dsh` connection route names one model, and
Claude Code and Codex list their vendor's models. Codex's catalog never
names a provider connection's default model, so the adapter lists the model
the Thread opened with first, and the operator can switch back to it. An adapter switches only to
an ID from its own latest list. Anything else, or a harness that refuses, is a
non-fatal `model_unavailable` error and the session keeps its model. Frames
hold at most 128 options with unique IDs up to 256 bytes, labels up to 128,
and descriptions up to 512, with no control characters.

### Reasoning effort

When both hellos also negotiate `effort` (ADR 0033), a `models` frame carries
the current model's effort levels as `effort: {current, options}`, at most 16
options with the same bounds, and a `set_effort` frame changes it
(`POST /threads/{threadId}/effort`). An empty `current` means the harness's
own default. The composer shows "Model · Effort" on the picker and lists the
levels below the models; a single level is not shown.

| Driver | Levels | Current | Change |
| --- | --- | --- | --- |
| `claude-stream` | the current model's `supportedEffortLevels` | empty until a change succeeds | control request `apply_flag_settings` with `effortLevel` |
| `codex-app-server` | the current model's `supportedReasoningEfforts`; none for a model outside the catalog | `thread/start` or `thread/resume` `reasoningEffort`, or the model's default after a switch to a model without it | `effort` on the next `turn/start`, which Codex keeps |
| `pi-rpc` | `get_available_thinking_levels`, asked again after each model switch | `get_state` `thinkingLevel` | `set_thinking_level` |
| `acp` | the `select` config option with category `thought_level` | its `currentValue` | `session/set_config_option` |
| `generic` | the child adapter | as reported | forwarded |

A level outside the latest list, or a refused change, is a non-fatal
`effort_unavailable` error.

## Adding an ACP harness

A harness that speaks ACP over stdio needs no new Go code (ADR 0030). Add a
pack directory whose `pack.env` pins the release and whose `project.yaml`
declares the structured profile as
`["acp", "--", "/usr/local/bin/<pack>", <the agent's ACP arguments>...]`, then
add the name to `InstallationHarnessImages`. The agent binary must be
installed at image build time from a pinned, checksummed artifact: a release
archive, or an npm lockfile with the `npm` layout. Launching through `npx`,
`bunx`, or any download at Capsule start is not supported. If the agent has
no terminal UI, omit the native profile and declare only the structured mode
in the catalog, as `dsh` does.

The `acp` driver maps ACP onto `meridian.adapter.v1` like this:

| ACP | Meridian |
| --- | --- |
| `session/new`, `session/resume`, or `session/load` | `status: idle` with resume state `{"driver":"acp","sessionId":...}` |
| `user_message` | one `session/prompt` with a single text block; messages sent during a turn queue (at most 32) |
| `agent_message_chunk` | `assistant_delta`, then one `assistant_message` per ACP message ID |
| `tool_call`, `tool_call_update` | `tool_start` once the call runs or asks permission, `tool_result` when it completes or fails |
| `plan` | a `plan` tool with a checklist; an unchanged plan is not repeated |
| `available_commands_update` | `commands` |
| `session/request_permission` | `permission_request` whose options are the agent's own option names |
| `elicitation/create` (form mode) | one question per field, in order: `permission_request` of kind `question` for confirmations, booleans, and single-selects; `input_request` for typed answers (see below) |
| stop reason `end_turn` / `cancelled` | `idle` / `end: cancelled` |
| stop reason `max_tokens`, `max_turn_requests`, `refusal` | `error` (`acp_max_tokens`, `acp_max_turn_requests`, `acp_refusal`), then `idle` |
| controller `cancel` | answers open permission prompts with `cancelled` and open elicitations with `cancel`, sends `session/cancel`, and ends after the agent stops or 2 seconds |

Thoughts, user-message replays, mode, model, usage, and session-info updates
are dropped. The adapter advertises no `fs` or `terminal` client capability
and refuses every other agent-to-client request, so the agent edits files and
runs commands with its own tools inside the Capsule, under its own permission
prompts.

With permissions enabled the adapter advertises form-mode elicitation
(ADR 0032), so an agent can ask the operator questions. A form without fields
is an **Accept**/**Decline**/**Cancel** confirmation. Otherwise each field is
asked in turn, prefixed "(n of m)" with the agent's message: a boolean or a
single-select string with at most 30 choices gets one button per choice, then
**Skip** for an optional field and **Decline**; numbers, integers, free text,
multi-selects (comma-separated), and larger single-selects get a text prompt
that says what the answer must look like, and an optional one accepts `-` to
leave it out. The adapter checks each answer against the schema (ranges,
choices, lengths, the `email`, `uri`, `date`, and `date-time` formats, and a
`pattern` when Go's RE2 compiles it) and asks again with the reason when it
does not fit; the agent receives `accept` with typed content only after every
field is answered. URL-mode, request-scoped, and unsupported schemas (more
than 16 fields, nested objects, unknown types or formats) are declined
without asking. Resume uses `session/resume` when offered and otherwise
`session/load`, whose replayed history is not re-emitted; an agent with
neither fails resume with `acp_resume_unsupported`. Authentication is the
agent's own configuration (a personal setup or provider connection); an
agent that answers `auth_required` fails with `acp_auth_required`.

## Tested upstream contracts

The Pi driver follows the authoritative `badlogic/pi-mono`
`packages/coding-agent/docs/rpc.md` and `rpc-types.ts` contract retrieved
2026-08-24: strict LF JSONL, correlated `response` records, delta-only
`message_update`, authoritative `message_end`, tool execution events,
`extension_ui_request`/`extension_ui_response`, `get_state`,
`switch_session`, and `abort`. Command discovery and compaction follow the
`get_commands` and `compact` commands of Pi 0.87.1. Resume state contains only the bounded Pi session
path/id/name. Unknown events, malformed records, correlation mismatches, and
oversized records terminate the adapter.

The ACP driver follows the ACP v1 schema (`agentclientprotocol/agent-client-protocol`
`schema/schema.json`, schema release 1.29.1, retrieved 2026-11-07): strict LF
JSON-RPC 2.0, `initialize` with protocol version 1, `session/new`,
`session/resume`, `session/load`, `session/prompt` and its stop reasons,
`session/cancel`, `session/update` (`agent_message_chunk`, `tool_call`,
`tool_call_update`, `plan`, `available_commands_update`, and the ignored
kinds), `session/request_permission` with `allow_once`, `allow_always`,
`reject_once`, and `reject_always` options, and `$/cancel_request`. It was
recorded against `opencode acp` from OpenCode 1.18.32 and pinned in the
official pack at OpenCode 2.2.6, which also runs `/name args` prompts as its
own commands. Unknown update kinds are ignored; malformed or oversized
records, uncorrelated responses, and a protocol version other than 1 end the
adapter. `opencode acp` does not collect credentials: sign in with
`opencode auth login` in a native terminal session, or attach a provider
connection.

The driver also runs `dsh --profile acp` from DeepSeek Harness 0.2.2-rc.1
(agent `deepseek-harness-acp` 0.0.1), recorded on 2026-11-07 against local
mocks of DeepSeek's Anthropic-protocol endpoint and of an OpenAI Chat
Completions server behind the custom-connection provider gateway: streamed
turns, tool calls, a shell escalation permission prompt resolved by "Allow
once", and no command list. Its first `session/request_permission` uses
JSON-RPC id `0`.

OpenCode 2's `opencode acp` accepts no flags but `--login`; given any other
flag it prints its help to stdout and exits, which the adapter reports as
`acp_protocol_failed`. The profile therefore runs it bare. Each launch starts a
private child, `opencode serve --stdio --port 0`, that listens on
`127.0.0.1` at a random port and ends with the ACP process. `opencode acp`
generates a fresh 32-byte password for that child and passes it only in the
child's environment, and the child drops it from the environment its tools
inherit. The `/api` routes require HTTP Basic authentication with that
password; the static web-app shell is served without it. The stdio-mode
server does not read a hostname or password from OpenCode's configuration, and
OpenCode 2 has no mDNS advertisement. `opencode acp` never connects to
OpenCode's shared background service (`opencode service`). This was observed
in the 2.2.6 pack image on 2026-11-08.

The Claude Code driver follows the stream-json control protocol of Claude Code
2.1.319, checked against the real binary on 2026-11-08 (the fixtures were
recorded from 2.1.283 on 2026-11-07 and still match): an `initialize`
control request, `user` input records, `stream_event` partial messages whose
text deltas share an ID with the authoritative per-block `assistant` records,
`user` tool results, `result` turn ends (including `is_error` results such as
"Not logged in"), `can_use_tool` control requests answered with `allow` plus
`updatedInput` or `deny`, `control_cancel_request`, and `interrupt`. Records
from subagents (non-null `parent_tool_use_id`) are dropped. Commands come from
the `initialize` response's `commands` and `system/init`'s
`terminal_slash_commands`.

The Codex driver follows the `codex app-server generate-json-schema` output of
Codex 0.167.0, compared with the 0.157.1 schema the fixtures were recorded
from on 2026-11-08: `initialize` and `initialized`, `thread/start`,
`thread/resume`, `turn/start`, `turn/steer`, `turn/interrupt`, `turn/*` and
`item/*` notifications, `item/agentMessage/delta`, `error` with `willRetry`,
`serverRequest/resolved`, the v2 approval, user-input, and elicitation
server requests, `skills/list`, `thread/compact/start`, and `review/start` with
its nested review turn. Resume state contains only the thread UUID. Correlation
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
  round trips, resume, cancellation, Pi RPC fixtures, and an ACP agent fixture
  covering streaming, tools, plans, permissions, commands, resume and load,
  cancellation, refused client requests, hostile records, and form
  elicitations (field order, choice buttons, typed-answer checks and retries,
  skip and omit, decline, confirmation, URL and unsupported schemas, cancel,
  and permissions disabled). Claude Code and
  Codex fixtures replay recorded upstream records for streaming, final
  messages, tools, permissions, questions, MCP approval, queueing and steering,
  withdrawn prompts, failed turns, resume, and MCP registration. Every driver
  has a slash-command fixture covering discovery, bounds, actions, and unknown
  `/text`, and a models fixture covering the reported list, a switch, an
  unlisted ID, and a refused switch, plus the same for effort levels and how a
  model switch changes them;
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
