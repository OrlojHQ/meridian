# ADR 0014: Broker encrypted secrets by purpose

- Status: Accepted
- Date: 2026-08-24
- Extends: ADR 0006

## Context

Private clones, authenticated delivery, GitHub pull requests, and selected
harnesses need credentials. A reusable credential copied into a Capsule,
workspace, Moment, transcript, or delivery artifact would turn compromise of
hostile repository code into durable authority. Opaque `secretReferences` need
defined storage, purpose, and delivery semantics before they can be resolved.

## Decision

- Store operator-managed named secrets as authenticated ciphertext. Envelope
  encryption uses a dedicated installation credential KEK kept outside SQLite,
  artifact storage, Moments, and Meridian backups. This KEK is distinct from
  the structured-transcript KEK and has independent custody and rotation.
- Bind every secret to one purpose: `git_https`, `git_push`, `github_api`, or
  `harness_env`. A reference cannot be substituted across purposes, and policy
  validates the requested Project, repository, ref, session, and operation
  before use.
- Perform private HTTPS clone with a clone-time, one-shot Git grant scoped to
  the expected repository identity. Materialize the grant only for the Git
  process, disable ambient credential helpers, and revoke or discard it when
  that process ends.
- Resolve `harness_env` references only while starting the approved harness
  session. Session material is available to that process environment and can
  therefore be stolen by hostile Capsule code, but it is never written to the
  workspace or retained for a later session.
- Reserve `git_push` for one-shot delivery grants and `github_api` for
  host-side GitHub API calls as specified by ADR 0020. Neither grants standing
  Capsule authority.
- Never persist reusable credential plaintext in SQLite, artifacts, Moments,
  archives, transcripts, logs, events, audit records, crash reports, command
  arguments, or generated configuration. Temporary files or descriptors, when
  required by a tool, are private, bounded to the operation, and removed
  regardless of outcome.
- Audit secret identity, purpose, scope, issuance, use, revocation, and outcome
  without recording secret values. Decryption and purpose mismatch fail
  closed.

## Consequences

Losing the credential KEK makes stored named secrets unrecoverable. Compromise
of both the control plane and KEK defeats at-rest protection, and a hostile
process can exfiltrate any temporary grant it receives during its valid
window. Short lifetime and purpose binding reduce that authority but do not
make credential delivery safe against arbitrary egress.

This decision defines target architecture. Existing unresolved
`secretReferences` continue to fail closed until the broker and each
purpose-specific flow are implemented end to end.
