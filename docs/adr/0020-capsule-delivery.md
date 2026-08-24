# ADR 0020: Deliver Capsule work with narrow grants

- Status: Accepted
- Date: 2026-08-24
- Extends: ADRs 0006 and 0014

## Context

Turning reviewed Capsule work into a commit, remote branch, and pull request
crosses from an untrusted workspace into durable source-host side effects.
Repository content or an agent must not acquire delivery authority merely by
requesting it, and retries after ambiguous Git or GitHub responses must not
create duplicate commits, pushes, or pull requests.

## Decision

- Represent delivery as an explicit, authenticated, idempotent intent with
  separately approved optional commit, push, and pull-request steps. Agent or
  repository output can propose values but cannot approve delivery.
- If requested, create the commit inside the Capsule through a bounded
  `capsuled` Git operation after validating repository identity, worktree
  scope, author policy, message bounds, and expected input tree. Do not run
  hooks, a shell command, or a host Git credential helper. Existing commits may
  be selected without creating a new commit.
- Push only with a broker-issued, one-shot `git_push` grant scoped to the
  expected repository and one exact source commit-to-destination ref update.
  Revalidate the remote and expected old ref immediately before push; reject
  additional refspecs, tags, wildcard authority, URL rewrites, and force
  updates by default.
- Protect the repository's default branch by default: delivery targets a
  non-default branch and proceeds through review. Direct default-branch
  updates, branch deletion, non-fast-forward updates, and bypass of source-host
  protection require a future explicit policy decision and are not implied by
  this workflow.
- Create or update the pull request from `meridiand` with a purpose-bound
  `github_api` secret. No standing GitHub API token, Git credential, or
  installation API token enters the Capsule. Bind the call to the verified
  repository, exact pushed head ref, configured base branch, and bounded
  title/body supplied by the approved intent.
- Persist content-free step identities, expected object IDs and refs, approval
  facts, attempts, and outcomes. Before retrying, observe Git and GitHub state:
  accept an already matching commit/ref/PR as success, conflict on divergent
  state, and never guess that an ambiguous side effect did not occur.
- Revoke or discard grants after the single operation and audit approver,
  repository identity, exact refs and commit IDs, policy result, and external
  object identity without storing credential values, source, diffs, or
  unbounded pull-request content.

## Consequences

Delivery has reviewable authority and recoverable retry semantics while the
Capsule never receives general GitHub access. A compromised Capsule can still
alter the proposed commit before the final expected-object checks, so approval
must identify the reviewed tree or commit rather than merely a branch name.
Host-side GitHub API calls reduce token exposure but place the control plane
inside the source-host trust boundary.

This ADR defines the approved target workflow and does not claim that commit,
push, or pull-request delivery is currently implemented.
