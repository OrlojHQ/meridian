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
7. a Capsule and external networks, source hosts, registries, and model services;
8. authenticated ingress and terminal or preview services inside a Capsule; and
9. Capsule-originated coordination requests, pulled by the control plane from
   `capsuled`, and the control plane's delegated actions on other Capsules
   (ADR 0029).

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
- coordination requests from any process in a lead or subagent Capsule,
  agent-authored subagent names and prompts, subagent reports returned to a
  lead, and bounded git patch series a lead fetches from a subagent Capsule;
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

The chart permits DNS, Capsule TCP to the in-namespace control-plane
provider gateway, operator-selected Capsule CIDRs, and operator-selected
control-plane CIDRs for the Kubernetes API and for HTTPS model providers.
Capsules do not receive provider CIDRs, API-server CIDRs, cloud metadata,
default internet egress, or a host Docker socket. Inference to
`api.openai.com:443` and `api.anthropic.com:443` (and HTTPS custom
endpoints) leaves from the control plane through the provider gateway.
NetworkPolicy cannot select the Kubernetes API server or those provider
hosts by authenticated identity, so those CIDRs can become stale or
encompass unintended endpoints. RBAC remains the authorization boundary;
high-assurance deployments need infrastructure-level egress controls.

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
explicit modes, and process-group timeouts. Harness adapters re-resolve that
working directory under `/workspace` with `Lstat` before starting a child or
sending ACP `cwd`: a host path, an escaping path, or a symlink in the chain
fails closed. No shell concatenation is used.
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

Official packs carry a native and a structured profile under one name. A name
defined by a trusted image manifest cannot be defined by the repository in any
interaction mode, so repository YAML cannot attach its own adapter, executable,
or `secretReferences` to an official harness (ADR 0026). The Claude Code and
Codex adapters translate only those CLIs' published stdio protocols: Claude
Code's stream-json control protocol and the Codex app-server JSON-RPC. They
never parse terminal output, reject malformed or oversized upstream records,
refuse upstream requests they do not model (Codex dynamic tools, ChatGPT token
refresh, attestation, and data-collecting MCP elicitations), and deny any
permission choice that is not an explicit allow. Codex's internal sandbox
cannot run in a Capsule, which drops all capabilities and sets
`no-new-privileges`, so the shipped Codex profile disables it and requires
operator approval for every command Codex does not consider read-only. The
Capsule remains the only isolation boundary, with the limits stated above.
The `acp` driver (ADR 0030) speaks the Agent Client Protocol to any agent a
trusted profile names, so it treats every agent record as hostile. Records are
bounded to 16 MiB and must be valid JSON-RPC; responses must correlate with an
outstanding request; text is forced to valid UTF-8 without NUL and truncated to
the frame limits; agent tool and message IDs are replaced by hashes when they
are not safe frame IDs, and session IDs are bounded and validated before they
enter resume state; and queued messages, open permission
requests, tracked tool calls, and plan entries have fixed caps. The adapter
advertises no `fs` or `terminal` client capability and refuses every
agent-to-client request except `session/request_permission`, so an agent
cannot read or write host files or start processes through Meridian.
`session/new` and resume receive only a Capsule-contained cwd; adapters never
inherit a host `Getwd()` path or follow a symlink out of `/workspace`. A
permission answer is always one of the options the agent offered for that
request: the controller's choice is matched by label or option ID, and
anything else selects the agent's reject option or `cancelled`. With
permissions disabled, every request is rejected without a prompt. Updates for
another session and replayed history from `session/load` are dropped. ACP
agents are installed from pinned artifacts at image build time; the driver
never runs a package manager. `--secret-env` values are fresh random strings
generated per launch, never credentials, and are passed only to the agent
process. `opencode acp` also starts a private OpenCode API server on Capsule
loopback at a random port. In OpenCode 2 the agent itself generates a fresh
256-bit Basic-auth password for that server on each launch, keeps it out of
the environment its tools inherit, and binds `127.0.0.1` regardless of
OpenCode configuration; OpenCode 2 has no mDNS advertisement. This keeps
other Capsule processes off the API, but it is not a boundary against code
running as the same user, which can read the password from the server
process's environment. The OpenCode profile no longer sets a password with
`--secret-env`, which has no effect in OpenCode 2. Provider credentials keep
the lease design of ADR 0024.

