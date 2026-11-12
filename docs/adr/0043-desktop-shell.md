# ADR 0043: Desktop shell around the existing web UI

- Status: Accepted
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
  and load `/ui/` from it.
- The other connection is an operator-chosen gateway origin. Remember the mode
  and, for that mode, the origin. Do not start the sidecar. If this process
  had started one, stop it first. Plain HTTP is accepted only for a loopback
  host. Any other gateway must be HTTPS. Until per-user accounts exist, a
  remote gateway uses the existing browser session: the page exchanges the
  installation bearer once at `POST /auth/browser-session` and does not keep
  it. The shell never writes the installation token, a query, or URL userinfo
  into its config.
- Closing the window hides it and leaves the tray. Quit is separate. Quit
  sends SIGTERM only to a sidecar this process started, then SIGKILL if it
  does not exit. A daemon that was already running is left running.
- Desktop-only behavior is a bridge the page detects
  (`window.__MERIDIAN_DESKTOP__`). In a browser the bridge is absent. The
  bridge can show a notification, set the tray attention count, and open the
  gateway picker. The shell accepts a notification only when the title is
  "Meridian needs you" and the body is the same count string the web UI
  already uses. The tray tooltip uses that same count. No Capsule name, prompt,
  or permission text is accepted.
- v1 builds are macOS and Linux and are distributed unsigned. A tag release
  attaches a universal macOS disk image and an amd64 Linux package, and the
  Homebrew tap publishes a cask. Apple code signing and notarization run only
  when their secrets are configured. Windows is a later follow-up. Apple
  signing without those secrets is not part of this decision.
- The shell checks `releases/latest/download/latest.json` on launch and every
  four hours. It never installs an update without a click. A Homebrew cask
  install, detected by the Caskroom path or a marker the cask writes, tells
  the operator to run `brew upgrade --cask meridian-desktop`. A Linux package
  tells them to run the curl installer, because the updater cannot replace a
  `.deb`. A macOS app that was not installed with Homebrew can download a
  signed update and restart. That signature uses a Tauri updater key, not an
  Apple certificate. The private key is a repository secret. When it is
  absent the release still ships and the app shows the curl command instead
  of installing. If this process started the sidecar, it stops that sidecar
  before restarting into an update.
- The bundle identifier is not chosen. Builds use `io.orloj.meridian`, the
  same prefix the daemon already uses for Docker labels, until a final
  identifier is picked. Do not treat that string as the decided id.

## Consequences

Browser and desktop share one UI. v1 ships on macOS WebKit and Linux
WebKitGTK. Those engines can still differ, especially the Capsule terminal on
Linux. Windows and WebView2 are a later follow-up. Update checks are quiet
until a newer release exists, and a dismissed version stays dismissed for a
day. The shell is the trusted operator's client, in the same sense as the
browser: it does not receive transcript keys, credential keys, or supervisor
tokens. A gateway the operator chose can call the bridge commands, and those
commands reject any other payload. Hiding the window does not stop a sidecar;
only Quit does, and only for a process this app started. An accepted update
stops that same sidecar before the new build launches. Follow-ups are Apple
code signing, Windows, a final bundle identifier, and the per-user session
work already tracked in #87.
