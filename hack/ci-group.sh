#!/usr/bin/env bash
# One CI job: a group of recipes run in order, every one of them to the end.
#
#   hack/ci-group.sh <group> <recipe>...         each as `just --no-deps <recipe>`
#   hack/ci-group.sh --run <group> <command>...  each command as it stands (the
#                                                services job, which has no devbox)
#
# A group is one job and one row on a pull request, so a red row has to say
# which recipe is red. Every recipe runs even after one fails, the job summary
# ($GITHUB_STEP_SUMMARY, and the end of the log) lists each with its result and
# time, and the script exits non-zero if any failed.
#
# `--no-deps`: the group lists what each recipe needs (the console bundle through
# `console-ready`, `deps` before `ts`), so a dependency is built once per job and
# not once per recipe.
#
# A recipe that changes the working tree fails, as the shared recipe action
# (truvity/ci-actions/recipe) has it: a formatter or a generator that rewrote a
# tracked file found something to commit. Only a change made by that recipe
# counts, so one dirty recipe does not turn the rest red.
#
# The go test recipes rerun a failed test (gotestsum --rerun-fails) and write
# the tests they reran to dist/ci/reruns-*.txt. A test that passed only on a
# rerun is a flake, not a pass: it is listed in the summary and raised as a
# warning, so a green job still says so.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

mode=recipe
if [ "${1:-}" = "--run" ]; then
  mode=script
  shift
fi
group=${1:?usage: hack/ci-group.sh [--run] <group> <recipe>...}
shift
[ "$#" -gt 0 ] || { echo "ci-group: $group lists nothing to run" >&2; exit 1; }

summary=${GITHUB_STEP_SUMMARY:-/dev/null}
reruns=dist/ci
rm -f "$reruns"/reruns-*.txt
ci=${GITHUB_ACTIONS:-}

rows=()
failed=()
for item in "$@"; do
  [ -n "$ci" ] && echo "::group::$item"
  echo "==> $group: $item"
  before=$(git status --porcelain)
  start=$(date +%s)
  if [ "$mode" = recipe ]; then just --no-deps "$item"; else "$item"; fi
  code=$?
  took=$(($(date +%s) - start))
  after=$(git status --porcelain)
  result=pass
  if [ "$code" -ne 0 ]; then
    result="FAIL (exit $code)"
  elif [ "$after" != "$before" ]; then
    result="FAIL (changed the working tree)"
    echo "$item changed the working tree; commit the result, or make it read-only:" >&2
    diff <(echo "$before") <(echo "$after") >&2
  fi
  [ -n "$ci" ] && echo "::endgroup::"
  if [ "$result" != pass ]; then
    failed+=("$item")
    [ -n "$ci" ] && echo "::error title=$group: $item::$result"
  fi
  rows+=("| \`$item\` | $result | ${took}s |")
done

{
  echo "### $group"
  echo
  echo "| recipe | result | time |"
  echo "|---|---|---|"
  printf '%s\n' "${rows[@]}"
  for report in "$reruns"/reruns-*.txt; do
    [ -s "$report" ] || continue
    echo
    echo "**Rerun after failing** ($(basename "$report" .txt)): flaky if the recipe passed, broken if not."
    echo
    echo '```'
    cat "$report"
    echo '```'
  done
} | tee -a "$summary"
if [ -n "$ci" ]; then
  for report in "$reruns"/reruns-*.txt; do
    [ -s "$report" ] && echo "::warning title=$group: flaky tests::$(basename "$report" .txt): $(tr '\n' ' ' <"$report")"
  done
fi

if [ "${#failed[@]}" -gt 0 ]; then
  echo "ci-group: $group failed: ${failed[*]}" >&2
  exit 1
fi
echo "ci-group: $group passed (${#rows[@]} recipes)"
