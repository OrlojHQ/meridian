# ADR 0027: Custom endpoint provider connections

- Status: Accepted
- Date: 2026-11-07
- Extends: ADRs 0024 and 0026

## Context

ADR 0024 lets Capsules use a provider API key through a scoped gateway lease.
It only supported the official OpenAI and Anthropic APIs. Users also run their
own models, such as vLLM or Ollama on a GPU host or a LiteLLM proxy. These
servers have OpenAI- or Anthropic-compatible APIs, often on a private network
and sometimes with no API key at all. Capsules must still never learn the
upstream address or key, and must not be able to choose an upstream.

## Decision

### Two custom connection types

- `openai-compatible` exposes the same gateway routes as `openai`:
  `POST /v1/responses` and `POST /v1/chat/completions`.
- `anthropic-compatible` exposes the same routes as `anthropic`:
  `POST /v1/messages` and `POST /v1/messages/count_tokens`.
- A custom connection has a required `baseUrl`, an optional API key and an
  optional `defaultModel`. Without a key, the gateway sends no upstream
  credential. With one, it sends `Authorization: Bearer` for
  `openai-compatible`. For `anthropic-compatible` it sends both `X-Api-Key`
  and `Authorization: Bearer`, because vLLM's `--api-key` check accepts
  only bearer tokens, even on its Anthropic Messages routes. Official
  Anthropic still receives only `X-Api-Key`.
- Official connections keep fixed upstreams and reject `baseUrl`,
  `defaultModel`, and `allowPrivateHttp`.

### The base URL lives on the connection

The base URL is set on the provider connection through the
installation-authenticated API, UI, or `meridian harness connect`, not in
daemon configuration. Under ADR 0015 that principal is already the operator.
A connection is already the unit of encryption, grants, and revocation, so an
endpoint and its key stay together. More than one endpoint works without a
daemon restart. The daemon keeps one installation-wide policy switch for
plaintext HTTP (below).

- The Capsule receives only the gateway URL
  (`<gateway>/provider-gateway/<type>`), its 24-hour lease, the type, and the
  model name. The gateway reads the base URL from the store on every request.
  The request path must name the leased type. Query strings are rejected and
  client headers never select an upstream, so a Capsule cannot choose or
  influence the upstream.
- The base URL and the private HTTP flag are part of the key envelope's
  authenticated context. If someone edits the stored metadata to point at
  another host, decryption fails and the key is not released. Changing the
  base URL means sending the key again, so a saved key is never sent to a new
  destination without being re-entered.
- Validation accepts `https://` and, with the opt-in, `http://`. It rejects
  credentials, queries, fragments, encoded or dot path segments, and invalid
  ports. A trailing `/v1` is stripped.

### Plaintext HTTP is a double opt-in

`http://` needs both `meridiand --provider-gateway-allow-private-http` and
`allowPrivateHttp` on the connection. Such a connection uses a transport that
only dials loopback, RFC 1918, IPv6 ULA, or RFC 6598 (CGNAT, used by
tailnets) addresses. The check runs on the address actually being dialed, so
DNS rebinding cannot move it to a public host. If the daemon flag is removed
later, existing HTTP connections fail closed.

### Address and redirect limits

Every custom transport refuses to dial unspecified, link-local (including
cloud metadata at `169.254.169.254`), and multicast addresses. HTTPS custom
endpoints may otherwise use public or private addresses and the host's system
trust store. Redirects are never followed: a 3xx becomes a 502 and its
`Location` is dropped. The existing request-size, response-size, per-lease,
and gateway concurrency limits and the 10-minute deadline still apply. Custom
endpoints wait up to five minutes for response headers, because self-hosted
servers may load a model first. Proxy environment variables are ignored.

### Upstream error reasons

A self-hosted server's error message is often the only clue to a
configuration mismatch. For a 4xx or 5xx upstream response, the gateway
returns that status (any other non-2xx, non-3xx status becomes 502),
`error.upstreamStatus`, and the message `upstream provider rejected the
request (HTTP <status>): <reason>`. The reason comes from one JSON error
string or a `text/plain` body, read from at most 16 KiB. It has control and
format characters removed, the upstream key, lease, and upstream hostname
redacted, URLs and credential-shaped strings replaced, and is cut to 300
characters. Upstream headers are never copied. This applies to official
connections too. The threat model gives the reasoning.

### Harness pairing

`harnesssetup.ConnectionSupported` is checked at grant time, when the lease is
issued, and in `capsuled`:

| Harness | Accepted types |
|---|---|
| Claude Code | `anthropic`, `anthropic-compatible` |
| Codex | `openai`, `openai-compatible` |
| OpenCode, Pi | `openai`, `anthropic`, and `openai-compatible` with a default model |

- Codex: `capsuled` writes the `meridian` model provider with
  `wire_api = "responses"`, the only wire API that Codex 0.157 accepts, and
  sets `model` when one is configured. Codex 0.157 adds a `web_search` tool
  and, through the default-on `multi_agent` feature, a `namespace` tool to
  every request. Servers that accept only `function` tools, such as
  TabbyAPI, reject those requests. For custom connections only, `capsuled`
  writes `web_search = "disabled"` and `features.multi_agent = false` unless
  the imported config sets those keys.
- OpenCode: a `meridian` provider using `@ai-sdk/openai-compatible`
  (Chat Completions), the model as its only model entry, and
  `model = "meridian/<model>"`.
- Pi: a `meridian` provider in `models.json` with `api: openai-completions`
  and `apiKey: "$MERIDIAN_PROVIDER_TOKEN"`, plus `defaultProvider` and
  `defaultModel` in `settings.json`. The official Pi path now also uses
  `$NAME` interpolation, which Pi 0.87 requires; previously the variable
  name was sent as a literal key.
- Claude Code: `ANTHROPIC_BASE_URL` and the lease as before, plus
  `ANTHROPIC_MODEL` and the opus, sonnet, and haiku default model variables
  when a model is configured, so background requests also use a served model.

The lease still reaches the harness only through the process environment.

## Consequences

Users can run official harnesses against their own models without exposing
the server to Capsules. Meridian does not translate between protocols. The
upstream must implement the routes its harness uses (Codex needs
`/v1/responses`), and Meridian does not vouch for model quality or
tool-calling support.

A custom base URL makes the control plane an HTTP client of an
operator-chosen host. The operator can point it at internal services, which
then receive Capsule-authored POST bodies on the fixed paths above. This is
accepted for the single-principal trust model and limited by the address
rules above, not removed. Plaintext HTTP exposes prompts, repository
content, and any upstream key to anyone on the network path between the
control plane and the model server. It is off by default and documented as
suitable only for trusted LAN or tailnet segments.
