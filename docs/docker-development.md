# Docker Capsule development

The Docker provider is a trusted, single-user local-development provider. It is not a multi-tenant security boundary. A container escape or compromise of `meridiand`, which controls Docker, can compromise the host.

## Build and run

Build the Capsule image explicitly:

```console
make capsule-image
make capsule-opencode-image
```

`capsule-image` is the thin supervisor (`capsuled` plus the adapter).
`capsule-opencode-image` starts from that tag and installs a pinned OpenCode
binary plus a trusted native PTY profile. Apply
`opencode=meridian-capsule-opencode:dev` to a Project; repositories do not copy
the image-owned profile. Other official harness images should follow the same
`FROM meridian-capsule:dev` plus `/etc/meridian/harnesses.d/*.yaml` pattern.
The OpenCode launcher keeps the general-purpose `/tmp` mount `noexec` and sets
`BUN_TMPDIR` to a private directory under the bounded, executable
`/home/capsule` tmpfs so Bun can load OpenTUI's extracted native render library.
The executable home mount is Capsule-private; `/tmp` remains unsuitable for
loading code.

Run the daemon directly on the Docker host:

```console
go run ./cmd/meridiand \
  --provider=docker \
  --data-dir=/tmp/meridian-docker \
  --docker-image=meridian-capsule:dev
```

The project image reference is inspected before creation and the container is created from Docker's immutable `sha256:` image ID. A mutable tag is never passed through to container creation. Rebuilding a tag therefore affects only newly created Capsules.

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
default Capsule image) and `opencode` (`meridian-capsule-opencode` with the
same tag).

```console
meridian project create example \
  --repository-url=https://github.com/example/public-repository.git \
  --image=meridian-capsule:dev \
  --harness-image=mock=meridian-capsule:dev \
  --harness-image=opencode=meridian-capsule-opencode:dev
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

`deploy/docker/compose.yaml` builds and runs `meridiand`; it does **not** build the dynamically selected Capsule image. The service mounts the Docker socket into `meridiand` only. Daemon state is bind-mounted from `.local/meridian-data` so the host CLI/TUI can read the installation token.

```console
make meridiand-up
make tui
```

`meridiand-up` builds `meridian-capsule-integration:dev`, starts Compose detached, and waits for `/healthz`. The Compose `meridiand` process runs as root so it can use the Docker socket; the target then chowns `.local/meridian-data/api-auth` for the host TUI. Override the Capsule image with `CAPSULE_IMAGE=meridian-capsule:dev`. Stop with `make meridiand-down`. The TUI still runs on the host; it is a client of `127.0.0.1:8080`.

The Compose file uses host networking so `meridiand` can reach Capsule supervisor ports that are intentionally bound only to host loopback. Host networking must be supported by the local Docker installation. Running `meridiand` directly on the Docker host is the portable development path.

## Integration test

```console
make docker-integration
```

Normal `make test` remains hermetic and skips the Docker contract unless `MERIDIAN_DOCKER_TEST=1` is set. The integration target builds `meridian-capsule:dev` and runs the provider lifecycle contract against the local daemon.
