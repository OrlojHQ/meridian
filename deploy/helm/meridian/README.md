# Meridian Helm chart

This chart installs one Meridian control plane and its namespaced RBAC, PVC,
ClusterIP Service, quotas, and NetworkPolicies. Agent Sandbox v0.5.6 CRDs and
controller are external prerequisites. The chart never installs CRDs,
RuntimeClasses, CSI components, or unscoped cluster-scoped RBAC.

When `agentSandbox.isolationClass` is `sandbox` or `microvm`, the chart adds
exactly one cluster-scoped grant: a ClusterRole and ClusterRoleBinding that
let the daemon `get` the single RuntimeClass named in
`agentSandbox.runtimeClassName` (ADR 0050). The default install — and any
install that leaves `isolationClass` empty — renders no cluster-scoped
resource at all.

`requireIsolation` sets the installation-wide floor (`--require-isolation`):
the daemon refuses to start unless the provider verified at least that class.

Safe defaults deny API-server, model-provider, and Capsule internet egress
until CIDRs are configured. Capsules may reach the in-namespace provider
gateway on the control-plane Service; they do not receive provider CIDRs,
a host Docker socket, or default internet egress. The chart keeps
preview/PTY without public ingress, uses the Restricted Pod Security
shape, and runs the control plane non-root with a read-only root
filesystem. Pin both images with `image.digest` and `capsuleImage.digest`.
When the Capsule image is digest-pinned, set `capsuleImage.tag` to the
verified release version so official harness packs advertise
`ghcr.io/orlojhq/meridian-capsule-{opencode,pi,claude,codex}:<tag>`.
The restricted UID-10001 init container creates a private data subdirectory on
the fsGroup-mounted PVC; it does not run as root or change the volume root's
ownership. `podSecurityContext.fsGroupChangePolicy` is fixed at
`OnRootMismatch`: a recursive fsGroup chown on each start would add group
access to the 0700/0600 key and token files, and `meridiand` refuses to
start on those modes.

The daemon explicitly opts into its authenticated wildcard pod listener. Its
256-bit installation API token is atomically generated in the private data
directory and persists on the control-plane PVC. The chart supports default
single-user self-hosting and one team's private per-user work (ADR 0046).
Team mode does not provide hostile multi-tenancy: Capsules share the selected
runtime's isolation, and a shared kernel remains a shared kernel.

In single-user mode, authorized operators can retrieve the installation token
with `kubectl exec` for a port-forwarded CLI or the UI's bearer-to-cookie
bootstrap. The image is distroless, so ask `meridiand` to print it:

```sh
kubectl -n <namespace> exec deployment/<release>-meridian -- \
  /usr/local/bin/meridiand installation-token \
  --data-dir=/var/lib/meridian-volume/data
```
 In team mode that token is for the one-time claim and break-glass
membership, bootstrap-owner identity recovery, audit, and departed-member
actions. Members use OIDC sessions or personal access tokens; copying the
installation token is not team sign-in.

## Team sign-in

Create an OIDC client with callback
`https://meridian.example.com/auth/oidc/callback`. For a confidential client,
provision its client secret as an existing Kubernetes Secret in the release
namespace, then configure:

```yaml
publicUrl: https://meridian.example.com
auditRetention: 2160h # 90 days; empty or "0" keeps every audit record
oidc:
  issuer: https://id.example.com/realms/meridian
  clientId: meridian
  clientSecret:
    existingSecret: meridian-oidc
    key: client-secret
  requiredGroup: meridian-users
  groupsClaim: groups
```

All values are optional and default to empty, except the Secret key, which
defaults to `client-secret`. Set the issuer, client id, and `publicUrl` together.
Without a required group, every identity the issuer authenticates can join
once the installation is claimed; configure the group if the issuer serves
people outside the team. An empty `groupsClaim` uses the daemon's `groups`
default. Public OIDC clients may omit `clientSecret.existingSecret`.
`auditRetention` is a nonnegative Go duration such as `2160h`, independently
of OIDC. Flags are emitted only for values that are set.

The chart mounts only the selected Secret key read-only at
`/etc/meridian/oidc/client-secret` on the daemon and passes
`--oidc-client-secret-file`. It creates no Secret, adds no Secret RBAC, and
never puts the secret value in arguments, environment variables, values, or
Capsules. File mode `0440` with the pod's fsGroup lets the non-root daemon
read it. Restart the daemon after rotating the Secret; it reads the file at
startup.

The `agentSandbox` section bounds each phase of a Capsule's life:
`operationTimeout` (default `2m`) per Kubernetes API call, `startTimeout`
(default `10m`) for one Sandbox Ready wait during create including image
pull and VM boot, and `setupTimeout` (default `10m`) for the clone and
setup work inside the Capsule. Their sum must stay below `ttl`.

The chart keeps its ClusterIP Service and creates no public Ingress or TLS
terminator. Supply HTTPS access separately and preserve the external origin
so browser cookies and same-origin checks see the configured `publicUrl`.
Allow the issuer's discovery, token, device, and JWKS destinations through
`networkPolicy.providerEgressCIDRs` on TCP 443, alongside model-provider
CIDRs; those destinations are allowed only to the control plane. Default
egress remains denied. The issuer URLs used here must use HTTPS.

Configuring OIDC does not claim the installation. Follow the **Team mode**
steps in [`docs/operations.md`](../../../docs/operations.md#team-mode): sign
in as the intended bootstrap owner and present the installation token once
in that browser. After the claim, members use **Sign in** or
`meridian login --server https://meridian.example.com` when the issuer
supports device authorization; otherwise mint a personal token in **Account**.
Loopback pages and the installation token no longer mint browser sessions.

Follow [`docs/operations.md`](../../../docs/operations.md) for
signature/SBOM/checksum verification, backup-before-upgrade, rollback limits,
deterministic smoke, and uninstall cleanup. Runtime validation and external
security review remain separate deployment responsibilities.

See [`docs/kubernetes-agentsandbox.md`](../../../docs/kubernetes-agentsandbox.md)
for installation, RBAC rationale, Secret risks, NetworkPolicy limitations,
storage backup, isolation-class verification, gVisor/Kata validation, and
unsupported CSI Moment semantics.

Validate deterministically with:

```console
make helm-test
```
