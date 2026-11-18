#!/usr/bin/env bash
# Decide whether a completed release attempt should rerun its failed jobs.
#
# The macOS desktop job is sometimes cancelled before a hosted runner accepts
# it. That attempt has no runner name, no steps, and an annotation that the
# job was not acquired. Rerunning failed jobs starts that job again and also
# starts jobs that depend on it. A job that received a runner, or any other
# job that failed or was cancelled, is left alone.
#
# Usage: scripts/retry-unacquired-release-job.sh JOBS.json ANNOTATIONS.json
# JOBS.json is the attempt's jobs array. ANNOTATIONS.json is the annotation
# array for the macOS desktop job (or [] when that job has none).
# Env:
#   RUN_ATTEMPT     attempt that just completed (required, starting at 1)
#   MAX_ATTEMPTS    total attempts allowed, including the first (default 3)
#   MACOS_JOB_NAME  job name (default is the release workflow's macOS job)
# Stdout is one word: rerun, skip, or cap. The reason is on stderr.
# Exits 0 for a decision, 1 when the input cannot be read.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: scripts/retry-unacquired-release-job.sh JOBS.json ANNOTATIONS.json" >&2
  exit 1
fi

jobs_file=$1
annotations_file=$2

if [[ ! -f "$jobs_file" || ! -f "$annotations_file" ]]; then
  echo "jobs and annotations files must exist" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 1
fi

MAX_ATTEMPTS=${MAX_ATTEMPTS:-3}
macos_job_name=${MACOS_JOB_NAME:-Publish macOS desktop app}

if [[ ! "${RUN_ATTEMPT:-}" =~ ^[1-9][0-9]*$ ]]; then
  echo "RUN_ATTEMPT must be a positive integer" >&2
  exit 1
fi
if [[ ! "$MAX_ATTEMPTS" =~ ^[1-9][0-9]*$ ]]; then
  echo "MAX_ATTEMPTS must be a positive integer" >&2
  exit 1
fi
if [[ -z "$macos_job_name" ]]; then
  echo "MACOS_JOB_NAME is empty" >&2
  exit 1
fi

if ! jq -e 'type == "array" and all(type == "object")' "$jobs_file" >/dev/null; then
  echo "jobs file must be a JSON array of job objects" >&2
  exit 1
fi
if ! jq -e 'type == "array"' "$annotations_file" >/dev/null; then
  echo "annotations file must be a JSON array" >&2
  exit 1
fi