The DeepSeek Harness pack (`dsh`) is installed from an npm lockfile of about
700 third-party packages. Each is pinned to a `registry.npmjs.org` tarball and
`sha512` integrity that the build verifies, links and other sources are
rejected, and `npm ci --ignore-scripts` runs no package script, but the code
itself is unreviewed upstream code with the authority of the harness. Its
dependency versions are upstream's: the pack cannot override a vulnerable
transitive pin, so known advisories can ship until a `dsh` release fixes them,
and each one the dependency scan accepts is listed with an expiry in
`.trivyignore.yaml`. The build checks `--help` for DeepSeek Harness's own text so Debian's unrelated
`dsh` cannot stand in for it. The launcher disables DeepSeek's product
telemetry. `dsh` applies its own workspace-write sandbox and asks Meridian's
permission prompt for escalations; neither is a Meridian boundary. Its model
traffic uses the lease design: for an `openai-compatible` or
`anthropic-compatible` connection with a default model, `capsuled` writes a
home-level Cordis patch whose only model route is the connection's gateway
URL, reading the lease from `MERIDIAN_PROVIDER_TOKEN`. The patch is JSON, so
no `!!js` tag reaches `dsh`'s evaluating YAML loader, and the model name is
validated. It disables the DeepSeek adapters, which are the only senders of a
`dsh_session_log` copy of the session's events and the only users of
DeepSeek's Files API, the session-log uploader, and DeepSeek web search,
which reads its own `DEEPSEEK_API_KEY`. `web_fetch` stays and is bounded by
the Project's egress policy. Model listing does not run in ACP, and the
gateway refuses anything but `POST` to a known route. The gateway accepts
only the query `beta=true`, only on Anthropic routes, and does not forward it.
Hostile code in the Capsule can rewrite the patch or re-enable those plugins,
but it can only send what it could already send with the lease it holds or
through the Capsule's egress; no upstream key enters the Capsule (ADR 0030).
Profile-registered MCP servers (`--mcp-server`) are trusted profile metadata.
They take absolute executables and quote-free arguments, run with the harness's
authority, and their tool calls still pass through harness permission prompts.
Pack skills (`/usr/local/share/meridian/<pack>/skills/`) are trusted image
content too: the pack installer accepts only bounded `SKILL.md` regular files,
and a profile loads them by absolute path. The Pi pack's `meridian-coord`
skill is instructions only. Pi runs the CLI with its bash tool, which asks no
per-command permission, so a Pi lead spends its grant without prompts, as any
process in a granted Capsule already can (ADR 0029). A repository can ship its
own skills or `AGENTS.md` that mention `meridian-coord`; they are hostile
content but gain nothing the Capsule's processes did not already have.

Slash command lists come from the harness, which reads them from repository
files, skills, plugins, and MCP servers, so they are hostile (ADR 0028). A
`commands` frame is accepted only after both hellos negotiate it, and
`adapterproto` bounds it to 128 entries with restricted, unique names and
aliases, bounded descriptions and hints, and no control characters. The list
is stored only inside the encrypted transcript and rendered as text. It
grants no authority: the controller sends `/name args` as an ordinary user
message, and the adapter maps a name to a harness protocol call (Codex
compaction, review, and skills, Pi compaction) only when it
advertised that name as an action itself. Those calls run with the same harness
permissions as a prompt. Meridian's own controls (stop, new session) are API
calls the UI makes and are never sent to the harness. Model switching is not
exposed, so a reported command cannot move a session off its provider
connection.

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

Model and Capsule output can cause bounded lifecycle effects, namely spawning,
messaging, and cancelling subagent Threads, but only under an operator-created
Coordination Grant on that lead (ADR 0029). Delivery approval, credential, network, and
policy actions are unchanged: an agent may propose a Delivery and cannot
approve one. A subagent's permission and input requests go to
the operator, never to its lead, so a hostile lead cannot approve its subagents'
tool use. Delegated prompts and subagent reports carry encrypted
coordination provenance and are labelled "from the lead" or "from subagents";
they are never presented as operator input. A wake prompt frames subagent
reports as untrusted data, but a model may still follow instructions in them.
Bounded patch bytes are untrusted repository content: a lead must treat them
as data, not as instructions, and must not apply them outside its own Capsule.

### Coordination and delegated authority

