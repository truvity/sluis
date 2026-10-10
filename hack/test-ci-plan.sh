#!/usr/bin/env bash
# hack/ci-plan.sh against lists of changed paths, and the rule it exists for:
# every Go module of the repository is linted by a recipe its changes select.
# A module nobody lints, or a plan that leaves `lint` out for the module's own
# files, lets findings reach master (deploy/pulumi, 2026-10-10: #508, #532).
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
plan() { CI_PLAN_FILES="$1" EVENT=pull_request GITHUB_OUTPUT=/dev/stdout hack/ci-plan.sh; }

# expect <paths> <recipes line must contain|must not contain> <recipe>
has() {
  if plan "$1" | grep -q "^recipes=.*\"$2\""; then :; else echo "FAIL: [$1] does not plan $2" >&2; fail=1; fi
}
hasnt() {
  if plan "$1" | grep -q "^recipes=.*\"$2\""; then echo "FAIL: [$1] plans $2" >&2; fail=1; fi
}

has 'deploy/pulumi/lambda.go' lint
has 'deploy/pulumi/policy_modules_test.go' lint
has 'deploy/pulumi/edge/cloudflare/main.go' lint
has 'storage/kv.go' lint
has 'deploy/pulumi/lambda.go' pulumi-test
has 'internal/x/x.go' lint
has 'Justfile' lint
hasnt 'docs/index.md' lint
hasnt 'audit/server/x.go' lint
hasnt 'ts/a.ts' lint
if ! plan 'audit/server/x.go' | grep -q '^audit=true'; then echo "FAIL: audit change does not plan audit" >&2; fail=1; fi

# Every go.mod is linted: the root and the nested modules by `lint`, the modules
# under audit/ (planned whenever audit/ changes) by `audit-lint`.
recipe() { awk -v r="$1" '$0 ~ "^"r":" {on=1; next} on && /^[^ #\t]/ {exit} on' Justfile; }
lint_body="$(recipe lint)"
audit_body="$(recipe audit-lint)"
while IFS= read -r mod; do
  dir="$(dirname "$mod")"
  case "$dir" in
    .) continue ;;
    audit) continue ;;
    audit/*) rel="${dir#audit/}"; body="$audit_body" ;;
    *) rel="$dir"; body="$lint_body" ;;
  esac
  if ! grep -qE "cd $rel( |&)" <<<"$body"; then echo "FAIL: $dir/go.mod is linted by no recipe" >&2; fail=1; fi
done < <(git ls-files '*go.mod' | grep -E '(^|/)go\.mod$')

[ "$fail" = 0 ] && echo "test-ci-plan: ok"
exit "$fail"
