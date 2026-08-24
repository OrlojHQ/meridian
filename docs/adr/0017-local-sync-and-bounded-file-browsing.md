# ADR 0017: Sync local work and browse files through `capsuled`

- Status: Accepted
- Date: 2026-08-24

## Context

A Project Thread may need uncommitted work from a user's local checkout, and
review clients need bounded file navigation. Giving the daemon a host path,
copying through a container runtime, or invoking `kubectl exec` would couple
product semantics to one provider and widen privileged host and cluster
authority. Local archives and Capsule files are hostile inputs.

## Decision

- Model local sync as a client-produced, bounded archive overlay applied to the
  prepared Capsule workspace. The archive protocol has explicit entry, byte,
  depth, path, type, compression, and expansion limits and rejects absolute
  paths, traversal, unsafe links, devices, sockets, and other special files.
- Preserve the Capsule clone's `.git` directory and repository identity.
  Overlays cannot create, replace, delete, or descend through `.git`; Git
  metadata and credentials are never imported from the local checkout.
- Before upload, require the client to prove that the local checkout's
  canonical configured remote matches the Project repository identity. The
  control plane independently verifies the Project and Capsule clone identity
  before applying the overlay. Missing, ambiguous, or mismatched identity
  fails closed.
- Transfer and apply the archive only through versioned,
  bearer-authenticated, bounded `capsuled` RPCs exposed by the provider's
  private runtime transport. A partial overlay is staged and validated before
  atomic publication; it never exposes a host filesystem path to the Capsule.
- Provide file listing and file read as separate capsuled RPCs rooted in the
  workspace. Normalize paths, refuse escapes and unsafe links, paginate and
  bound directory results, cap bytes and time, and distinguish text from
  unsupported or truncated binary content.
- Treat names and bytes returned by browsing as hostile, sensitive content.
  They are not interpreted as HTML, terminal control, configuration, or
  authorization and are omitted from logs, metrics, traces, and ordinary
  lifecycle events.
- Do not implement sync or browsing with host bind mounts, host path
  traversal, Docker `cp`, Docker archive extraction, `kubectl exec`, direct PVC
  mounts, or provider-specific shell commands.

## Consequences

Local uncommitted work can enter a fresh Project Thread without replacing the
clone's history or credential configuration, and review clients get one
portable file API across providers. Overlay semantics do not make local files
trusted and do not establish that two repositories have identical history;
the remote-identity check only prevents obvious cross-repository confusion.

Providers advertise these operations only after their private capsuled
transport implements the bounds and atomicity end to end. This ADR does not
claim that local sync or file browsing is currently available.
