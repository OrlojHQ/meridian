#!/usr/bin/env bash
# Render the Homebrew formula for a published release from its checksums.txt.
# Writes the formula to stdout. Fails unless every supported archive has
# exactly one SHA-256 entry.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: scripts/render-homebrew-formula.sh <version> <checksums.txt>" >&2
  exit 1
fi

VERSION="$1"
CHECKSUMS="$2"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="${ROOT}/deploy/homebrew/meridian.rb.tmpl"

if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be MAJOR.MINOR.PATCH without a leading v: ${VERSION}" >&2
  exit 1
fi

checksum() {
  local archive="meridian_${VERSION}_$1.tar.gz" matches
  matches="$(awk -v name="${archive}" '$2 == name { print $1 }' "${CHECKSUMS}")"
  if [[ ! "${matches}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "missing or ambiguous checksum for ${archive}" >&2
    exit 1
  fi
  printf '%s' "${matches}"
}

macos_arm64="$(checksum macOS_arm64)"
macos_amd64="$(checksum macOS_amd64)"
linux_arm64="$(checksum linux_arm64)"
linux_amd64="$(checksum linux_amd64)"

sed \
  -e "s/@VERSION@/${VERSION}/g" \
  -e "s/@SHA256_MACOS_ARM64@/${macos_arm64}/g" \
  -e "s/@SHA256_MACOS_AMD64@/${macos_amd64}/g" \
  -e "s/@SHA256_LINUX_ARM64@/${linux_arm64}/g" \
  -e "s/@SHA256_LINUX_AMD64@/${linux_amd64}/g" \
  "${TEMPLATE}"
