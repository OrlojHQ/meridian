# Meridian threat model

## Status and scope

This threat model establishes security requirements for Meridian's intended
architecture, including single-user release operations, backup/restore,
content-free observability, and supply-chain policy. The fake provider executes
no workload.
Docker is trusted single-user local development. Agent Sandbox orchestrates
pod/PVC lifecycle but does not itself provide an isolation boundary or justify
an untrusted multi-tenant claim.

The initial deployment assumption is one trusted human operator on a dedicated or personally controlled host. The code, repositories, dependencies, and agent actions executed in a future Capsule remain untrusted even under that assumption.

ADRs 0014 through 0022 are requirements for this architecture. API
authentication, brokered named secrets, private clone, local sync, file
browsing, idle pause, setup cache, and delivery are implemented. Agent Sandbox
Moments and previews remain unimplemented (ADR 0018). Where current behavior
differs, it is called out explicitly and must fail closed.

## Assets

Assets requiring protection include:

- host files, devices, processes, credentials, and network identity;
- the installation API bearer, credential and transcript KEKs, encrypted named
  secrets, control-plane signing keys, provider credentials, database, and
  configuration;
- source repositories, uncommitted work, private dependencies, and Git identity;
- temporary clone, harness, push, pull-request, model-provider, cloud, and
  package-registry credentials;
- Capsule filesystems, Moments, lineage, explicit structured Thread
  transcripts and installation transcript keys, logs, events, terminal data,
  and artifacts;
- API, terminal, preview, and provider control channels; and
- availability, compute, storage, network quotas, and billing authority.

## Trust boundaries

The principal boundaries are between:

1. the single trusted operator's browser or CLI and the installation-bearer
   authenticated control-plane APIs;
2. the control plane and its SQLite database/local artifact storage;
3. the control plane and the credential broker;
4. the control plane and a Capsule provider;
5. the provider/host and each Capsule;
6. `capsuled` and untrusted processes inside its Capsule;
7. a Capsule and external networks, source hosts, registries, and model services; and
8. authenticated ingress and terminal or preview services inside a Capsule.

Crossing a boundary requires authenticated identity, explicit authorization,
bounded input, and auditable outcomes. Meridian's initial API has one
installation principal, not per-user RBAC or a tenant boundary. A provider's
advertised isolation class is a security property and must not be silently
downgraded.

## Hostile inputs and actors

Meridian assumes the following may be malicious or compromised:

- a repository, its history, submodules, hooks, setup scripts, build tools, and devcontainer metadata;
- agent-generated source, shell commands, tool calls, configuration, and output;
- package dependencies, compiler plugins, language servers, and development servers;
- web content, issues, pull requests, documentation, logs, and other prompt-injection sources;
- coding-agent harnesses, MCP servers, plugins, browser automation, and tools installed in a Capsule;
- files, archives, terminal escape sequences, HTTP headers, event payloads, and generated diffs returned by a Capsule;
- local-sync archives and remote metadata, browsed filenames and file bytes,
  setup-cache archives, and pull-request fields;
- external services reached over the network and credentials returned by integrations; and
- another tenant or operator in deployment modes that exceed the initial single-user assumption.

Compromise of the host, control plane, provider control plane, browser, or the trusted operator is outside the protection offered by Capsule isolation, but deployments must still minimize blast radius and support recovery.

## Threats and required controls

### Host escape and cross-Capsule access

Untrusted code may exploit the kernel, container runtime, `capsuled`, mounted filesystems, device access, or provider APIs to reach the host or another Capsule. Capsules must run non-root where possible, without privileged mode, host namespaces, host paths, broad Linux capabilities, or direct runtime control. Apply explicit CPU, memory, process, disk, and time limits. Provider conformance tests must verify teardown and cross-Capsule isolation.

Container isolation is acceptable only for trusted single-user development where the human accepts that a container escape can compromise the host. It is not a multi-tenant security boundary. Multi-user, internet-exposed, sensitive, or mutually untrusted workloads require stronger isolation such as microVMs, Kata Containers, gVisor or another appropriately hardened sandbox, dedicated nodes/accounts, and independently evaluated network and storage boundaries.

### Docker socket consequences

