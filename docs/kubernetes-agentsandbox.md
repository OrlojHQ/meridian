# Kubernetes Agent Sandbox operations

## Scope and prerequisites

Meridian supports the upstream Agent Sandbox **v0.5.6** core API
`agents.x-k8s.io/v1beta1`. Install its CRD and controller externally. Meridian's
chart deliberately installs no CRD, webhook, controller, RuntimeClass,
cluster-scoped RBAC, CSI driver, or snapshot controller.

Required:

- Kubernetes 1.30 or newer;
- Agent Sandbox v0.5.6 serving `agents.x-k8s.io/v1beta1`;
- a namespace with enforced ResourceQuota and a NetworkPolicy-capable CNI;
- a dynamic StorageClass supporting `ReadWriteOnce`;
- a Meridian control-plane image and Capsule image pinned by digest; and
- for hostile code, a separately validated sandboxed RuntimeClass.

The default chart creates no Namespace and has empty API-server, provider, and
Capsule egress CIDR lists. This is fail-closed but not immediately operational.
Set the target namespace explicitly, label it for the Restricted Pod Security
standard, and configure the routable Kubernetes Service/API endpoint CIDR plus
the routable CIDRs for `api.openai.com` and `api.anthropic.com` (and any HTTPS
custom endpoint). Capsules already receive in-namespace TCP to the control-plane
provider gateway; they do not receive those provider CIDRs. NetworkPolicy cannot
select the API server or provider hosts by identity; an IP block can become
stale or include unintended endpoints. Enforce API authorization with RBAC and
use infrastructure-level egress controls where this limitation is unacceptable.
CPU, memory, ephemeral storage, PVC size, namespace quota, operation time, and
TTL are bounded. Kubernetes exposes no portable per-Pod PID resource field;
configure kubelet `podPidsLimit` or runtime policy and validate it separately.

The provider bounds each phase of a Capsule's life with its own budget. The
operation timeout (`--agentsandbox-operation-timeout`, default `2m`) covers
each Kubernetes API call: reading, creating, and polling the Sandbox object
and its token Secret. The start timeout (`--agentsandbox-start-timeout`,
default `10m`) covers one wait for the Sandbox to become Ready during create,
including image pull and VM boot; if it expires, the error names the last
startup phase the provider observed. The setup timeout
(`--agentsandbox-setup-timeout`, default `10m`) covers the clone and setup
work inside the Capsule after it is Ready. Their sum must stay below the
Sandbox TTL. A readiness failure is reported as
`Sandbox failed readiness: <reason>` with the condition's short reason;
controller and kubelet message text is never copied into Capsule-facing
errors.

## Install

Install the upstream controller from its pinned release asset:

```console
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.5.6/sandbox.yaml
kubectl -n agent-sandbox-system rollout status deploy/agent-sandbox-controller
```

Then install Meridian. Digest values are strongly recommended:

```console
helm upgrade --install meridian deploy/helm/meridian \
  --namespace meridian \
  --create-namespace \
  --set namespace.create=false \
  --set image.repository=ghcr.io/orlojhq/meridiand \
  --set image.digest=sha256:CONTROL_PLANE_DIGEST \
  --set capsuleImage.repository=ghcr.io/orlojhq/meridian-capsule \
  --set capsuleImage.tag=v0.1.0 \
  --set capsuleImage.digest=sha256:CAPSULE_DIGEST \
  --set agentSandbox.runtimeClassName=gvisor \
  --set agentSandbox.storageClassName=WORKSPACE_CLASS \
  --set providerGatewayURL=https://meridian.example \
  --set 'networkPolicy.kubernetesAPIServerCIDRs[0]=10.96.0.1/32' \
  --set 'networkPolicy.providerEgressCIDRs[0]=192.0.2.1/32' \
  --set 'networkPolicy.capsuleEgressCIDRs[0]=192.0.2.8/32'
```

Replace the example `providerEgressCIDRs` and `capsuleEgressCIDRs` values
with the current routable addresses of `api.openai.com` / `api.anthropic.com`
(and any HTTPS custom endpoint) and of the HTTPS gateway origin. NetworkPolicy
cannot select those hosts by name.

