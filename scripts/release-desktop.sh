#!/usr/bin/env bash
# Build the unsigned desktop release artifacts into dist/desktop/.
# macOS produces one universal disk image and a zip of Meridian.app.
# Linux produces an amd64 .deb. WebKit does not cross-compile, so there is
# no arm64 Linux package and no AppImage.
#
# Usage: scripts/release-desktop.sh macos-universal|linux-amd64
# Env: VERSION (no leading v), COMMIT, BUILD_DATE (optional).
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: scripts/release-desktop.sh macos-universal|linux-amd64" >&2
  exit 1
fi

target="$1"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="${VERSION:?VERSION is required}"
commit="${COMMIT:-$(git -C "${root}" rev-parse HEAD)}"
build_date="${BUILD_DATE:-$(git -C "${root}" show -s --format=%cI HEAD)}"
out="${root}/dist/desktop"
mkdir -p "${out}"

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "VERSION must be MAJOR.MINOR.PATCH without a leading v: ${version}" >&2
  exit 1
fi

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  else
    shasum -a 256 "$1" | awk '{ print $1 }'
  fi
}

record() {
  local file="$1" name hash
  name="$(basename "${file}")"
  hash="$(sha256_file "${file}")"
  printf '%s  %s\n' "${hash}" "${name}" >> "${out}/checksums-fragment.txt"
}

ldflags="-s -w -buildid= -X github.com/OrlojHQ/meridian/internal/buildinfo.Version=${version} -X github.com/OrlojHQ/meridian/internal/buildinfo.Commit=${commit} -X github.com/OrlojHQ/meridian/internal/buildinfo.Date=${build_date}"

cd "${root}/frontend"
bun install --frozen-lockfile
cd "${root}"
make ui-build
cd "${root}/desktop"
bun install --frozen-lockfile

config="$(mktemp)"
trap 'rm -f "${config}"' EXIT
printf '{"version":"%s"}\n' "${version}" > "${config}"

case "${target}" in
  macos-universal)
    if [[ "$(uname -s)" != "Darwin" ]]; then
      echo "macos-universal must be built on macOS" >&2
      exit 1
    fi
    rustup target add aarch64-apple-darwin x86_64-apple-darwin
    mkdir -p "${root}/desktop/src-tauri/binaries"
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -buildvcs=false \
      -ldflags "${ldflags}" -o "${root}/desktop/src-tauri/binaries/meridiand-aarch64-apple-darwin" \
      "${root}/cmd/meridiand"
    CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -buildvcs=false \
      -ldflags "${ldflags}" -o "${root}/desktop/src-tauri/binaries/meridiand-x86_64-apple-darwin" \
      "${root}/cmd/meridiand"
    chmod 0755 "${root}/desktop/src-tauri/binaries"/meridiand-*-apple-darwin
    # --no-sign keeps a runner without a Developer ID from failing the bundle.
    # scripts/desktop-apple-signing.sh exports APPLE_SIGNING_IDENTITY only
    # when the Apple secrets are configured.
    if [[ -z "${APPLE_SIGNING_IDENTITY:-}" ]]; then
      bun run tauri -- build --config "${config}" \
        --target universal-apple-darwin --bundles dmg,app --no-sign
    else
      bun run tauri -- build --config "${config}" \
        --target universal-apple-darwin --bundles dmg,app
    fi
    bundle="${root}/desktop/src-tauri/target/universal-apple-darwin/release/bundle"
    dmg="$(find "${bundle}/dmg" -name '*.dmg' | head -n 1)"
    app="${bundle}/macos/Meridian.app"
    [[ -n "${dmg}" && -d "${app}" ]]
    dmg_name="meridian-desktop_${version}_macOS_universal.dmg"
    zip_name="meridian-desktop_${version}_macOS_universal.app.zip"
    cp "${dmg}" "${out}/${dmg_name}"
    ditto -c -k --keepParent "${app}" "${out}/${zip_name}"
    if command -v syft >/dev/null 2>&1; then
      syft "dir:${app}" -o "spdx-json=${out}/${dmg_name}.spdx.json"
    fi
    record "${out}/${dmg_name}"
    record "${out}/${zip_name}"
    [[ -f "${out}/${dmg_name}.spdx.json" ]] && record "${out}/${dmg_name}.spdx.json"
    ;;
  linux-amd64)
    if [[ "$(uname -s)" != "Linux" ]]; then
      echo "linux-amd64 must be built on Linux" >&2
      exit 1
    fi
    mkdir -p "${root}/bin" "${root}/desktop/src-tauri/binaries"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
      -ldflags "${ldflags}" -o "${root}/bin/meridiand" "${root}/cmd/meridiand"
    triple="$(rustc -vV | awk '/^host:/{ print $2 }')"
    cp "${root}/bin/meridiand" "${root}/desktop/src-tauri/binaries/meridiand-${triple}"
    chmod 0755 "${root}/desktop/src-tauri/binaries/meridiand-${triple}"
    bun run tauri -- build --config "${config}" --bundles deb
    deb="$(find "${root}/desktop/src-tauri/target/release/bundle/deb" -name '*.deb' | head -n 1)"
    [[ -n "${deb}" ]]
    deb_name="meridian-desktop_${version}_linux_amd64.deb"
    cp "${deb}" "${out}/${deb_name}"
    if command -v syft >/dev/null 2>&1; then
      syft "file:${out}/${deb_name}" -o "spdx-json=${out}/${deb_name}.spdx.json"
    fi
    record "${out}/${deb_name}"
    [[ -f "${out}/${deb_name}.spdx.json" ]] && record "${out}/${deb_name}.spdx.json"
    ;;
  *)
    echo "unknown target: ${target}" >&2
    exit 1
    ;;
esac

echo "desktop artifacts:"
find "${out}" -type f -print
