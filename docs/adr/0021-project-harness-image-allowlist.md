# ADR 0021: Allowlist named harness images on a Project

- Status: Accepted
- Date: 2026-08-25
- Extends: ADRs 0016 and 0019

## Context

A Project is a host-owned recipe. Today it stores one Capsule
`imageReference`. Structured spawn (`POST /projects/{id}/threads`) and the TUI
launcher pick a harness name, but every Capsule still boots the same image.
Contributors who want OpenCode, Pi, and mock on one repository would
otherwise create a second Project or bake every binary into one image.

Clients must not supply an arbitrary image at spawn time. Image choice is
operator policy: only names the Project already allowlisted.

## Decision

- Keep `imageReference` as the default Capsule image for Capsule creation
  without a harness and for spawn when the Project has no harness-image allowlist.
- Add optional `harnessImages`: a bounded list of `{name, imageReference}`.
  When the list is present, Project Thread spawn and harness-selected Capsule
  creation must pick one of those names. The daemon resolves that name to an
  image; the client cannot pass a free image on the spawn request.
- Freeze the resolved image on the Capsule at intent creation so later Project
  edits do not change an in-flight provision, and so setup-Moment reuse (ADR
  0019) hashes the image that Capsule will actually run.
- Expose the allowlist on Project create (API, CLI `--harness-image
  name=image`) and on later apply (`PATCH /projects/{id}`, TUI `/harness`,
  CLI `project apply-harness`). `/new` picks among applied names.
- Advertise official packs on `GET /capabilities` (`mock` = the daemon
  default image; `opencode`, `pi`, `claude`, and `codex` reuse that
  registry, organization, and tag on `meridian-capsule-opencode`,
  `meridian-capsule-pi`, `meridian-capsule-claude`, and
  `meridian-capsule-codex`). ADR 0023 publishes those pack images on the
  same release tag. Applying a pack copies that name and image onto the
  Project. Clients still cannot invent an image at spawn time.

## Consequences

One Project can offer several official or project-local harness packs without
a kitchen-sink image. Setup cache keys already include image identity; distinct
allowlisted images stay in separate prepare hashes. Operators still own which
images exist. This is not a path for Capsule code or untrusted YAML to add
images.
