# Release and operations

## Supported deployment scope

Meridian's initial release profile is **single-user, self-hosted operation**.
Hosted hostile multi-tenancy is unsupported pending an external security review
and a separately validated hardened runtime deployment. Docker is a
trusted-local boundary: a Capsule or container-runtime escape can compromise the
host. Agent Sandbox schedules pods but is not itself an isolation boundary;
hostile workloads require an independently validated gVisor or Kata
`RuntimeClass`, dedicated infrastructure where appropriate, storage and network
policy, admission controls, and external review.

The public API, previews, and Prometheus endpoint are unauthenticated. Keep them
on literal loopback unless an operator-controlled authenticated proxy provides
the missing boundary. The metrics listener is disabled by default and rejects a
non-loopback bind unless `--allow-unsafe-metrics-listen` is explicitly set.

## Verify a release

Download an archive, its `.sigstore.json` bundle, `checksums.txt`, and the
corresponding SBOM from the same GitHub release. Set `VERSION` without a leading
`v` and select the archive for the host:

```console
VERSION=0.1.0
ARCHIVE="meridian_${VERSION}_macOS_arm64.tar.gz"
sha256sum -c checksums.txt --ignore-missing
cosign verify-blob \
  --bundle "${ARCHIVE}.sigstore.json" \
  --certificate-identity "https://github.com/OrlojHQ/meridian/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "${ARCHIVE}"
gh attestation verify "${ARCHIVE}" --repo OrlojHQ/meridian
syft scan "sbom:${ARCHIVE}.spdx.json"
```

On macOS, replace `sha256sum` with:

```console
grep "  ${ARCHIVE}$" checksums.txt | shasum -a 256 -c -
```

Verify release images by digest, never by an unverified mutable tag:

```console
cosign verify \
  --certificate-identity "https://github.com/OrlojHQ/meridian/.github/workflows/release.yml@refs/tags/v${VERSION}" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  "ghcr.io/orlojhq/meridiand:v${VERSION}"
docker buildx imagetools inspect "ghcr.io/orlojhq/meridiand:v${VERSION}"
```

The release workflow is tag-only, uses GitHub OIDC keyless signing, records
GitHub build provenance, and leaves the GitHub release in draft state for human
review. A signature proves workflow identity and artifact integrity, not code
safety or runtime isolation.

## Binary installation

Extract a verified archive into a new directory and install the client and
daemon with root-owned, non-writable permissions:

```console
tar -xzf "$ARCHIVE"
install -m 0755 meridian "$HOME/.local/bin/meridian"
install -m 0755 meridiand "$HOME/.local/bin/meridiand"
meridian --version
meridiand --version
```

`capsuled` is distributed only for Linux and normally runs in the Capsule image.
Run `meridiand` as a dedicated OS user with a mode-0700 data directory. The
default API and preview listeners are loopback-only:

```console
install -d -m 0700 "$HOME/.local/state/meridian"
meridiand --provider=fake \
  --data-dir="$HOME/.local/state/meridian" \
  --metrics-listen=127.0.0.1:9090
```

Startup atomically creates the structured-transcript installation key at
`<data-dir>/transcript-keys/installation.key` in a mode-0700 directory with a
mode-0600 file. `--transcript-key-file` selects a separately managed location.
The path must be a real restrictive directory and regular file, not a symlink.
Back up this key through a separate encrypted, access-controlled key-custody
process. Meridian backup intentionally never copies it.

Transcript encryption is an at-rest boundary. The local daemon necessarily
holds the installation key and decrypts explicit Thread reads for the local
user. It does not protect content from a compromised daemon, host, or principal
with access to the loopback API.

Optional tracing uses OTLP/HTTP and exports only fixed operation names and
bounded operational attributes:

```console
meridiand --otel-otlp-endpoint=127.0.0.1:4318 --otel-otlp-insecure
```

The current coverage includes HTTP, reconciliation, provider lifecycle,
startup recovery, Run transitions and event gaps, Moment capture size/duration,
PTY and preview sessions, cleanup/retry outcomes, and artifact failures and
retention. It does not emit repository names, IDs, prompts, paths, diffs,
terminal bytes, tokens, or arbitrary error text as metric labels or span
attributes.

## Docker installation

Pin the verified multi-platform image digest and mount a dedicated writable
directory. The image is non-root, shell-free, and compatible with a read-only
root filesystem:

