# ADR 0036: ChatGPT plan connections

- Status: Proposed
- Date: 2026-11-09
- Amends: ADR 0024
- Extends: ADRs 0026 and 0027

## Context

ADR 0024 left subscription sign-in to each native harness. A ChatGPT login
therefore lives in `~/.codex/auth.json` inside one Capsule and is lost with
it. Every new Capsule shows Codex's sign-in screen, and a structured Thread
cannot sign in at all, so a Codex Thread in a fresh Capsule fails on its
first turn. API-key provider connections avoid this but bill API usage, not
the user's ChatGPT plan.

Copying a login cache into Capsules is not acceptable. A ChatGPT refresh
token is a long-lived credential, Capsules are hostile (threat model), and
reusable credential plaintext must stay out of them.

OpenAI now publishes a supported route for this case: Sign in with ChatGPT
plan usage for open-source and locally or self-hosted apps
(<https://developers.openai.com/siwc/token-sharing-open-source>). An app
registers a per-host OAuth client, keeps the resulting tokens in protected
local or self-hosted storage, and calls the public Responses API at
`https://api.openai.com/v1/responses` with the user's access token. OpenAI's
documented Codex integration runs `codex app-server` with a custom
`wire_api = "responses"` provider whose `env_key` holds that token. That is
the shape Meridian already uses for API-key connections, with the gateway
lease standing in for the upstream credential.

Anthropic's consumer terms reserve Claude Free, Pro, and Max OAuth tokens
for Claude Code and Claude.ai and forbid third parties from routing requests
through them. Meridian therefore does not broker Claude subscription logins.

## Decision

### A `chatgpt` connection type

`chatgpt` is a new provider connection type alongside `openai`, `anthropic`,
and the ADR 0027 custom types.

- It uses the OpenAI wire family and the fixed upstream
  `https://api.openai.com`. It rejects `baseUrl`, `defaultModel`,
  `allowPrivateHttp`, and an API key.
- The gateway exposes only `POST /v1/responses` for it. Chat Completions is
  not offered.
- It pairs only with the Codex harness, which is OpenAI's documented client
  for this route and already sets `store: false` and `stream: true`. Other
  harnesses can be added once they are validated against OpenAI's preview
  limitations.
- `capsuled` writes the same `meridian` model provider it writes for an
  `openai` connection (`wire_api = "responses"`,
  `env_key = "MERIDIAN_PROVIDER_TOKEN"`). The Capsule receives the gateway
  URL and its 24-hour lease, never a ChatGPT token.

### Sign-in is owned by the daemon

The daemon runs the authorization-code-with-PKCE flow from OpenAI's
registration guide. Only the installation-authenticated principal (ADR 0015)
can start or complete it.

- The daemon generates `state`, `nonce`, and the PKCE verifier, keeps them
  in memory for one attempt with a ten-minute expiry, and returns only the
  authorization URL. First registration uses `client_id=dynamic_agent_client`
  and `agent_name_hint=Meridian`. Reauthorization uses the issued client ID,
  the installation host ID, and the account email as `login_hint`. The ID
  token is verified and then discarded, so it is never stored or sent as
  `id_token_hint`.
- The redirect URI is `http://127.0.0.1:<port>/auth/callback`, as OpenAI
  requires. The callback reaches the machine running the browser, so it is
  delivered in one of three ways:
  - **Local daemon:** the daemon listens on that loopback port only for the
    duration of the attempt.
  - **CLI:** `meridian harness connect chatgpt` listens on the laptop's
    loopback port and relays `code`, `state`, `scope`, and `client_id` to the
    daemon over the authenticated API.
  - **Remote daemon from the web UI:** the user pastes the redirected URL,
    which their browser shows as an unreachable page.
- The daemon checks `state`, exchanges the code with its own verifier,
  verifies the ID token against OpenAI's JWKS (issuer, audience equal to the
  issued client ID, expiry, nonce), and requires the
  `chatgpt.tokens.use.direct` scope. A relayed or pasted code is useless
  without the verifier, which never leaves the daemon.
- Tokens are never sent to the browser or CLI and never appear in URLs,
  logs, events, or API responses. The UI shows the account email taken from
  the validated ID token and the connection state.

### Host identity

