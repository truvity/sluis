#!/usr/bin/env bash
# The keys KMS backend against a real KMS API (LocalStack), and a refusal to
# call an empty run a pass.
#
# KEYS_KMS_URL names the endpoint. The tests SKIP when it is unset so that
# `go test ./...` needs nothing; this script is the one place that sets it, so
# it is also the one place that must notice the tests skipped anyway.
set -euo pipefail

: "${KEYS_KMS_URL:?set KEYS_KMS_URL to the LocalStack endpoint (just test-s3 does)}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

(cd storage && go test -count=1 -v ./keys/kms/) 2>&1 | tee "$out"

if grep -q -- '--- SKIP' "$out"; then
    echo "FAIL: a keys test skipped although KEYS_KMS_URL is set:" >&2
    grep -- '--- SKIP' "$out" >&2
    exit 1
fi

required=(
    'TestConformance/round_trip'
    'TestConformance/context_binds_the_instance'
    'TestConformance/decrypt_context_override'
    'TestConformance/data_key'
    'TestConformance/sign'
    'TestConformance/MAC'
    'TestRSASignsRS256'
    'TestMACWrappedKeys'
)
for name in "${required[@]}"; do
    if ! grep -q -- "--- PASS: ${name} " "$out"; then
        echo "FAIL: ${name} did not run and pass" >&2
        exit 1
    fi
done
echo "ok: ${#required[@]} required keys tests ran against ${KEYS_KMS_URL}"
