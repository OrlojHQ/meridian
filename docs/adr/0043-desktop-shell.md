# ADR 0043: Desktop shell around the existing web UI

- Status: Proposed
- Date: 2026-11-12

## Context

The review UI is one React bundle, embedded in `meridiand` and served under
`/ui/` (ADR 0008). A desktop window is useful for a tray icon and a native
notification when a Capsule needs the operator, including when the window is
not focused. A second UI would have to track every change to that bundle.

The API already authenticates a browser: loopback requests receive an HttpOnly
session cookie, and a remote HTTPS origin exchanges the installation bearer
once at `POST /auth/browser-session` (ADR 0015). The UI does not store that
bearer. Per-user sessions are later work (#87). This shell is the desktop
client from that note, started before that sequencing at the owner's request.
It does not invent a user identity.

## Decision

- Ship a Tauri v2 shell. It uses the system webview and can carry `meridiand`
  as a sidecar. The window loads the gateway's `/ui/`. The shell does not
  bundle a second copy of the frontend.
- Default connection is bundled local. If `http://127.0.0.1:8080` is already a
  ready Meridian gateway, use it and do not start another daemon. Otherwise
  start the sidecar with the daemon's normal defaults, wait until readiness,
  and load `/ui/` from it. Quit sends SIGTERM only to a sidecar this process
  started, then SIGKILL if it does not exit. A daemon that was already running
  is left running.
- The other connection is an operator-chosen gateway origin. Remember the mode
  and, for that mode, the origin. Do not start the sidecar. If this process
  had started one, stop it first. Plain HTTP is accepted only for a loopback
  host. Any other gateway must be HTTPS. The shell never writes the
  installation token, a query, or URL userinfo into its config.
- Desktop-only behavior is a bridge the page detects
  (`window.__MERIDIAN_DESKTOP__`). In a browser the bridge is absent. The
  bridge can show a notification, set the tray attention count, and open the
  gateway picker. The shell accepts a notification only when the title is
  "Meridian needs you" and the body is the same count string the web UI
  already uses. The tray tooltip uses that same count. No Capsule name, prompt,
  or permission text is accepted.
- Code signing, auto-update, and a Windows build are not part of this decision.

## Consequences

Browser and desktop share one UI. WebKit, WebKitGTK, and WebView2 can still
differ, especially the Capsule terminal on Linux. The shell is the trusted
operator's client, in the same sense as the browser: it does not receive
transcript keys, credential keys, or supervisor tokens. A gateway the operator
chose can call the three bridge commands, and those commands reject any other
payload. Follow-ups are code signing, auto-update, Windows if it is in v1, and
the per-user session work already tracked in #87.
