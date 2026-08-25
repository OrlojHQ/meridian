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

`--server` and the installation token apply to both the dashboard and
scriptable commands.

## Browse, local sync, and Delivery

Ready Capsules expose the supervisor's bounded workspace browser:

```console
meridian capsule files list CAPSULE_ID [PATH] [--after NAME] [--limit N]
meridian capsule files read CAPSULE_ID PATH
```

Private `.git` data, `.meridian-prepared`, symlinks, oversized files, traversal,
and unsupported file types are not readable. `read` writes binary-safe bytes to
stdout; callers decide whether to save or render them.

Local sync streams an authenticated portable archive without putting any token
in the URL:

```console
meridian capsule sync CAPSULE_ID --to /existing/git/worktree
meridian capsule sync CAPSULE_ID --to /existing/git/worktree --force
```

The CLI prints the remote Capsule Git status and bounded diff before mutation.
The target must be the root of an existing Git worktree whose `origin` matches
the Project repository; equivalent GitHub HTTPS/SSH forms are normalized.
Without `--force`, the target must be clean and replacements must already
match. Force mode mirrors non-Git content and deletions, but both modes stage
and validate the full archive in a private sibling directory before publishing,
preserve executable bits and safe relative symlinks, reject special files and
expansion abuse, and never write or remove `.git` or `.meridian-prepared`.

Delivery requires an explicit branch and local approval:

```console
meridian capsule ship CAPSULE_ID \
  --branch feature/reviewed \
  --commit-message "Ship reviewed changes" \
  --yes

meridian capsule ship CAPSULE_ID \
  --branch feature/reviewed \
  --commit-message "Ship reviewed changes" \
  --open-pull-request --title "Reviewed changes" --base main --yes
```

The CLI refuses before API access when `--yes` is absent, prints status/diff,
and binds approval to the freshly inspected Capsule resource version, HEAD, and
tree. Dirty trees require a commit message. Delivery credentials come only from
the Project's named purpose-scoped secrets. TUI browse, sync, and ship controls
are intentionally deferred; use these scriptable commands or the browser review
UI.

## Project-first sessions

The primary session command accepts a Project, structured harness, and first
prompt in one authenticated action:

```console
printf '%s' 'inspect this project' | meridian thread spawn PROJECT_ID \
  --harness opencode \
  --prompt-stdin \
  --name review-session \
  --follow
```

Each accepted mutation reserves stable intent, Capsule, Thread, Run, and
message IDs, then provisions a fresh Capsule and Timeline. `--follow` polls the
durable intent and attaches to the encrypted Thread after promotion. Retry an
ambiguous mutation with the same `--idempotency-key`; a replay never allocates
a second Capsule. Capsule-scoped `thread create` remains available for
power-user workflows.

## Sessions and Capsules

The dashboard is a Project and Capsule launcher, not a wrapper around each
harness. Its stable header, Capsule fleet, contextual footer, and temporary
command surface use the terminal as one composed screen. Enter hands the
terminal to the selected Capsule's native harness TUI. Deleted Capsules stay
off the fleet unless you show them from the action palette.

At 80 columns the fleet uses the full content width. At 100 columns and wider,
a compact inspector appears beside it with the selected Capsule's harness,
lifecycle, active Run, activity, Moments, failure, and primary Enter action.
At 140 columns the page gains wider gutters rather than filling the space with
more operational chrome. Loading, empty, disconnected, and attach-wait states
keep the same header, body, command slot, and footer anchors. `NO_COLOR`
preserves labels, borders, selection markers, and readable state text without
ANSI color.

`/project` creates a Project (name, repository URL, harness). ← → picks a pack
from the installation catalog. `/switch` chooses the active Project when more
than one exists, and `/harness` applies more packs later. `/new` asks for a
Capsule name and one applied harness; no first prompt is required.
The daemon creates the Capsule with that pack's allowlisted image, starts its
native PTY profile when Ready, and the dashboard attaches as soon as the Run is
available.

The scriptable equivalent creates and auto-starts without attaching:

```console
meridian capsule create PROJECT_ID review --harness opencode
```

Enter on an existing Capsule attaches its active native Run. If the Capsule is
Ready and idle, Enter starts a new Run from its frozen launcher harness and
attaches. Creating/Preparing Capsules wait for their initial Run. A legacy
Capsule without a frozen launcher harness, or a selected profile that is
missing, structured-only, or non-PTY, fails closed instead of opening Meridian
chat.

`/` opens an ephemeral bordered command component; there is no idle chat
composer. `/help` and `/refresh` are the other everyday commands. `:` opens
grouped Capsule, structured Thread, history, Project, and view actions.
Selecting history leaves the launcher for an explicitly labeled structured
history view; Esc returns to the same Capsule. Only one native Run or structured
Thread session can be live in a Capsule at a time. Arrow keys move command
menus; Esc closes them.

## Keybindings

- `j`/`k` or arrow keys: move in the focused list
- `Enter`: open the selected Capsule's native harness
- `Esc`: close command entry, a modal, or structured history
- `/`: temporary launcher commands (`/new`, `/project`, `/switch`, `/harness`, `/help`, `/refresh`)
- `:`: grouped actions, including encrypted structured history
- `PageUp` / `PageDown`: scroll a bounded structured transcript
- `r`: refresh immediately
- `Ctrl-O` or `?`: help
- `q` or `Ctrl-C`: leave the dashboard

Single-letter operator keys from earlier builds still work as hidden aliases
on the launcher for one release. They are not advertised in the contextual
footer; prefer `:`. They never steal keys from structured input.

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
`meridian run attach` command after `/new` or Enter. Bubble Tea does not proxy
or render PTY bytes. When attachment ends, the dashboard reacquires and redraws
the terminal, restores the selected Capsule, and briefly reports
`Detached · Run still active`.
This path remains independent from structured Threads: structured text is
consumed only through Thread block APIs and is never PTY-scraped.

Before handoff, Meridian identifies the Capsule and harness in the local
terminal title when the terminal supports OSC titles. It restores a Meridian
Project title on detach, disconnect, and attach errors. If a harness emits its
own title, attach reasserts the Meridian title after the complete output frame.
The remote frame is written unchanged and Meridian does not parse or filter it.
Standalone callers can set `--title` and `--restore-title`, or disable this
local metadata with `MERIDIAN_TERMINAL_TITLE=0`. `NO_COLOR` does not disable
terminal titles.

Press `Ctrl-P`, release it, then press `Ctrl-Q` to detach locally. `Ctrl-\` and
`Ctrl-]` are alternatives. `Ctrl-C` remains a byte sent to the remote PTY
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
