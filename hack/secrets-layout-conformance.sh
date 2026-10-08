#!/usr/bin/env bash
# The move of an installation's secrets to layout v4 (`sluis migrate
# secrets-layout`) against a real Parameter Store API (LocalStack), and a
# refusal to call an empty run a pass.
#
# STORAGE_LOCALSTACK_URL names the endpoint. The test SKIPS when it is unset so
# that `go test ./...` needs nothing; this script is the one place that sets it,
# so it is also the one place that must notice it skipped anyway.
set -euo pipefail

: "${STORAGE_LOCALSTACK_URL:?set STORAGE_LOCALSTACK_URL to the LocalStack endpoint (just test-s3 does)}"

out="$(mktemp)"
trap 'rm -f "$out"' EXIT

go test -count=1 -v -run 'TestSecretsLayoutOnLocalStack' ./internal/migrate/ 2>&1 | tee "$out"

if grep -q -- '--- SKIP' "$out"; then
    echo "FAIL: the layout test skipped although STORAGE_LOCALSTACK_URL is set" >&2
    exit 1
fi
if ! grep -q -- '--- PASS: TestSecretsLayoutOnLocalStack ' "$out"; then
    echo "FAIL: TestSecretsLayoutOnLocalStack did not run and pass" >&2
    exit 1
fi
echo "ok: the secrets layout move ran against ${STORAGE_LOCALSTACK_URL}"
