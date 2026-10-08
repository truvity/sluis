#!/usr/bin/env bash
# The storage module's ssm and s3 state backends against a real SSM and S3 API
# (LocalStack), and a refusal to call an empty run a pass.
#
# STORAGE_LOCALSTACK_URL names the endpoint. The tests SKIP when it is unset so
# that a plain `go test` needs nothing; this script is the one place that sets
# it, so it is also the one place that must notice the tests skipped anyway.
set -euo pipefail

: "${STORAGE_LOCALSTACK_URL:?set STORAGE_LOCALSTACK_URL to the LocalStack endpoint (just test-s3 does)}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

(cd storage && GOWORK=off go test -count=1 -v ./state/ssm/ ./state/s3/) 2>&1 | tee "$out"

if grep -q -- '--- SKIP' "$out"; then
    echo "FAIL: a storage test skipped although STORAGE_LOCALSTACK_URL is set:" >&2
    grep -- '--- SKIP' "$out" >&2
    exit 1
fi

required=(
    'TestConformance/CRUD'
    'TestConformance/ConditionalPut'
    'TestConformance/ListDirectChildrenOnly'
    'TestConformance/GetRev'
    'TestConformance/RotatingInsideGrace'
    'TestConformance/RotatingOutsideGrace'
    'TestSizeTiers'
    'TestConformanceConditional/ConditionalPut'
    'TestConformanceCompressed/GetRev'
    'TestOpenHonoursEndpointAndRegion'
)
for name in "${required[@]}"; do
    # Both packages run these names; each must have passed at least once.
    if ! grep -q -- "--- PASS: ${name} " "$out"; then
        echo "FAIL: ${name} did not run and pass" >&2
        exit 1
    fi
done
# Each package must have passed on its own.
for pkg in state/ssm state/s3; do
    grep -q -- "^ok  	github.com/truvity/sluis/storage/${pkg}" "$out" || { echo "FAIL: ${pkg} did not pass" >&2; exit 1; }
done
echo "ok: ${#required[@]} required tests ran against ${STORAGE_LOCALSTACK_URL}"
