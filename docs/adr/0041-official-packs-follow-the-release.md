# ADR 0041: Official harness packs follow the installation release

- Status: Accepted
- Date: 2026-11-11

## Context

A Project stores each allowlisted harness as a name and a full image
reference. Applying an official pack copies the reference the daemon's catalog
advertised at that moment, such as
`ghcr.io/orlojhq/meridian-capsule-codex:v0.4.0`. New Capsules used that stored
reference. Upgrading `meridiand` changed the catalog, but not the references
already saved on Projects. The launcher's "Add agent" list leaves out a harness
the Project already has, and `meridian project apply-harness PROJECT_ID NAME`
had to be run per Project and per harness to move it.

So after an upgrade, existing Projects kept starting new Capsules from the old
pack. A pack image is built on the Capsule supervisor image, so it carries its
own `capsuled`. An old pack therefore means:

- the Go toolchain of the old release. GO-2026-6859, fixed in 0.4.1, is
  reachable from `capsuled`;
- the old personal-setup rules (ADR 0040) and gateway trust handling, so
  features added on the daemon side are missing in new Capsules;
- eventually, a `capsuled` protocol version the daemon refuses, at which point
  the harness stops starting until someone edits every Project.

The pinning had no benefit that is not already provided elsewhere. Each
Capsule records the image `sha256` it resolved at create, and recreation,
Moments, and prepared environments use that digest.

## Decision

When a new Capsule is created for a Project's harness, an official pack
resolves to the daemon's current catalog image. A stored reference is an
official pack when:

- its harness name matches a catalog pack;
- it names the same registry and repository as that catalog pack, such as
  `ghcr.io/orlojhq/meridian-capsule-codex`; and
- it has no digest and its tag is a release tag (`v0.4.0`, or a prerelease
  such as `v0.5.0-rc.1`).

`domain.ResolveHarnessImage` applies this rule in `internal/app` for native
Capsules, Project Threads, and coordinated subagents. Interaction-mode checks
use the same match (`domain.CatalogPack`), so an older official pack has the
current pack's modes.

Everything else is used exactly as written: a custom image, a custom tag of an
official repository (`:my-build`), a digest-pinned reference, or an image from
another registry. That is how an operator keeps a specific pack.

The Project's stored allowlist is not rewritten. Resolution happens at create,
so a downgrade of `meridiand` follows the catalog back down the same way.

Existing Capsules keep the image digest they were created with until they are
deleted. The Capsule API now returns the `imageReference` each Capsule was
created from, and the web UI marks a Capsule built on an older release, for
example "Built on v0.4.0", with a note that new Capsules use the current
release. It offers no in-place upgrade.

## Consequences

The first Capsule of each harness after an upgrade pulls the new pack image,
which the startup progress from #82 shows. Its prepared environment is keyed
by image digest, so the Project's setup runs again once per release.

A Project can no longer stay on an old official pack by accident. Keeping one
on purpose now needs a reference that is not a plain release tag of the
official repository, such as a digest pin.

The set of images a Project can start does not grow. A spawn request still
carries only a harness name, and the resolved image is either the reference
the operator stored or the pack the operator's own `meridiand` advertises,
which is a signed release artifact from the same repository.
