# Docker Capsule development

The Docker provider is a trusted, single-user local-development provider. It is not a multi-tenant security boundary. A container escape or compromise of `meridiand`, which controls Docker, can compromise the host.

## Build and run

Build the Capsule image explicitly:

```console
make capsule-image
make capsule-opencode-image
make capsule-pi-image
make capsule-claude-image
make capsule-codex-image
```

`capsule-image` is the thin supervisor (`capsuled` plus the adapter).
The official harness-pack targets start from that tag and install a pinned
binary plus a trusted native PTY profile. Apply
`opencode=meridian-capsule-opencode:dev`, `pi=meridian-capsule-pi:dev`,
`claude=meridian-capsule-claude:dev`, or `codex=meridian-capsule-codex:dev` to
a Project; repositories do not copy the image-owned profile. Other official
harness images should follow the same `ARG CAPSULE_BASE` /
`FROM ${CAPSULE_BASE}` plus `/etc/meridian/harnesses.d/*.yaml` pattern.
Published releases push `ghcr.io/orlojhq/meridian-capsule-<pack>:<tag>` from
that base; local `make try-*` still builds `:dev`.
The OpenCode, Pi, and Claude Code launchers keep the general-purpose `/tmp`
mount `noexec` and set `BUN_TMPDIR` to a private directory under the bounded,
executable `/home/capsule` tmpfs so Bun can load extracted native libraries.
The executable home mount is Capsule-private; `/tmp` remains unsuitable for
loading code.

Run the daemon directly on the Docker host:

```console
go run ./cmd/meridiand \
  --provider=docker \
  --data-dir=/tmp/meridian-docker \
  --docker-image=meridian-capsule:dev
```

The project image reference is inspected before creation and the container is
created from Docker's immutable `sha256:` image ID. A mutable tag is never
passed through to container creation. Registry-qualified references that are
not present locally are pulled once (bounded by `--docker-setup-timeout`);
short names such as `meridian-capsule:dev` are never fetched from Docker Hub.
Rebuilding a local tag therefore affects only newly created Capsules.

Create a project with an executable-first setup argument array:

```console
meridian project create example \
  --repository-url=https://github.com/example/public-repository.git \
  --setup-arg=/usr/bin/make \
  --setup-arg=bootstrap \
  --image=meridian-capsule:dev
```

To offer more than one harness pack on the same Project, apply installation
packs after create (`/harness` in the TUI, or `meridian project apply-harness`)
or pass `--harness-image` at create. `/new` and `thread spawn --harness` pick a
name; the daemon uses the matching image. `/new` also freezes the name on the
Capsule and starts its native PTY profile when Ready. The daemon advertises `mock` (its
default Capsule image) and official packs that keep that image's registry and
tag: `opencode`, `pi`, `claude`, and `codex`. A digest-pinned Capsule image
needs `--official-pack-tag` so the catalog can still advertise release tags.

```console
meridian project create example \
  --repository-url=https://github.com/example/public-repository.git \
  --image=meridian-capsule:dev \
  --harness-image=mock=meridian-capsule:dev \
  --harness-image=opencode=meridian-capsule-opencode:dev \
  --harness-image=pi=meridian-capsule-pi:dev \
  --harness-image=claude=meridian-capsule-claude:dev \
  --harness-image=codex=meridian-capsule-codex:dev
```

Each `--setup-arg` is one argument. Meridian does not concatenate these values
into a shell command. Public repositories and fixture repositories already
visible inside the provider environment are supported. For a private HTTPS
repository, store a `git_https` secret through stdin and authorize its name on
the Project. `meridiand` sends it only over the authenticated private
`capsuled` Prepare request; clone disables ambient helpers and uses a temporary
askpass helper outside `/workspace`, removed on every outcome. SSH, Git, file,
and local-path repositories cannot use a configured Git secret.

Docker also proxies bounded browse, workspace capture, and Delivery operations
through the same authenticated private `capsuled` client. It never reads the
volume from the host or mounts a local worktree. A `git_push` credential is
decrypted for one exact `refs/heads/...` push and sent only in that private
request; a `github_api` token remains in `meridiand` and is used only for the
host-side GitHub API call. Neither secret becomes container environment,
repository configuration, an archive, or a durable event.

