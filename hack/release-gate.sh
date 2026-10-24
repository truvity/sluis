#!/usr/bin/env bash
# The release's gate, before anything is published: the tagged commit must be
# on master, and master's own CI must have passed on it.
#
#   1. the commit is an ancestor of origin/master (or is its tip): a tag on a
#      branch nobody merged, or on a commit master never had, publishes nothing;
#   2. the CI workflow (.github/workflows/ci.yaml) ran on that commit for a push
#      to master and concluded `success`, and its `check` check run, the one
#      required context, concluded `success` too. A run still queued or in
#      progress is waited for, up to RELEASE_GATE_WAIT seconds; no run, a run
#      that failed, was cancelled or skipped, or a wait that ran out is a
#      refusal.
#
# Fail closed: anything this cannot read is a refusal. It needs the checkout's
# history of origin/master (actions/checkout with fetch-depth: 0) and a token
# that may read Actions and checks (GH_TOKEN; `actions: read`, `checks: read`).
#
# Usage: hack/release-gate.sh <tag>
# Environment: GITHUB_REPOSITORY (owner/name, required), RELEASE_GATE_BASE
# (default origin/master), RELEASE_GATE_WAIT (default 3600), RELEASE_GATE_POLL
# (default 30), RELEASE_GATE_WORKFLOW (default ci.yaml), RELEASE_GATE_CHECK
# (default check).
set -euo pipefail

tag="${1:?usage: hack/release-gate.sh <tag>}"
repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY (owner/name) is required}"
base="${RELEASE_GATE_BASE:-origin/master}"
wait_for="${RELEASE_GATE_WAIT:-3600}"
poll="${RELEASE_GATE_POLL:-30}"
workflow="${RELEASE_GATE_WORKFLOW:-ci.yaml}"
check="${RELEASE_GATE_CHECK:-check}"

refuse() { echo "release-gate: $tag: $*" >&2; exit 1; }

sha=$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}") || refuse "no such tag in this checkout"
git rev-parse --verify --quiet "$base^{commit}" > /dev/null || refuse "$base is not in this checkout (fetch-depth: 0)"
if ! git merge-base --is-ancestor "$sha" "$base"; then
    refuse "commit $sha is not on $base: a release is cut from a commit master has"
fi
echo "release-gate: $tag: $sha is on $base"

# The newest run of the CI workflow for a push of this commit to master.
deadline=$(( $(date +%s) + wait_for ))
while :; do
    runs=$(gh api "repos/$repo/actions/workflows/$workflow/runs?head_sha=$sha&event=push&branch=master&per_page=20") ||
        refuse "the CI runs of $sha could not be read"
    run=$(jq -c '[.workflow_runs[]? | select(.head_sha == "'"$sha"'")] | sort_by(.run_number, .run_attempt) | last // empty' <<<"$runs") ||
        refuse "the CI runs of $sha are not what GitHub answers"
    status=$(jq -r '.status // ""' <<<"${run:-null}")
    conclusion=$(jq -r '.conclusion // ""' <<<"${run:-null}")
    if [ -n "$run" ] && [ "$status" = completed ]; then
        break
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
        refuse "master's CI on $sha has not completed (${status:-no run}) after ${wait_for}s"
    fi
    echo "release-gate: $tag: waiting for master's CI on $sha (${status:-no run yet})"
    sleep "$poll"
done
url=$(jq -r '.html_url // ""' <<<"$run")
[ "$conclusion" = success ] || refuse "master's CI on $sha concluded '${conclusion:-none}', not success: $url"

checks=$(gh api "repos/$repo/commits/$sha/check-runs?check_name=$check&filter=latest&per_page=100") ||
    refuse "the check runs of $sha could not be read"
verdicts=$(jq -r '[.check_runs[]? | select(.name == "'"$check"'") | .conclusion // "pending"] | join(" ")' <<<"$checks") ||
    refuse "the check runs of $sha are not what GitHub answers"
[ -n "$verdicts" ] || refuse "no '$check' check ran on $sha"
for v in $verdicts; do
    [ "$v" = success ] || refuse "the '$check' check on $sha is '$v', not success"
done
echo "release-gate: $tag: master's CI passed on $sha ($url)"
