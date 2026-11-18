#!/usr/bin/env bash
# Decisions for scripts/retry-unacquired-release-job.sh.
set -euo pipefail

root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
script=${root}/scripts/retry-unacquired-release-job.sh
release=${root}/.github/workflows/release.yml
retry=${root}/.github/workflows/release-retry.yml
work=$(mktemp -d "${TMPDIR:-/tmp}/meridian-release-retry-test.XXXXXX")
trap 'rm -rf "$work"' EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  if [[ -f "$work/err" ]]; then
    cat "$work/err" >&2
  fi
  exit 1
}

if ! command -v jq >/dev/null 2>&1; then
  fail "jq is required"
fi

macos_name=$(awk '
  /^  desktop-macos:$/ { found = 1; next }
  found && /^    name: / { sub(/^    name: /, ""); print; exit }
  found && /^  [^ ]/ { exit }
' "$release")
[[ -n "$macos_name" ]] || fail "could not read the macOS job name from release.yml"

manifest_name=$(awk '
  /^  desktop-checksums:$/ { found = 1; next }
  found && /^    name: / { sub(/^    name: /, ""); print; exit }
  found && /^  [^ ]/ { exit }
' "$release")
[[ -n "$manifest_name" ]] || fail "could not read the manifest job name from release.yml"

needs=$(awk '
  /^  desktop-checksums:$/ { found = 1; next }
  found && /^    needs:/ { print; exit }
  found && /^  [^ ]/ { exit }
' "$release")
case "$needs" in
  *desktop-macos*) ;;
  *) fail "desktop-checksums must need desktop-macos so a failed-job rerun starts the manifest job" ;;
esac

grep -F "MACOS_JOB_NAME: \"${macos_name}\"" "$retry" >/dev/null \
  || fail "release-retry.yml must set MACOS_JOB_NAME to the release workflow's macOS job"
grep -F "MACOS_JOB_NAME:-${macos_name}" "$script" >/dev/null \
  || fail "decision script default job name must match release.yml"
grep -F "$manifest_name" "$retry" >/dev/null \
  || fail "release-retry.yml must name the manifest job it expects a rerun to start"
rerun_line="gh run rerun \"\${RUN_ID}\" --failed --repo \"\${GH_REPO}\""
grep -F "$rerun_line" "$retry" >/dev/null \
  || fail "release-retry.yml must rerun failed jobs"
grep -F 'actions: write' "$retry" >/dev/null || fail "release-retry.yml needs actions: write"
grep -F 'checks: read' "$retry" >/dev/null || fail "release-retry.yml needs checks: read"

job() {
  jq -nc \
    --arg name "$1" \
    --arg conclusion "$2" \
    --arg runner "$3" \
    --argjson runner_id "$4" \
    --argjson steps "$5" \
    '{
      name: $name,
      conclusion: (if $conclusion == "" then null else $conclusion end),
      runner_name: (if $runner == "" then null else $runner end),
      runner_id: $runner_id,
      steps: $steps
    }'
}

combine() {
  printf '%s\n' "$@" | jq -s '.'
}

linux_ok=$(job "Publish Linux desktop package" success "hosted" 100 '[{"name":"Checkout","conclusion":"success"}]')
manifest_skip=$(job "$manifest_name" skipped "" null '[]')
verify_ok=$(job "Verify release source" success "hosted" 7 '[{"name":"Checkout","conclusion":"success"}]')
acquired='[{"message":"The job was not acquired by Runner of type hosted even after multiple attempts"},{"message":"Waiting for a runner was longer than usual."}]'
none='[]'

around() {
  combine "$1" "$linux_ok" "$manifest_skip" "$verify_ok"
}

expect() {
  local label=$1 attempt=$2 jobs=$3 annotations=$4 want=$5 max=${6:-3}
  local decision
  printf '%s\n' "$jobs" >"$work/jobs.json"
  printf '%s\n' "$annotations" >"$work/annotations.json"
  decision=$(
    RUN_ATTEMPT="$attempt" \
      MAX_ATTEMPTS="$max" \
      MACOS_JOB_NAME="$macos_name" \
      "$script" "$work/jobs.json" "$work/annotations.json" 2>"$work/err"
  ) || fail "${label} exited $?"
  if [[ "$decision" != "$want" ]]; then
    fail "${label}: expected ${want}, got ${decision}"
  fi
  printf 'ok %s\n' "$label"
}

expect_fail() {
  local label=$1
  shift
  if "$@" >"$work/out" 2>"$work/err"; then
    fail "${label} succeeded"
  fi
  printf 'ok %s\n' "$label"
}

unacquired=$(job "$macos_name" cancelled "" null '[]')
expect "cancelled before a runner" 1 "$(around "$unacquired")" "$acquired" rerun

