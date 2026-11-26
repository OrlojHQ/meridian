# ADR 0065: Agent Sandbox reaches capsuled over pod-IP mutual TLS

- Status: Accepted; implemented in v0.18.1
- Date: 2026-11-26
- Amends: ADR 0010 (capsuled is no longer reachable only on pod loopback, and
  native port-forward is no longer the in-cluster transport) and ADR 0018
  (portable Moments, Delivery, and previews no longer require a port-forward)
- Extends: ADRs 0050 and 0064

## Context

ADR 0010 put `capsuled` on pod loopback (`127.0.0.1:7777`) and made native
Kubernetes port-forward the only way `meridiand` reaches it. Every protocol
call goes through it: prepare, Runs, PTY attachment, file browsing, portable
Moment archives, Delivery, and previews.

A run on a real Kata cluster (k3s v1.37.2, kata-deploy 4.3.1, Agent Sandbox
v0.5.6, Meridian v0.18.0) shows this never worked under the `microvm` class.
The container runtime handles a pod port-forward by connecting to `localhost`
in the pod's network namespace on the **host**. Under Kata, `capsuled` runs
inside the guest VM, so nothing listens there. The first `POST /v1/prepare`
fails with `EOF`, the port-forward logs `failed to connect to localhost:7777
inside namespace`, and the Capsule becomes Failed. A plain busybox pod gives
the same result on `kata-qemu`, `kata-clh`, `kata-dragonball`, and both
`-runtime-rs` variants, even with the server bound to `0.0.0.0`. A direct
connection to the pod IP works. Upstream tracks this as kata-containers#1693,
which has been open for years and has no committed fix.

Three things let this ship as validated:

- The Kind integration test runs the provider **outside** the cluster on
  `runc`, where port-forward works. It cannot exercise the path a `microvm`
  installation depends on.
- The Kata validation script (`testing/agentsandbox-kata.sh`) could not get
  past reading the installation token from the distroless image, so step 2
  (Capsule reaches Ready) never ran. That is fixed separately with
  `meridiand installation-token`.
- Validation step 5 requires `capsuled` to refuse connections on the pod IP,
  which rules out every fix that keeps the Kata runtime.

Port-forward has costs beyond Kata. Every byte (PTY streams, archives,
previews) passes through the API server, the kubelet, and the container
runtime's streaming server. They share idle timeouts and connection limits,
and some hardened clusters restrict streaming. Whether gVisor handles
port-forward correctly on every network mode has not been checked on a real
cluster either.

We considered three options:

1. **Exec-based tunnel.** It works under Kata and from outside the cluster.
   It needs `pods/exec`, which RBAC cannot narrow to one command, so Meridian
   could run anything in any pod in the namespace. That reverses ADR 0010's
   rule that Meridian holds no exec authority. Many clusters block exec with
   admission policy, and the tunnel keeps port-forward's API-server path and
   timeouts.
2. **Wait for upstream Kata port-forward.** It has no timeline, and the
   `microvm` class is unusable until then.
3. **Direct pod-IP connection with mutual TLS.** It works on every runtime,
   takes protocol traffic off the API server, needs no new RBAC verbs, and
   can be exercised end to end on Kind.

## Decision

Meridian takes option 3 for every in-cluster Agent Sandbox installation,
whatever its isolation class.

### Listeners

- `capsuled serve` gains `--tls-listen`, `--tls-cert-file`, `--tls-key-file`,
  and `--tls-client-cert-file`. With all four set, it serves the same
  protocol handler on a second listener at `:7778` using TLS 1.3 only. It
  requires and verifies a client certificate against a pool that holds
  exactly one certificate, the one in `--tls-client-cert-file`. A handshake
  without that certificate fails before any HTTP parsing. The bearer token is
  still required on every request, as it is today.
- Any partial set of TLS flags is a startup error. There is no TLS mode
  without client verification.
- The loopback listener on `127.0.0.1:7777` stays the same. It serves the
  `capsuled healthcheck` readiness probe and the port-forward transport that
  remains for out-of-cluster daemons (below).

### Per-Capsule key material

- At create, after the Sandbox exists, `meridiand` generates two ECDSA P-256
  key pairs with `crypto/rand`, each with a self-signed certificate:
  - a **server** certificate whose only DNS SAN is
    `<sandbox-name>.capsule.meridian.invalid`, with `ExtKeyUsageServerAuth`;
  - a **client** certificate with `ExtKeyUsageClientAuth`.

  Both have `NotAfter` set to the Sandbox `shutdownTime` plus one hour.
  Agent Sandbox deletes the Sandbox at `shutdownTime`, so a certificate never
  outlives the Capsule it was made for.
- Both pairs are stored in one immutable Opaque Secret, `<sandbox-name>-tls`,
  owner-referenced to the Sandbox and carrying the same ownership labels and
  annotation as the token Secret. The keys are `tls.crt`, `tls.key`,
  `client.crt`, and `client.key`.
- The Capsule pod mounts that Secret read-only, mode 0400, with `items` that
  project **only** `tls.crt`, `tls.key`, and `client.crt`. `client.key` never
  enters the pod. `meridiand` reads it through the Kubernetes API when it
  connects, the same way it reads the token today. `meridiand` persists no
  key material in its data directory or SQLite.
- Each Capsule has its own pairs. Reading one Capsule's Secret does not let
  anyone talk to, or pose as, another Capsule.

### Connecting

