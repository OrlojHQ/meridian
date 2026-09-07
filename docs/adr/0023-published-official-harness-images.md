# ADR 0023: Publish official harness pack images

- Status: Accepted
- Date: 2026-08-26
- Extends: ADRs 0011 and 0021

## Context

Official harness packs are thin images: `FROM` the supervisor-only Capsule
image, plus one pinned harness binary and a trusted native PTY profile.
Local `make capsule-*-image` and `make try-*` stay the contributor path.
Operators and first-time users of a published release should pull signed
images, not compile them. Docker remains a trusted-local provider, not the
long-term Capsule runtime and not a hosted Capsule service.

The installation catalog previously derived pack names from the daemon Capsule
image tag alone and dropped the registry, so a GHCR Capsule image advertised
`meridian-capsule-opencode:<tag>` instead of a pullable published reference.
A digest-pinned Capsule image has no tag to copy.

## Decision

- Publish `ghcr.io/orlojhq/meridian-capsule-{opencode,pi,claude,codex}` on the
  same tag-only release as `meridiand` and `meridian-capsule`. Sign those
  manifests with the same keyless Cosign identity. Do not bake pack builds
  into the parallel GoReleaser docker jobs: packs must wait for the published
  thin Capsule image.
- Pack Dockerfiles accept `CAPSULE_BASE` and default to `meridian-capsule:dev`.
  Release builds set `CAPSULE_BASE` to the published thin image for that tag.
- Advertise official packs by replacing only the final repository name and
  keeping the registry/org. Copy the Capsule image tag, or
  `--official-pack-tag` when the installation Capsule image is digest-only.
  Do not copy a Capsule digest onto a pack reference.
- Helm passes `capsuleImage.tag` as `--official-pack-tag` so a digest-pinned
  Capsule image can still advertise release-tagged packs. Operators verify
  pack images by digest and Cosign the same way as other release images.
  Docker still freezes `sha256` at Capsule create.
- Keep one harness per official image. Images contain no credentials. This
  does not create a hosted Capsule runtime or a kitchen-sink image.

## Consequences

Release wall-clock time grows with four multi-arch pack builds. Local
development still builds `:dev` tags. A digest-only Helm install that leaves
`capsuleImage.tag` at `dev` advertises unpublished `:dev` pack tags; operators
must set that tag to the verified release version when pinning the Capsule by
digest. Publishing pack images does not change spawn policy: clients still
select a Project-allowlisted name, never a free image reference.
