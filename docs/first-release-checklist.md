# First-release checklist

No step in this checklist authorizes a release from an unreviewed working tree.

- [ ] Confirm every source file is committed and the release commit is reviewed.
- [ ] Confirm the version tag matches `vMAJOR.MINOR.PATCH` and points to the
      intended commit without moving an existing tag.
- [ ] Run `make bootstrap generate check-generated build test lint ui-build
      ui-test helm-test`.
- [ ] Run `go test -race ./... -count=1`, `make docker-integration`, and
      `make release-smoke`.
- [ ] Re-run `make agentsandbox-integration` against the pinned Agent Sandbox
      controller when the release claims that provider.
- [ ] Run `make release-check`, `make release-snapshot`, and
      `make release-docker-validate`; inspect all archives and image platforms.
- [ ] Confirm `git diff --check` and a clean working tree.
- [ ] Review dependency, secret, vulnerability, and generated-code CI results.
- [ ] Review `CHANGELOG.md`, upgrade/rollback notes, migration compatibility,
      SBOM generation, and known limitations.
- [ ] Verify Docker base digests, action commit pins, GoReleaser, Syft, Cosign,
      Go, Bun, Agent Sandbox, and Helm versions against their upstream sources.
- [ ] Confirm release workflow permissions are limited to the tag-only release
      job and no pull-request job receives signing or package-write authority.
- [ ] Inspect the draft GitHub release, archives, checksums, SPDX SBOMs,
      provenance attestations, Sigstore bundles, and multi-architecture manifests.
- [ ] Independently verify at least one archive and both OCI images using the
      exact commands in `docs/operations.md`.
- [ ] Exercise install, upgrade from every migration boundary, backup, restore,
      cleanup, and uninstall on disposable infrastructure.
- [ ] Confirm hosted hostile multi-tenancy remains explicitly unsupported
      pending external security review and a validated hardened runtime.
- [ ] Publish the already verified draft manually; do not sign, tag, or publish
      from a developer workstation.
