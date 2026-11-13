#!/usr/bin/env bash
# Render the Homebrew cask for the unsigned desktop app from checksums.txt.
# Writes the cask to stdout. The CLI formula is rendered separately and is
# not modified by this script.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: scripts/render-homebrew-cask.sh <version> <checksums.txt>" >&2
  exit 1
fi

version="$1"
checksums="$2"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
template="${root}/deploy/homebrew/meridian-desktop.rb.tmpl"
archive="meridian-desktop_${version}_macOS_universal.dmg"

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be MAJOR.MINOR.PATCH without a leading v: ${version}" >&2
  exit 1
fi

matches="$(awk -v name="${archive}" '$2 == name { print $1 }' "${checksums}")"
if [[ ! "${matches}" =~ ^[0-9a-f]{64}$ ]]; then
  echo "missing or ambiguous checksum for ${archive}" >&2
  exit 1
fi

sed \
  -e "s/@VERSION@/${version}/g" \
  -e "s/@SHA256_MACOS_UNIVERSAL@/${matches}/g" \
  "${template}"
