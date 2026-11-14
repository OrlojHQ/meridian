# ADR 0046: Per-user identity and ownership on a team installation

- Status: Accepted; not yet implemented
- Date: 2026-11-13
- Amends: ADR 0015 (single-principal API authentication), ADR 0014
  (credential broker), ADR 0029 (bounded Thread coordination), ADR 0036
  (Claude login at the gateway), ADR 0038 (ChatGPT plan connections), and
  ADR 0043 (desktop shell sign-in)
- Tracks: #84

## Context

ADR 0015 authenticates the API as one installation principal. Whoever holds
the installation bearer, or the browser cookie minted from it, can list and
change every Project, Capsule, Thread, Moment, secret, harness setup, and
provider connection. That covers the Claude and ChatGPT sign-ins too. That was
the right boundary for one trusted operator. It is the wrong one for the
deployment people are asking for: one self-hosted `meridiand` per team, with
each teammate's work private to them.

The code has no second principal to check:

- `Server.authenticate` in `internal/httpapi/server.go` accepts one bearer or
  one daemon-lifetime `SameSite=Strict` cookie. Both are installation
  authority. Loopback page loads get that cookie automatically
  (`bootstrapLoopbackBrowser`).
- `internal/app` list methods (`ListProjects`, `ListCapsules`, `ListSecrets`,
  `ListProviderConnections`, `ListHarnessSetups`, the board, coordination
  requests) return every row. Get and mutate trust any id the caller names.
- No table has an owner column. `secrets.name` is `UNIQUE` across the
  installation (`migrations/0007_credential_broker.sql`).
  `provider_connections` and `harness_setups` are installation-wide.
  `idempotency_outcomes` is keyed by `(scope, key)` only.
- Secret envelopes bind id, name, purpose, envelope version, and resource
  version in their AAD (`internal/secrets/crypto.go`). Connection envelopes
  bind id, provider, credential generation, and revocation. Neither binds an
  owner, so a ciphertext could be reattached to another user's row.
- Provider gateway leases (`internal/providergateway`) carry connection,
  Project, Capsule, harness, and provider. They do not check a user.
- The `events` table has no actor. Nothing records who signed in, who read a
  secret, or who approved a Delivery.

