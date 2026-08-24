# Docker Capsule development

The Docker provider is a trusted, single-user local-development provider. It is not a multi-tenant security boundary. A container escape or compromise of `meridiand`, which controls Docker, can compromise the host.

## Build and run

Build the Capsule image explicitly:

```console
make capsule-image
```

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

Each `--setup-arg` is one argument. Meridian does not concatenate these values into a shell command. Public repositories and fixture repositories already visible inside the provider environment are supported. Private repository authentication, Git credential helpers, reusable credentials, and long-lived credentials in Capsules are intentionally unsupported.

## Resource and credential boundaries

Each Capsule receives one uniquely named volume, bridge network, and container. Only its volume is mounted at `/workspace`. The container runs as UID/GID 10001, with a read-only root filesystem, dropped capabilities, `no-new-privileges`, bounded memory/CPU/PIDs, bounded Docker logs, and writable bounded tmpfs paths. The supervisor is published on one random host port bound to `127.0.0.1`.

Only `meridiand` may access the Docker socket. Capsule containers never receive the socket, a host path, a host namespace, a device, control-plane state, or provider credentials. Per-Capsule supervisor tokens are stored outside SQLite under the provider state directory with mode 0600 and are never returned by the public API.

Docker ownership labels and deterministic names support adoption after restart. Cleanup verifies every resource's labels before removal and retries partial cleanup. Do not manually reuse Meridian's configured name or label prefix.

## Compose

`deploy/docker/compose.yaml` builds and runs `meridiand`; it does **not** build the dynamically selected Capsule image. Run `make capsule-image` first. The Compose service mounts the Docker socket into `meridiand` only and persists daemon data.

The Compose file uses host networking so `meridiand` can reach Capsule supervisor ports that are intentionally bound only to host loopback. Host networking must be supported by the local Docker installation. Running `meridiand` directly on the Docker host is the portable development path.

## Integration test

```console
make docker-integration
```

Normal `make test` remains hermetic and skips the Docker contract unless `MERIDIAN_DOCKER_TEST=1` is set. The integration target builds `meridian-capsule:dev` and runs the provider lifecycle contract against the local daemon.
