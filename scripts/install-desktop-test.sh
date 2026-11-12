#!/bin/sh
# Exercise scripts/install-desktop.sh against a local fixture release.
set -eu

root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
installer=${root}/scripts/install-desktop.sh
work=$(mktemp -d "${TMPDIR:-/tmp}/meridian-install-test.XXXXXX")
cleanup() {
  if [ -n "${server:-}" ]; then
    kill "$server" 2>/dev/null || true
  fi
  if [ -f "${work:-}/bad.pid" ]; then
    kill "$(cat "$work/bad.pid")" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

expect_fail() {
  label=$1
  shift
  if "$@" >"$work/out" 2>"$work/err"; then
    fail "${label} succeeded"
  fi
}

version=1.2.3
rel=${work}/release/v${version}
mkdir -p "$rel" "$work/app/Meridian.app/Contents" "$work/cli" "$work/bin" "$work/applications" "$work/scratch"

printf 'new\n' >"$work/app/Meridian.app/Contents/marker"
ZIP=${rel}/meridian-desktop_${version}_macOS_universal.app.zip
python3 - "$work/app" "$ZIP" <<'PY'
import os, sys, zipfile
os.chdir(sys.argv[1])
with zipfile.ZipFile(sys.argv[2], "w") as z:
    for dirpath, _, files in os.walk("Meridian.app"):
        for name in files:
            path = os.path.join(dirpath, name)
            z.write(path, path)
PY

printf 'not-a-real-deb\n' >"${rel}/meridian-desktop_${version}_linux_amd64.deb"
printf '#!/bin/sh\necho new\n' >"$work/cli/meridian"
printf '#!/bin/sh\necho daemon\n' >"$work/cli/meridiand"
chmod 755 "$work/cli/meridian" "$work/cli/meridiand"
tar -czf "${rel}/meridian_${version}_linux_amd64.tar.gz" -C "$work/cli" meridian meridiand
cp "${rel}/meridian_${version}_linux_amd64.tar.gz" "${rel}/meridian_${version}_linux_arm64.tar.gz"

{
  for file in \
    "meridian-desktop_${version}_macOS_universal.app.zip" \
    "meridian-desktop_${version}_linux_amd64.deb" \
    "meridian_${version}_linux_amd64.tar.gz" \
    "meridian_${version}_linux_arm64.tar.gz"
  do
    hash=$(sha256sum "${rel}/${file}" | awk '{ print $1 }')
    printf '%s  %s\n' "$hash" "$file"
  done
  printf '%s  %s\n' "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" \
    "meridian-desktop_${version}_linux_amd64.missing"
} >"${rel}/checksums.txt"

# A second copy of the deb with a wrong checksum line is the mismatch case.
# The served file matches the good line. Mismatch is tested by editing a copy.

port=8765
while [ "$port" -lt 8800 ]; do
  if python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1', ${port}))" 2>/dev/null; then
    break
  fi
  port=$((port + 1))
done
[ "$port" -lt 8800 ] || fail "no free port"
python3 -m http.server --bind 127.0.0.1 --directory "$work/release" "$port" >"$work/http.log" 2>&1 &
server=$!
i=0
while [ "$i" -lt 50 ]; do
  if curl -fsS -o /dev/null "http://127.0.0.1:${port}/v${version}/checksums.txt" 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 0.1
done
curl -fsS -o /dev/null "http://127.0.0.1:${port}/v${version}/checksums.txt" || fail "fixture server did not start"

base="http://127.0.0.1:${port}/v${version}"
export MERIDIAN_RELEASE_BASE="$base"
export MERIDIAN_VERSION="$version"

"$installer" --help >/dev/null

expect_fail "unknown option" "$installer" --nope
if ! grep -q "Unknown option" "$work/err"; then
  fail "unknown option message"
fi

expect_fail "bad version" env MERIDIAN_VERSION= "sh" "$installer" --version '1.2.3;rm -rf /'
if ! grep -q "Version must look like" "$work/err"; then
  fail "bad version message"
fi

expect_fail "unsupported arch" env MERIDIAN_ARCH=aarch64 "$installer"
if ! grep -q "amd64 only" "$work/err"; then
  fail "unsupported arch message"
fi

# Checksum mismatch: serve the deb under a name whose recorded hash is wrong.
bad=${work}/badrel/v${version}
mkdir -p "$bad"
cp "${rel}/meridian-desktop_${version}_linux_amd64.deb" \
  "${bad}/meridian-desktop_${version}_linux_amd64.deb"
printf '%s  %s\n' "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  "meridian-desktop_${version}_linux_amd64.deb" >"${bad}/checksums.txt"
bad_port=$((port + 1))
python3 -m http.server --bind 127.0.0.1 --directory "$work/badrel" "$bad_port" >"$work/http-bad.log" 2>&1 &
bad_server=$!
printf '%s\n' "$bad_server" >"$work/bad.pid"
i=0
while [ "$i" -lt 50 ]; do
  if curl -fsS -o /dev/null "http://127.0.0.1:${bad_port}/v${version}/checksums.txt" 2>/dev/null; then
    break
  fi
  i=$((i + 1))
  sleep 0.1
done

mkdir -p "$work/scratch"
expect_fail "checksum mismatch" env TMPDIR="$work/scratch" \
  MERIDIAN_RELEASE_BASE="http://127.0.0.1:${bad_port}/v${version}" \
  MERIDIAN_DRY_RUN=1 "$installer"
if ! grep -q "Checksum mismatch" "$work/err"; then
  fail "checksum mismatch message"
fi
find "$work/scratch" -mindepth 1 -print | grep -q . && fail "temp dir leaked"
kill "$bad_server" 2>/dev/null || true

env MERIDIAN_DRY_RUN=1 "$installer" >"$work/out"
grep -q "Verified meridian-desktop_${version}_linux_amd64.deb." "$work/out" || fail "linux verify output"
grep -q "Would install" "$work/out" || fail "linux dry-run output"
grep -q "sudo apt remove meridian" "$work/out" || fail "linux uninstall note"

mkdir -p "$work/fakebin"
cat >"$work/fakebin/xattr" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"${XATTR_LOG:?}"
EOF
chmod 755 "$work/fakebin/xattr"
export XATTR_LOG="$work/xattr.log"
: >"$XATTR_LOG"
env MERIDIAN_OS=Darwin PATH="$work/fakebin:$PATH" MERIDIAN_APP_DIR="$work/applications" \
  "$installer" >"$work/out"
grep -q "Installed ${work}/applications/Meridian.app." "$work/out" || fail "macos install output"
grep -q "To uninstall: rm -rf \"${work}/applications/Meridian.app\"" "$work/out" || fail "macos uninstall note"
grep -q "com.apple.quarantine" "$XATTR_LOG" || fail "quarantine was not cleared"
[ "$(cat "$work/applications/Meridian.app/Contents/marker")" = "new" ] || fail "app contents"
printf 'stale\n' >"$work/applications/Meridian.app/stale"
env MERIDIAN_OS=Darwin PATH="$work/fakebin:$PATH" MERIDIAN_APP_DIR="$work/applications" \
  "$installer" >/dev/null
[ ! -e "$work/applications/Meridian.app/stale" ] || fail "old app was not replaced"
[ "$(cat "$work/applications/Meridian.app/Contents/marker")" = "new" ] || fail "replaced app contents"

env MERIDIAN_BIN_DIR="$work/bin" "$installer" --cli >"$work/out"
grep -q "Installed ${work}/bin/meridian and ${work}/bin/meridiand." "$work/out" || fail "cli install output"
grep -q "rm -f \"${work}/bin/meridian\"" "$work/out" || fail "cli uninstall note"
"$work/bin/meridian" | grep -q new || fail "cli binary"
printf '#!/bin/sh\necho old\n' >"$work/bin/meridian"
chmod 755 "$work/bin/meridian"
env MERIDIAN_BIN_DIR="$work/bin" "$installer" --cli >/dev/null
"$work/bin/meridian" | grep -q new || fail "cli binary was not replaced"

expect_fail "desktop arm64" env MERIDIAN_ARCH=aarch64 "$installer"
env MERIDIAN_ARCH=aarch64 MERIDIAN_BIN_DIR="$work/armbin" "$installer" --cli >"$work/out"
grep -q "Installed ${work}/armbin/meridian" "$work/out" || fail "arm64 cli install"
[ -x "$work/armbin/meridiand" ] || fail "arm64 meridiand"

printf 'ok\n'
