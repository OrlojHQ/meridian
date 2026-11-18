# ADR 0055: Named Stations

- Status: Accepted
- Date: 2026-11-18
- Amends: ADR 0048 (desktop gateway indicator and recent gateways)

## Context

ADR 0048 kept at most five remote origins and showed them as host:port. The
only ways to switch were the launch picker and the tray. The name "Local" and
the setup-change checkbox sat on that first screen, and a Station page could
not see the other Stations the window can open.

## Decision

- The shell stores a name for the Station on this computer and a name plus
  origin for every remote Station the operator saves. There is no recent-list
  cap. A file that still has only `recent` origins is read once as named
  Stations, each named by its host, and rewritten in the new shape. Names and
  origins are re-validated on read. No token, cookie, or session is stored.
- An address without a scheme is `host` or `host:port` and is saved as HTTPS.
  An explicit `http://` URL is still accepted only for loopback, which is the
  rule `validate_gateway_url` already enforces. Userinfo, a query, and a
  fragment are still rejected.
- The Station on this computer can be renamed and cannot be removed. Removing
  a remote Station asks for confirmation first. An offline Station can still
  be opened. Status dots come from the existing readiness probe for the
  bundled Station and a short TCP connect for any other saved origin, off the
  UI thread.
- The launch screen is a welcome on first run ("Run Meridian on this Mac", or
  "this computer" elsewhere) and the saved list after that. The window title
  is "Meridian — <name>". The tray's Switch Station submenu uses those names.
- The active Station page may list saved names and origins and ask the shell
  to switch to one of them, through the desktop bridge. It cannot add, rename,
  or remove, and a switch id that is not already saved is refused. Add and
  Manage open the picker. Switching reloads the window. If the Thread composer
  has an unsent message, the page asks before it reloads. ⌘1–⌘9 (Ctrl+1–9
  elsewhere) switch the first nine Stations. Those chords do not overlap ⌘K,
  ⌘N, or the single-key shortcuts, and Ctrl+digit is left to a focused
  terminal. In a browser there is no switcher.
- "Check this computer for setup changes" moves to Harness settings. It stays
  on by default and still only prompts. Nothing is uploaded until Save.

## Consequences

The config file names every Station the operator saved, not only the last
five. Anyone who can read that file learns those hosts. A compromised Station
page learns the same list and can move the window among those Stations. It
cannot point the window at a new address. An older shell ignores `stations`
and `localName` and still reads `mode` and `origin`.