Container or microVM isolation does not fix this. A teammate who holds the
token never needs to escape a Capsule. The API already returns everyone
else's objects. Capsule isolation between teammates is a separate control
(#86).

Meridian should not become an account system, an organization model, or a
hosted multi-tenant service (ADR 0001).

## Decision

### Deployment modes

An installation runs in one of two modes.

- **Single-user mode** is today's product and stays the default. There is
  one user, the bootstrap owner. The installation bearer and the loopback
  cookie authenticate as that user. Nothing changes for an existing install
  that does not configure an identity provider.
- **Team mode** starts when the operator configures an OIDC issuer and the
  installation is claimed (below). One installation is one team. Every
  object belongs to exactly one user and is private to them. There are no
  organizations, tenants, or nested teams.

Team mode is not hostile multi-tenancy. Teammates are authenticated members
of one organization who may not see each other's work. With the Docker
provider, or Agent Sandbox without a validated VM RuntimeClass, their
Capsules still share a kernel. A Capsule escape still compromises every user
on that host. Documentation must say so until the microVM provider (#86)
reports `microvm`.

### Principals

The control plane recognizes four principals. Every request and every
background job runs as exactly one of them.

- **User.** A member authenticated by a session cookie or a personal access
  token. A user sees and changes only objects they own.
- **Break-glass admin.** The installation bearer, in team mode only. It can
  manage membership, revoke sessions and tokens, read the audit log, transfer
  or delete a departed user's objects, and nothing else. It is not accepted
  on Project, Capsule, Thread, secret, connection, or setup endpoints. Every
  use writes an audit record. In single-user mode the bearer authenticates as
  the bootstrap owner, as today.
- **System.** Daemon-internal work: the reconciler, idle pause, transcript
  refresh, coordination mailbox pulls, cleanup sweeps, and startup recovery.
  It cannot be presented over the API. System jobs act on one object at a
  time and record that object's owner. They do not combine rows from
  different owners into one result or event.
- **Capsule.** A supervisor token, a provider gateway lease, or a
  coordination mailbox entry. A Capsule never becomes a user. Its authority
  is its owner's, narrowed by what the lease or Coordination Grant allows.

### Identity provider and membership

- `meridiand` is an OIDC relying party for one issuer the operator runs or
  subscribes to (Authentik, Keycloak, Dex, Okta, Entra ID, Google). The
  configuration is the issuer URL, a client id, a client secret file (kept
  outside SQLite like the other key files), and an optional required group
  claim. Multiple issuers, SAML, and SCIM are out of scope.
- Web sign-in is the authorization code flow with PKCE. The daemon chooses
  the redirect URI. `state`, `nonce`, and the PKCE verifier live in daemon
  memory and are single-use, like the Claude ceremony (ADR 0036). The ID
  token's issuer, audience, signature, expiry, and nonce are checked.
- A user is identified by `(issuer, subject)`. Email and display name are
  display-only and refreshed on each sign-in. They never authorize anything,
  so a reused or changed email cannot inherit another user's objects.
- Membership is a `users` row. With a required group configured, a sign-in
  without that group is refused and creates no row. Without one, every
  identity the issuer authenticates becomes a member on first sign-in. The
  admin can disable a user. Disabling revokes that user's sessions and
  tokens at once and stops their running Capsules' gateway leases.

### Claiming the installation

Team mode does not let the first sign-in become the owner. On an exposed
server that is a race anyone can win.

Instead, the operator claims the installation once. They sign in through
OIDC, then present the installation bearer in the same browser session at a
claim endpoint. The daemon then binds that OIDC identity to the existing
bootstrap owner, turns on team mode, and writes an audit record. The claim
needs both the token and the identity. After the claim, every existing row
belongs to that person. The claim can be made only once. Moving the
bootstrap identity later is a break-glass admin action.

Until the claim, an installation with OIDC configured stays in single-user
mode and refuses other OIDC sign-ins.

### Roles

There are two roles: **member** and **admin**. The bootstrap owner is an
admin. An admin can disable and re-enable users, revoke any user's sessions
and tokens, grant or remove the admin role, and read the full audit log.

An admin cannot read or change another user's Projects, Capsules, Threads,
transcripts, secrets, connections, or setups. A team-wide "see everything"
role would recreate the installation token's authority, so it is not
offered. An admin who needs another user's work asks that user, or uses the
break-glass transfer for a departed user.

### Ownership

- A `users` table holds `id`, `issuer`, `subject`, display fields, role,
  disabled state, and timestamps.
- `owner_user_id` is stored, `NOT NULL`, on every root object: Projects,
  secrets, provider connections, and harness setups. It is also stored on
  every row that belongs to a Project: Capsules, Runs, Threads, messages and
  blocks, Timelines, Moments, Deliveries, preparations and setup caches,
  Coordination Grants, Thread links, and coordination requests. A child's
  owner must equal its parent's. The value is stamped in the same
  transaction as the create and never updated except by the break-glass
  transfer.
- The denormalized owner on child rows lets every list query filter by owner
  in SQL. It also lets a store-level check refuse a write whose parent has a
  different owner, as a second line behind the service check.

### Authorization

- Authorization lives in `internal/app`, the service every client uses.
  `internal/httpapi` authenticates and puts the principal in the request
  context. Every `Service` method reads it, and a call with no principal
  fails closed. Tests cover that every exported method rejects a missing
  principal.
- List methods filter by the caller's user id. Get, mutate, and stream calls
  for an object the caller does not own return the same content-free
  not-found the API returns for a missing object, so ids do not become a
  directory.
- References must stay within one owner. A Project may name only its
  owner's secrets, harness setups, and provider connections. A Capsule
  inherits the Project's owner. A Moment can be restored or forked only into
  the same owner's Project.
- Every stream is filtered by owner: the board, Thread block SSE, Run
  events, activity, coordination requests, and the attention count that
  drives desktop and browser alerts. The `events` table gets an owner column
  for that filter.
- PTY and preview tickets stay ticket-scoped at redemption (ADR 0015).
  Minting checks that the caller owns the Run or Capsule. A ticket records
  the owner and is refused once that user is disabled.
- Coordination (ADR 0029) is owner authority. A Coordination Grant belongs
  to the lead's owner. Subagents it spawns are owned by the same user. They
  use that user's Project, image allowlist, secrets, setups, and provider
  connection. A grant can never reference another user's objects.
  Cross-user coordination is out of scope.

### Names and idempotency

- Secret names are unique per owner: `UNIQUE(owner_user_id, name)`. Two
  users can each have a `github_api` secret. Provider connection and harness
  setup ids stay opaque and are resolved only within the caller's rows.
- Idempotency keys are scoped to the caller. The key becomes
  `(owner_user_id, scope, key)`, so one user's replay key cannot suppress or
  replay another user's mutation.

### Credentials

- Secret and connection envelopes bind `owner_user_id` in their AAD. Secret
  envelopes move to `meridian.secret.v2`. Connection envelopes gain the owner
  next to generation and revocation. A ciphertext moved to another user's row
  fails to open.
- Migration re-wraps every existing envelope with the bootstrap owner in the
  AAD. That runs once at startup while holding the data-directory lease (ADR
  0035), in a transaction, with the KEK already loaded. A failure leaves the
  old rows in place.
- Claude (ADR 0036) and ChatGPT (ADR 0038) sign-in ceremonies record the
  user who started them in their in-memory state. `GET /callback`, the
  code-paste completion, the ChatGPT loopback relay, and the CLI code relay
  complete only that user's ceremony and write only that user's connection.
  `GET /callback` stays unauthenticated. The single-use `state` is what
  authorizes it, and `state` now names one user.
- A provider connection can be granted only to Projects its owner owns. That
  enforces ADR 0038's account-holder condition in code instead of leaving it
  as an operator rule. A shared ChatGPT or Claude login is not possible in
  team mode.
- Gateway leases carry the Capsule's owner. Each gateway request checks that
  the lease's owner still owns the connection and is not disabled.
  Disconnecting a connection revokes only its owner's refresh token.
- Transcript envelopes stay bound to their Thread (ADR 0012). A Thread's
  owner is immutable outside break-glass transfer, so transcripts need no
  re-encryption.
- Keys stay installation-wide. There is one credential KEK and one
  transcript KEK, as in ADR 0014. Per-user data keys and crypto-shredding are
  not in this decision (see Consequences).

### Sessions and client credentials

- **Browser and desktop.** After OIDC sign-in the daemon sets a random
  256-bit session cookie, `HttpOnly`, `SameSite=Strict`, `Secure` off
  loopback, with the `__Host-` prefix when served over TLS. The daemon stores
  only its SHA-256 digest, the user, and the times it was created and last
  used. Sessions expire after 12 hours idle and 7 days absolute, and each
  user can list and revoke theirs. The desktop app (ADR 0043) uses the same
  cookie in its webview and still stores no credential.
- In team mode, `POST /auth/browser-session` no longer accepts the
  installation bearer, and loopback page loads no longer receive a cookie.
  Any local process on a shared server could otherwise become the owner.
  Single-user mode keeps both.
- **CLI and TUI.** `meridian login` uses the OAuth device authorization grant
  when the issuer supports it. Otherwise the user mints a personal access
  token in the web UI and passes it through `--token-file` or
  `MERIDIAN_TOKEN_FILE`, as today. Tokens carry a fixed prefix so secret
  scanners can find them. They are shown once and stored only as digests.
  They expire (default 90 days, maximum 1 year) and can be read-only. They
  cannot be limited to specific Projects in this version.
- The installation token in team mode is break-glass only, as above. It
  stays outside SQLite. It can be rotated with the existing file flags.

### Audit

- An append-only `audit_records` table records the time, principal kind,
  user id, action, object type and id, and result (allowed, denied, failed).
  It holds no secret values, prompts, transcript text, diffs, or file
  contents.
- Recorded actions: sign-in, sign-out, session and token create and revoke,
  claim, role and disable changes, break-glass use, secret create, update,
  and delete, provider connection sign-in and disconnect, Project grants of
  secrets and connections, Delivery approve and reject, spawn approval,
  Coordination Grant changes, and every denied cross-owner access.
- Users read their own records. Admins read all of them. There is no API to
  edit or delete records. Retention is a daemon setting. The table does not
  claim tamper evidence: anyone who can write the database can change it.
  Operators who need that ship records to an external log.
- The content-free coordination events stay. They are not a substitute for
  this record.

### Out of scope

The first team-mode release does not include:

- sharing objects with another user
- organizations or sub-teams
- multiple issuers, SCIM, or SAML
- per-user quotas (#85 leaves a quota interface)
- per-user data keys or an external KMS
- Project-scoped tokens
- cross-user coordination
- hosted multi-tenancy

Each needs its own ADR.

## Consequences

- Existing installs keep working unchanged in single-user mode. The upgrade
  adds owner columns, stamps the bootstrap owner, re-wraps envelopes, and
  rewrites idempotency keys. The schema rule stays forward-only (ADR 0011).
- Every `internal/app` method and every store list query changes signature
  to take a principal. This is the largest part of the work. It can land
  before OIDC: in single-user mode the only principal is the bootstrap
  owner, so behavior stays the same while the plumbing and tests go in.
- The threat model states team mode's requirements now, in "Team mode and
  per-user identity" and short notes in the affected sections. The change
  that ships team mode must then rewrite the single-principal text in:
  - the trust boundaries and principal list
  - the API and event-stream section
  - credential theft, now including per-user binding and the claim
  - coordination authority as owner authority
  - audit and repudiation
  - security invariants, which may no longer say "single-principal
    installation bearer"
  - residual risks
- Residual risks that stay:
  - A host administrator, anyone with the data directory and the KEKs, or a
    compromised `meridiand` can read every user's data.
  - Backups contain every user's ciphertext under one KEK. Deleting a
    departed user's rows does not remove their data from older backups.
    Per-user crypto-shred needs per-user data keys, which this ADR defers.
  - Capsule isolation between teammates is only as strong as the provider.
    Container providers remain a shared kernel (#86).
  - The identity provider becomes part of the trust base. A compromised
    issuer, or a group claim the operator does not control, can mint
    members.
- The desktop shell (#121), remote access (#129), native and mobile clients
  (#87), and production Kubernetes (#85) can use user sessions instead of
  the installation token. None of them stores a long-lived credential except
  a personal access token the user chose to mint.

## Resolved questions

The open questions in #84 are settled as follows:

1. **Bootstrap.** The installation is claimed with OIDC sign-in plus the
   installation token, not by the first sign-in.
2. **Roles.** Member and admin only. Admins manage membership and audit but
   cannot see other users' objects.
3. **Break-glass token.** In team mode it does admin actions only and is
   refused on object APIs.
4. **Sharing.** None in v1.
5. **Departing users.** Disable, then transfer or delete. No crypto-shred in
   v1.
6. **Personal access tokens.** Expire, with a 90-day default and 1-year
   maximum. Read-only scope is offered, Project scope is not.
7. **Membership without a group claim.** Any identity the issuer
   authenticates becomes a member. Operators whose issuer serves people
   outside the team must configure the required group claim.
8. **Session lifetime.** 12 hours idle and 7 days absolute.
