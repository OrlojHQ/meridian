# ADR 0036: Keep the Claude login at the provider gateway

- Status: Accepted
- Date: 2026-11-10
- Extends: ADRs 0006, 0014, 0024, and 0027
- Amends: ADR 0024 (subscription login for Claude Code)

## Context

Claude Code accepts two non-interactive logins the official pack already
understands: an Anthropic API key, and a setup token from `claude setup-token`.
The provider gateway already proxies `api.anthropic.com` and stores an API key
as authenticated ciphertext. A Capsule receives only a gateway lease in
`ANTHROPIC_API_KEY` and `ANTHROPIC_BASE_URL`.

ADR 0024 left subscription login inside the harness. A setup token pasted into
a terminal, a file, or a `harness_env` secret would put that reusable
credential in the Capsule. Hostile repository code, a process listing, or a
home-directory snapshot could then keep it. Codex, OpenCode, Pi, and DeepSeek
Harness keep their existing credential paths; this decision is Claude only.

Meridian has no browser OAuth flow, and this decision does not add one.

## Decision

- The Claude login is the official `anthropic` provider connection. The
  operator sets, replaces, or clears it in the installation-authenticated UI
  or with `meridian harness connect anthropic`. The value is an API key or a
  setup token. It is read from a password field or stdin, never from a
  command-line argument.
- Store it as authenticated ciphertext under the installation credential KEK,
  the same envelope as other provider connections. The plaintext is not
  written to SQLite, artifacts, Moments, logs, events, or the API response.
  After save, the UI clears the field and does not show the value again.
  Clearing revokes the connection, replaces the ciphertext with an empty
  envelope, and cancels gateway leases.
- `credentialKind` is `api_key` (the default, stored as empty) or `oauth` for
  a setup token. It is not secret. Only `anthropic` accepts `oauth`. The oauth
  kind is part of the envelope's authenticated context, so editing stored
  metadata cannot change how the gateway attaches the token. Existing API-key
  envelopes keep their previous context and still open.
- On each leased `anthropic` request the gateway checks the lease, drops it,
  and attaches the login itself. An API key is `X-Api-Key`. A setup token is
  `Authorization: Bearer`, plus the `oauth-2025-04-20` `anthropic-beta` marker
  Claude Code already sends with that token. Caller cookies, authorization,
  and the lease are not forwarded. `anthropic-compatible` and every other
  provider keep their current header rules.
- The Capsule spec does not gain an environment variable, file, volume, image
  layer, or prompt for the login. `capsuled` still gives Claude Code the
  gateway URL and the lease, and only those. Codex, OpenCode, Pi, and
  DeepSeek Harness injection is unchanged.
- Container isolation is the selected provider's boundary. This decision does
  not claim that Docker or any other container runtime supports untrusted
  multi-tenancy, and it does not expose a host Docker socket or runtime API
  to a Capsule.

## Consequences

A person who can use the installation can spend the Claude login until it is
cleared or the lease expires. Compromise of the control plane and the
credential KEK reveals the login. Losing the KEK makes stored logins
unrecoverable.

A setup token is a reusable Anthropic credential with the scope Claude Code
already grants it. Keeping it on the control plane removes it from the
Capsule; it does not reduce what a stolen control plane can do with it.

Codex subscription login is out of scope and stays on its current path.
