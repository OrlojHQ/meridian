# Meridian Helm chart

This chart installs one Meridian control plane and its namespaced RBAC, PVC,
ClusterIP Service, quotas, and NetworkPolicies. Agent Sandbox v0.5.6 CRDs and
controller are external prerequisites. The chart never installs CRDs,
RuntimeClasses, CSI components, or cluster-scoped RBAC.

Safe defaults deny API-server and Capsule internet egress until CIDRs are
configured, keep preview/PTY without public ingress, use the Restricted Pod
Security shape, and run the control plane non-root with a read-only root
filesystem. Pin both images with `image.digest` and `capsuleImage.digest`.
The restricted UID-10001 init container creates a private data subdirectory on
the fsGroup-mounted PVC; it does not run as root or change the volume root's
ownership.

Follow [`docs/operations.md`](../../../docs/operations.md) for
signature/SBOM/checksum verification, backup-before-upgrade, rollback limits,
deterministic smoke, and uninstall cleanup. The chart supports only the
single-user self-hosted profile; hosted hostile multi-tenancy requires external
security review and an independently validated gVisor/Kata runtime deployment.

See [`docs/kubernetes-agentsandbox.md`](../../../docs/kubernetes-agentsandbox.md)
for installation, RBAC rationale, Secret risks, NetworkPolicy limitations,
storage backup, gVisor/Kata validation, and unsupported CSI Moment semantics.

Validate deterministically with:

```console
make helm-test
```
