# ADR 0050: Provider isolation classes and verified RuntimeClasses

- Status: Accepted; not yet implemented
- Date: 2026-11-15
- Amends: ADR 0005 (capability advertisement names the isolation class a
  provider actually verified) and ADR 0010 (the Agent Sandbox provider reads
  the node's RuntimeClass and reports the verified class)
- Reaffirms: ADR 0001 (no hypervisor or microVM monitor inside Meridian)
- Tracks: #86

## Context

ADR 0005 already lists isolation class among the capabilities a provider must
advertise, and ADR 0010 defines the Kubernetes Agent Sandbox provider, but no
current provider expresses a machine-readable isolation class. The Docker
provider runs containers that share the host kernel; the Agent Sandbox
provider orchestrates pods on whatever container runtime the node runs, which
may be `runc`, gVisor, or Kata depending on operator configuration. Nothing
forces those facts into the capability set, so a Project cannot state an
isolation requirement, the scheduler cannot fail closed when an installation
cannot meet it, and the API cannot distinguish "sandbox" from "container".

Two failure modes are unacceptable:

- **Advertising a stronger class than the runtime provides.** A gVisor pod is
  sandboxed: it has a distinct user-kernel boundary, but it still runs as one
  host-kernel process tree. A Kata pod runs inside a hardware-virtualized
  guest with its own kernel. Reporting `microvm` for gVisor, or `sandbox` for
  `runc`, would let a hostile or merely misconfigured node silently downgrade
  a security requirement.
- **Silently downgrading a requirement.** ADR 0005 says Meridian does not
  silently downgrade a security requirement. An installation that cannot
  provide a required class must refuse the operation with a clear error, not
  schedule the Capsule anyway.

The Kubernetes Agent Sandbox provider must ground its claim in something
verifiable. A pod's effective isolation is selected by the `RuntimeClass`
named in the pod spec, whose `handler` names the container-runtime
implementation. Meridian therefore verifies the configured RuntimeClass
against a built-in handler vocabulary rather than trusting configuration text.

## Decision

Meridian defines three ordered isolation classes:

- **`container`** — processes share the host kernel with no added user-kernel
  boundary (Docker, and Agent Sandbox pods on `runc`/`crun`/`youki`).
- **`sandbox`** — an additional user-kernel boundary exists inside the host
  kernel's process tree (gVisor `runsc`).
- **`microvm`** — a hardware-virtualized guest with its own kernel (Kata
  Containers under QEMU, Cloud Hypervisor, or Dragonball).

The order is strict: `container < sandbox < microvm`. A provider may report
only a class it verified, and a requirement is satisfied when the verified
class is greater than or equal to the required class. A class is never
downgraded to meet a requirement.

Every provider reports a class in `ProviderCapabilities.Isolation`:

- Docker always reports `container`. Docker is trusted single-user local
  development (threat model, ADR 0005); it has no stronger mode.
- The Agent Sandbox provider declares the class it is configured for
  (`sandbox` or `microvm`) plus the RuntimeClass to verify, then reads that
  RuntimeClass from the Kubernetes API at provider construction and re-checks
  it before each create. It reports the verified class, never the declared
  text. Verification compares the RuntimeClass `handler` against a built-in
  handler set for the declared class:
  - `sandbox`: `runsc`, `gvisor`;
  - `microvm`: `kata`, `kata-qemu`, `kata-clh`, `kata-dragonball`, and the
    `-runtime-rs` variants present at kata-deploy 4.3.1
    (`kata-qemu-runtime-rs`, `kata-clh-runtime-rs`). Firecracker (`kata-fc`)
    is not in the set: at 4.3.1 it requires a devmapper snapshotter and block
    storage that Meridian's Agent Sandbox provider does not provision, so a
    pod on `kata-fc` is not accepted as `microvm` until a follow-up adds
    block-mode workspace support.
- A handler that never isolates — `runc`, `crun`, `youki` — is refused for
  any declared class. An empty declaration is legal and reported as
  `container`: the operator must opt in to a stronger claim, and Meridian
  never infers one.
- An operator may extend the accepted handler list per class. Extensions are
  still constrained: never-isolating handlers stay refused, and a handler
  recognized as a built-in for a *different* class is rejected so a
  mis-declared class cannot launder a weaker boundary.

The daemon grows a `--require-isolation` flag, an installation-wide floor:
when set, `meridiand` refuses to start unless the configured provider's
verified capability is at least that class. Projects grow a
`requiredIsolation` field, set at creation and immutable thereafter. Capsule
creation for a Project checks the requirement against the installation's
verified capability before any side effect — before rows are written, before
the provider is called — and fails with a fixed unsupported error when the
requirement is unmet. The reconciler re-checks the same requirement before
each `provider.Create` so a provider reconfiguration cannot quietly satisfy
the check with a weaker runtime.

The narrow ClusterRole that reads RuntimeClasses is scoped to a single
resource name — the configured RuntimeClass — and renders only when the
declared class is `sandbox` or `microvm`, amending ADR 0010's
cluster-scoped-RBAC posture.

Supply-chain properties of the guest (pinned guest kernel, guest image
integrity, attestation) remain the operator's responsibility; Meridian
verifies only the class the node advertises through the Kubernetes API.

## Consequences

- `ProviderCapabilities.Isolation` becomes API surface: capabilities and the
  Project `requiredIsolation` field are exposed by the HTTP API and CLI, and
  a Project that cannot be scheduled reports why.
- An unmet requirement is a fixed error (`domain.ErrUnsupported` → HTTP 422),
  never a Kubernetes error message.
- Installations that declare `sandbox` or `microvm` gain a small, one-verb,
  one-resource ClusterRole; the default install stays namespace-scoped with
  no cluster-scoped rules, matching the chart's policy tests.
- Deploying Kata is documented as an operator task (kata-deploy, node
  prerequisites, a dedicated RuntimeClass profile); Meridian's own validation
  script runs against a real cluster, and a Kind run does not validate
  isolation.
- Container-class installations keep today's posture. Nothing in this ADR
  claims container isolation supports untrusted multi-tenancy.
- ADR 0005's capability list is unchanged in spirit; this ADR pins the
  vocabulary (`container`/`sandbox`/`microvm`), the ordering, and the
  verification duty for the Kubernetes provider.

## Rejected alternatives

- **Operator allowlist only (no built-in handler sets).** Rejected: an
  operator-supplied list alone invites typos that turn a `microvm`
  declaration into an accepted `runc` node. Built-in sets catch the common
  runtimes; the operator extension exists for everything else.
- **Built-in handler sets only (no operator extension).** Rejected: custom
  runtime-rs builds, forked handlers, and vendor names would be unusable.
- **Direct Firecracker provider (bypassing Kubernetes).** Rejected for now:
  ADR 0001 keeps Meridian out of the VMM business, and issue #86 resolves to
  Kata under Agent Sandbox. A direct VMM provider remains a possible
  follow-up.
- **Accept `kata-fc` as `microvm` now.** Rejected: kata-deploy 4.3.1 ships
  Firecracker only behind a devmapper/block storage profile that Meridian's
  provider does not set up. Accepting the handler would claim a class whose
  workspace path is unvalidated. Follow-up: block-mode workspace support.
