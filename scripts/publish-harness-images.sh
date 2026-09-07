#!/usr/bin/env bash
# Publish signed official harness pack images for a release tag.
# Requires the thin Capsule image already published at
# ${REGISTRY}/meridian-capsule:${TAG}. This is not a hosted Capsule service.
set -euo pipefail

if [[ $# -ne 1 || -z "${1:-}" ]]; then
  echo "usage: scripts/publish-harness-images.sh <tag>" >&2
  exit 1
fi

TAG="$1"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REGISTRY="${REGISTRY:-ghcr.io/orlojhq}"
CAPSULE_BASE="${CAPSULE_BASE:-${REGISTRY}/meridian-capsule:${TAG}}"
COMMIT="${COMMIT:-$(git -C "$ROOT" rev-parse HEAD)}"
VERSION="${TAG#v}"
PACKS=(opencode pi claude codex)

for pack in "${PACKS[@]}"; do
  image="${REGISTRY}/meridian-capsule-${pack}"
  dockerfile="${ROOT}/images/capsule-${pack}/Dockerfile"
  if [[ ! -f "$dockerfile" ]]; then
    echo "missing pack Dockerfile: ${dockerfile}" >&2
    exit 1
  fi
  for arch in amd64 arm64; do
    docker buildx build --push \
      --platform "linux/${arch}" \
      --build-arg "CAPSULE_BASE=${CAPSULE_BASE}" \
      --label "org.opencontainers.image.revision=${COMMIT}" \
      --label "org.opencontainers.image.version=${VERSION}" \
      -t "${image}:${TAG}-${arch}" \
      -f "$dockerfile" \
      "$ROOT"
  done
  docker buildx imagetools create \
    -t "${image}:${TAG}" \
    "${image}:${TAG}-amd64" \
    "${image}:${TAG}-arm64"
  cosign sign --yes "${image}:${TAG}"
done
