#!/bin/sh
# Install Meridian from a published GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh
#
# The same file is attached to each release as install-desktop.sh.
# Pin a version with --version or MERIDIAN_VERSION. --cli installs the
# meridian and meridiand binaries instead of the desktop app.
#
# Test overrides, not part of the public install command: MERIDIAN_RELEASE_BASE,
# MERIDIAN_DRY_RUN, MERIDIAN_OS, MERIDIAN_ARCH, MERIDIAN_APP_DIR, MERIDIAN_BIN_DIR.
set -eu

repo="OrlojHQ/meridian"

usage() {
  cat <<'EOF'
Usage: install-desktop.sh [--version VERSION] [--cli]

Install the Meridian desktop app from the latest published release.
The download is checked against that release's checksums.txt.

  --version VERSION   release to install, such as 0.6.0
  --cli               install the meridian and meridiand binaries only

  curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh
  curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh -s -- --version 0.6.0
  curl -fsSL https://raw.githubusercontent.com/OrlojHQ/meridian/main/scripts/install-desktop.sh | sh -s -- --cli
EOF
}

die() {
  printf '%s\n' "$1" >&2
  exit 1
}

say() {
  printf '%s\n' "$1"
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required."
}

cleanup() {
  if [ -n "${tmp:-}" ]; then
    rm -rf "$tmp"
    tmp=
  fi
}

validate_version() {
  case $version in
    "" | *[!0-9A-Za-z._-]*)
      die "Version must look like 0.6.0."
      ;;
    [0-9]*) ;;
    *)
      die "Version must look like 0.6.0."
      ;;
  esac
}

resolve_version() {
  if [ -n "$version" ]; then
    version=${version#v}
    validate_version
    return
  fi
  if [ -n "${MERIDIAN_RELEASE_BASE:-}" ]; then
    die "Set --version or MERIDIAN_VERSION when MERIDIAN_RELEASE_BASE is set."
  fi
  say "Looking up the latest Meridian release."
  if ! curl -fsSL \
    -H "Accept: application/vnd.github+json" \
    -H "User-Agent: meridian-install" \
    -o "$tmp/release.json" \
    "https://api.github.com/repos/${repo}/releases/latest"; then
    die "Could not look up the latest Meridian release."
  fi
  tag=$(sed -n 's/.*"tag_name": *"\(v[^"]*\)".*/\1/p' "$tmp/release.json" | head -n 1)
  [ -n "$tag" ] || die "Could not read the latest Meridian release version."
  version=${tag#v}
  validate_version
}

release_base() {
  if [ -n "${MERIDIAN_RELEASE_BASE:-}" ]; then
    printf '%s' "$MERIDIAN_RELEASE_BASE"
  else
    printf '%s' "https://github.com/${repo}/releases/download/v${version}"
  fi
}

fetch() {
  url=$1
  dest=$2
  if ! curl -fsSL -o "$dest" "$url"; then
    die "Download failed: ${url}"
  fi
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{ print $1 }'
  else
    die "sha256sum or shasum is required."
  fi
}

require_checksum() {
  name=$1
  count=$(awk -v name="$name" '$2 == name { c++ } END { print c + 0 }' "$tmp/checksums.txt")
  if [ "$count" -ne 1 ]; then
    die "No checksum for ${name} in this release. Desktop packages are published with Meridian releases."
  fi
}

verify_asset() {
  name=$1
  file=$2
  require_checksum "$name"
  want=$(awk -v name="$name" '$2 == name { print $1; exit }' "$tmp/checksums.txt")
  got=$(sha256_file "$file")
  want=$(printf '%s' "$want" | tr 'A-F' 'a-f')
  got=$(printf '%s' "$got" | tr 'A-F' 'a-f')
  if [ "$want" != "$got" ]; then
    die "Checksum mismatch for ${name}."
  fi
  say "Verified ${name}."
}

run_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    need sudo
    sudo "$@"
  fi
}

install_macos_app() {
  zip=$1
  need unzip
  mkdir -p "$tmp/unpack"
  unzip -q "$zip" -d "$tmp/unpack"
  app="$tmp/unpack/Meridian.app"
  [ -d "$app" ] || die "The archive does not contain Meridian.app."
  if [ -n "${MERIDIAN_APP_DIR:-}" ]; then
    dest=$MERIDIAN_APP_DIR
    mkdir -p "$dest"
  elif [ -d /Applications ] && [ -w /Applications ]; then
    dest=/Applications
  else
    dest=${HOME}/Applications
    mkdir -p "$dest"
  fi
  if [ "${MERIDIAN_DRY_RUN:-}" = 1 ]; then
    say "Would install ${dest}/Meridian.app."
    say "To uninstall: rm -rf \"${dest}/Meridian.app\""
    return
  fi
  rm -rf "${dest}/Meridian.app"
  cp -R "$app" "${dest}/Meridian.app"
  if command -v xattr >/dev/null 2>&1; then
    xattr -dr com.apple.quarantine "${dest}/Meridian.app" || true
  else
    die "xattr is required to clear the quarantine flag."
  fi
  say "Installed ${dest}/Meridian.app."
  say "To uninstall: rm -rf \"${dest}/Meridian.app\""
  say "This removes the app. Meridian projects are kept."
}

install_linux_deb() {
  deb=$1
  case $arch in
    x86_64 | amd64) ;;
    *)
      die "The desktop app on Linux is amd64 only. This machine is ${arch}. To install the meridian and meridiand binaries instead, run this script with --cli."
      ;;
  esac
  if ! command -v dpkg >/dev/null 2>&1; then
    die "The Linux desktop app is a .deb package. This system does not have dpkg, so it cannot be installed."
  fi
  if [ "${MERIDIAN_DRY_RUN:-}" = 1 ]; then
    say "Would install ${deb} with apt."
    say "To uninstall: sudo apt remove meridian"
    return
  fi
  pkg=meridian
  if command -v dpkg-deb >/dev/null 2>&1; then
    detected=$(dpkg-deb -f "$deb" Package 2>/dev/null || true)
    if [ -n "$detected" ]; then
      pkg=$detected
    fi
  fi
  if command -v apt >/dev/null 2>&1; then
    run_root apt install -y "$deb"
  elif command -v apt-get >/dev/null 2>&1; then
    run_root apt-get install -y "$deb"
  else
    run_root dpkg -i "$deb"
  fi
  say "Installed the Meridian desktop package."
  say "To uninstall: sudo apt remove ${pkg}"
  say "This removes the app. Meridian projects are kept."
}

