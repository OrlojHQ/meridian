# ADR 0055: Named Stations

- Status: Accepted; implemented in v0.12.0
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
  be opened. Status dots in the picker come from the existing readiness
  probe for the bundled Station and a short TCP connect for any other saved
  origin, off the UI thread. One check runs at a time, with a fixed pool of
  workers, a bounded wait for each name lookup, and an overall time limit; a
  Station the check cannot decide in time shows no dot.
- The launch screen is a welcome on first run ("Run Meridian on this Mac", or
  "this computer" elsewhere) and the saved list after that. The window title
  is "Meridian — <name>". The tray's Switch Station submenu uses those names.
- A Station page never receives the saved list. A Station may be run by
  someone else, and the list names the operator's other hosts. The page
  shows only the open Station's own name, which the shell already gives it.
  Its sidebar menu and command palette open the shell's picker, the only
  place Stations are listed, checked for status, added, renamed, or removed.
  The bridge has no command that returns names, origins, or status.
- ⌘1–⌘9 (Ctrl+1–9 elsewhere) switch to the first nine saved Stations. The
  page sends only the slot number. The shell resolves it: 1 is this
  computer, then saved Stations in the picker's order. An empty slot, a slot
  past nine, or the Station already open does nothing. Those chords do not
  overlap ⌘K, ⌘N, or the single-key shortcuts, and Ctrl+digit is left to a
  focused terminal. Switching reloads the window. If the Thread composer has
  an unsent message, the page asks before a switch or before it opens the
  picker. In a browser there is no switcher.
- "Check this computer for setup changes" moves to Harness settings. It stays
  on by default and still only prompts. Nothing is uploaded until Save.

## Consequences

The config file names every Station the operator saved, not only the last
five. Anyone who can read that file learns those hosts. A Station page does
not learn them. A compromised Station page can still move the window to one
of the first nine saved Stations or open the picker, and it may infer how
many Stations are saved by trying slots. It cannot read their names or
addresses, and it cannot point the window at a new address. An older shell ignores `stations`
and `localName` and still reads `mode` and `origin`.
