# Bring your harness setup

In the UI, select **Import my setup** while creating a Capsule, or open
**Harness settings** in the sidebar. Choose the harness configuration folder or
individual configuration files, select **Review import**, inspect the portable
files and exclusions, then select **Save setup**. The launcher selects the saved setup and
returns you to Capsule creation. No CLI command is required.

For OpenCode, select `~/.config/opencode`. For DeepSeek Harness, select `~/.dsh`
(or `$DSH_HOME` if you override it). On macOS, use Command-Shift-G in the
picker to enter a hidden folder path. Use the location field or show hidden files
on other systems. Folder selection preserves nested skills and plugins; choosing
individual files is intended for top-level configuration files. More files can be
added before review, and individual selected files can be removed.

Known authentication/runtime filenames and unsupported paths are filtered before
the browser reads their contents. Clicking Review sends the listed files to the
same Meridian installation for in-memory checks. Nothing is saved until Save;
the saved bundle uses the existing encrypted revision storage. The browser does
not expose the absolute source directory, symlink metadata, or executable bits.
Absolute laptop paths may therefore be excluded, and executable scripts can be
marked explicitly in the reviewed file details. Use the CLI when you need its
additional local discovery and path relocation behavior. A folder picker may
require browser-specific permission prompts; individual file selection is the
fallback when folder selection is unavailable.

Reimporting updates the existing default setup with optimistic version checks.
Review shows sanitized file contents, portable npm dependencies and explicit
exclusions. Login caches, conversation history and local services remain outside
the import. Native account sign-in is separate.

## CLI import

Run `meridian harness import` on the computer containing your configuration.
Choose all detected harnesses, or specify `claude`, `codex`, `dsh`, `opencode`, or `pi`.
Meridian shows included files and excluded settings before asking to save.
It never prints imported file contents or imports native login caches.

```console
meridian harness import --preview
meridian harness import codex
meridian --server https://meridian.example --token-file /path/to/token harness import
```

For automation, review `--preview --json`, then use `--yes --accept-exclusions`
to explicitly accept the reported omissions. Imports are bounded to 256 files,
128 KiB per file, and 512 KiB total content. Symlinks and unsupported paths are
excluded. Configuration-directory environment overrides are supported.

Harness settings shows saved setups, their files and revisions, defaults,
rename, rollback, and deletion. Import again to update a default setup. An
interactive `capsule create --harness NAME` notices changes in an existing local
default and offers the same reviewed import before launching.

Choose a Project and New Capsule, select a harness, and click Create Capsule.
The saved default applies automatically. Customize offers clean start, another
setup, a project default, and an optional name. New Capsules receive independent
copies. Updates and rollback apply to future Capsules. Existing session edits
remain local; after runtime recreation, Meridian reapplies the pinned original
revision. Repository files retain the harness's native precedence.

```console
meridian capsule create PROJECT_ID --harness codex
meridian capsule create PROJECT_ID scratch --harness codex --setup clean
```

## Reusable API connections

Enable the gateway on the daemon using an HTTPS origin reachable and trusted
from inside Capsules:

```console
meridiand --provider=docker --provider-gateway-url=https://meridian.example
```