Set `capsuleImage.tag` to the verified release version even when pinning the
thin Capsule image by digest. The chart passes that tag as
`--official-pack-tag` so official harness packs advertise
`ghcr.io/orlojhq/meridian-capsule-{opencode,pi,claude,codex}:<tag>`.

Helm must create the release namespace before it can store release state;
`--create-namespace` handles that bootstrap. Label it for Restricted Pod
Security before admitting Capsules:

```console
kubectl label namespace meridian \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

`namespace.create` is available for GitOps engines that reconcile the rendered
Namespace before namespaced release resources. Do not combine it with Helm's
`--create-namespace` unless the Namespace has Helm ownership metadata.

The chart exposes only a ClusterIP control-plane Service. It creates no public
preview or PTY ingress. Put any user-facing API behind independently configured
TLS, network authorization, origin checks, and rate limits. Meridian's API
requires its installation bearer, but is a single-principal interface rather
than a multi-user authorization system.

## Runtime profiles

Agent Sandbox manages a singleton pod and PVC. It does **not** supply a kernel,
VM, or multi-tenant isolation boundary.

### Isolation classes

Meridian reports an isolation class in its capabilities (ADR 0050):
`container` (shared host kernel), `sandbox` (gVisor's user-kernel boundary
inside the host kernel's process tree), or `microvm` (a hardware-virtualized
guest with its own kernel, Kata Containers). The order is strict:
`container < sandbox < microvm`.

The class is **verified, not declared**. Setting
`agentSandbox.isolationClass=sandbox` or `microvm` also requires
`agentSandbox.runtimeClassName`; meridiand reads that RuntimeClass from the
Kubernetes API and checks its `handler` against a built-in vocabulary:

- `sandbox`: `runsc`, `gvisor`;
- `microvm`: `kata`, `kata-qemu`, `kata-clh`, `kata-dragonball`,
  `kata-qemu-runtime-rs`, `kata-clh-runtime-rs` (the kata-deploy 4.3.1
  handler names for QEMU, Cloud Hypervisor, and Dragonball).

A handler that never isolates (`runc`, `crun`, `youki`) is refused for any
declared class, and so is Firecracker (`kata-fc`): at kata-deploy 4.3.1 it
needs a devmapper snapshotter and block storage that this provider does not
provision. Custom handlers can be added per class with the repeatable
`--agentsandbox-isolation-handler` flag (`agentSandbox.isolationHandlers` in
the chart); never-isolating handlers and handlers that belong to the other
class's built-in set stay refused. When the declaration is empty, meridiand
reports `container` and never reads a RuntimeClass.

Verification is re-run before each Capsule create and before resuming a
paused Capsule; a RuntimeClass whose handler or UID changed after startup
fails with a restart error. Pausing and deleting skip the recheck so a paused
Capsule survives a RuntimeClass change. Adopting an existing Sandbox whose
runtime class differs from the configured one is refused. Capabilities report
only the verified class — never the declared text.

Verifying the class needs `get` on the named RuntimeClass, which is
cluster-scoped. With `agentSandbox.isolationClass` set, the chart renders one
ClusterRole and ClusterRoleBinding pinned to that single resource name;
otherwise the install remains purely namespace-scoped.

Private HTTPS clone and authorized `harness_env` references use the same
authenticated `capsuled` protocol as Docker. Meridian does not create
Kubernetes Secrets for user credentials and does not place those values in the
Sandbox Pod specification or provider environment. The control-plane
credential key remains in the protected Meridian data volume (or the
`--secret-key-file` override) and must be backed up separately.

When pod port-forward transport is available, bounded browsing, portable
workspace export, and exact-ref Delivery use that same private protocol.
`git_push` plaintext exists only for one private push request. `github_api`
plaintext never enters the Sandbox; the control plane performs GitHub lookup
and pull-request creation host-side.

### Validated gVisor profile

Create a RuntimeClass whose handler matches the installed gVisor runtime
(commonly `runsc`), then test on dedicated nodes:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: gvisor
handler: runsc
```

