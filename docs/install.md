# Install Meridian

Every install path needs Docker (Docker Desktop, OrbStack, or the Docker
service). Pick the one that fits:

- [Homebrew](#homebrew) for the daemon and CLI on macOS or Linux
- [Desktop app](#desktop-app) for Meridian in its own window
- [Release archive](#release-archive) for a manual, verified install
- [From source](#from-source) for development

Then [connect a model](#use-api-keys-or-your-own-model-server) if you don't
want to sign in to each agent, and see [Troubleshooting](#troubleshooting) if
something goes wrong. For Docker Compose, Helm, backups, and team mode, see
[Operations](operations.md).

## Homebrew

```console
brew install orlojhq/tap/meridian
brew services start meridian
meridian tui --harness claude
```

`brew services` keeps `meridiand` running in the background and restarts it at
login; its log is in `$(brew --prefix)/var/log/meridiand.log`. Upgrade with
`brew upgrade meridian`. Stop with `brew services stop meridian`.

## Desktop app

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

`MERIDIAN_VERSION` does the same job as `--version`.

If you download the disk image from
[Releases](https://github.com/OrlojHQ/meridian/releases) yourself, open it,
copy Meridian to Applications, then right-click Meridian and choose Open. Or
run:

```console
xattr -dr com.apple.quarantine /Applications/Meridian.app
```

### Updates

The window checks for a newer release when it opens, every four hours while
the app runs, after the computer wakes, and when the window is shown again
after four hours, so a window left open for days still shows a new release.
Nothing is installed until you click **Update & restart**. Later hides that
version for a day. Check for updates… in the tray menu checks again
immediately. A Homebrew install shows `brew upgrade --cask meridian-desktop`
instead of replacing itself. A Linux package shows the curl command above,
because that package cannot be replaced in place. macOS copies installed
from the disk image, the app zip, or the curl command update themselves
once the updater signing key is configured. Until then they show the curl
command too. The key setup is in
[Operations](operations.md#desktop-updater-signing).

### Uninstall

Remove the app with `rm -rf /Applications/Meridian.app` (or
`~/Applications/Meridian.app` when it was installed there), or
`sudo apt remove meridian-desktop` on Linux. That package installs
`meridian-desktop` only. It does not replace `meridian` or `meridiand`, so
removing the app leaves a separate CLI install in place, and removing the CLI
leaves the app. To remove the CLI binaries, delete `meridian` and `meridiand`
from the directory the script printed. Removing the app does not delete
Meridian projects.

## Release archive

Download the archive for your platform from
[Releases](https://github.com/OrlojHQ/meridian/releases) (`macOS` or `linux`,
`arm64` or `amd64`) and [verify it](operations.md#verify-a-release):

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
there once; Threads in that Capsule use the same login. Swap
`--harness claude` for `codex`, `opencode`, or `pi`.

### Open the browser workspace

The browser workspace is at
[http://127.0.0.1:8080/ui/](http://127.0.0.1:8080/ui/). On this computer it
signs you in on its own. From another machine it asks once for the
installation token, which `meridiand` wrote to
`~/Library/Application Support/meridian/api-auth/installation.token` on macOS
or `~/.config/meridian/api-auth/installation.token` on Linux.

To bring your agent setup along, run `meridian import` and review what it
finds before you save. See
[Bring your harness setup](personal-harness-setups.md).

## From source

You need Go 1.26.11, Bun 1.3.0, Git, Make, and Docker with BuildKit.

```console
make bootstrap
make try-claude
```

That builds everything, starts `meridiand` on loopback, and opens the same
dashboard. Also available: `make try-codex`, `make try-opencode`,
`make try-pi`.

## Use API keys or your own model server

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

See [Bring your harness setup](personal-harness-setups.md) for every
option, including vLLM, Ollama, LiteLLM, and `--provider-gateway-url` for a
reverse proxy or Kubernetes.

## Troubleshooting

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
  [Reusable API connections](personal-harness-setups.md#reusable-api-connections).
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
  [verifying the archive](operations.md#verify-a-release), run
  `xattr -d com.apple.quarantine meridian meridiand`. Downloads made with
  `curl`, as above, are not quarantined.