```console
install -d -m 0700 "$PWD/meridian-data"
docker run --rm --name meridiand \
  --read-only --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -p 127.0.0.1:8080:8080 \
  -v "$PWD/meridian-data:/var/lib/meridian" \
  "ghcr.io/orlojhq/meridiand@sha256:VERIFIED_DIGEST" \
  --provider=fake --listen=0.0.0.0:8080 \
  --preview-listen=127.0.0.1:8081 --data-dir=/var/lib/meridian
```

The Docker Capsule provider additionally requires controlled access by
`meridiand` to the host Docker daemon. Never mount the Docker socket into a
Capsule. A remote daemon requires mutually authenticated transport and an
equally trusted dedicated host.

## Helm installation

Install Agent Sandbox v0.5.6 and its CRDs/controller separately. Pin both image
digests, use one namespace, and set an explicit API-server CIDR. For hostile
workloads, configure a validated gVisor/Kata `RuntimeClass`; the chart does not
install or certify one.

```console
helm upgrade --install meridian deploy/helm/meridian \
  --namespace meridian --create-namespace \
  --set image.repository=ghcr.io/orlojhq/meridiand \
  --set image.digest="sha256:VERIFIED_DAEMON_DIGEST" \
  --set capsuleImage.repository=ghcr.io/orlojhq/meridian-capsule \
  --set capsuleImage.digest="sha256:VERIFIED_CAPSULE_DIGEST" \
  --set agentSandbox.runtimeClassName=gvisor \
  --set-json 'networkPolicy.kubernetesAPIServerCIDRs=["192.0.2.10/32"]'
```

Back up the control-plane PVC and Capsule PVC/PV state consistently. A local
Meridian backup does not capture Kubernetes PVC contents or reconstruct
provider-owned Secrets.

## Backup and verification

Stop `meridiand` and ensure no backup, restore, GC, migration, or provider
operation is running. Backups are published atomically as mode-restricted
directories containing a SQLite `VACUUM INTO` image, the artifact CAS, and a
checksummed manifest. Runtime supervisor tokens and provider credential state
are deliberately excluded and must be recreated. Structured transcript
ciphertext and wrapped per-Thread keys are included. The installation
transcript key is excluded; the manifest records only each required key ID and
version.

```console
meridian --json admin backup create \
  --data-dir="$HOME/.local/state/meridian" \
  --output="/offline/backup/meridian-$(date -u +%Y%m%dT%H%M%SZ)"
meridian --json admin backup verify --backup=/offline/backup/meridian-TIMESTAMP
meridian --json admin backup verify \
  --backup=/offline/backup/meridian-TIMESTAMP --deep \
  --transcript-key-file=/separate/key-custody/installation.key
meridian --json admin cas verify --data-dir="$HOME/.local/state/meridian"
```

Backups can contain source code and secrets stored in repository files or
Moments, plus visible transcript metadata and encrypted transcript content.
Encrypt and access-control backup media accordingly. Copy the complete backup
directory; modifying or omitting one file causes verification failure.
Checksum verification does not need the transcript key. Use `--deep` with a
separately supplied key to authenticate wrapped keys and every transcript
block; the key is read in place and is not added to the backup.

## Restore and disaster recovery

Stop `meridiand`. Verify on the destination host before restoring. A restore to
an absent or empty directory needs no replacement flag. Replacing an existing
installation is explicit and retains the original until the verified restore
is atomically published:

```console
meridian admin backup verify --backup=/offline/backup/meridian-TIMESTAMP
meridian admin restore \
  --backup=/offline/backup/meridian-TIMESTAMP \
  --data-dir="$HOME/.local/state/meridian" \
  --transcript-key-file=/separate/key-custody/installation.key
# Existing non-empty destination:
meridian admin restore --backup=/offline/backup/meridian-TIMESTAMP \
  --data-dir="$HOME/.local/state/meridian" --replace --yes
meridian admin cas verify --data-dir="$HOME/.local/state/meridian"
```

Restore rejects traversal, symlinks, non-regular files, checksum corruption,
SQLite integrity failures, and schemas newer than the running binary. After a
restore, restart `meridiand`; provider adoption recreates or validates runtime
credentials and only adopts resources with exact Meridian ownership metadata.
Restore Kubernetes PVC data by the storage provider's supported process before
resuming the control plane.