No coordination credential exists in a Capsule. `meridian-coord` writes
requests to a `capsuled` mailbox on a private Unix socket (0600 in a 0700
directory under the Capsule user's home). `meridiand` pulls requests over the
existing authenticated private Capsule transport and identifies the requester
by the Capsule it dialed; Capsules never call the public API, receive no host
runtime socket, and gain no new network route, so coordination works under
deny-all egress. The mailbox refuses every request until the daemon
configures it for a lead or a subagent, and again once the grant ends.
A subagent mailbox relays `report`, `propose-delivery`, and `whoami`.

Authority is the intersection of the grant, the Project policy, the
requester's links, and the grant's remaining counters. A grant is created only
by an operator API call together with its lead Thread. The operator may later
loosen or tighten its limits, including `spawnApproval`. Capsules cannot
change a grant. It bounds harnesses (a subset of the Project's structured
allowlist), depth (1), fan-out, total and live subagent Capsules, messages per
link, an absolute deadline, and whether each spawn needs a further operator
click. A request can never name an image, Project, secret, provider
connection, or Capsule; address a Thread or link it did not create; approve a
spawn, permission, or Delivery; change a grant; start a native Run, PTY,
preview, sync, or browse session; act while its lead is paused, not Ready, in
maintenance, or revoked; or start new work after the deadline. Subagents
receive no spawn, send, wait, cancel, or patch authority; they may only
report a bounded summary to their lead. When `spawnApproval` is `operator`, an
accepted spawn stays `pending_approval` with its prompt encrypted under the
child Thread key and is not provisioned until the operator approves it; deny
closes the link without deleting the Capsule. Pending and denied children do
not hold compute. Switching a grant to `auto` does not start queued spawns.

The protocol is closed and versioned; records are bounded (256 KiB, prompts
128 KiB, report summaries 16 KiB, patches 1–4 MiB), unknown fields are
rejected, and the mailbox bounds pending requests, bytes, request rate, and
its reply cache. Mutating requests carry a Capsule-scoped idempotency key
recorded with a request digest in the same transaction as their effect, so
retries do not duplicate spawns, messages, or reports and a reused key with
different content is refused. The only agent-authored plaintext at rest is
the subagent name, limited to `[a-z0-9-]{1,48}`; prompts, reports, and patch
bytes stay out of SQLite plaintext. Report summaries are re-derived from the
child transcript; patch bytes are produced on demand by `capsuled` with Git
hooks disabled and are never stored. Link rows may hold a content-free
diffstat and SHAs. The board still never polls Git.

Revocation, the deadline, and cancellation are daemon-side state changes that
take effect on the next request. Revoking or cancelling cancels the subagent's
run and then pauses its Capsule through the provider without Capsule
cooperation. Closing a link never deletes a subagent Capsule or transcript.
Coordination is advertised only by providers that implement the structured
transport and mailbox end to end.

The board (`GET /board`) adds no authority. It joins rows the API already
returns with the in-memory attention derivation, so it shows statuses, names,
counts and delivery URLs but no prompt, report or transcript text, and it
never polls Git. A subagent that needs the operator is listed under its own
name, so a lead cannot hide a subagent's permission request. Operator messages
sent straight to a subagent are counted on its link (plaintext counters only)
and do not use the lead's message allowance. The lead's next report wake says
that the operator messaged that subagent and how many times, without the text.
The subagent's own next report can still repeat or act on what the operator
said, and that report reaches the lead like any other. Store triggers keep the
counters monotonic so a notice cannot be replayed or suppressed by rewinding
them.

Subagent lifecycle changes add no authority either. The auto-archive sweep
only ends an idle subagent session, pauses its Thread and marks the link; it
never sends a prompt, deletes a workspace or transcript, or resumes a stopped
Capsule, and it skips subagents that need the operator, failed, are working,
have a delivery in flight, or were promoted, so a lead cannot use it to hide a
pending permission request or a failure. Archiving is an operator-set grant
limit. The link is marked before the session is cancelled so
the cancellation is not reported to the lead, and an archived subagent refuses
the lead's messages until the operator resumes it. Promotion is an operator
API call only; a Capsule cannot promote itself or a sibling. A promoted
subagent gains no grant, so its agent's coordination requests are still
refused, and it is no longer stopped by its lead's cancel or revoke, which is
the point of taking it over. Detaching closes the link for good, so the lead
can no longer message it or hear from it, and the Capsule then counts only
toward installation limits. Store triggers keep promotion permanent and refuse
a detach without promotion.

Operator spawn approval and grant updates add no Capsule authority. Approve
and deny are installation-bearer operator APIs; a Capsule cannot approve its
own spawn. Grant PATCH is the same principal as grant create. Tightening a
limit does not cancel work already started; loosening does not start queued
spawns. Denied children stay on disk until the operator deletes them and still
count toward descendants, so a lead cannot free budget by issuing and
withdrawing hostile names.

Proposed Deliveries add no Capsule authority. `propose-delivery` persists an
unapproved Delivery for the requester's own Capsule only; it never binds
HEAD, tree, or Capsule version, never checks delivery secrets, and never
executes. Approve and reject are installation-bearer operator APIs; a Capsule
cannot approve its own proposal. Title and body are hostile bounded plaintext
and are sanitized before display. Recovery and auto-archive skip proposed and
in-flight Deliveries, so a lead cannot use a proposal to hide work or to
force a ship.

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

A prompt crosses the Capsule boundary only from an explicit client mutation
or from a coordination request accepted under an active grant (ADR 0029). The
daemon also sends a lead one coalesced wake prompt when subagent reports are
unread and the lead is idle; report delivery resumes from content-free
sequence acknowledgements, not from content. Reports and delegated prompts
copy content between Threads, re-encrypted under the receiving Thread's key,
so crypto-shredding a subagent does not erase copies already delivered to its
lead, and crypto-shredding a lead does not erase its prompts in subagents.

Thread responses may include a content-free `awaiting` kind and start time
while an active session has an unanswered permission or input request, and a
content-free `turn` (`working` or `idle`) while the session is live. The
daemon derives both in memory from a bounded tail of the decrypted transcript
and never persists, logs, or emits them; they omit request summaries, options,
and prompts, so SQLite and backup exposure are unchanged. Background runtime
refresh ingests frames for Threads nobody is watching into the same encrypted
store; it never sends, resends, or resumes a session (ADR 0025). The browser's
Inbox peek reads one Thread's transcript tail through the same
authenticated block and response endpoints as the Thread view, and only when
the operator expands that row; it is never prefetched, polled, or cached, and
shows agent-authored request text as capped plain text without control or
bidirectional characters. Browser notifications, when the operator opts in,
are derived only from that content-free `awaiting` flag and Capsule failure:
a count plus "Meridian needs you", never Thread titles, prompts, permission
details, or transcript text, because OS notifications surface on lock screens
and in logs. They are local to the open tab; there is no phone push. Prompt
drafts stay in page memory, and browser storage may hold only harness names.

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
resume. Background runtime refresh is a reconciliation poll: it counts as
activity only when ingestion persists agent output, never for heartbeats,
cursors, or gaps, and it skips paused, non-Ready, and maintenance Capsules
rather than waking them. Idle pause, absolute provider/Capsule deletion TTL, reusable setup
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

A compromised lead can spend its grant: subagent Capsules, compute, and model
calls up to the grant's fan-out, total, and live limits, and its messages per
link. The grant deadline is a separate clock from idle pause and Capsule TTL;
when it passes, the default action cancels subagents and pauses them. The
optional `--max-live-capsules` installation cap refuses coordinated spawns
when that many Capsules already hold compute; it does not limit
operator-started Capsules. Mailbox polling is a reconciliation poll: it never
wakes a paused Capsule or counts as activity.

### Audit tampering and repudiation

A Capsule must not be able to modify control-plane audit records. Record authenticated lifecycle, credential, policy, ingress, and delivery actions with timestamps and stable object identities in append-oriented storage. Minimize secret and source content in audit records. Clock, retention, export, and integrity controls must match deployment assurance requirements.

Coordination records content-free events for grant creation, revocation, and
expiry; link creation, reports, report delivery, and closing; forwarded
messages (with their size); and refused requests (with their kind and code).
They carry identifiers, codes, and sizes, never prompts, reports, or subagent
names.

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
- Thread attention (`awaiting`, `turn`) is derived in memory, content-free, and never
  persisted; background runtime refresh never sends, resumes, or wakes work.
- Public PTY access uses expiring Run-scoped tickets rather than Capsule supervisor credentials.
- Review UI assets cannot shadow API routes and untrusted diffs are never injected as HTML.
- Preview ingress is loopback-only and exists only with end-to-end bounded Capsule discovery and routing; fake and incomplete providers fail closed.
- Structured adapter transport is non-PTY, exact-versioned, bounded at both
  framing and private HTTP layers, and advertised only by providers that
  implement the protected Capsule transport end to end.
- Harness-reported slash commands are negotiated, bounded, encrypted with the
  transcript, and grant no authority: an adapter runs only the actions it
  advertised, from an ordinary user message.
- ACP agents get no file-system or terminal client capability, and their
  permission requests resolve only to options they offered.
- Harness adapter working directories are re-checked inside the Capsule:
  they must be a real directory under `/workspace`, with no symlink
  components, and never a host path.
- No coordination credential exists in any Capsule. The daemon pulls
  coordination requests; Capsules never call the public API. Coordination
  authority is a subset of an operator-created grant, which is a subset of
  the operator's authority. Subagents never answer to their lead for
  permissions or input, and Delivery stays operator-approved.
- Capsule `role` is presentation only: it never suppresses attention, never
  grants authority, and closing a link never deletes a subagent's transcript or
  workspace. Coordination is advertised only with end-to-end structured
  transport and mailbox support.
- Harness packs are installed at image build time from pinned checksummed
  archives or sha512-locked npm registry tarballs with install scripts
  disabled; no pack downloads code at Capsule start.
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

Any process in a lead Capsule, not only its agent, can use the coordination
mailbox. A compromised lead can amplify compute and model cost within its
grant, steer subagents within their existing egress, and send them hostile
prompts; hostile subagent reports and patches can prompt-inject the lead.
Subagents run the
same Project's repository for the same operator, so coordination exposes no
new data, and it adds no confidentiality or isolation guarantee.

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
Capsule-to-gateway routing are explicit deployment prerequisites. On
Kubernetes, Capsule NetworkPolicy allows in-namespace TCP to that gateway;
upstream `api.openai.com:443` / `api.anthropic.com:443` CIDRs attach only to
the control plane. Compromised
Capsule code can spend against its authorized provider connection until revoked
or expired; a scoped gateway is not a guarantee against abusive inference use.

Custom endpoint connections (`openai-compatible`, `anthropic-compatible`,
ADR 0027) replace the fixed host with a base URL that the installation
principal sets on the connection. The base URL never reaches a Capsule. The
gateway reads it from the store on each request, and Capsule paths, queries,
and headers cannot change it. The base URL and the plaintext-HTTP flag are
bound into the key envelope's authenticated context, so editing stored
metadata cannot redirect a saved key, and changing the endpoint requires
sending the key again. Custom transports never dial unspecified, link-local
(cloud metadata), or multicast addresses and never follow redirects. Plaintext
`http://` upstreams need both the daemon's
`--provider-gateway-allow-private-http` flag and a per-connection opt-in. They
dial only loopback, RFC 1918, ULA, or CGNAT addresses, checked on the
connected address so DNS rebinding cannot defeat it. Prompts, repository
content, and any upstream key then cross that network unencrypted. Use this
only on trusted LAN or tailnet segments. An operator-chosen base URL turns
the control plane into an HTTP client of that host. Pointing it at an internal
service exposes that service to Capsule-authored POST bodies on the fixed
inference paths. A custom server's responses are as hostile as any other
provider output.

Upstream error responses (4xx/5xx) are not relayed. The gateway returns the
upstream status code and a reason built from at most 16 KiB of the body: one
string field of a JSON error (`message`, `detail`, `error`, `msg`, or
`reason`), or a `text/plain` body. HTML and other content types are dropped.
It removes invalid UTF-8 and format characters (including bidirectional
overrides), turns control characters into spaces, and collapses whitespace.
It then redacts, case-insensitively, the upstream key, the Capsule's lease,
and the upstream hostname, plus URLs, bearer/basic credentials, `key=`-style
assignments, `sk-`-style tokens, and 32-character or longer word tokens.
The result is cut to 300 characters. No upstream headers are copied. The
reason crosses a trust boundary only from the provider to a Capsule that
already receives that provider's full successful responses, so this adds no
new channel. Its purpose is to keep the operator's upstream address and key
out of Capsule-visible output even when a server echoes them in errors.
The redaction is a best-effort filter: a hostile server could still spell a
secret in a form these patterns miss, so it is not a guarantee against an
upstream that wants to leak data it holds.

Subscription credentials and MCP account tokens are not imported or managed by
this feature. Native login state remains ephemeral Capsule-local state.
Structured sessions cannot sign in interactively; official structured profiles
rely on the provider connection's gateway lease. The lease reaches the harness
only through its process environment, and Meridian never writes it to the
setup home's native configuration, repository YAML, adapter arguments, or
transcripts. A harness that prints its own environment can still expose the
lease in its output. Backup
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

DeepSeek Harness YAML is parsed without evaluating tags. Files that contain
`!!js` are excluded; sanitized YAML is rewritten as JSON, which `dsh`'s YAML
loader accepts. `.credentials.yaml` is never an allowed import path.

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