cli_bindir() {
  if [ -n "${MERIDIAN_BIN_DIR:-}" ]; then
    bindir=$MERIDIAN_BIN_DIR
  elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    bindir=/usr/local/bin
  else
    bindir=${HOME}/.local/bin
  fi
}

install_cli() {
  archive=$1
  name=$(basename "$archive")
  verify_asset "$name" "$archive"
  cli_bindir
  if [ "${MERIDIAN_DRY_RUN:-}" = 1 ]; then
    say "Would install ${bindir}/meridian and ${bindir}/meridiand."
    say "To uninstall: rm -f \"${bindir}/meridian\" \"${bindir}/meridiand\""
    return
  fi
  tar -xzf "$archive" -C "$tmp" meridian meridiand
  mkdir -p "$bindir"
  cp "$tmp/meridian" "$tmp/meridiand" "$bindir/"
  chmod 755 "$bindir/meridian" "$bindir/meridiand"
  say "Installed ${bindir}/meridian and ${bindir}/meridiand."
  say "To uninstall: rm -f \"${bindir}/meridian\" \"${bindir}/meridiand\""
  case ":${PATH}:" in
    *":${bindir}:"*) ;;
    *) say "Add ${bindir} to PATH if it is not already there." ;;
  esac
}

version=${MERIDIAN_VERSION:-}
cli=0
while [ $# -gt 0 ]; do
  case $1 in
    -h | --help)
      usage
      exit 0
      ;;
    --cli)
      cli=1
      ;;
    --version)
      shift
      [ $# -gt 0 ] || die "Missing version after --version."
      version=$1
      ;;
    --version=*)
      version=${1#--version=}
      ;;
    *)
      die "Unknown option: $1"
      ;;
  esac
  shift
done

need curl
os=${MERIDIAN_OS:-$(uname -s)}
arch=${MERIDIAN_ARCH:-$(uname -m)}
case $os in
  Darwin | Linux) ;;
  *) die "The installer supports macOS and Linux. This machine is ${os}." ;;
esac

tmp=
trap 'cleanup' EXIT
trap 'cleanup; exit 1' INT TERM
tmp=$(mktemp -d "${TMPDIR:-/tmp}/meridian-install.XXXXXX")

resolve_version
base=$(release_base)
fetch "${base}/checksums.txt" "$tmp/checksums.txt"

if [ "$cli" -eq 1 ]; then
  case $os in
    Darwin)
      case $arch in
        arm64 | aarch64) suffix=macOS_arm64 ;;
        x86_64) suffix=macOS_amd64 ;;
        *) die "The command line tools do not have a macOS build for ${arch}." ;;
      esac
      ;;
    Linux)
      case $arch in
        x86_64 | amd64) suffix=linux_amd64 ;;
        aarch64 | arm64) suffix=linux_arm64 ;;
        *) die "The command line tools do not have a Linux build for ${arch}." ;;
      esac
      ;;
  esac
  name="meridian_${version}_${suffix}.tar.gz"
  say "Installing Meridian ${version} command line tools."
  require_checksum "$name"
  fetch "${base}/${name}" "$tmp/${name}"
  install_cli "$tmp/${name}"
  exit 0
fi

say "Installing Meridian desktop ${version}."
case $os in
  Darwin)
    name="meridian-desktop_${version}_macOS_universal.app.zip"
    require_checksum "$name"
    fetch "${base}/${name}" "$tmp/${name}"
    verify_asset "$name" "$tmp/${name}"
    install_macos_app "$tmp/${name}"
    ;;
  Linux)
    case $arch in
      x86_64 | amd64) ;;
      *)
        die "The desktop app on Linux is amd64 only. This machine is ${arch}. To install the meridian and meridiand binaries instead, run this script with --cli."
        ;;
    esac
    name="meridian-desktop_${version}_linux_amd64.deb"
    require_checksum "$name"
    fetch "${base}/${name}" "$tmp/${name}"
    verify_asset "$name" "$tmp/${name}"
    install_linux_deb "$tmp/${name}"
    ;;
esac