Each installation has one OAuth host. On first use the daemon creates a
random `urn:uuid:` `ext_agent_host_id` and keeps it in the data directory.
It is not a credential and is not derived from user identity. Capsules are
runtimes of that host, not separate hosts. Signing out or switching accounts
keeps the host ID and the issued client ID.

### Token storage and refresh

- The token set (issued client ID, access token, refresh token, expiry,
  granted scopes, account subject, and email) is sealed with the existing
  credential-KEK connection envelope. The envelope context binds the
  connection ID, the type, the revoked flag, and a credential generation
  counter.
- The control plane refreshes the access token shortly before it expires,
  or once after an upstream 401. Refreshes are single-flight per connection,
  use the issued client ID and `resource=https://api.openai.com/v1`, and
  store the rotated refresh token in the same transaction that bumps the
  generation counter. Rotation does not change the connection's resource
  version, so it does not conflict with user edits.
- If a refresh returns `invalid_grant`, the connection is marked
  `needsSignIn`. Gateway requests then fail with HTTP 401 and a reason that
  tells the user to sign in to ChatGPT again. Active leases stay valid and
  resume working after reauthorization. A transient refresh failure keeps
  using an access token that has not yet expired.
- Through the connection API a ChatGPT connection accepts only rename and
  revocation. It cannot be created there, and no key can replace its token
  set.

### Gateway enforcement

For `chatgpt` the gateway reads the bounded request body (8 MiB, as today)
before forwarding it. It rejects a request unless `stream` is `true` and
`store` is `false`, and it rejects `previous_response_id` and `background`.
These are OpenAI's preview requirements. Enforcing them keeps a hostile
Capsule from turning the lease into general Responses API access outside
the Codex session shape. Other OpenAI limitations are left to upstream
validation, whose error reasons the gateway already relays (ADR 0027).
The body is forwarded unmodified.

### Disconnect and revocation

Revoking a `chatgpt` connection first replaces its token set with an empty
envelope and cancels its leases, then revokes the refresh token at the
`revocation_endpoint` from OpenAI's OpenID configuration using the issued
client ID. Revocation is tried three times with backoff, within 20 seconds.
If OpenAI does not confirm, the connection is marked
`revocationUnconfirmed` and the UI points the user to ChatGPT's settings.

### Terms-of-use limits

OpenAI's terms require persistent token storage to be local and under the
user's control, requests to come from a runtime only that user controls, and
plan usage not to be pooled, shared, or resold.

- A `chatgpt` connection is offered only on a single-principal installation,
  which every Meridian installation is (ADR 0015). Documentation states that
  the installation must be controlled by the ChatGPT account holder alone,
  that one login must not be shared between people, and that a paid or
  hosted Meridian offering needs OpenAI's commercial approval.
- Meridian cannot technically prove who controls an installation. These are
  documented operator obligations, not enforced guarantees.

### Prerequisite

ChatGPT plan connections use the provider gateway, which today requires
`--provider-gateway-url`. Enabling the gateway by default on local Docker
installations is a separate decision and does not block this one.

## Consequences

- One sign-in per installation replaces one sign-in per Capsule for Codex,
  for both terminal sessions and structured Threads, and the token never
  enters a Capsule.
- Compromised Capsule code can spend the user's ChatGPT plan usage through
  its lease until the lease expires or the connection is revoked. This is
  the same exposure API-key connections already accept.
- Compromise of both the daemon and the credential KEK reveals a refresh
  token scoped to plan inference for that account, until the user revokes
  it in ChatGPT.
- The gateway now parses `chatgpt` request bodies. Parsing is bounded and
  only reads the `stream`, `store`, `previous_response_id`, and `background`
  fields.
- Native Codex sign-in inside a Capsule still works and remains
  Capsule-local. Claude Code subscription sign-in is unchanged and stays
  per-Capsule.
- OpenAI marks this route as a preview. Its parameters, scopes, or terms may
  change and need tracking.

## Open questions

- Whether first registration accepts a loopback port other than 1455.
  Codex CLI's own login uses 1455, so a fixed port conflicts with
  `codex login` running on the same machine. When the port is busy, the
  daemon and CLI fall back to a pasted redirect address.
- Whether to list the account's models from `GET /v1/models`
  (`visibility == "list"`) for the model picker, instead of Codex's bundled
  catalog.
