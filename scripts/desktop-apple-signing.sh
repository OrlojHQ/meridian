#!/usr/bin/env bash
# Apple code signing and notarization for the desktop release.
#
# TODO: this hook turns on only when the repository secrets exist. Until then
# the desktop app is shipped unsigned. There is no Apple Developer account yet.
#
# Signing secrets (all three, or none):
#   APPLE_CERTIFICATE            base64-encoded Developer ID .p12
#   APPLE_CERTIFICATE_PASSWORD   password for that .p12
#   APPLE_SIGNING_IDENTITY       codesign identity, for example
#                                "Developer ID Application: Example (TEAMID)"
# Notarization secrets (all three, or none; ignored when signing is off):
#   APPLE_ID
#   APPLE_PASSWORD               app-specific password
#   APPLE_TEAM_ID
#
# When signing secrets are set, the certificate is imported into a temporary
# keychain and APPLE_SIGNING_IDENTITY is exported for Tauri. When notarization
# secrets are also set, they are exported and Tauri notarizes the disk image.
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Apple signing hook is macOS-only; leaving the desktop app unsigned."
  exit 0
fi

require_all_or_none() {
  local label="$1"
  shift
  local present=0 missing=0 name
  for name in "$@"; do
    if [[ -n "${!name:-}" ]]; then
      present=1
    else
      missing=1
    fi
  done
  if [[ "${present}" -eq 1 && "${missing}" -eq 1 ]]; then
    echo "Incomplete ${label} secrets. Set all of: $*" >&2
    exit 1
  fi
  [[ "${present}" -eq 1 ]]
}

publish_env() {
  local name="$1"
  if [[ -n "${GITHUB_ENV:-}" ]]; then
    printf '%s=%s\n' "${name}" "${!name}" >> "${GITHUB_ENV}"
  fi
  export "${name}"
}

if ! require_all_or_none "Apple code signing" \
  APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD APPLE_SIGNING_IDENTITY; then
  echo "Apple signing secrets are absent. The desktop app will be unsigned."
  exit 0
fi

workdir="${RUNNER_TEMP:-/tmp}"
cert="${workdir}/meridian-apple-cert.p12"
keychain="${workdir}/meridian-signing.keychain-db"
keychain_password="$(openssl rand -base64 32)"
printf '%s' "${APPLE_CERTIFICATE}" | base64 -D > "${cert}"
security create-keychain -p "${keychain_password}" "${keychain}"
security set-keychain-settings -lut 21600 "${keychain}"
security unlock-keychain -p "${keychain_password}" "${keychain}"
security import "${cert}" -P "${APPLE_CERTIFICATE_PASSWORD}" -A -t cert -f pkcs12 -k "${keychain}"
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "${keychain_password}" "${keychain}"
security list-keychains -d user -s "${keychain}"
security default-keychain -s "${keychain}"
rm -f "${cert}"
unset keychain_password APPLE_CERTIFICATE APPLE_CERTIFICATE_PASSWORD
publish_env APPLE_SIGNING_IDENTITY
echo "Apple signing identity imported. Tauri will sign the desktop app."

if require_all_or_none "Apple notarization" APPLE_ID APPLE_PASSWORD APPLE_TEAM_ID; then
  publish_env APPLE_ID
  publish_env APPLE_PASSWORD
  publish_env APPLE_TEAM_ID
  echo "Notarization secrets are present. Tauri will notarize after signing."
else
  echo "Notarization secrets are absent. The app will be signed and not notarized."
fi