Mounting `/var/run/docker.sock` into a Capsule, forwarding an unprotected Docker API, or granting equivalent container-runtime credentials gives Capsule code host-level control in ordinary Docker deployments. An attacker can create privileged containers, mount and modify the host root filesystem, enter host namespaces, read other containers' files and environment variables, steal image/registry credentials, publish host ports, change networks, kill workloads, establish persistence, and delete evidence. Docker group access is effectively root access.

Meridian must never expose the host Docker socket or equivalent runtime API to untrusted Capsule code. A socket proxy with a reduced verb list is still a high-risk privileged component and is not a general isolation boundary. If nested image builds are required later, use an isolated builder with a narrow API, separate credentials and cache, no host mounts, and its own resource and network policy. Remote Docker endpoints require mutual TLS, authorization, network restriction, and separate tenancy, but even authenticated API access still grants the authority exposed by that daemon.

In the Docker deployment, only `meridiand` accesses Docker. Each Capsule receives one owned volume at `/workspace`, one bridge network, and one non-root container with dropped capabilities, `no-new-privileges`, a read-only root filesystem, bounded tmpfs paths, CPU/memory/PID limits, and a supervisor port published to host loopback. The general `/tmp` tmpfs is `noexec`; the private `/home/capsule` tmpfs remains executable because native harness runtimes such as Bun must load generated libraries there. This does not expand the trust boundary: Capsule code can already execute from its owned workspace, and container isolation is not a hostile multi-tenant boundary. Workspace capture and restore use only that loopback-bound, versioned, bearer-authenticated endpoint; no Docker archive extraction or host path is exposed to the Capsule. A Docker or kernel escape can still compromise the host.

Agent Sandbox creates the pod and PVC but does not strengthen
the container runtime. Capsule pods remain non-root, capability-free,
seccomp-default, service-account-token-free, and read-only outside bounded
writable mounts. A validated gVisor/Kata RuntimeClass, dedicated nodes where
appropriate, storage/network isolation, admission policy, and external review
are required before hostile multi-tenancy. The chart installs or certifies none
of those controls.

### Credential theft and persistence

Untrusted processes may read environment variables, command arguments, config files, shell history, process metadata, logs, swap, or snapshots. They may trick tools into forwarding credentials. Reusable credentials remain outside Capsules. The broker issues short-lived, narrowly scoped credentials or performs a constrained operation on behalf of the Capsule. Plaintext credentials must not be persisted in SQLite, local artifacts, Moments, logs, events, crash dumps, or generated configuration. Redact known secret forms, prevent command-line disclosure, support revocation, and audit issuance and use.

The target named-secret store uses authenticated envelope encryption under a
dedicated installation credential KEK. That KEK is distinct from the
transcript KEK, held outside SQLite, artifacts, Moments, and backups, and
protected and rotated independently. Database theft still reveals secret
names, purposes, and metadata; compromise of both the daemon and KEK defeats
at-rest protection. Losing the KEK makes the ciphertext unrecoverable.

Every secret is bound to exactly one of `git_https`, `git_push`, `github_api`,
or `harness_env`. Private clone receives only a repository-bound, clone-time
one-shot Git grant. A harness secret is resolved only for its approved session
and may be exposed to that hostile process and its descendants, but must not be
written to the workspace or retained in a Moment. Push receives an
exact-repository, exact-ref, one-shot grant; GitHub pull-request API authority
stays host-side. Temporary material is revoked or discarded after its single
operation and omitted from command arguments and ambient credential helpers.

Kubernetes Capsules use one high-entropy immutable namespaced Secret solely for
the private supervisor token. It is owner-referenced and projected with mode
0400 requested; Kubernetes `fsGroup` handling may expose it to the Capsule
process as root:10001 mode 0440. `capsuled` rejects token files with other-user,
execute, or group-write access. Plaintext is absent from labels, annotations,
arguments, SQLite, and logs. This does not protect it from cluster
administrators, etcd or backup access, node agents, or RBAC principals able to
read Secrets.

### Network exfiltration and lateral movement

A compromised Capsule can encode source or credentials into any allowed request, including DNS and model prompts. Review-only presentation does not prevent exfiltration. Egress should be deny-by-default, destination- and protocol-scoped, logged with privacy-aware metadata, and grant exceptions explicitly. Block cloud metadata services, host-local services, control-plane administrative endpoints, and other Capsules. Stronger deployments need network policy enforced outside the Capsule and controls appropriate to data sensitivity; no policy can guarantee confidentiality while arbitrary outbound communication is allowed.