- For each operation, `meridiand` reads the owned pod (with the existing
  ownership and controller checks), takes `status.podIP`, and dials
  `https://<podIP>:7778`. The client's TLS config has `RootCAs` holding only
  that Capsule's server certificate, `ServerName` set to its
  `.capsule.meridian.invalid` name, the client certificate, TLS 1.3 as the
  minimum, and no proxy. Standard verification applies, with no
  `InsecureSkipVerify` and no custom verification callback.
- Pinning to the exact server certificate is what makes the pod IP safe to
  use. If a pod is replaced and its IP is reused by another workload, the
  handshake fails rather than sending the token to the wrong pod.
- `capsuleproto.NewClient` keeps refusing anything other than
  `http://127.0.0.1:`. A separate constructor accepts only `https://` with a
  non-nil pinned TLS config. No client accepts plain HTTP to a non-loopback
  address.
- The provider's per-operation session interface stays the same, so Attach
  and preview keep owning their connection until they close.

### Choosing the transport

- The provider records the transport on the Sandbox as the annotation
  `meridian.orloj.io/transport`, with the value `pod-mtls` or `port-forward`.
  It is set at create and checked with the other ownership markers. Meridian
  never infers the transport from cluster state.
- An in-cluster daemon (`--agentsandbox-in-cluster`) creates every new
  Sandbox with `pod-mtls`.
- An out-of-cluster daemon (kubeconfig) keeps `port-forward`, because it
  generally cannot route to pod IPs. It refuses to start with
  `--agentsandbox-isolation-class=microvm`, with an error that names this
  ADR, because port-forward cannot reach a Kata guest.
- Sandboxes created before this change have no annotation and are treated as
  `port-forward`. ADR 0064's upgrade (suspend, update the PodTemplate,
  resume) moves an official-pack Capsule to `pod-mtls`: it creates the TLS
  Secret, adds the arguments, port, and volume, and then sets the
  annotation. A Capsule on a custom image keeps `port-forward` until it is
  recreated.
- A custom image whose `capsuled` is older than this change rejects the new
  flags and crash-loops, so the Capsule fails at the start timeout. Meridian
  does not fall back to port-forward for it. The Agent Sandbox guide names
  the requirement: a custom image needs a `capsuled` newer than v0.18.0 on an
  in-cluster installation.

### Network policy and RBAC

- The chart's Capsule NetworkPolicy allows ingress on TCP 7778 only from the
  control-plane pod selector and nothing else. The control-plane policy gains
  egress to Capsule pods on TCP 7778.
- With `networkPolicy.enabled=false`, mutual TLS and the token still gate
  every connection. The `microvm` validation keeps requiring an enforcing CNI.
- No RBAC verbs are added. Secret `create`/`get`/`delete` already exist for
  the token Secret. `pods/portforward` stays for out-of-cluster daemons and
  for Sandboxes that still use `port-forward`. A later release may remove it
  from in-cluster installs once no such Sandboxes can remain.

### Validation

- Kata validation step 5 changes to these checks:
  - the Sandbox records `pod-mtls` (Ready already proves the transport:
    prepare runs over it);
  - `capsuled` still does not accept connections on the pod IP at 7777;
  - on 7778, a TLS client without the pinned client certificate, and plain
    HTTP, get no response.
- Step 7 adds that a connection from another Capsule to 7778 times out, so
  the NetworkPolicy, not just the handshake, keeps other pods away.
- Step 8 also checks that the TLS Secret is deleted.
- The Kind integration run (`make agentsandbox-integration`) adds an
  in-cluster Capsule driven through the deployed chart's public API: create
  to Ready (prepare), file listing, workspace export, pause and resume onto a
  new pod IP, and delete including the TLS Secret. That exercises `pod-mtls`
  on `runc` without Kata. NetworkPolicy is disabled on Kind, so only the Kata
  validation checks the ingress rule. The out-of-cluster Go lifecycle test
  keeps covering `port-forward`.
- The Kata profile in `docs/kubernetes-agentsandbox.md` stays marked as not
  validated until the script passes on a real Kata cluster with this
  transport, and that run fills the startup table.

## Consequences

- The `microvm` class becomes usable. The transport is the same on `runc`,
  gVisor, and Kata, and the Kind run exercises the path production uses.
- Protocol traffic stops going through the API server and kubelet streaming,
  which removes their idle timeouts and load from PTY, archive, and preview
  traffic.
- `capsuled` now has a listener on the pod network. Its pre-authentication
  surface is the Go TLS 1.3 stack with required client certificates.
  NetworkPolicy is defense in depth, not the boundary.
- Processes inside a Capsule can read that Capsule's server key, as they can
  already read its token. The TLS layer protects the network path and pod-IP
  reuse, not a Capsule from its own code. A compromised Capsule still cannot
  reach or pose as another Capsule.
- As with the token, cluster administrators, node agents, and anyone with
  Secret read access in the namespace can recover the key material. A node
  compromise can still see or alter that node's Capsules. Nothing here
  supports untrusted multi-tenancy (ADR 0010's bar is unchanged).
- Two transports exist while older Sandboxes and out-of-cluster daemons
  remain. The annotation makes the choice explicit and auditable for each
  Capsule.
- `docs/threat-model.md` changes with this ADR: its statements that pod IPs
  are "not substitutes" and that Moments, previews, and browsing use only
  port-forward now describe pod-IP mutual TLS in-cluster and port-forward
  out-of-cluster, and it gains a section on the pod-network listener and the
  per-Capsule keys.