Validation must cover the exact Kubernetes/runtime versions, seccomp behavior,
PVC mount support, DNS/egress policy, process limits, port-forward, suspension,
node drain, and known gVisor compatibility constraints. Set
`agentSandbox.runtimeClassName=gvisor` and
`agentSandbox.isolationClass=sandbox` only after those checks pass.

### Validated Kata profile

Install Kata Containers through the cluster operator's supported method and
use the handler it supplies, for example:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: kata
handler: kata-qemu
```

Validate hardware virtualization, node selection/taints, guest kernel and image
policy, PVC/CSI attachment, memory overhead, port-forward, suspension, and
upgrade behavior. Set `agentSandbox.runtimeClassName=kata` and
`agentSandbox.isolationClass=microvm` only for the tested handler. Neither
example is installed by the chart or a security certification.

Do not claim untrusted multi-tenancy without hardened runtime, node, network,
storage and control-plane boundaries and external review.

## Identity, Secret, and RBAC

All resources are namespace-bound. Meridian refuses to adopt or delete a
Sandbox unless deterministic name, full Capsule annotation, ownership labels,
role, and identity hash agree. Updates retry resource-version conflicts;
deletes use a UID precondition and wait for finalizers.

The Role permits exact lifecycle operations on Sandboxes, token Secret
create/get/delete, pod listing, and pod port-forward creation. The Agent Sandbox
controller—not Meridian—owns PVC creation and deletion. Because CSI Moments are
unsupported, the chart grants no PVC or VolumeSnapshot verbs. It also grants no
cluster-scoped resource, CRD, node, exec, Service, Ingress,
workload-controller, or Secret list permission — with one exception (ADR 0050):
when `agentSandbox.isolationClass` is `sandbox` or `microvm`, the chart adds a
ClusterRole and ClusterRoleBinding whose only rule is `get` on
`node.k8s.io/runtimeclasses` pinned to the configured RuntimeClass name.

Each Capsule gets an immutable Secret containing only its private `capsuled`
bearer token. It is owner-referenced and requests projection mode 0400.
Kubernetes applies the Pod's `fsGroup` to projected volumes and may present the
root-owned file as 0440 so UID/GID 10001 can read it; `capsuled` accepts only
private 0400/0440/0600/0640 shapes. Kubernetes's atomic Secret projection uses
a symlink; `capsuled` resolves it only when the final regular file remains
inside the mounted token directory. Other-user, executable, group-writable,
non-regular, and external symlink targets are rejected. Kubernetes etcd, backup
systems, cluster administrators, node agents, and any principal with Secret
read authority can expose it. Enable Kubernetes at-rest encryption, restrict
namespace RBAC and backups, avoid Secret logging, and rotate by recreating the
Capsule after suspected disclosure.

## Storage, Moments, and backup

Workspace PVCs follow the selected StorageClass reclaim and encryption policy.
Back up SQLite together with WAL state and back up workspace volumes using a
storage-system-consistent process. A Kubernetes backup must preserve CRD,
Sandbox, PVC/PV, Secret, RuntimeClass references, and storage topology; restoring
only SQLite does not restore Capsules.

Agent Sandbox portable Moments, local-sync export, and Clone restore use the
bounded `capsuled` archive contract over authenticated port-forward. Meridian
still does not claim CSI VolumeSnapshot compatibility: provider-native CSI
snapshots are not silently substituted for the portable archive and the chart
grants no VolumeSnapshot verbs.

## Verification

```console
make helm-test
make agentsandbox-integration
```

The integration target skips unless `MERIDIAN_AGENTSANDBOX_TEST=1` and Docker,
Kind, kubectl, Go, and Helm are available. When enabled it uses the pinned Agent
Sandbox v0.5.6 manifest and pinned Kind v1.36.1 node digest, installs the chart,
and tests create/adopt, suspend, resume, delete, and cleanup. It also verifies
the isolation-class path end to end with temporary RuntimeClasses: a `runc`
handler must be refused before any Sandbox is created, and a `kata-qemu`
handler must verify and report `microvm`. None of this constitutes gVisor/Kata
or production security validation: the Kind node runs no real Kata or gVisor
runtime, so the positive case proves the API verification flow only.