The chart permits DNS plus operator-selected CIDRs. NetworkPolicy cannot select
the Kubernetes API server by authenticated identity, so its CIDR can become
stale or encompass unintended endpoints. RBAC remains the authorization
boundary; high-assurance deployments need infrastructure-level egress controls.

### Malicious ingress, terminals, and previews

Terminal and preview URLs can expose command execution, cookies, source, or vulnerable development servers. Require authenticated, authorized, short-lived sessions; origin and host validation; TLS; anti-CSRF protections where cookies are used; and explicit port publication. Do not place preview applications on the control-plane origin or share privileged cookies. WebSocket PTY sessions require authorization at connection and reauthorization or expiration during long sessions. Treat terminal output and escape sequences as untrusted.

Public PTY attachment uses random, short-lived, scoped, in-memory tickets and never exposes the Capsule supervisor bearer token. Tickets are small-use and omitted from normal logs. Browser origins fail closed unless loopback; loopback CLI clients may omit `Origin`. PTY input/frame size, concurrent attachments, sessions, output memory, and replay events are bounded. Detaching does not terminate the Run. The CLI restores local terminal state; `capsuled` does not attempt to restore client terminals. Best-effort Meridian terminal-title OSC writes are local attachment metadata; title text is control-stripped and bounded. The client writes each remote frame unchanged and then reasserts its local title. Remote title escapes remain untrusted PTY output and are neither filtered nor interpreted by the control plane.

The daemon stores public attach- and preview-ticket digests rather than
plaintext values. The review UI validates same-origin, Run-scoped WebSocket
paths, applies a restrictive CSP, renders diffs as text, and never logs or
persists PTY bytes. Preview is effective only when the optional provider runtime
implements authenticated bounded discovery and forwarding end to end.
`capsuled` discovers at most 32 loopback-reachable listening ports and dials
only `127.0.0.1` at a requested discovered port. A separate daemon listener
accepts only literal loopback bind configuration and routes by digest-backed
Capsule+port scope, never `Host`. Tokens expire after two minutes, are reusable
until expiry for page resources, and are revoked by deletion, Seal, or restart;
active traffic is canceled on expiry or revocation. Header, body, frame, path,
time, and metadata limits apply at both proxy hops. Authorization, cookies,
Meridian-private headers, redirects, and hop-by-hop headers do not cross into or
out of preview applications.

The Agent Sandbox target uses those same discovery, destination, ticket,
header, body, frame, expiry, and revocation rules through an owned,
operation-scoped pod port-forward. It creates no Service, Ingress, LoadBalancer,
public pod-IP route, or shared proxy. Agent Sandbox preview is currently
unsupported and must remain unadvertised until that complete route exists.

### Harness execution and content-bearing responses

Repository-controlled harness configuration and executables are hostile.
Configuration is parsed only inside the Capsule with strict fields, bounded
sizes, direct argument arrays, workspace-contained working directories,
explicit modes, and process-group timeouts. No shell concatenation is used.
Inline credentials are rejected. The broker resolves only purpose-checked
`harness_env` references into session-only process material without persisting
values.

Official images may add trusted profiles under
`/etc/meridian/harnesses.d/*.yaml`. Trust comes from the operator-authorized
image, not the repository. `capsuled` applies the same strict parser to trusted
and repository sources, accepts only regular trusted manifest files, and rejects
duplicate names across all sources so repository content cannot replace an
image-owned executable. These manifests carry no plaintext credentials.

A harness-selected Capsule freezes its allowlisted profile name and image.
Readiness starts at most one initial native PTY Run with a stable idempotency
key; any Run history prevents recovery from restarting a process that exited or
failed. The existing active-session conflict prevents a native Run and
structured Thread from simultaneously owning the Capsule. Missing,
structured-only, and non-PTY launcher profiles fail closed rather than falling
back to a different command or Meridian chat.

Structured adapters use a separate non-PTY process supervisor and the closed
`meridian.adapter.v1` LF-delimited JSON protocol. Exact version negotiation,
strict fields and roles, bounded frames/content/tool/result/metadata, one
serialized stdin writer, process-group teardown, request deadlines, bounded
queues, and bounded cursor replay fail closed against malformed or flooding
adapters. CRLF and unterminated records are rejected; Unicode U+2028/U+2029 are
content, not frame delimiters. Unknown frame types terminate the session, while
forward-compatible presentation data is confined to a bounded JSON metadata
object. Metadata retained for typed transcript presentation stays inside the
encrypted block payload and is never copied into plaintext lifecycle records.
Dedicated bearer-authenticated private endpoints use the existing
Docker loopback or owned Agent Sandbox port-forward and are not public or
shared with PTY attachment.