# A job GitHub never handed to a runner has an empty runner name, runner id
# 0 (or no id), and no steps. The jobs API uses "" and 0, not null.
never_started() {
  jq -e '
    (.runner_name // "") == ""
    and ((.runner_id // 0) == 0)
    and (
      .steps == null
      or ((.steps | type) == "array" and (.steps | length) == 0)
    )
  ' <<<"$1" >/dev/null
}

not_acquired() {
  jq -e '
    [ .[]? | select(type == "object")
      | (.message // "" | tostring), (.title // "" | tostring)
      | ascii_downcase
    ] | any(contains("not acquired"))
  ' "$annotations_file" >/dev/null
}

shorten() {
  local text=$1
  text=${text//$'\n'/ }
  if ((${#text} > 200)); then
    printf '%s' "${text:0:197}..."
  else
    printf '%s' "$text"
  fi
}

finish() {
  printf '%s\n' "$2" >&2
  printf '%s\n' "$1"
  exit 0
}

printf 'Attempt %s of %s.\n' "$RUN_ATTEMPT" "$MAX_ATTEMPTS" >&2
echo "Jobs:" >&2
jq -r '
  def runner:
    if (.runner_name // "") == "" then "none" else .runner_name end;
  def nsteps:
    if (.steps | type) == "array" then (.steps | length) else 0 end;
  .[]
  | "  \(.name // "(unnamed job)"): conclusion=\(.conclusion // "none"), runner=\(runner), steps=\(nsteps)"
' "$jobs_file" >&2

macos_jobs=()
while IFS= read -r line; do
  macos_jobs+=("$line")
done < <(jq -c --arg name "$macos_job_name" '.[] | select(.name == $name)' "$jobs_file")

if ((${#macos_jobs[@]} > 1)); then
  echo "Expected one job named ${macos_job_name}, found ${#macos_jobs[@]}." >&2
  exit 1
fi

blockers=()
while IFS= read -r job; do
  name=$(jq -r '.name // ""' <<<"$job")
  if [[ -z "$name" ]]; then
    name="(unnamed job)"
  fi
  if [[ "$name" == "$macos_job_name" ]]; then
    continue
  fi
  conclusion=$(jq -r '.conclusion // ""' <<<"$job")
  case "$conclusion" in
    success|skipped|neutral|"")
      ;;
    cancelled)
      if never_started "$job"; then
        blockers+=("${name} was cancelled before a runner accepted it")
      else
        blockers+=("${name} concluded cancelled")
      fi
      ;;
    *)
      blockers+=("${name} concluded ${conclusion}")
      ;;
  esac
done < <(jq -c '.[]' "$jobs_file")

joined=""
if ((${#blockers[@]} > 0)); then
  joined=$(printf '%s; ' "${blockers[@]}")
  joined=${joined%; }
fi

if ((${#macos_jobs[@]} == 0)); then
  finish skip "Skip: this attempt has no job named ${macos_job_name}."
fi

macos=${macos_jobs[0]}
conclusion=$(jq -r '.conclusion // ""' <<<"$macos")
runner=$(jq -r 'if (.runner_name // "") == "" then "none" else .runner_name end' <<<"$macos")
steps=$(jq -r 'if (.steps | type) == "array" then (.steps | length) else 0 end' <<<"$macos")
failed_step=$(jq -r '[.steps[]? | select(type == "object" and .conclusion == "failure") | .name // empty] | first // empty' <<<"$macos")

if [[ "$conclusion" == "cancelled" || "$conclusion" == "failure" ]] && never_started "$macos" && not_acquired; then
  if ((${#blockers[@]} > 0)); then
    finish skip "Skip: ${macos_job_name} was not acquired by a runner, but other jobs are not that kind of miss: ${joined}."
  fi
  if ((RUN_ATTEMPT >= MAX_ATTEMPTS)); then
    finish cap "Cap: ${macos_job_name} was not acquired by a runner, and attempt ${RUN_ATTEMPT} has reached the cap of ${MAX_ATTEMPTS}."
  fi
  quote=$(jq -r '
    [ .[]? | select(type == "object")
      | (.message // "" | tostring), (.title // "" | tostring)
      | select(ascii_downcase | contains("not acquired"))
      | gsub("\\s+"; " ")
    ][0] // "the job was not acquired"
  ' "$annotations_file")
  quote=$(shorten "$quote")
  finish rerun "Rerun: ${macos_job_name} concluded ${conclusion} before a runner accepted it. Annotation: ${quote}. No other job failed or was cancelled."
fi

summary=$(jq -r '
  [ .[]? | select(type == "object")
    | (.message // "" | tostring), (.title // "" | tostring)
    | gsub("^\\s+|\\s+$"; "")
    | select(length > 0)
    | gsub("\\s+"; " ")
  ] | unique | join(" | ")
' "$annotations_file")
if [[ -z "$summary" ]]; then
  summary="none"
fi
summary=$(shorten "$summary")

if [[ "$conclusion" == "cancelled" || "$conclusion" == "failure" ]] && never_started "$macos"; then
  extra=""
  if ((${#blockers[@]} > 0)); then
    extra=" Other jobs: ${joined}."
  fi
  finish skip "Skip: ${macos_job_name} never started (conclusion=${conclusion}), and its annotations do not say the job was not acquired. Annotations: ${summary}.${extra}"
fi

extra=""
if ((${#blockers[@]} > 0)); then
  extra=" Other jobs: ${joined}."
fi
if [[ -n "$failed_step" ]]; then
  finish skip "Skip: ${macos_job_name} concluded ${conclusion:-none} after it started (runner=${runner}, steps=${steps}, failed step=${failed_step}).${extra}"
fi
finish skip "Skip: ${macos_job_name} concluded ${conclusion:-none} (runner=${runner}, steps=${steps}).${extra}"
