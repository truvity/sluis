#!/usr/bin/env bash
# audit's tests that skip without their service (Postgres, S3 with SQS and
# DynamoDB, OpenBAO), each followed by a check that the test it exists for RAN:
# a service that stopped answering would otherwise turn them green by skipping.
# The CI job starts the services and exports AUDIT_*_URL.
set -euo pipefail
cd "$(dirname "$0")/../audit"
out="$(mktemp)"
trap 'rm -f "$out"' EXIT

ran() { # <package> <test regexp> <passing subtest>...
  local pkg="$1" re="$2"
  shift 2
  go test "$pkg" -run "$re" -v | tee "$out"
  for t in "$@"; do grep -q -- "--- PASS: $t" "$out" || { echo "audit-service-tests: $t did not run" >&2; exit 1; }; done
}

# devbox.json pins the Go every other job runs; go.mod pins this one. Two
# toolchains for one module is how "passes in one job, fails in another" starts.
have=$(python3 -c 'import json;print(json.load(open("../devbox.json"))["packages"]["go"])')
for mod in go.mod sdk/go.mod; do
  want=$(sed -n 's/^go \([0-9.]*\).*/\1/p' "$mod")
  [ "$want" = "$have" ] || { echo "audit/$mod wants go $want, devbox.json pins $have" >&2; exit 1; }
done

go test ./...

ran ./index/postgres/ TestMigrateIsIdempotent TestMigrateIsIdempotent
go test ./internal/observe/ ./index/postgres/ -run 'TestTheIndexerOverPostgres|TestTheObserveRoleHasTheIndexAndNothingElse' -v | tee "$out"
grep -q -- "--- PASS: TestTheIndexerOverPostgres" "$out"
grep -q -- "--- PASS: TestTheObserveRoleHasTheIndexAndNothingElse" "$out"

ran ./internal/s3test/ TestPrefixesListsEveryTenant TestPrefixesListsEveryTenant
ran ./internal/bucketcontract/ TestWhatTheWriterWritesConforms TestWhatTheWriterWritesConforms/s3-locked TestWhatTheWriterWritesConforms/s3-unlocked
ran ./dedupe/dynamodbdedupe/ TestLocalStackConditionalPut TestLocalStackConditionalPut
ran ./sink/sqssink/ TestLocalStackRoundTrip TestLocalStackRoundTrip/fifo
ran ./internal/s3test/ TestTheIndexerOverS3 TestTheIndexerOverS3/indexes_every_tenant_and_profile_it_finds_by_listing

ran ./index/postgres/ TestTheKMSAdapterOverThePostgresWrappedStore TestTheKMSAdapterOverThePostgresWrappedStore
ran ./keys/ TestTransitReplicasAgreeAndPurposesDoNot TestTransitReplicasAgreeAndPurposesDoNot