Normal Run prompts remain transient. Run records and events do not persist
prompts, raw terminal bytes, full diffs, setup output, or secrets. An explicitly
created structured Thread is the sole exception: its typed blocks are durably
stored under per-Thread envelope encryption. This exception never captures PTY
traffic or retroactively persists Run prompts. PTY output, Git diffs, and
decrypted Thread blocks can contain source, credentials, malicious JSON, or
terminal escapes; callers must treat them as sensitive untrusted data and avoid
logging them. Bounded in-memory replay is availability control, not redaction.

### Prompt injection and confused-deputy actions

Repository content, issues, web pages, logs, and tool output can instruct an agent to misuse available authority. The control plane must not treat model or Capsule output as authorization. Sensitive delivery actions—credential grants, network exceptions, branch pushes, pull requests, destructive lifecycle operations, or policy changes—require independently verified permissions and explicit approval according to policy. Tool APIs should expose narrow operations rather than ambient administrator tokens.

### Supply-chain and setup execution

Package managers, install scripts, binaries, actions, generated code, and updates may be compromised. Pin project and CI dependencies, verify downloaded release checksums where practical, scan dependencies and secrets, and keep build provenance. Setup hooks execute only inside the selected isolation boundary with the same limits and egress policy as agent commands. Never run repository hooks on the control-plane host.

### API and event-stream abuse

Attackers may forge identifiers, replay requests, exploit parser differences, enumerate Capsules, inject events, or exhaust long-lived connections. Validate all input against bounded schemas, use opaque identifiers, authorize every object access, apply idempotency and replay protection to mutations, cap request and stream sizes, and rate-limit by identity. Events require stable ordering/cursors and tenant binding; clients must not infer authorization from event possession.

The target public API authenticates REST, SSE, and PTY/preview ticket minting
with one high-entropy installation bearer token in the `Authorization` header.
Only content-free liveness and readiness probes remain open. Tokens in URLs,
cookies, or bodies are rejected and bearer values never enter logs. PTY and
preview redemption continue to require their short-lived scoped tickets; the
installation token is never forwarded to a Capsule or preview. This is a
single-principal boundary with installation-wide authority, not RBAC or
multi-tenancy. Until this is implemented, the current unauthenticated public
API must remain inside a trusted local or independently authenticated
boundary.

### Provider compromise and capability confusion

A provider or adapter can lie about lifecycle completion, isolation, snapshot durability, or cleanup. Pin and authenticate adapters, validate callback identity, reconcile observed state, and audit privileged calls. Scheduling must match declared requirements to versioned provider capabilities and fail closed when a required capability is absent. High-assurance deployments must evaluate provider personnel, control planes, images, firmware, and jurisdiction in addition to Meridian software.

### Snapshot, lineage, and artifact leakage

Moments can retain deleted source, tokens, build caches, and malicious files. They capture filesystem state only and must not imply process-memory continuity. Encrypt storage where required, restrict artifact paths and permissions, validate archive extraction against traversal and symlinks, apply retention consistently, and erase provider copies and local artifacts on destruction. Rewind and Shard operations create descendants and never silently overwrite lineage. Restored content remains untrusted.

CSI VolumeSnapshots are storage-provider artifacts with reclaim, topology,
backup, consistency, and deletion semantics unlike portable Meridian Moment
archives. Agent Sandbox currently discovers prerequisites but advertises
Snapshot/Clone false because Moment v1 cannot represent those semantics. The
target Agent Sandbox implementation carries the same bounded deterministic tar
archive and manifest used by Docker over an authenticated, ownership-checked
pod port-forward to `capsuled`. It does not use `kubectl exec`, host/PVC mounts,
or CSI as a Moment substitute. CSI remains a distinct provider artifact even
after portable Agent Sandbox Moments ship. Back up SQLite with WAL state and
Kubernetes PVC/PV/CRD state consistently; the database alone does not preserve
Capsule workspaces.

