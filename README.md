<div align="center">

<img src="docs/assets/meridian-mark.svg" width="96" alt="Meridian logo">

# Meridian

**A self-hosted control station for coding agents.**

Run Claude Code, Codex, OpenCode, Pi, or DeepSeek Harness in disposable
workspaces, let a lead agent hand work to subagents, then review and ship what
they built.

[![CI](https://github.com/OrlojHQ/meridian/actions/workflows/ci.yml/badge.svg)](https://github.com/OrlojHQ/meridian/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/OrlojHQ/meridian?include_prereleases&sort=semver)](https://github.com/OrlojHQ/meridian/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

[Quick start](#quick-start) · [What you get](#what-you-get) · [Security model](#security-model) · [Documentation](#documentation)

</div>

Coding agents do their best work when you let them run: install packages, run
the test suite, rewrite half the repo, try three approaches at once. That is
exactly what you don't want happening in your only checkout, on your laptop,
one attempt at a time.

Meridian gives every agent session its own **Capsule**: a throwaway container
with a fresh clone of your repository at `/workspace` and the agent you picked
already installed. Hand it a task from the browser, follow along in a chat or
its terminal, let it start subagents for the parts that can run in parallel,
review the diff, and open a pull request when you like the result. When you
don't, throw it away.

Your **Station** is the Meridian you run: one `meridiand` on your machine or
your cluster, with the agents and models you already pay for, or your own model
server. Use it alone, or share one Station with your team.

![The Meridian browser workspace: Capsules that need you are listed first, then every Project's Capsules](docs/assets/screenshots/ui-home.png)

## What you get

### A workspace per task

Each Capsule is a non-root container with bounded CPU, memory, and process
count, a fresh clone, and one agent. It never receives your Docker socket,
your home directory, or Meridian's credentials. Prepared environments cache
your project's setup step, so the next Capsule on the same commit starts
without reinstalling.

### Agents you can talk to in the browser

Start a task and the agent works in a **Thread**: a chat you can follow live,
answer, and steer from the web UI.

- Approve or deny the agent's tool use, and answer the questions it asks.
- Switch the model and reasoning effort from the message box, mid-session.
- Type `/` for the agent's own slash commands.
- The **Inbox** shows every Capsule by status, puts sessions that need you
  first, and lets you peek at and answer them in place. Browser notifications
  tell you when an agent is waiting.
- Prefer a terminal? Every agent also runs in a live terminal in the browser
  or in the `meridian tui` dashboard.

Transcripts are encrypted at rest.

![A Thread waiting for permission to push release notes, with the Capsule's CHANGELOG diff beside it](docs/assets/screenshots/ui-thread.png)

### A lead agent with subagents

Tick **Lead agent with subagents** when you start a task and the agent can
split the work: it starts subagents, each in its own Capsule, sends them
instructions, waits for their reports, and reads their diffs.

- You set the limits: how many subagents a day, how many at once, how long the
  lead keeps going without you, and whether each spawn needs your approval.
- The **Agents** list in the lead's composer shows every subagent's status and
  model; click one to open it.
- Subagents ask **you**, never the lead, for permission to use tools.
- A lead can propose a pull request; only you can approve it.
- The lead and its subagents share **working notes**, so a finished
  subagent's findings outlive its Capsule.

### Project notes

Write what every Thread in a Project should know, such as how you like pull
requests written or which lead owns which directory, once as **Project
notes**. Each new session gets them as files and a short index. Agents can
propose changes, and nothing reaches other Threads until you review the diff
and approve it.

### Any agent, any model

- **Five official agents**: Claude Code, Codex, OpenCode, Pi, and DeepSeek
  Harness, each pinned by checksum and signed on every release.
- **Your models**: sign in to the agent as you do today, add an OpenAI or
  Anthropic API key once, or point Meridian at your own server: vLLM, Ollama,
  LiteLLM, or any OpenAI- or Anthropic-compatible endpoint, with the reasoning
  effort levels it supports. Keys never enter a Capsule.
- **Other agents**: any CLI agent runs as a custom harness, and any
  [Agent Client Protocol](https://agentclientprotocol.com) agent can also run
  as a browser Thread through the built-in `acp` adapter.

### Your setup, in every Capsule

Import your computer once and every new Capsule starts with the agent setup
you already tuned: settings, `AGENTS.md` and `CLAUDE.md`, skills, slash
commands, agent definitions, prompts, and MCP servers that can run in a
container, plus your git name and email.

- **One review, then one save.** `meridian import` on your computer, or
  **Review this computer's setup** in the desktop app, shows what will be
  kept, what is left out, and why, before anything is uploaded.
- **Logins never travel.** `auth.json`, OAuth tokens, API keys, and SSH keys
  are left out on purpose. Model access comes from a provider connection,
  whose key stays in the daemon, and laptop-only permission settings are
  stripped.
- **A snapshot, not a live link.** Each Capsule pins the profile it started
  with. When your setup changes, the desktop app offers to review it again;
  you choose when a Capsule updates.

<p align="center">
  <img src="docs/assets/screenshots/desktop-import.png" width="420" alt="The desktop app's import window: Claude Code and Codex found, files kept and left out, the git identity, and Save">
</p>

![Harness settings: what new Capsules inherit, with a setup and default connection per harness](docs/assets/screenshots/ui-harnesses.png)

### One Station for your team

Turn on team mode and teammates sign in through your identity provider (OIDC:
Okta, Entra ID, Google, Keycloak, Authentik, Dex). Every Project, Capsule,
secret, setup, and model login belongs to one person and is private by
default.

- `meridian login` signs the CLI and TUI in; personal access tokens can be
  read-only and expire.
- Admins add and disable members, sign them out everywhere, and transfer a
  departed teammate's work.
- An append-only audit log records who did what, without prompts or secrets.

![Account settings in team mode: members, roles, and sessions](docs/assets/screenshots/ui-team.png)

### Snapshot, fork, and rewind

Capture a **Moment** of the workspace at any point. Shard it into a new Capsule
to try a different approach in parallel, or rewind to an earlier Moment
without losing anything that came after.

### Review before anything leaves the box

The browser workspace has syntax-highlighted diffs with line comments you can
send back to the agent, a file browser, a live terminal, and authenticated
previews of dev servers running inside the Capsule. Push a reviewed branch and
open a GitHub pull request, or sync the changes into your local checkout.
Approval is bound to the exact tree you reviewed.

### Script everything

Every UI action is also a CLI command and a documented
[OpenAPI](api/openapi.yaml) endpoint.

## Quick start

You need Docker. Then pick an install path.

### With Homebrew (macOS or Linux)

```console
brew install orlojhq/tap/meridian
brew services start meridian
meridian tui --harness claude
```

`brew services` keeps `meridiand` running in the background and restarts it at
login; its log is in `$(brew --prefix)/var/log/meridiand.log`. Upgrade with
`brew upgrade meridian`. Stop with `brew services stop meridian`.

### Desktop window

The desktop app is the same web UI in its own window. It is not code-signed
yet. The `meridian` formula above is unchanged and is still how you install
the daemon and the CLI on its own.

With Homebrew on macOS:

```console
brew install --cask orlojhq/tap/meridian-desktop
```

The cask removes the `com.apple.quarantine` flag after install so Gatekeeper
does not report the app as damaged.

Without Homebrew, on macOS or on Linux amd64:

```console
curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh
```

The script downloads the latest published release, checks it against that
release's `checksums.txt`, and stops if the check fails. On macOS it installs
`Meridian.app` to `/Applications` when that directory is writable, and to
`~/Applications` otherwise, then clears the quarantine flag. On Linux it
installs the `.deb` with `sudo apt install`. Each stable release also
carries this script as `install-desktop.sh`, signed and listed in
`checksums.txt`.

Pin a version, or install only the `meridian` and `meridiand` binaries:

```console
curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh -s -- --version 0.10.1
curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh -s -- --cli --version 0.10.1
```

The window checks for a newer release when it opens and every four hours.
Nothing is installed until you click **Update & restart**. Later hides that
version for a day. Check for updates… in the tray menu checks again
immediately. A Homebrew install shows `brew upgrade --cask meridian-desktop`
instead of replacing itself. A Linux package shows the curl command above,
because that package cannot be replaced in place. macOS copies installed
from the disk image, the app zip, or the curl command update themselves
once the updater signing key is configured. Until then they show the curl
command too. The key setup is in
[Operations](docs/operations.md#desktop-updater-signing).

`MERIDIAN_VERSION` does the same job as `--version`. To remove the app:
`rm -rf /Applications/Meridian.app` (or `~/Applications/Meridian.app` when it
was installed there), or `sudo apt remove meridian-desktop` on Linux. That
package installs `meridian-desktop` only. It does not replace `meridian` or
`meridiand`, so removing the app leaves a separate CLI install in place, and
removing the CLI leaves the app. To remove the CLI binaries, delete
`meridian` and `meridiand` from the directory the script printed. Removing
the app does not delete Meridian projects.

If you download the disk image from
[Releases](https://github.com/OrlojHQ/meridian/releases) yourself, open it,
copy Meridian to Applications, then right-click Meridian and choose Open. Or
run:

```console
xattr -dr com.apple.quarantine /Applications/Meridian.app
```

### From a release

Download the archive for your platform from
[Releases](https://github.com/OrlojHQ/meridian/releases) (`macOS` or `linux`,
`arm64` or `amd64`) and [verify it](docs/operations.md#verify-a-release):

```console
VERSION=0.10.1
curl -fsSLO "https://github.com/OrlojHQ/meridian/releases/download/v${VERSION}/meridian_${VERSION}_macOS_arm64.tar.gz"
tar -xzf "meridian_${VERSION}_macOS_arm64.tar.gz" meridian meridiand
```

Start the daemon, then open the dashboard in a second terminal:

```console
./meridiand --provider=docker
./meridian tui --harness claude
```

Enter a repository (`owner/repo` or a clone URL) and submit. Meridian pulls
the Claude Code image, starts a Capsule, and hands you its terminal. Sign in
there once; Threads in that Capsule use the same login.

Then open the browser workspace at
[http://127.0.0.1:8080/ui/](http://127.0.0.1:8080/ui/). On this computer it
signs you in on its own. From another machine it asks once for the
installation token, which `meridiand` wrote to
`~/Library/Application Support/meridian/api-auth/installation.token` on macOS
or `~/.config/meridian/api-auth/installation.token` on Linux.

To bring your agent setup along, run `meridian import` and review what it
finds before you save.

Swap `--harness claude` for `codex`, `opencode`, or `pi`.

### From source

You need Go 1.26.11, Bun 1.3.0, Git, Make, and Docker with BuildKit.

```console
make bootstrap
make try-claude
```

That builds everything, starts `meridiand` on loopback, and opens the same
dashboard. Also available: `make try-codex`, `make try-opencode`,
`make try-pi`.

### Use API keys or your own model server

Threads can also use a reusable **provider connection** instead of a sign-in:
an OpenAI or Anthropic API key, or your own endpoint. Connections go through
Meridian's provider gateway, so Capsules never see the key. With Docker, which
includes Homebrew's `brew services start meridian`, the daemon runs a local
gateway for Capsules on its own; there is nothing to configure. Add a key
under Provider connections in Harness settings, or from the CLI:

```console
./meridian harness connect openai --stdin < key.txt
./meridian harness connect openai-compatible \
  --base-url https://models.example --model glm-5.3-flash \
  --effort low,high,max --stdin < key.txt
```

See [Bring your harness setup](docs/personal-harness-setups.md) for every
option, including vLLM, Ollama, LiteLLM, and `--provider-gateway-url` for a
reverse proxy or Kubernetes.

### Troubleshooting

- **`cannot reach Docker`**: start Docker (Docker Desktop, OrbStack, or the
  Docker service). On Linux, your user needs access to the Docker socket. Use
  `--docker-host` to point at a different engine.
- **Port 8080 is already in use**: pick other ports and point the CLI at them:

  ```console
  ./meridiand --provider=docker --listen=127.0.0.1:18080 --preview-listen=127.0.0.1:18081
  ./meridian --server http://127.0.0.1:18080 tui --harness claude
  ```

  The browser workspace is then at `http://127.0.0.1:18080/ui/`.
- **`provider connections are unavailable: no local provider gateway`**: the
  Docker engine is remote or rootless, so `meridiand` cannot pick an address
  Capsules reach. Set `--provider-gateway-listen=IP:PORT` or
  `--provider-gateway-url`; see
  [Reusable API connections](docs/personal-harness-setups.md#reusable-api-connections).
  On Linux, also check that a firewall such as `ufw` allows containers to
  reach the host.
- **`pushes are unavailable: no Station Git gateway`**: pushes go through the
  same gateway listener so the Git credential never enters a Capsule
  (ADR 0054). Fix the provider gateway as above. Only if that is impossible,
  `--delivery-push=capsule` pushes from inside the Capsule instead.
- **`data directory is in use`**: another `meridiand`, or a maintenance
  command, already holds that data directory. Stop it, or give this daemon
  its own `--data-dir`. Never point two daemons at one directory.
- **macOS says `meridiand` cannot be opened**: the binaries are not notarized,
  and archives downloaded in a browser are quarantined. After
  [verifying the archive](docs/operations.md#verify-a-release), run
  `xattr -d com.apple.quarantine meridian meridiand`. Downloads made with
  `curl`, as above, are not quarantined.

## Supported agents

| Agent | Image | Terminal | Browser Threads | Lead with subagents |
| --- | --- | :---: | :---: | :---: |
| Claude Code | `ghcr.io/orlojhq/meridian-capsule-claude` | ✓ | ✓ | ✓ |
| Codex | `ghcr.io/orlojhq/meridian-capsule-codex` | ✓ | ✓ | ✓ |
| OpenCode | `ghcr.io/orlojhq/meridian-capsule-opencode` | ✓ | ✓ | ✓ |
| Pi | `ghcr.io/orlojhq/meridian-capsule-pi` | ✓ | ✓ | ✓ |
| DeepSeek Harness | `ghcr.io/orlojhq/meridian-capsule-dsh` | | ✓ | ✓ |

Every image pins its agent release by checksum, is built for `amd64` and
`arm64`, and is signed with Sigstore on each release. DeepSeek Harness is a
developer preview with no terminal UI; it runs only as a browser Thread and
uses a provider connection for its model. Any other CLI agent can run as a
custom harness through [`.meridian/project.yaml`](docs/harness-configuration.md),
and an Agent Client Protocol agent can run as a browser Thread through the
[`acp` adapter](docs/harness-adapters.md#adding-an-acp-harness).

## How it works

```mermaid
flowchart LR
    You["You"] --> Client["meridian<br/>CLI · TUI · browser"]
    Client -->|"REST / SSE / WebSocket"| Daemon["Station<br/>meridiand"]
    Daemon --> Store["SQLite + Moments"]
    Daemon --> Provider["Docker or Kubernetes<br/>Agent Sandbox"]
    Provider --> Lead["Lead Capsule"]
    Provider --> Sub["Subagent Capsules"]
    Lead --> Agent["agent + /workspace"]
    Sub --> SubAgent["agent + /workspace"]
```

| Piece | Role |
| --- | --- |
| Station (`meridiand`) | The daemon you run: API, storage, Capsule lifecycle, provider gateway, embedded web UI |
| `meridian` | CLI and terminal dashboard, a client of one Station |
| Capsule | Container with `capsuled` (a small supervisor) and one harness, the coding agent |
| Project checks | Commands such as `make test` that run in the Capsule when the agent finishes, with a bounded number of automatic retries, plus GitHub CI on a pull request Meridian opened |

You run one Station. It starts Capsules on local Docker or, for a cluster, on
[Kubernetes Agent Sandbox](docs/kubernetes-agentsandbox.md), and stores
Moments next to its SQLite database. A lead agent asks for subagents through a
mailbox inside its own Capsule; the Station picks up each request, checks it
against the limits you set, and starts the subagent. No Capsule ever holds a
Meridian credential.

## Security model

Meridian treats repositories, agent output, terminals, and Capsule processes as
hostile.

- The API listens on loopback by default and requires an installation token,
  or, in team mode, a member's own sign-in session or personal access token.
- In team mode every object has one owner, and one member's API calls cannot
  read or change another member's work.
- Only `meridiand` touches the Docker socket; Capsules never get it, host
  paths, or control-plane credentials.
- Secrets and transcripts are encrypted at rest, and secrets are scoped to a
  purpose, such as one clone or one pull request.
- Model API keys stay in the daemon; Capsules get a revocable gateway lease.
- Subagents ask you, not their lead, for permission, and a lead can propose a
  pull request but never approve one.

Meridian is built for **one person or one team of trusted teammates**
running agents on their own machines or cluster. Team mode keeps teammates'
work private through the API, but their Capsules still share the provider's
isolation: with Docker that is one kernel. Containers are not a boundary
between mutually hostile tenants, so do not host Meridian for untrusted
users. Read the [threat model](docs/threat-model.md) and
[security policy](SECURITY.md).

## Status

Meridian is pre-1.0. APIs and on-disk formats may change between minor
releases; see the [changelog](CHANGELOG.md).

## Documentation

**Use**
- [CLI and terminal dashboard](docs/cli-and-tui.md): `thread spawn`,
  [leads and subagents](docs/cli-and-tui.md#leads-and-subagents), the board,
  sync, ship
- [Browser workspace and previews](docs/review-ui.md)
- [Bring your harness setup](docs/personal-harness-setups.md): importing your
  computer, API keys, and self-hosted models
- [Prepared project environments](docs/prepared-environments.md)
- [Project checks](docs/checks.md): run tests when the agent finishes, and send failures back
- [Moments and lineage](docs/moments-and-lineage.md): capture, shard, rewind,
  seal

**Extend**
- [Harness configuration](docs/harness-configuration.md): `.meridian/project.yaml`
- [Harness adapters](docs/harness-adapters.md): official images, the
  structured protocol, and ACP agents

**Operate**
- [Operations](docs/operations.md): install, verify, back up, Compose, Helm
- [Team mode](docs/operations.md#team-mode): OIDC sign-in, members, and the
  audit log
- [Docker development](docs/docker-development.md)
- [Kubernetes Agent Sandbox](docs/kubernetes-agentsandbox.md)
- [Threat model](docs/threat-model.md)
- [Architecture decisions](docs/adr/)

## Contributing

Issues and pull requests are welcome. Start with
[CONTRIBUTING.md](CONTRIBUTING.md), and discuss large changes in an issue
first.

```console
make bootstrap
make build test lint ui-build helm-test
```

## License

[Apache License 2.0](LICENSE). Meridian is developed by
[OrlojHQ](https://github.com/OrlojHQ) and is independent of
[Orloj](https://github.com/OrlojHQ/orloj).