# The jobs API reports an unacquired job with an empty runner name and id 0,
# and the annotation title is an empty string.
api_shape=$(jq -nc --arg name "$macos_name" '{
  name: $name,
  conclusion: "cancelled",
  runner_name: "",
  runner_id: 0,
  steps: []
}')
api_note='[{"annotation_level":"failure","message":"The job was not acquired by Runner of type hosted even after multiple attempts","title":""}]'
expect "jobs API shape" 1 "$(around "$api_shape")" "$api_note" rerun

failed_conclusion=$(job "$macos_name" failure "" null '[]')
expect "failure conclusion, still never started" 1 "$(around "$failed_conclusion")" "$acquired" rerun

zero_id=$(job "$macos_name" cancelled "" 0 '[]')
expect "runner id zero" 1 "$(around "$zero_id")" "$acquired" rerun

null_steps=$(job "$macos_name" cancelled "" null 'null')
expect "null steps" 1 "$(around "$null_steps")" "$acquired" rerun

expect "second attempt still retries" 2 "$(around "$unacquired")" "$acquired" rerun

title_only='[{"message":"","title":"The job was not acquired by Runner of type hosted even after multiple attempts"}]'
expect "annotation title" 1 "$(around "$unacquired")" "$title_only" rerun

upper='[{"message":"THE JOB WAS NOT ACQUIRED BY RUNNER OF TYPE HOSTED"}]'
expect "annotation case" 1 "$(around "$unacquired")" "$upper" rerun

ran=$(job "$macos_name" failure "hosted" 42 '[{"name":"Build universal disk image","conclusion":"failure","number":1}]')
phrase='[{"message":"Build failed: image was not acquired from the cache"}]'
expect "real build failure" 1 "$(around "$ran")" "$phrase" skip

canceled='[{"message":"The operation was canceled."}]'
expect "cancelled without the acquisition annotation" 1 "$(around "$unacquired")" "$canceled" skip

billing='[{"message":"The job was not started because recent account payments have failed."}]'
expect "payment failure is not retried" 1 "$(around "$unacquired")" "$billing" skip

named_runner=$(job "$macos_name" cancelled "hosted" 0 '[]')
expect "runner name means it started" 1 "$(around "$named_runner")" "$acquired" skip

assigned_id=$(job "$macos_name" cancelled "" 99 '[]')
expect "runner id means it started" 1 "$(around "$assigned_id")" "$acquired" skip

skipped_macos=$(job "$macos_name" skipped "" null '[]')
expect "prerelease skip" 1 "$(around "$skipped_macos")" "$none" skip

succeeded=$(job "$macos_name" success "hosted" 5 '[{"name":"Checkout","conclusion":"success"}]')
expect "macOS job succeeded" 1 "$(around "$succeeded")" "$none" skip

expect "macOS job absent" 1 "$(combine "$linux_ok" "$verify_ok")" "$none" skip

linux_failed=$(job "Publish Linux desktop package" failure "hosted" 100 '[{"name":"Build","conclusion":"failure"}]')
expect "other job failed" 1 "$(combine "$unacquired" "$linux_failed" "$manifest_skip")" "$acquired" skip

harness_cancelled=$(job "Publish official harness pack images" cancelled "hosted" 8 '[{"name":"Build","conclusion":"cancelled"}]')
expect "other job cancelled after start" 1 "$(combine "$unacquired" "$harness_cancelled" "$linux_ok")" "$acquired" skip

expect "attempt cap" 3 "$(around "$unacquired")" "$acquired" cap
expect "lower cap" 2 "$(around "$unacquired")" "$acquired" cap 2
expect "cap does not hide a real failure" 3 "$(around "$ran")" "$phrase" skip

duplicate=$(combine "$unacquired" "$unacquired")
printf '%s\n' "$duplicate" >"$work/jobs.json"
printf '%s\n' "$acquired" >"$work/annotations.json"
expect_fail "duplicate macOS job" \
  env RUN_ATTEMPT=1 MAX_ATTEMPTS=3 MACOS_JOB_NAME="$macos_name" \
  "$script" "$work/jobs.json" "$work/annotations.json"

printf 'not-json\n' >"$work/jobs.json"
printf '%s\n' "$none" >"$work/annotations.json"
expect_fail "jobs are not json" \
  env RUN_ATTEMPT=1 MACOS_JOB_NAME="$macos_name" \
  "$script" "$work/jobs.json" "$work/annotations.json"

printf '%s\n' "$(around "$unacquired")" >"$work/jobs.json"
printf '{}\n' >"$work/annotations.json"
expect_fail "annotations are not an array" \
  env RUN_ATTEMPT=1 MACOS_JOB_NAME="$macos_name" \
  "$script" "$work/jobs.json" "$work/annotations.json"

printf '%s\n' "$(around "$unacquired")" >"$work/jobs.json"
printf '%s\n' "$acquired" >"$work/annotations.json"
expect_fail "missing attempt" \
  env -u RUN_ATTEMPT MAX_ATTEMPTS=3 MACOS_JOB_NAME="$macos_name" \
  "$script" "$work/jobs.json" "$work/annotations.json"

printf 'ok\n'