The local CAS verifies digests, rejects symlink/non-regular paths, and publishes
through same-filesystem temporary files before inserting Moment metadata.
Offline verification and dry-run-by-default garbage collection verify every
archive and manifest referenced by a visible Moment before reporting
unreachable blobs; deletion requires explicit confirmation. Manifests omit
known credential channels, prompts, PTY history, process state, and full diffs,
but repository files can themselves contain secrets; a Moment therefore has
the same confidentiality requirements as the captured workspace.

Backups atomically combine a SQLite `VACUUM INTO` image with the artifact CAS
and a checksummed bounded manifest. They reject symlinks, traversal,
non-regular or unmanifested files, corruption, and newer schemas. Backups
deliberately exclude provider supervisor-token state, but Moment repository
files can contain secrets and require encrypted restricted storage.
Structured transcript ciphertext and wrapped per-Thread keys are included, but
the installation transcript key is not. The manifest exposes only required key
IDs and versions. Checksum verification needs no key; optional deep verification
authenticates every retained envelope. Restore fails before replacing an
installation when its separately supplied or preserved key cannot authenticate
required transcript data. Losing that key makes the transcript ciphertext
permanently unrecoverable.

Offline key rotation uses `meridian admin transcript-key rotate`: a
mode-restricted transitional keyring makes both old and new envelopes readable
across crashes, all non-deleted Thread DEKs change in one SQLite transaction,
deep verification precedes old-key retirement, and backup manifests continue
to contain only key IDs/versions—not key material.

### Structured transcript disclosure and tampering

SQLite theft exposes Thread metadata, ordering, roles/kinds, adapter identity,
timestamps, and ciphertext. Each Thread uses a random AES-256 DEK; AES-256-GCM
AAD binds immutable Thread/Message/block identity, sequence, type, role, and
timestamp. The installation KEK is a restrictive non-symlink regular file
outside SQLite and excluded from backup. Ciphertext rows are immutable, inserts
are strictly ordered and bounded, and authentication failures collapse to
content-free corruption errors. A host or daemon compromise that can read both
SQLite and the KEK defeats at-rest confidentiality.

The local daemon and trusted user are intentionally inside the decryption
boundary: explicit Thread APIs return plaintext. Harness adapters own upstream
agent, tool, permission, and compaction semantics; Meridian does not implement
an agent loop.

Outbound transcript input uses content-free durable delivery acknowledgements
and stable controller IDs. A client replay of the same idempotency key may
deliver the one pending message after a crash-before-send, while an
acknowledged replay does not resend. Supervisor deduplication is additional
protection around the send/acknowledgement crash boundary. Startup recovery
never auto-sends a transcript message.

Crypto-shred removes the wrapped DEK and opaque live linkage while retaining
content-free metadata and unreadable ciphertext. It is logical erasure and does
not guarantee that storage media, swap, filesystem snapshots, copied keys, or
previous backups have been sanitized. Rotation requires explicit re-wrap and
verification; retiring an old key before all referenced DEKs are re-wrapped
causes irreversible loss.

### Git and delivery integrity

Malicious repositories may alter remotes, hooks, authorship, signatures, or generated diffs. Delivery must pin the intended repository identity, inspect destination ref and remote, avoid host Git credential helpers, and require authorization for pushes and pull requests. Do not rely on a clean diff as proof that no external side effect occurred. Preserve auditable verification results without claiming they establish code safety.

Private HTTPS clone is a target brokered operation, not ambient Git
authentication: validate canonical repository identity, disable credential
helpers and hooks where applicable, provide one clone-time grant, and destroy
it after the process. Current private Git remains unsupported until this flow
is implemented end to end.

Delivery requires an explicit approved intent. Optional commit creation runs
through bounded `capsuled` Git operations without hooks or shell
concatenation. Push is one exact source-commit-to-destination-ref update under
a one-shot `git_push` grant with expected-old-ref checking. Direct default
branch updates, deletes, tags, wildcard refspecs, and non-fast-forward or force
updates fail closed by default. `meridiand`, not the Capsule, uses the
purpose-bound `github_api` secret to create or update a pull request for the
verified repository and refs. No standing GitHub token enters a Capsule.
Stable step identities, observed commit/ref/PR state, and content-free audit
make retries idempotent; divergent state is a conflict, not permission to
repeat an ambiguous side effect.

### Local sync and file browsing

