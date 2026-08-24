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

The default chart creates no Namespace and has empty API-server and Capsule
egress CIDR lists. This is fail-closed but not immediately operational. Set the
target namespace explicitly, label it for the Restricted Pod Security
standard, and configure the routable Kubernetes Service/API endpoint CIDR.
NetworkPolicy cannot select the API server by identity; an IP block can become
stale or include unintended endpoints. Enforce API authorization with RBAC and
use infrastructure-level egress controls where this limitation is unacceptable.
CPU, memory, ephemeral storage, PVC size, namespace quota, operation time, and
TTL are bounded. Kubernetes exposes no portable per-Pod PID resource field;
configure kubelet `podPidsLimit` or runtime policy and validate it separately.

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
  --set image.repository=ghcr.io/orlojhq/meridian \
  --set image.digest=sha256:CONTROL_PLANE_DIGEST \
  --set capsuleImage.repository=ghcr.io/orlojhq/meridian-capsule \
  --set capsuleImage.digest=sha256:CAPSULE_DIGEST \
  --set agentSandbox.runtimeClassName=gvisor \
  --set agentSandbox.storageClassName=WORKSPACE_CLASS \
  --set 'networkPolicy.kubernetesAPIServerCIDRs[0]=10.96.0.1/32'
```

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
authentication, authorization, TLS, origin checks, and rate limits; Meridian's
current public API itself remains unauthenticated.

## Runtime profiles

Agent Sandbox manages a singleton pod and PVC. It does **not** supply a kernel,
VM, or multi-tenant isolation boundary.

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
`agentSandbox.runtimeClassName=gvisor` only after those checks pass.

### Validated Kata profile

Install Kata Containers through the cluster operator's supported method and use
the handler it supplies, for example:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: kata
handler: kata-qemu
```

Validate hardware virtualization, node selection/taints, guest kernel and image
policy, PVC/CSI attachment, memory overhead, port-forward, suspension, and
upgrade behavior. Set `agentSandbox.runtimeClassName=kata` only for the tested
handler. Neither example is installed by the chart or a security certification.

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
cluster-scoped resource, CRD, RuntimeClass, node, exec, Service, Ingress,
workload-controller, or Secret list permission.

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

Meridian currently detects CSI snapshot prerequisites but reports Snapshot and
Clone false. Moment v1 requires portable archive artifacts and cannot safely
encode a CSI VolumeSnapshot. No CSI snapshot is silently replaced by a tar
capture. Docker filesystem Moments remain unchanged.

## Verification

```console
make helm-test
make agentsandbox-integration
```

The integration target skips unless `MERIDIAN_AGENTSANDBOX_TEST=1` and Docker,
Kind, kubectl, Go, and Helm are available. When enabled it uses the pinned Agent
Sandbox v0.5.6 manifest and pinned Kind v1.36.1 node digest, installs the chart,
and tests create/adopt, suspend, resume, delete, and cleanup. It does not
constitute gVisor/Kata or production security validation.
