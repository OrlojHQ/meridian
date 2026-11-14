# ADR 0047: Desktop sign-in through the system browser

- Status: Accepted
- Date: 2026-11-14
- Amends: ADR 0043 (desktop shell) and ADR 0046 (per-user identity on a team
  installation)

## Context

ADR 0046 says the desktop app uses the member's session cookie in its
webview. The shell (ADR 0043) lets the webview load only the chosen
gateway's origin and its own pages, and hands every other URL to the system
browser. So `/auth/oidc/start` redirects to the issuer, the shell opens the
issuer in the system browser, and the session or claim cookie lands in that
browser, never in the webview. A team-mode gateway cannot be used from the
desktop app at all.

Two designs were considered.

**A. Let the webview load the issuer during a sign-in.** The shell would
allow the issuer's origin while a sign-in started from the gateway is in
progress. This was rejected:

- RFC 8252 (OAuth 2.0 for Native Apps, BCP 212) says native apps must not
  use embedded user-agents for authorization requests: the app can read
  what the member types and the member cannot see the address. Some issuers
  refuse embedded webviews outright.
- Issuers chain origins. Dex sends the member on to an upstream connector,
  an enterprise IdP sends them to SAML and MFA providers. The issuer's
  origin alone would not complete a real sign-in, and an allowlist the
  gateway supplies would widen what the webview loads based on remote input.
- The webview has no address bar, no password manager, and no existing IdP
  session, and passkeys work poorly in it. A phishing page in the window
  would look like the real thing.

**B. Sign in in the system browser and hand the result to the webview.**
The member signs in where they always do, and the gateway gives the shell a
one-time ticket that the webview redeems for its own cookie. The open
questions are how the ticket reaches the shell, and how to stop a ticket
from reaching anyone else.

## Decision

Use B, with the loopback redirect of RFC 8252 section 7.3 and a
PKCE-style verifier.

- When the active gateway's page navigates to `/auth/oidc/start`, the shell
  cancels that navigation. It binds a listener on `127.0.0.1` with an
  ephemeral port, generates a random 32-byte `state` and `verifier`, and
  opens `<gateway>/auth/oidc/start?desktopPort=…&desktopState=…&desktopChallenge=…`
  in the system browser. The challenge is the base64url SHA-256 of the
  verifier. The verifier stays in the shell's memory.
- The gateway records the three values with the sign-in attempt. It accepts
  only a port in 1024–65535 and 43-character base64url values, and all
  three or none. After the issuer verifies the identity, the callback sets
  no cookie in that browser. It stores the verified identity under a
  single-use ticket (32 random bytes, kept only as a digest, for two
  minutes) and redirects to
  `http://127.0.0.1:<port>/meridian-sign-in?state=…&ticket=…`. The host is
  always the IPv4 loopback literal, and the path is fixed.
- The listener answers any request that is not that path with exactly one
  matching `state` and one well-formed ticket with a fixed "not a Meridian
  sign-in" page, and keeps waiting. The matching request gets a fixed
  "return to Meridian" page. The listener waits at most ten minutes and gives
  up early when the operator switches gateways, the shell connects again, a
  newer sign-in starts, or the app quits.
- The shell then runs a fixed script in the webview, only while the active
  gateway is the one the sign-in began on and the webview's page is on that
  origin. The script checks the origin again and POSTs the ticket and
  verifier in a JSON body to `POST /auth/desktop/redeem`. It then follows the
  `/ui/` path the gateway returns, or `/ui/` on failure.
- Redemption needs a same-origin `Origin` header. It consumes the ticket on
  any attempt, so a wrong verifier cannot be retried. It checks the
  verifier against the challenge in constant time and only then decides
  what to grant. That is the same as the browser callback: a claim cookie
  before the claim, or a member session after it. A member disabled after
  the callback is refused. The cookie is set on the webview's own
  same-origin request, so it lands in the webview, and the shell still
  stores no credential.
- Rebind recovery (ADR 0046) stays a browser ceremony. A desktop attempt
  never carries a rebind.

## Consequences

The webview still loads only the active gateway and the shell's pages. The
member signs in with their own browser, its address bar, password manager,
and existing issuer session. The handoff adds:

- a loopback listener that lives only during a sign-in and accepts nothing
  but a redirect carrying its own state;
- an unauthenticated `POST /auth/desktop/redeem` whose credential is the
  ticket plus the verifier;
- query parameters on `/auth/oidc/start` that only change where the callback
  sends the result.

Threats this design addresses:

- **Ticket in browser history or extensions.** The redirect URL carrying
  the ticket is visible to the system browser. Without the verifier, which
  never leaves the shell before redemption, the ticket redeems nothing.
- **Phished start link.** An attacker who sends a member a start URL with
  the attacker's parameters gets nothing: the member's browser delivers the
  ticket to the member's own loopback address, where the attacker is not
  listening. This is unlike the device grant (ADR 0046), which a remote
  attacker can phish.
- **Login CSRF into the desktop.** A web page that probes loopback ports and
  posts an attacker's ticket to the listener lacks the listener's state. If
  it guessed the state, redemption would still fail on the verifier.
- **Ticket sent to the wrong gateway.** The shell runs the redeem script
  only while the sign-in's gateway is active and loaded, and the script
  checks the page's origin itself, so a gateway switch cannot hand one
  gateway's ticket to another.

It does not address:

- **A malicious process running as the same user.** It can read the
  shell's memory, or the webview's cookie store, whether or not this flow
  exists. Another local user can connect to the listener but cannot supply
  its state or the verifier. At worst they make it answer requests until it
  times out.
- **The gateway itself.** It mints the session anyway. The redeem script
  runs in the gateway's page, so a compromised gateway UI could redeem the
  ticket itself, into the same webview.

While the shell waits, the window keeps the page it was on. A failed
redemption returns to `/ui/`, which shows sign-in again. An older shell
loads `/auth/oidc/start` the old way, and the member ends up signed in only
in the browser. A newer shell against a gateway without this ADR gets the
same result, because such a gateway ignores the parameters. The shell then
waits until its timeout.