Local sync archives may contain traversal, symlink attacks, special files,
decompression bombs, `.git` replacement, unrelated repository content, or
secrets. The target client validates its canonical remote against the Project;
the control plane independently checks the Capsule clone identity. A bounded
archive overlay rejects unsafe entries and preserves the Capsule clone's
`.git`. It is staged and validated before publication. Remote equality reduces
repository confusion but does not make local bytes or history trustworthy.

File list and read operations are rooted, normalized, paginated, byte- and
time-bounded `capsuled` RPCs. They do not follow escaping links or interpret
returned names and bytes as HTML, terminal control, configuration, or
authorization. Sync and browse use only the authenticated private Capsule
transport: no host path, bind mount, Docker `cp`, Docker archive extraction,
`kubectl exec`, or direct PVC mount is allowed.

### Local persistence and filesystem attacks

SQLite, WAL files, and local artifacts may expose metadata or be replaced through symlink and path traversal attacks. Use explicit directories with restrictive permissions, canonicalize paths, avoid following attacker-controlled links, use atomic writes, and keep database and artifacts out of Capsule mounts. Back up SQLite consistently with its WAL state. Local storage is not suitable for mutually untrusted host users without OS-level separation and encryption.

Backup, restore, transcript-key rotation, upgrade, and CAS deletion require a
stopped control plane.
Restore verifies into a sibling temporary directory before atomic publication
and preserves an existing installation on failure. Migrations are forward-only;
a newer schema fails closed, and rollback requires the pre-upgrade backup plus
the old binary. SQLite backup alone does not preserve Docker volumes or
Kubernetes PVC/PV state.

### Observability and release infrastructure

Metrics, traces, health checks, CI logs, SBOMs, provenance, and release metadata
can become data-exfiltration channels. Meridian uses fixed span names and
bounded label allowlists; object IDs, repository names, prompts, paths, diffs,
PTY bytes, tokens, and arbitrary error text are forbidden as labels or
attributes. The unauthenticated Prometheus listener is separate, disabled by
default, and rejects non-loopback exposure without an explicit unsafe opt-in.
OTLP is disabled by default and uses bounded buffering and shutdown.

Release automation runs only for version tags after tests, with least job
permissions, immutable action revisions, checksums, SPDX SBOMs, GitHub
provenance, and keyless signatures. Pull requests receive no signing or package
authority. These controls establish origin and integrity, not code safety,
isolation, or hosted multi-tenancy fitness.

### Denial of service and cost abuse

Fork bombs, disk filling, log flooding, decompression bombs, network loops, runaway model calls, excessive snapshots, and abandoned resources can exhaust capacity or incur cost. Enforce resource and concurrency quotas outside the Capsule, bound logs/artifacts/events, validate archive expansion, set deadlines, meter provider usage, and reconcile and destroy orphaned resources. Administrative cancellation must not depend on a responsive Capsule.

The target idle policy pauses only a quiescent Capsule using a durable activity
clock. Authenticated Thread/Run progress and PTY, preview, sync, browse, setup,
archive, or delivery work count as activity; probes, reconciliation polls, and
passive reads do not. Runtime use wakes a paused Capsule through an idempotent
resume. Idle pause, absolute provider/Capsule deletion TTL, reusable setup
Moment retention, user Moment retention, and ticket/credential expiry remain
separate clocks: pause cannot renew an absolute TTL or turn destruction into
retention.

Reusable setup Moments are keyed by a versioned prepare hash covering canonical
repository and exact revision, immutable image, setup arguments, relevant
Project configuration, platform, and protocol version. They are verified on
restore and exclude grants, secret material, runtime tokens, transcripts,
local overlays, sessions, and post-setup work. When a Project allowlists
named harness images, the hash uses the image frozen on that Capsule at
spawn, not only the Project default, so official harness packs and mock do
not share a prepare archive. Undeclared mutable setup inputs remain a staleness risk, so
reuse can be disabled and a mismatch runs setup.

Project Thread spawn selects a Capsule image only from the Project's stored
default or allowlisted `harnessImages`. The spawn request carries a harness
name, not an arbitrary image reference. Those references are accepted only
from operator-authored Project create or apply, and apply copies a pack from
the installation catalog advertised by the daemon. Official pack images are
published and Cosign-signed on the same tag-only release as the thin Capsule
image. The catalog advertises registry-qualified tags, or `--official-pack-tag`
when the installation Capsule image is digest-only; it does not copy a Capsule
digest onto a pack reference. Docker still freezes `sha256` at Capsule create.
Operators verify pack images by digest and Cosign like other release images.
Publishing packs is not a hosted Capsule service, and pack images contain no
credentials.

