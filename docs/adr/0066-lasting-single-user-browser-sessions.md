# ADR 0066: Lasting single-user browser sessions

- Status: Accepted
- Date: 2026-11-27
- Amends: ADR 0015 (single-principal API authentication) and ADR 0043
  (desktop shell)

## Context

In single-user mode a browser, or the desktop shell's webview, on a remote
Station exchanges the installation bearer once at
`POST /auth/browser-session` for an HttpOnly cookie (ADR 0015, ADR 0043).
That cookie was a random value made when `meridiand` started, kept only in
its memory, and set with no `Max-Age`. It ended in two common cases:

- **The client quit.** A cookie without an expiry is a browser-session
  cookie. WebKit drops it when the desktop app quits, including when an
  update restarts it.
- **The daemon restarted.** A new process made a new value, so every cookie
  issued before it stopped matching. Updating a Station restarts it.

An operator who moves between a few remote Stations, or updates either side,
had to fetch the installation token from the Station host and paste it again
each time. That pushes people to keep the token somewhere handy, such as a
note or the clipboard, which is worse than any cookie.

Options considered:

- **The shell keeps the token in the OS keychain and signs in on its own.**
  ADR 0043 says the shell stores no installation token. This would keep a
  credential that never expires at rest, outside the Station's control, and
  make the shell a custodian of every Station's full authority.
- **A server-side session table.** Each session could be listed and revoked
  on its own. Single-user mode has one principal and no session store. Team
  mode already has one for members (ADR 0046); a second for the bootstrap
  owner adds a migration and state for little gain, since there is no other
  user to revoke from.
- **A signed expiry.** Stateless, survives restarts, and is revoked for every
  client at once by rotating the installation token. Chosen.

## Decision

- The single-user cookie value is `<expiry unix seconds>.<signature>`. The
  signature is HMAC-SHA-256 over a fixed label and the expiry text. The key
  is HMAC-SHA-256 of the installation token's SHA-256 digest with that same
  label. The daemon already holds only that digest. The cookie never
  contains the token, and the key is never written anywhere.
- A session lasts seven days. The cookie is set with `Max-Age` of seven days,
  so quitting the client does not end it. Its other attributes are
  unchanged: `HttpOnly`, `SameSite=Strict`, `Path=/`, and `Secure` unless the
  request is verified loopback.
- A request the cookie authenticates gets a fresh cookie once the session is
  more than a day old. A session therefore ends after seven days without use.
- The daemon rejects a value that is longer than 128 bytes, whose expiry is
  not plain decimal, whose signature does not match in constant time, that
  has expired, or whose expiry is further ahead than one lifetime.
- A restarted daemon with the same installation token accepts sessions an
  earlier process issued. A new token derives a new key, so rotating the
  token ends every browser session.
- Team mode is unchanged. Member sessions stay in the identity store with
  their own expiry, and the installation cookie does not authenticate there.
- A daemon built without an installation token (tests only) signs with a
  random key for the life of the process, as before.

## Consequences

Entering the token once per Station per week of disuse is enough. Quitting
or updating the desktop app, switching Stations, and updating a Station no
longer sign the operator out. Cookies issued before this change were random
values and stop working once, on the first restart into it.

The webview's or browser's cookie store now holds, at rest, a credential with
the bootstrap owner's full authority for up to seven days after last use.
Before, that credential lived in memory and only while the client ran. A
process running as the operator could already read the installation token
file on a loopback Station, or the in-memory cookie, so this widens exposure
mainly for a copied disk or profile, and for a client on a different machine
from the Station. A single session cannot be revoked on its own; rotating the
installation token revokes all of them. The daemon stores nothing new.

Cookies are scoped by host, not port. Two Stations on the same host name,
such as two ports on one machine or two tunnels on `localhost`, share the
`meridian_session` cookie, and opening one still signs the other out.
