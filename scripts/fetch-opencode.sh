#!/bin/sh
# Download the OpenCode binary pinned in images/capsule-opencode/pack.env.
# The capsule image installs that same archive. This copy is for the config
# schema test, which does not build the image.
set -eu

die() {
	echo "fetch-opencode: $*" >&2
	exit 1
}

dest=${1:-.cache/opencode}
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
envfile=$root/images/capsule-opencode/pack.env
[ -f "$envfile" ] || die "missing $envfile"

version= url= arch_amd64= arch_arm64= sha_amd64= sha_arm64=
while IFS= read -r line || [ -n "$line" ]; do
	case $line in '' | '#'*) continue ;; esac
	key=${line%%=*}
	value=${line#*=}
	case $key in
	PACK_VERSION) version=$value ;;
	PACK_URL) url=$value ;;
	PACK_ARCH_AMD64) arch_amd64=$value ;;
	PACK_ARCH_ARM64) arch_arm64=$value ;;
	PACK_SHA256_AMD64) sha_amd64=$value ;;
	PACK_SHA256_ARM64) sha_arm64=$value ;;
	esac
done <"$envfile"

# The pinned archive is the Linux build the Capsule image installs. It cannot
# run on another OS, and the schema test needs to run it.
[ "$(uname -s)" = Linux ] || die "the pinned OpenCode build is Linux-only; run this on Linux (CI does) or in a Linux container"

case $(uname -m) in
x86_64) arch=$arch_amd64 checksum=$sha_amd64 ;;
aarch64 | arm64) arch=$arch_arm64 checksum=$sha_arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac
[ -n "$version" ] && [ -n "$url" ] && [ -n "$arch" ] && [ -n "$checksum" ] || die "pack.env is incomplete"

mkdir -p "$dest"
if [ -x "$dest/opencode" ] && [ -f "$dest/pack.sha256" ] && [ "$(cat "$dest/pack.sha256")" = "$checksum" ]; then
	exit 0
fi

url=$(printf '%s' "$url" | sed "s/{version}/$version/g; s/{arch}/$arch/g")
case $url in *'{'* | *'}'*) die "PACK_URL has an unknown placeholder" ;; esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
	"$url" -o "$work/pack.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
	printf '%s  %s\n' "$checksum" "$work/pack.tar.gz" | sha256sum -c -
else
	printf '%s  %s\n' "$checksum" "$work/pack.tar.gz" | shasum -a 256 -c -
fi
tar -xzf "$work/pack.tar.gz" -C "$work" --no-same-owner
found=$(find "$work" -type f -name opencode ! -path "$work/pack.tar.gz")
[ "$(printf '%s\n' "$found" | wc -l)" -eq 1 ] || die "archive does not contain one opencode binary"
install -m 0755 "$found" "$dest/opencode"
printf '%s\n' "$checksum" >"$dest/pack.sha256"
"$dest/opencode" --version