### Audit tampering and repudiation

A Capsule must not be able to modify control-plane audit records. Record authenticated lifecycle, credential, policy, ingress, and delivery actions with timestamps and stable object identities in append-oriented storage. Minimize secret and source content in audit records. Clock, retention, export, and integrity controls must match deployment assurance requirements.

### Residual data and incomplete teardown

Stopped processes, volumes, snapshots, caches, logs, IP addresses, credentials, and provider resources may survive deletion. Destruction must revoke credentials and sessions first, then remove compute, network routes, writable storage, retained artifacts according to policy, and provider-side snapshots. Reconciliation must detect partial cleanup and retry safely. Document storage media sanitization guarantees from each provider.

## Security invariants

These are architecture requirements, including accepted target requirements;
they are not a statement that every planned path is already implemented.

- Untrusted Capsule code never receives the host container-runtime socket or control-plane/provider administrator credentials.
- The target public REST/SSE and ticket-mint surface requires the
  single-principal installation bearer; only content-free probes are open, and
  PTY/preview redemption remains ticket-scoped.
- Authorization and policy are checked at the control plane for every object and privileged action; Meridian does not claim RBAC or multi-tenant isolation.
- Security and durability requirements fail closed when a provider lacks a capability.
- Reusable credential plaintext is not persisted; encrypted named secrets use
  a dedicated externally custodied credential KEK and cannot cross purpose.
- Per-Capsule supervisor bearer tokens are high entropy, stored outside SQLite in mode-0600 provider state, omitted from labels/logs/public APIs, and removed after owned-resource cleanup.
- Kubernetes supervisor tokens exist only in immutable namespaced,
  owner-referenced Secrets with private 0400/0440 projection; cluster/node/backup
  administrator access remains an explicit deployment risk.
- Run prompts, raw PTY bytes, full diffs, setup output, and secret values are
  omitted from normal durable records and logs. Only explicit structured
  Threads persist prompt/tool content, always as authenticated ciphertext.
- Public PTY access uses expiring Run-scoped tickets rather than Capsule supervisor credentials.
- Review UI assets cannot shadow API routes and untrusted diffs are never injected as HTML.
- Preview ingress is loopback-only and exists only with end-to-end bounded Capsule discovery and routing; fake and incomplete providers fail closed.
- Structured adapter transport is non-PTY, exact-versioned, bounded at both
  framing and private HTTP layers, and advertised only by providers that
  implement the protected Capsule transport end to end.
- Network, compute, storage, and lifetime are bounded outside the Capsule.
- Agent Sandbox lifecycle management is never represented as a sandboxing or
  untrusted multi-tenancy guarantee.
- Moments are filesystem-only, immutable, and restored through non-destructive lineage.
- Agent Sandbox portable Moments and previews use owned pod port-forwards to
  the same bounded capsuled archive/loopback semantics as Docker; CSI,
  Ingress, Services, and public pod IPs are not substitutes.
- Local sync preserves `.git`, and sync/file browsing expose neither host paths
  nor provider exec/copy authority.
- Setup-Moment reuse excludes secrets and mutable session state; idle pause,
  absolute lifetime, cache retention, and artifact retention remain distinct.
  Distinct Project harness images use distinct prepare hashes.
- Project Thread spawn uses only the Project default image or an allowlisted
  harness image stored at Project create; clients cannot introduce a new
  image at spawn time. Official pack images are signed release artifacts,
  not a hosted Capsule runtime, and contain no credentials.
- Delivery requires explicit approval, exact-ref one-shot push authority,
  default-branch protection, host-side pull-request API use, and idempotent
  content-free audit.
- Destruction is reconciled to completion and credential revocation does not depend on Capsule cooperation.
- Metrics and trace attributes contain no user content, object identifiers,
  paths, tokens, terminal bytes, diffs, or arbitrary error text.
- Backups never include runtime supervisor credential plaintext; restore and
  retention never overwrite or delete verified live data implicitly.
- Backups never include the installation transcript KEK; retained encrypted
  Threads require separately protected key custody and fail-closed restore
  matching.

## Residual risks

