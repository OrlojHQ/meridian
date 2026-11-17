# ADR 0054: Push through a Station Git gateway

- Status: Accepted; implemented in v0.11.1
- Date: 2026-11-16
- Extends: ADR 0014 (purpose-scoped secrets), ADR 0020 (Capsule delivery), ADR 0051 (one GitHub connection)
- Uses: the gateway listener of ADRs 0027 and 0037

## Context

ADR 0020 pushes an approved commit from inside the Capsule. `meridiand`
resolves a `git_push` secret or, since ADR 0051, the owner's GitHub
connection, and sends it to `capsuled` with the push request. `capsuled` runs
`git push` with the token in the child environment (`MERIDIAN_GIT_PASSWORD`,
read by an askpass helper). The Git invocation is hardened: no hooks, no
credential helpers, no system or global config.

The Capsule image runs `capsuled`, the harness, and everything the harness or
the repository starts as one user (uid 10001). A process can read the
environment of another process with the same uid. A background process left
by a hostile dependency, a prompt-injected agent, or repository code can
therefore read the token while the push runs. ADR 0014 accepted that exposure
for a secret the owner created for one purpose, which can be a fine-grained
token limited to one repository.

ADR 0051 changes what the token usually is. The default push credential is
now the owner's GitHub login, often the host `gh` token. That login usually
reaches every repository and organization the owner can, often with the
`workflow` scope. A leak lets an attacker push to other repositories, change
CI workflows to reach repository secrets, and read private code. The
mechanism is unchanged; the reach of a leaked token is not.

`meridiand` already keeps the pull-request token on the Station. Push is the
remaining step that hands a reusable source-host credential to the Capsule.

## Decision

The Capsule still runs `git push`, but against a Git gateway on the Station
instead of the source host. The gateway adds the credential and forwards the
push upstream. No reusable source-host credential enters the Capsule.

### Route

The gateway is served under `/git-gateway/` on the same listener as the
provider gateway: the dedicated local listener of ADR 0037 on Docker, or
`--provider-gateway-url` on Kubernetes. It speaks Git smart HTTP for
`git-receive-pack` only. Any other path or service returns 404 before
authentication. Capsules already trust this listener's certificate. `capsuled`
passes it to Git with `GIT_SSL_CAINFO`.

### Capability

After the operator approves a push, `meridiand` issues a push capability: a
random value bound to one Delivery, the Project's repository, the source
commit, the destination ref, and the expected old value of that ref. It is
valid for two minutes and is consumed by the first `git-receive-pack` request
that presents it. `capsuled` presents it as the HTTP Basic password through
the existing askpass helper, never in the URL.

A stolen capability can do only the push the operator already approved, once.

### What the gateway checks

The upstream URL comes from the Project's stored repository URL, never from
the Capsule's request. The credential follows ADR 0051's resolution: a named
`git_push` secret, or the owner's active GitHub connection for an HTTPS
`github.com` repository only.

- `GET info/refs?service=git-receive-pack` is forwarded with the credential.
  The ref advertisement goes back to the Capsule. Upstream `Set-Cookie`,
  `WWW-Authenticate`, and redirect headers are dropped.
- `POST git-receive-pack` reads only the command section, at most 64 KiB of
  pkt-lines up to the first flush. It requires exactly one command, and that
  command must be `<expected old> <approved source> <approved ref>`. The old
  value is all zeros for a new branch. The capabilities may not include
  `push-options`. Any `Content-Encoding` is refused, so the gateway never
  checks one byte stream and forwards another.
- The command bytes, then the rest of the body, are streamed upstream
  unchanged. The gateway does not unpack or parse the packfile. GitHub
  validates the objects, and the commit ID binds the approved tree.
- Before a large push, Git sends a lone flush to check its credentials. The
  gateway answers it without using the capability up.
- The body is capped at 1 GiB, a request is bounded to 10 minutes, and at
  most four receive-pack requests run at once. The upstream `report-status`
  streams back to the Capsule, so `capsuled` reads the result as it does
  today.
