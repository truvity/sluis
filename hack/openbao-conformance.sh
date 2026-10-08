#!/usr/bin/env bash
# The storage module's OpenBao backends (state on KV version 2, keys on
# transit) against a real OpenBao development server, and a refusal to call an
# empty run a pass.
#
# STORAGE_OPENBAO_ADDR and STORAGE_OPENBAO_ROOT_TOKEN name the server. The
# tests SKIP when they are unset so that a plain `go test` needs nothing; this
# script is the one place that sets them, so it is also the one place that must
# notice the tests skipped anyway.
set -euo pipefail

: "${STORAGE_OPENBAO_ADDR:?set STORAGE_OPENBAO_ADDR to the dev server (just test-openbao does)}"
: "${STORAGE_OPENBAO_ROOT_TOKEN:?set STORAGE_OPENBAO_ROOT_TOKEN to its root token (just test-openbao does)}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

(cd storage && GOWORK=off go test -count=1 -v ./openbao/... ./state/openbao/ ./keys/transit/) 2>&1 | tee "$out"

if grep -q -- '--- SKIP' "$out"; then
    echo "FAIL: an OpenBao test skipped although the server is set:" >&2
    grep -- '--- SKIP' "$out" >&2
    exit 1
fi

required=(
    'TestLoginOnceAndNamespace'
    'TestTokenFileIsReReadAtEachLogin'
    'TestRevokedTokenLogsInAgainOnce'
    'TestConformance/CRUD'
    'TestConformance/ConditionalPut'
    'TestConformance/ListDirectChildrenOnly'
    'TestConformance/GetRev'
    'TestConformance/RotatingInsideGrace'
    'TestConformance/RotatingOutsideGrace'
    'TestOpenRefusesOneVersion'
    'TestFieldsAreTheSecretKeys'
    'TestPrunedPrevious'
    'TestPolicyBoundsThePrefix'
    'TestConformance/round_trip'
    'TestConformance/context_binds_the_instance'
    'TestConformance/decrypt_context_override'
    'TestConformance/data_key'
    'TestConformance/sign'
    'TestConformance/MAC'
    'TestConformanceDerived'
    'TestContextThatWouldBeIgnoredIsRefused'
    'TestRSASignsRS256'
    'TestSignPinsTheVersion'
    'TestMACIsStableAndRotationProof'
    'TestPolicyPinsTheContext'
)
for name in "${required[@]}"; do
    # TestConformance runs in the state and in the keys package; each name
    # must have passed at least once.
    if ! grep -q -- "--- PASS: ${name} " "$out"; then
        echo "FAIL: ${name} did not run and pass" >&2
        exit 1
    fi
done
# Each package must have passed on its own.
for pkg in openbao state/openbao keys/transit; do
    grep -q -- "^ok  	github.com/truvity/sluis/storage/${pkg}" "$out" || { echo "FAIL: ${pkg} did not pass" >&2; exit 1; }
done
echo "ok: ${#required[@]} required tests ran against ${STORAGE_OPENBAO_ADDR}"
