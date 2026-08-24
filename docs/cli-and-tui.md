# CLI and terminal dashboard

Meridian exposes the same lifecycle operations through scriptable Cobra
commands and an interactive Bubble Tea dashboard. The dashboard is a client of
the generated public API; it does not load daemon, provider, store, or domain
packages.

## Launch

Start `meridiand` on its default loopback listener, then run:

```console
meridian tui
```

The dashboard requires terminal stdin and stdout. It fails without emitting
terminal escape sequences when invoked from a pipe, redirected output, or
another non-TTY environment. For automation and noninteractive summaries, use
resource commands with `--json`, for example:

```console
meridian --json project list
meridian --json capsule list PROJECT_ID
meridian --json run list --capsule CAPSULE_ID
```

`--server` applies to both the dashboard and scriptable commands. Keep the
server on loopback: the current public API is unauthenticated.

## Fleet and Thread layout

The dashboard is a unified Capsule and retained-Thread fleet. At 100 columns or
more the navigator and selected detail/transcript are side by side. On narrower
terminals, `Tab` switches between navigator and detail without losing the
selected Thread, transcript position, or unread count.

Thread rows expose lifecycle/session state, harness, structured adapter
protocol, latest activity, replay gaps, and transcript lock/corruption. A
Capsule with no structured harness profile reports Threads as unsupported and
keeps native PTY Runs available as a separate fallback.

## Keybindings

- `j`/`k` or arrow keys: select a Capsule or retained Thread
- `Tab`: switch navigator/detail on narrow terminals
- `PageUp` / `PageDown`: scroll a bounded transcript
- `r`: refresh immediately
- `?`: help
- `q` or `Ctrl-C`: leave the dashboard
- `c`: create a Capsule
- `t`: create a Thread from the selected Capsule and structured harness; an
  optional first message can start the session atomically
- `n`: compose a multi-line Thread message (`Enter` inserts a newline,
  `Ctrl-S` submits)
- `e`: start or resume the selected structured Thread
- `P`: answer the latest pending select, confirm, or input request
- `z`: cancel the active structured session
- `A`: archive a retained Thread
- `D`: crypto-shred a Thread after typing `crypto-shred`
- `p` / `u`: pause or resume
- `a`: attach the latest active PTY Run
- `g`: show bounded Git diff
- `m`: capture a Moment
- `s`: create a Shard from a Moment
- `w`: create a non-destructive Rewind descendant and new Timeline
- `S`: Seal after confirmation
- `x`: delete after confirmation
- `Esc`: cancel a form or close an overlay

Forms validate required fields before sending an API mutation and use the
Capsule resource version shown by the latest dashboard refresh. A conflict
means the resource changed; refresh and review its current state before
retrying. Rewind creates a new Timeline and does not delete later history.

The dashboard polls at a bounded cadence, preserves selection where possible,
and keeps the last snapshot visible while reconnecting. Provider identity and
resource usage are displayed as unavailable because the current public API
does not expose them; no values are inferred.

Thread replay also preserves the durable message cursor. Duplicates are
discarded, streamed assistant deltas are collapsed, and an authoritative final
message replaces matching deltas. Transcript rendering is bounded. Tool and
unknown blocks are summaries/text only, and terminal control characters are
removed before display. A replay gap is explicit and is never filled by
scraping PTY output.

Thread mutation clients must retry with the same idempotency key after an
ambiguous transport or daemon failure. A durable content-free delivery
acknowledgement distinguishes a pending persisted message from one already
accepted: retry may deliver the pending message once, but an acknowledged
replay does not resend it. Startup recovery never auto-sends Thread input.

When a structured session reaches a terminal state, its Thread becomes
`paused`; resume starts a new adapter session from the latest encrypted resume
state. This frees the Capsule's single active-Thread slot without archiving or
deleting history.

Transcript `locked` means the daemon installation key is missing or does not
match; restore the correct operator key and retry. `corrupt` means the encrypted
transcript failed validation; stop using that Thread and restore from a trusted
backup. Neither state displays ciphertext or key identifiers.

The local daemon decrypts requested transcript pages for the trusted local
user. Encryption protects SQLite and backups at rest; it is not an
authorization boundary against local daemon/API access.

## Native PTY attach

The dashboard releases the terminal and starts the existing native
`meridian run attach` command. Bubble Tea does not proxy or render PTY bytes.
When attachment ends, the dashboard reacquires and redraws the terminal.
This path remains independent from structured Threads: structured text is
consumed only through Thread block APIs and is never PTY-scraped.

Press `Ctrl-]` to detach locally. `Ctrl-C` remains a byte sent to the remote PTY
while attached. On an abnormal disconnect, attach reports the highest rendered
output cursor:

```console
meridian run attach RUN_ID --after CURSOR
```

Terminal mode is restored before attach errors are printed, including on
server failure, cancellation, `SIGTERM`, `SIGHUP`, and `SIGQUIT`. Replay gaps
are reported on the diagnostic stream without mixing the notice into PTY
output.

## Security boundary

PTY bytes and Git diffs are untrusted content and may contain secrets. They are
not written to normal durable events or logs. Attach tickets remain short-lived
and Run-scoped.

The Docker provider remains for trusted, single-user local development only.
It is not an untrusted multi-tenant isolation boundary, and Capsule workloads
must never receive the host Docker socket.