When the manifest lists required transcript key metadata, restore must find the
matching existing default key or receive `--transcript-key-file`. It
authenticates all retained transcript envelopes before replacing existing
state, then installs the supplied key at the destination default path. A
mismatch leaves the destination untouched. There is no recovery path when the
matching key and every separately protected copy have been lost.

## Transcript key rotation and deletion

Rotation is deliberately explicit, coordinated, and offline. Stop `meridiand`
and every backup, restore, GC, migration, or other maintenance process, then
run:

```console
meridian admin transcript-key rotate \
  --data-dir="$HOME/.local/state/meridian" --yes
# Or accept an independently generated restrictive mode-0600 regular file:
meridian admin transcript-key rotate \
  --data-dir="$HOME/.local/state/meridian" \
  --new-key-file=/separate/key-custody/replacement.key --yes
```

The command never prints key material. It:

1. creates a new separately protected 256-bit installation key with a new key
   version;
2. atomically publishes a restrictive transitional keyring containing old and
   new KEKs, so either database side remains decryptable after a crash;
3. authenticates every retained transcript and re-wraps every non-deleted Thread
   DEK in one SQLite transaction (deleted Threads remain crypto-shredded);
4. deep-verifies every retained transcript with the new key; and
5. atomically retires old material from the live key file only after successful
   verification. A failure before completion rolls the database and active key
   back; a crash leaves the transitional keyring usable for offline recovery.
   With the daemon still stopped, rerun the same command without
   `--new-key-file` to detect and complete that staged transition.

Create and deep-verify a fresh backup after rotation. Its manifest must list
only the new key ID/version. Retain old separately protected keys only while a
backup that must remain recoverable still references them. Never manually
overwrite the key file.

Thread crypto-shred clears the wrapped DEK and marks the Thread deleted while
retaining immutable ciphertext and content-free metadata. It makes that
ciphertext unreadable with the live installation key, but does not erase old
backups, copied wrapped keys, filesystem snapshots, swap, or physical media.

## Upgrades and rollback limits

There is no downgrade mechanism. Before upgrading:

1. stop/quiesce `meridiand` and all maintenance commands;
2. create and independently verify a backup;
3. record verified daemon and Capsule image digests;
4. upgrade the binary/image and start one control-plane replica; and
5. verify `/healthz`, `/readyz`, provider adoption, and `admin cas verify`.

Startup applies forward-only SQLite migrations transactionally. A database with
a newer schema is rejected. Rollback is possible only by stopping the new
binary and restoring the pre-upgrade backup with the old binary. Provider-side
changes and Capsule/PVC content may require separate infrastructure recovery;
restoring SQLite alone cannot roll those back.

## Artifact retention

Visible Moments protect both archive and manifest blobs. Verification and GC
reject missing, corrupt, symlinked, or malformed CAS entries. GC is a dry run by
default:

```console
meridian --json admin cas gc --data-dir="$HOME/.local/state/meridian"
meridian --json admin cas gc --data-dir="$HOME/.local/state/meridian" --delete --yes
```

Stop `meridiand` and take a verified backup before deletion. Actual deletion is
explicit and never removes a blob referenced by a visible Moment. Meridian does
not currently age out Moments automatically.

## Deterministic smoke and uninstall

Run the release smoke on a trusted development host with Docker:

```console
make release-smoke
```

It composes Docker create/setup, packaged `mock-structured` transport,
multi-turn/tool replay and cancellation, native Run PTY/event replay, Git
status/diff, Moment capture, two descendants (Shard and Rewind), Seal, restart
adoption/cleanup, plus application permission/input fixtures and offline
encrypted backup/deep-verify/restore/crypto-shred/CAS retention tests. The
credential-free contract fixtures are the authoritative deterministic
OpenCode/Pi coverage; real-provider smokes remain explicit opt-ins.

To uninstall, first delete Capsules and wait for terminal `Deleted` state.
Confirm no owned containers, networks, volumes, Sandboxes, Secrets, PVCs, or
port-forward processes remain. Then stop the daemon, make a final verified
backup, uninstall the binary/chart/container, and remove the data directory
only after explicit operator review. Helm resource deletion does not guarantee
storage-media sanitization; follow the storage provider's PV/reclaim policy.
