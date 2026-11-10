# ADR 0036: Keep the Claude login at the provider gateway

- Status: Accepted
- Date: 2026-11-10
- Extends: ADRs 0006, 0014, 0024, and 0027
- Amends: ADR 0024 (subscription login for Claude Code)

## Context

Claude Code accepts an Anthropic API key or an OAuth login. The OAuth login
is the one `claude` opens in a browser: Claude Code's public client, PKCE, and
a loopback redirect. `claude setup-token` is a narrower fallback that prints a
token the operator would otherwise paste. The provider gateway already proxies
`api.anthropic.com` and stores a credential as authenticated ciphertext. A
Capsule receives only a gateway lease in `ANTHROPIC_API_KEY` and
`ANTHROPIC_BASE_URL`.

ADR 0024 left subscription login inside the harness. A token in a terminal, a
file, or a `harness_env` secret would put that reusable credential in the
Capsule. Hostile repository code, a process listing, or a home-directory
snapshot could then keep it. Codex, OpenCode, Pi, and DeepSeek Harness keep
their existing credential paths; this decision is Claude only.

The operator signs in from Meridian's settings page. Meridian runs Claude
Code's OAuth client. The operator does not run `claude setup-token` and does
not paste a token that was minted somewhere else.

## Decision

- The Claude login is the official `anthropic` provider connection. The
  primary way to set or replace it is "Sign in with Claude" in Harness
  settings.
- Sign-in is Claude Code's OAuth flow, run by the daemon. Meridian uses
  Claude Code's public client id, the authorize URL `https://claude.ai/oauth/authorize`,
  and the token URL `https://platform.claude.com/v1/oauth/token`. The scopes
  are the ones Claude Code's own account login carries, including
  `user:sessions:claude_code`. The PKCE verifier and `state` are random, held
  only in daemon memory, and discarded after one exchange or ten minutes.
  They are not written to SQLite, logs, or the API response.
- When the operator's browser is on a loopback host, the authorize redirect
  is `http://localhost:<that port>/callback`, the same loopback redirect
  Claude Code uses for its own login. `GET /callback` is unauthenticated.
  The single-use `state` plus the in-memory verifier are what authorize the
  exchange. The callback response is a fixed HTML page that does not echo
  the code or the tokens, and it replaces the address so the code does not
  stay in the popup's location. The daemon chooses that redirect. The
  client cannot supply one.
- When the browser cannot return to loopback, the same ceremony uses
  Claude's code-display page (`code=true`,
  `https://platform.claude.com/oauth/code/callback`). The settings page
  accepts the authorization code that page shows (`code` or `code#state`).
  Meridian checks `state`, exchanges the code, and does not store the code.
  That is the fallback, not the primary path.
- Pasting an API key, or a setup token from `claude setup-token`, remains
  available in the same settings form and on `meridian harness connect
  anthropic`. It is a fallback for an operator who already has one of those
  credentials. The value is read from a password field or stdin, never from
  a command-line argument.
- The token endpoint is fixed to `https://platform.claude.com/v1/oauth/token`.
  The client does not follow redirects. A non-success body is discarded and
  never copied into an error string. Tests may point the client at an HTTP
  loopback server; that override is not an operator setting.
- Store the result as authenticated ciphertext under the installation
  credential KEK, the same envelope as other provider connections. An in-UI
  login stores the access token, the refresh token, and the expiry. A pasted
  setup token stays a single opaque string. The plaintext is not written to
  SQLite, artifacts, Moments, logs, events, or the API response. After save,
  the UI does not show the value. Clearing revokes the connection, replaces
  the ciphertext with an empty envelope, and cancels gateway leases.
- `credentialKind` is `api_key` (the default, stored as empty) or `oauth`
  for an in-UI login or a setup token. It is not secret. Only `anthropic`
  accepts `oauth`. The oauth kind is part of the envelope's authenticated
  context, so editing stored metadata cannot change how the gateway attaches
  the token. Existing API-key envelopes keep their previous context and
  still open.
- Before a leased `anthropic` request, the daemon opens the ciphertext. A
  stored OAuth record whose access token is within a minute of expiry is
  refreshed and sealed again. The refresh token stays inside that
  ciphertext. The gateway then receives only the current access token.
- On each leased `anthropic` request the gateway checks the lease, drops it,
  and attaches the login itself. An API key is `X-Api-Key`. An OAuth access
  token or a setup token is `Authorization: Bearer`, plus the
  `oauth-2025-04-20` `anthropic-beta` marker Claude Code already sends with
  that token. Caller cookies, authorization, and the lease are not
  forwarded. `anthropic-compatible` and every other provider keep their
  current header rules.
- The Capsule spec does not gain an environment variable, file, volume,
  image layer, or prompt for the login. `capsuled` still gives Claude Code
  the gateway URL and the lease, and only those. Codex, OpenCode, Pi, and
  DeepSeek Harness injection is unchanged.
- Container isolation is the selected provider's boundary. This decision
  does not claim that Docker or any other container runtime supports
  untrusted multi-tenancy, and it does not expose a host Docker socket or
  runtime API to a Capsule.

## Consequences

A person who can use the installation can spend the Claude login until it is
cleared, the refresh token is revoked, or the lease expires. Compromise of
the control plane and the credential KEK reveals the login, including the
refresh token. Losing the KEK makes stored logins unrecoverable.

The control plane is an OAuth client of `claude.ai` and `platform.claude.com`.
A stolen authorization code is not enough to mint a token: the verifier never
leaves the daemon. A loopback callback is reachable by any local process for
the few seconds the redirect is in flight; the code is single-use and is
exchanged immediately.

An OAuth login is a reusable Anthropic credential with the scopes Claude
Code's own login carries. Keeping it on the control plane removes it from
the Capsule; it does not reduce what a stolen control plane can do with it.
The daemon refreshes the access token while the refresh token remains valid,
so the operator does not repeat sign-in on the access token's short lifetime.

Codex plan sign-in is a separate decision (ADR 0038). This decision does not
change Codex, OpenCode, Pi, or DeepSeek Harness API-key setup.

The loopback redirect assumes the browser that signs in can open the
daemon's localhost port. That is the default install. A browser that cannot
is the code-entry fallback above, still inside Meridian, still without
`claude setup-token`.