Configure names and commit identity explicitly:

```console
meridian project create example \
  --repository-url=https://github.com/example/repository.git \
  --image=meridian-capsule:dev \
  --git-push-secret=delivery_push \
  --github-api-secret=github_api \
  --commit-author-name="Meridian Delivery" \
  --commit-author-email=delivery@example.invalid \
  --default-base-branch=main
```

## Resource and credential boundaries

Each Capsule receives one uniquely named volume, bridge network, and container. Only its volume is mounted at `/workspace`. The container runs as UID/GID 10001, with a read-only root filesystem, dropped capabilities, `no-new-privileges`, bounded memory/CPU/PIDs, bounded Docker logs, and writable bounded tmpfs paths. The supervisor is published on one random host port bound to `127.0.0.1`.

Only `meridiand` may access the Docker socket. Capsule containers never receive the socket, a host path, a host namespace, a device, control-plane state, or provider credentials. Per-Capsule supervisor tokens are stored outside SQLite under the provider state directory with mode 0600 and are never returned by the public API.

Docker ownership labels and deterministic names support adoption after restart. Cleanup verifies every resource's labels before removal and retries partial cleanup. Do not manually reuse Meridian's configured name or label prefix.

## Compose

`deploy/docker/compose.yaml` is the contributor stack: it **builds** `meridiand`
from this repo and defaults to `meridian-capsule:dev`. It does **not** build the
dynamically selected Capsule image. The service mounts the Docker socket into
`meridiand` only. Daemon state is bind-mounted from `.local/meridian-data` so
the host CLI/TUI can read the installation token.

`deploy/docker/compose.release.yaml` pulls a published `meridiand` image. Set
`MERIDIAN_VERSION` to a verified release tag. See the README **Run a published
release** section.

```console
make try-opencode
```

`try-opencode` builds `meridian-capsule-opencode:dev`, starts Compose with the
thin `meridian-capsule:dev` default if `127.0.0.1:8080` is not already healthy,
and opens the host TUI with `--harness opencode`. `try-pi`, `try-claude`, and
`try-codex` follow the same pattern.

```console
make meridiand-up
make tui
```

`meridiand-up` builds `meridian-capsule-integration:dev`, starts Compose detached, and waits for `/healthz`. The Compose `meridiand` process runs as root so it can use the Docker socket; the target then chowns `.local/meridian-data/api-auth` for the host TUI. Override the Capsule image with `CAPSULE_IMAGE=meridian-capsule:dev`. Stop with `make meridiand-down`. The TUI still runs on the host; it is a client of `127.0.0.1:8080`.

The Compose file uses host networking so `meridiand` can reach Capsule supervisor ports that are intentionally bound only to host loopback. Host networking must be supported by the local Docker installation. Running `meridiand` directly on the Docker host is the portable development path.

## Credential-free mock

`meridian-capsule-integration:dev` ships a fixture repository and mock
harnesses. It needs no provider login:

```console
make capsule-integration-image
./bin/meridiand --provider=docker \
  --data-dir=/tmp/meridian-demo \
  --docker-image=meridian-capsule-integration:dev
./bin/meridian project create demo \
  --repository-url=file:///fixture \
  --image=meridian-capsule-integration:dev \
  --harness-image=mock=meridian-capsule-integration:dev
printf '%s' 'inspect the fixture' | ./bin/meridian thread spawn PROJECT_ID \
  --harness mock-structured --prompt-stdin --name workspace --follow
```

`make meridiand-up` also builds that image. Scriptable follow-ups live in
[CLI and TUI](cli-and-tui.md).

## Integration test

```console
make docker-integration
```

Normal `make test` remains hermetic and skips the Docker contract unless `MERIDIAN_DOCKER_TEST=1` is set. The integration target builds `meridian-capsule:dev` and runs the provider lifecycle contract against the local daemon.
