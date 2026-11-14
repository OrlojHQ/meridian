# ADR 0048: Desktop gateway indicator and recent gateways

- Status: Accepted
- Date: 2026-11-14
- Amends: ADR 0043 (desktop shell)

## Context

The desktop shell (ADR 0043) remembers one choice: the bundled-local gateway
or one remote origin. Choosing "This computer" forgets the remote address, so
switching back means typing it again. Nothing in the window says which
gateway it is on: the title is always "Meridian" and the status bar says only
"Connected". Once team mode (ADR 0046) exists, an operator can have a personal
local installation and a team installation open at different times. Creating
a Project or a secret on the wrong one is easy, and the result is hard to
undo.

## Decision

- The shell keeps up to five recently used remote gateway origins, most
  recent first, in the same `gateway.json` (mode 0600, in a 0700 directory).
  It stores origins only, each re-validated on read with the same rules as a
  typed address. A tampered file cannot add a plain-HTTP remote, userinfo, a
  query, or a fragment. No token, cookie, session, or member name is stored.
- The picker lists them as one-click choices with a Forget control. The tray
  has a **Switch gateway** submenu with "This computer" and each recent
  gateway, and the active one is checked. A tray switch acts only on an origin
  still in the list that still validates. Switching runs the same connect
  path as the picker, so ADR 0043's bridge rule is unchanged: a capability
  granted to an earlier gateway stays registered but inert.
- The window title is "Meridian — <gateway>". That is "This computer" for the
  bundled-local choice, or the remote's host and any non-default port. It
  returns to "Meridian" at the picker.
- A new bridge command, `desktop_gateway`, returns that label and whether the
  gateway is the bundled-local one. Like every bridge command, only the active
  gateway's origin can call it. It tells the page nothing the page could not
  already infer from its own origin. The recent list and the Forget command
  stay in the picker's local-only capability. A gateway page cannot learn
  which other gateways the operator uses.
- The web UI shows the label in the status bar when the bridge offers it,
  plus the signed-in member's name in team mode. Clicking it opens the
  picker. In a browser it renders nothing, because the address bar already
  names the gateway.

## Consequences

Switching between a personal and a team gateway takes one click from the
tray or the picker, and the window always says which one is open. The config
file now names up to five gateway hosts the operator used. That is a small
local disclosure to anyone who can read the operator's files, and it is
removable with Forget. An older shell reading the new file ignores the extra
field. A gateway UI running in an older shell sees no `gateway` bridge
function and shows no label.