This flag does not configure TLS or expose an extra listener. Your reverse
proxy must forward `/provider-gateway/` to Meridian, disable response buffering
for streaming, preserve Authorization and X-Api-Key, and avoid body/credential
logging. Permit the origin in Capsule egress policy
(`networkPolicy.capsuleEgressCIDRs` on Kubernetes). The chart already allows
Capsules to reach the in-namespace control-plane gateway on TCP 8080. Permit
the control plane to reach `api.openai.com:443` and `api.anthropic.com:443`
(`networkPolicy.providerEgressCIDRs`; NetworkPolicy is IP-based, so set the
hosts' routable CIDRs). Keep broader control-plane access restricted using
the existing installation authentication.

Add your own OpenAI or Anthropic API key under Provider connections in Harness
settings. Alternatively, pipe a key from your secret manager into:

```console
meridian harness connect openai --stdin
```

The key must come from stdin, never a plaintext command-line argument. On first
launch, choose the connection to authorize it for the Project and harness.
That grant is reused on future launches. Revoke a connection in Settings to stop
active gateway streams and future requests. Replacement keys retain the same
connection identity. Restart active sessions after a daemon restart or expiry
of the 24-hour Capsule lease.

OpenAI Responses and Chat Completions, and Anthropic Messages and token counting
are the supported gateway operations, for official and custom endpoints. Other provider operations and arbitrary
upstream endpoints are unavailable. When a provider rejects a request, the
harness sees the provider's HTTP status and a short reason taken from the
provider's error message, for example `upstream provider rejected the request
(HTTP 400): Unsupported tool type "web_search"`. Meridian keeps at most 300
characters of that message, removes control characters, and replaces URLs,
the upstream host, and anything that looks like a key with `[url]` or
`[redacted]`. Upstream headers and other response content are never passed
on.

### Self-hosted models (vLLM, Ollama, LiteLLM)

A custom endpoint connection sends a harness's inference requests to your own
OpenAI- or Anthropic-compatible server, such as vLLM on a DGX. Meridian's
control plane makes the upstream request. Capsules receive only the usual
gateway URL and a 24-hour lease, never your server's address or key. See
ADR 0027.

| Type | Use with | Upstream routes |
|---|---|---|
| `openai-compatible` | Codex, OpenCode, Pi, DeepSeek Harness | `/v1/responses` (Codex), `/v1/chat/completions` (OpenCode, Pi, DeepSeek Harness) |
| `anthropic-compatible` | Claude Code, DeepSeek Harness | `/v1/messages`, `/v1/messages/count_tokens` |

The control plane must be able to reach the server; Capsules do not need to.
The examples below serve GLM-5.3-Flash.

**1. Serve the model with tool calling enabled.** Coding harnesses need tool
calls, so vLLM must parse them. GLM-5.3-Flash uses the `glm47` tool and
reasoning parsers and needs vLLM 0.29.0 or later (currently the nightly
image). See the [vLLM recipe](https://recipes.vllm.ai/zai-org/GLM-5.3-Flash)
for sizing; the FP8 checkpoint needs about 306 GiB of GPU memory before KV
cache.

```console
vllm serve zai-org/GLM-5.3-Flash \
  --served-model-name glm-5.3-flash \
  --tensor-parallel-size 8 \
  --enable-auto-tool-choice --tool-call-parser glm47 \
  --reasoning-parser glm47 \
  --host 0.0.0.0 --port 8000 --api-key "$VLLM_KEY"
```

One vLLM server answers `/v1/responses`, `/v1/chat/completions`, and
`/v1/messages`. Point an `openai-compatible` connection and an
`anthropic-compatible` connection at the same base URL to use it from all
four harnesses. Omit `--api-key` (and use `--no-api-key` below) only if the
port is unreachable from anything except the Meridian host.

Ollama cannot serve GLM-5.3-Flash locally. Its library offers only
`glm-5.3-flash:cloud`, which forwards prompts to Ollama's hosted service,
and its local engine does not support the model's architecture. With models
Ollama can run locally, expose it with `OLLAMA_HOST=0.0.0.0:11434 ollama
serve` and use a library tag whose page lists the `tools` capability. Ollama
parses tool calls from the model's template, so it needs no tool-parser flag.
Raise the context with `OLLAMA_CONTEXT_LENGTH` (for example `65536`); the
small default truncates coding-agent prompts.

**2. Connect it.** Over HTTPS with a certificate the control plane trusts,
pipe the key from stdin:

```console
printf %s "$VLLM_KEY" | meridian harness connect openai-compatible \
  --name "DGX GLM" --base-url https://dgx.example.com \
  --model glm-5.3-flash --stdin
printf %s "$VLLM_KEY" | meridian harness connect anthropic-compatible \
  --name "DGX GLM (Claude Code)" --base-url https://dgx.example.com \
  --model glm-5.3-flash --stdin
```

For a plain `http://` server on your LAN or tailnet, the daemon operator must
allow it once, and each connection must opt in too:

```console
meridiand --provider=docker --provider-gateway-url=https://meridian.example \
  --provider-gateway-allow-private-http
printf %s "$VLLM_KEY" | meridian harness connect openai-compatible \
  --name "DGX GLM" --base-url http://192.168.1.20:8000 --allow-private-http \
  --model glm-5.3-flash --stdin
```

The `--model` value must match the server's model ID: vLLM's
`--served-model-name`, or the Ollama tag. In plain HTTP mode, Meridian
connects only to loopback, RFC 1918 (`10/8`, `172.16/12`, `192.168/16`), IPv6
ULA, and `100.64/10` addresses, after DNS resolution. Plain HTTP sends
prompts, repository content, and the key unencrypted between Meridian and the
server, so use it only on a network you trust. Prefer HTTPS, or a tunnel such
as a tailnet, whenever the path leaves the host. A trailing `/v1` on the base
URL is accepted. Redirects are not followed. Custom endpoints never use
link-local addresses such as cloud metadata services.

The same fields are under **Provider connections** in Harness settings:
choose *OpenAI-compatible endpoint* or *Anthropic-compatible endpoint*, then
enter the base URL, the default model, the optional key, and, when the daemon
allows it, the plain HTTP opt-in. Saving changes to a custom connection
replaces its stored key, so enter the key again. Revoking works as for API
keys.

**3. Authorize it on first launch** as for any other connection.

Harness behavior:

- **Codex** uses a `meridian` model provider with `wire_api = "responses"`,
  because Codex 0.157 accepts no other wire API. The default model becomes
  `model`. Codex has no metadata for non-OpenAI models, so it warns at start
  and assumes a 272k-token context window. If the server's context limit is
  smaller (vLLM `--max-model-len`), add `model_context_window` with that limit
  to your imported `~/.codex/config.toml`. Meridian keeps your other settings.
  Codex 0.157 also sends a built-in `web_search` tool and, for multi-agent
  work, a `namespace` tool. Neither is a function tool, and servers that accept
  only function tools reject the whole request; TabbyAPI does. For
  `openai-compatible` connections Meridian therefore writes
  `web_search = "disabled"` and `multi_agent = false` under `[features]`. If
  your imported config sets either key, your value is kept, so set
  `web_search = "live"` or `multi_agent = true` there if your server
  supports those tools. Official `openai` connections are unchanged.
- **OpenCode** and **Pi** need a default model. Meridian registers a
  `meridian` provider that uses Chat Completions and selects that model.
- **Claude Code** accepts only `anthropic-compatible`: vLLM's Messages API, or
  a proxy such as LiteLLM. With a default model, Meridian sets
  `ANTHROPIC_MODEL` and the default opus, sonnet, and haiku model variables
  to it. Meridian sends the key as both `X-Api-Key` and a bearer token,
  because vLLM accepts only the latter.
- **DeepSeek Harness** (`dsh`, Threads only) accepts either custom endpoint
  type with a default model, and no official connection. Choose the
  connection in the task form. Meridian declares a `meridian` route for that
  model, makes it the only model for sessions and subagents, and turns off
  DeepSeek's own adapters, web search, and session-log upload. See
  [harness adapters](harness-adapters.md#deepseek-harness).

Model quality and tool-calling support depend on your model and server.
Meridian does not translate between APIs.

Subscription sign-in remains inside the unmodified native harness and may need
repeating for new Capsules. Imported settings do not imply that all local tools
will work remotely: laptop endpoints, external binaries, authentication helpers,
and unrecognized settings appear in the exclusion report. Self-contained
skills, hooks, and extensions run inside the Capsule. Supported npx MCP tools
with exact package versions are listed in the import preview and installed
automatically inside the Capsule. Installation disables npm lifecycle scripts,
requires access to registry.npmjs.org, and times out after two minutes. Imported
tools then use the local npm cache offline. Unpinned packages are excluded;
other dependencies must exist in the image or use the harness's supported
installer. Top-level npm versions are pinned, but transitive versions may vary.

Back up the database together with its installation encryption key. Personal
configuration and provider keys are excluded from workspace Moments. Deleted
setups remain retained for pinned Capsules and are unavailable for new selection.

Project dependency preparation can also be reused independently of your personal
configuration. See [Prepared project environments](prepared-environments.md) for
progress, setup settings, rebuild, and retry behavior.

## Manage your saved setup

In Harness settings, Your setups appears before Import from this computer.
Choose **Edit files and skills** to edit configuration, remove a file, or add a
skill directly in Meridian. **Review changes** shows the sanitized files and
exclusions; **Save changes** creates a revision for future Capsules. Existing
Capsules keep their pinned setup. If another edit changed the setup while you
were editing, saving reports a conflict; close and reopen the editor to load the
latest version before reapplying your changes.

Supported browsers use a read-only folder-access picker and skip dependency
folders such as node_modules without scanning their contents. Other browsers
show their standard file-count confirmation before Meridian filters the files;
the importer explains that prompt in advance. No files are sent until review.
Use **Add skills folder** for a folder such as `~/.agents/skills`. Nested files
are mapped into the selected harness's skills folder. Duplicate destinations
must be resolved explicitly.