An allowed coding agent or network destination can intentionally exfiltrate any data it can read, including session-only harness or Git grants during their valid operation. A kernel or hardware vulnerability may defeat even a strong sandbox. A compromised control plane, installation bearer, credential KEK, or provider can subvert isolation and audit. The trusted single-user deployment profile reduces operational complexity; it does not make untrusted code safe or support hostile multi-tenancy.

These risks must be stated in deployment documentation and revisited before implementing a provider, credentials, PTY access, Moments, delivery, multi-user access, or internet-facing operation.

## Personal harness setup and provider gateway boundary

Personal imports are hostile, bounded configuration bundles. Known config fields
are parsed and classified, authentication caches are excluded, and unknown or
machine-specific settings require exclusion. Arbitrary instruction and extension
text can still contain sensitive material: heuristics are not a secret detector.
All persisted bundle content uses an authenticated encrypted envelope distinct
from named secrets. Revision metadata contains paths and digests only. The
importer never executes user files or credential helpers on the control plane.

Capsule-owned homes are populated with rooted filesystem operations, bounded
files, and atomic revision installation. No host configuration directory is
mounted. Configuration pins are resolved transactionally; runtime edits cannot
modify saved defaults. Personal homes stay outside workspace snapshot/archive
roots. Installed hooks and extensions have the same hostile-code status as the
repository and inherit the selected provider's isolation limitations.

Provider connections store encrypted upstream keys with a separate authenticated
context. A project/harness grant is required to issue a Capsule gateway lease.
Every gateway request rechecks the connection, grant, Capsule ownership and
Ready state. Tokens are held only in memory and injected into the child
process environment; they expire in 24 hours. Revocation cancels active streams.
The gateway fixes upstream hosts and operation paths, strips caller credentials
and cookies, rejects redirects and query strings, bounds requests/responses,
and imposes request deadlines. It does not log inference payloads. TLS and
Capsule-to-gateway routing are explicit deployment prerequisites. Compromised
Capsule code can spend against its authorized provider connection until revoked
or expired; a scoped gateway is not a guarantee against abusive inference use.

Subscription credentials and MCP account tokens are not imported or managed by
this feature. Native login state remains ephemeral Capsule-local state. Backup
and recovery must retain the database and installation encryption key together;
a missing key must not silently be replaced when saved setups or connections
exist. A daemon restart invalidates gateway leases and requires active harness
sessions to restart. See ADR 0024 and personal-harness-setups.md.

Portable npx MCP dependency preparation executes npm only inside the Capsule,
using exact top-level package versions, a fixed HTTPS registry, disabled lifecycle
scripts, and a two-minute timeout. npm output is discarded. Package contents and
transitive dependencies remain hostile and inherit Capsule isolation and resource
limits. Imported npx tools resolve offline after preparation; this does not impose
an egress policy on arbitrary Capsule processes.

Prepared workspace reuse resolves source identity after cloning inside a fresh
Capsule and binds the cache to the actual image and platform. Project settings are
frozen before preparation; source scripts, dependencies and archives remain hostile.
The private staged setup endpoint accepts no provider/harness credentials. Imported
personal homes and runtime identities are applied after preparation and excluded
from reusable workspace artifacts. Legacy cache keys cannot select new entries.

Browser setup imports are untrusted file content. The authenticated preview route
accepts at most a 4 MiB JSON body and enforces the 256-file, 128 KiB-per-file and
512 KiB aggregate content bounds. It never opens uploaded paths on the server,
executes imports, persists originals, or logs request bodies. No-store preview
responses contain only the sanitized bundle and exclusion reasons. Client-side
filename filtering is a convenience, not an authorization or validation boundary.
Browser-provided files do not expose symlink targets or original executable bits;
selected contents are validated as uploads, rather than claims about local paths.
Sanitization cannot certify arbitrary prose or scripts free of all secrets.

Saved configuration editing exposes decrypted setup content only through the
installation-authenticated, no-store contents endpoint. Read responses include a
consistent resource version; edits retain optimistic concurrency and immutable
Capsule revision pins. The UI does not put contents in persistent browser storage
or its query cache. As with import review, arbitrary prose can contain secrets
that heuristic checks do not detect; the endpoint must not be logged or cached
by deployment intermediaries. Directory handles remain ephemeral and read-only;
allowlisted traversal skips dependency subtrees before enumeration. Browser file
input fallback still enumerates the selected folder before filtering, and the UI
explains this difference. Both paths use the same authoritative server validation.