- `capsuled` pushes with `http.postBuffer` at 64 MiB, so an ordinary push is
  one request with a length. Above that, Git sends the pack chunked after the
  credential probe, and some Git and libcurl builds cut that upload off after
  the first packet; that happens with or without the gateway.

Because the expected old value is in the receive-pack command, the source
host compares it atomically. Today `capsuled` runs `ls-remote` and then
pushes, which leaves a race between the check and the update.

### What changes

- `DeliveryPushRequest` loses `GitCredential` and gains a gateway URL and a
  capability. `capsuled` no longer accepts a source-host credential for push.
- The retry observation from ADR 0020 (accept a remote ref that already
  matches) runs on the Station, through the same credential resolution.
  `capsuled` no longer runs `ls-remote` with a token.
- Every push uses the gateway, whether its credential is a named `git_push`
  secret or the GitHub connection.
- The pull request is still opened by `meridiand` (ADR 0020). Delivery
  audit records are unchanged and never contain the capability.
- A Capsule no longer needs egress to the source host to push. Clone still
  does.

### Where there is no gateway

ADR 0037 leaves the local gateway off for a remote or rootless Docker engine
without `--provider-gateway-url`. There, push fails closed, before approval reaches
the Capsule, with a message to configure a gateway URL.
`--delivery-push=capsule` keeps the ADR 0020 in-Capsule push for that
installation, and `meridiand` logs a warning at startup. It is off by
default.

### Later

A GitHub App (Phase 2 of issue 197) mints a short-lived installation token
scoped to the repository at push time, and the gateway forwards with it. The
Capsule side does not change. Clone can move to the same gateway as
`git-upload-pack` with a read capability. That is a separate decision,
because a clone happens in a fresh Capsule before repository code runs.

## Consequences

- No reusable source-host token enters a Capsule for push. Hostile code in a
  Capsule can at most race the approved push with the same commit, which the
  operator approved.
- The Station gains a request parser reachable from hostile Capsules. It is
  limited to pkt-line framing of one command, bounded, and the packfile passes
  through unparsed. It shares the listener and isolation of the provider
  gateway.
- Push bandwidth passes through the Station. The body is streamed, not held in
  memory or on disk. At most four pushes run at once per Station.
- Push depends on the gateway being reachable, as model traffic already does
  for provider connections. Installations without a gateway need a URL or the
  explicit fallback.
- `capsuled`'s push endpoint and `ports.DeliveryRuntime.PushDelivery` change
  shape. The Docker and Agent Sandbox providers and the fake provider update
  together.

## Alternatives considered

- **Bundle the commit and push from the Station.** `capsuled` exports the
  approved commit as a `git bundle`. `meridiand` fetches the base from the
  source host, unpacks the bundle into a throwaway repository, checks the
  tree, and pushes. It also keeps the token on the Station, but the Station
  would unpack objects a hostile Capsule built. That needs fsck, size limits,
  and a scratch repository per push, plus a base fetch, because a small
  bundle needs history the Station does not have. The gateway forwards the
  pack without unpacking it and needs no base.
- **Run Git as a second uid inside the Capsule.** This blocks reading
  `/proc/<pid>/environ` from the agent's uid. It needs a second user and a
  setuid or privileged launcher in an image that drops capabilities, and the
  token still enters the container.
- **Keep the ADR 0020 push and recommend fine-grained tokens.** This leaves
  the default path, the GitHub connection, exposed, and puts the burden on
  the operator.
- **A credential helper that asks `meridiand` for the token.** The token
  still reaches a process in the Capsule.

## Decided while implementing

1. `--delivery-push=capsule` exists, off by default, for installations that
   cannot reach a gateway. Without it they cannot push at all.
2. Clone stays as it is. It runs in a fresh Capsule before repository code,
   and moving it is a separate decision.
3. The limits are a 1 GiB body, 10 minutes per request, and four concurrent
   pushes. They are constants, not flags, until someone needs them changed.
