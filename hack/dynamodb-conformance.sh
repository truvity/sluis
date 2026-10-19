#!/usr/bin/env bash
# The DynamoDB adapter (a table per module), and a migration into it, against a
# real DynamoDB API (LocalStack), and a refusal to call an empty run a pass.
#
# ACCESS_ROSTER_DYNAMODB_URL names the endpoint. The tests SKIP when it is unset
# so that `go test ./...` needs nothing (the adapter runs the same suite over a
# fake there); this script is the one place that sets it, so it is also the one
# place that must notice the tests skipped anyway: a green run that tested
# nothing is how an adapter reaches production unproven.
set -euo pipefail

: "${ACCESS_ROSTER_DYNAMODB_URL:?set ACCESS_ROSTER_DYNAMODB_URL to the LocalStack endpoint (just test-s3 does)}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

go test -count=1 -v ./internal/port/dynamodb/ ./internal/migrate/ ./internal/store/ -run 'TestAMissingTable|TestCreatingAnExistingTable|TestTheTableHasTTL|DynamoDB' 2>&1 | tee "$out"

# The only skips allowed are the ports a DynamoDB table does not hold; each says so.
if grep -- '--- SKIP' "$out" | grep -v -E 'TestConformance[A-Za-z]*/([a-z]+/)?(blob|identity)/'; then
    echo "FAIL: a test skipped although ACCESS_ROSTER_DYNAMODB_URL is set (only the other ports' assertions may):" >&2
    exit 1
fi

# Each of these must have RUN and passed, on the real engine.
required=(
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/cas/update-race'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/cas/create-race'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/cas/delete-if-revision'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/ttl/visibility'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/ttl/create-over-expired'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/ttl/list-omits-expired'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/ttl/index-expiry'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/lease/held-cannot-be-taken'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/lease/takeover-by-one'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/lease/renewal-after-takeover'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/paging/every-record-once'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/paging/concurrent-write'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/paging/foreign-token'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/revisions/change-with-content'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/revisions/change-on-identical-rewrite'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/revisions/stale-update'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/watch/put-delete'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/watch/expiry'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/watch/recover-by-listing'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/limits/too-large'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/limits/no-lifetime'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/limits/permanent-family'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/index/members'
    'TestConformanceOfEveryModulesTableOnDynamoDB/oidc/trigger/notify'
    'TestGrantCostOverTablesOnDynamoDB'
    'TestRouterExportsOnDynamoDB'
    'TestConformanceOfEveryModulesTableOnDynamoDB'
    'TestReuseCostOnDynamoDB'
    'TestSSOCookieOnDynamoDB'
    'TestAMissingTableIsUnavailableNotNotFound'
    'TestCreatingAnExistingTableIsHarmless'
    'TestTheTableHasTTLOnExpires'
    'TestMemoryToDynamoDBCopiesAndVerifies'
    'TestTheDynamoDBAdapterSharesStateAndTheTriggerAcrossStores'
)
for name in "${required[@]}"; do
    if ! grep -q -- "--- PASS: ${name} " "$out"; then
        echo "FAIL: ${name} did not run and pass" >&2
        exit 1
    fi
done
echo "ok: ${#required[@]} required tests ran against ${ACCESS_ROSTER_DYNAMODB_URL}"
