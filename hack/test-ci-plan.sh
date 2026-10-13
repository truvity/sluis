#!/usr/bin/env bash
# hack/ci-plan.sh against lists of changed paths, and the rule it exists for:
# every Go module of the repository is linted by a recipe its changes select.
# A module nobody lints, or a plan that leaves `lint` out for the module's own
# files, lets findings reach master (deploy/pulumi, 2026-10-10: #508, #532).
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
plan() { CI_PLAN_FILES="$1" EVENT=pull_request GITHUB_OUTPUT=/dev/stdout hack/ci-plan.sh; }

# has|hasnt <paths> <group>: the groups line must (not) name the group
has() {
  if plan "$1" | grep -q "^groups=.*\"$2\""; then :; else echo "FAIL: [$1] does not plan $2" >&2; fail=1; fi
}
hasnt() {
  if plan "$1" | grep -q "^groups=.*\"$2\""; then echo "FAIL: [$1] plans $2" >&2; fail=1; fi
}
# services <paths> <script>: the services job runs it
services() {
  if plan "$1" | grep -q "^services=.*$2"; then :; else echo "FAIL: [$1] does not run $2" >&2; fail=1; fi
}

has 'deploy/pulumi/lambda.go' go-lint
has 'deploy/pulumi/policy_modules_test.go' go-lint
has 'deploy/pulumi/edge/cloudflare/main.go' go-lint
has 'storage/kv.go' go-lint
has 'internal/x/x.go' go-lint
has 'internal/x/x.go' go-test
has 'Justfile' go-lint
has 'Justfile' audit
has 'docs/index.md' docs
has 'audit/server/x.go' docs
hasnt 'docs/index.md' go-lint
hasnt 'audit/server/x.go' go-lint
hasnt 'ts/a.ts' go-lint
has 'ts/a.ts' web
has 'audit/server/x.go' audit
services 'storage/kv.go' hack/storage-conformance.sh
services 'audit/server/x.go' hack/audit-service-tests.sh
if [ -n "$(plan 'docs/index.md' | sed -n 's/^services=//p')" ]; then echo "FAIL: a docs change runs the services job" >&2; fail=1; fi
if ! plan 'audit/server/x.go' | grep -q '^audit=true'; then echo "FAIL: audit change does not plan e2e" >&2; fail=1; fi

# The groups hold the recipes this table is about: `lint` and the nested
# modules' tests in go-lint, `audit-lint` in audit.
recipe() { awk -v r="$1" '$0 ~ "^"r":" {on=1; next} on && /^[^ #\t]/ {exit} on' Justfile; }
while read -r group item; do
  if ! grep -qE "ci-group.sh .* $item( |\$)" <<<"$(recipe "$group")"; then echo "FAIL: $group does not run $item" >&2; fail=1; fi
done <<'GROUPS'
ci-go-lint lint
ci-go-lint pulumi-test
ci-go-lint storage-test
ci-go-lint release-chain
ci-audit audit-lint
GROUPS

# Every go.mod is linted: the root and the nested modules by `lint`, the modules
# under audit/ (planned whenever audit/ changes) by `audit-lint`.
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
