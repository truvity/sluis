#!/usr/bin/env bash
# What a pull request has to run, from the files it changes.
#
#   EVENT=pull_request BASE=<base sha> hack/ci-plan.sh   (writes to $GITHUB_OUTPUT)
#
# On master everything runs. On a pull request each changed file belongs to one
# area, the first that matches, and an area runs the recipes that can see it:
#
#   audit/, charts/audit/             the audit jobs (and nothing of sluis's)
#   docs/, *.md                       docs-check
#   ts/, frontend/, package.json ...  ts, console
#   storage/                          storage-test and the S3 tier
#   deploy/                           pulumi-test, release-chain
#   Justfile, devbox, .github/, hack/, .goreleaser.yaml, go.mod/go.sum
#                                     everything: they are shared by all
#   anything else (Go, charts, proto, config ...)   every sluis recipe
#
# leak-canary reads the whole tree and always runs.
set -euo pipefail

full='"build","test","lint","chart-lint","telemetry","archive-check","docs-check","audit-catalogue","ts","console","pulumi-test","release-chain","storage-test","test-race"'
sluis=false audit=false docs=false web=false storage=false pulumi=false

if [ "${EVENT:-}" != "pull_request" ]; then
  sluis=true audit=true
else
  while IFS= read -r f; do
    case "$f" in
      audit/* | charts/audit/*) audit=true ;;
      docs/* | *.md) docs=true ;;
      ts/* | frontend/* | package.json | yarn.lock | .yarnrc.yml) web=true ;;
      storage/*) storage=true ;;
      deploy/*) pulumi=true ;;
      Justfile | devbox.* | .github/* | hack/* | .goreleaser.yaml | go.mod | go.sum | lefthook.yml) sluis=true audit=true ;;
      *) sluis=true ;;
    esac
  done < <(git diff --name-only "${BASE:?}" HEAD)
fi

if [ "$sluis" = true ]; then
  recipes="$full"
else
  recipes='"leak-canary"'
  [ "$docs" = true ] && recipes="$recipes,\"docs-check\""
  [ "$web" = true ] && recipes="$recipes,\"ts\",\"console\""
  [ "$storage" = true ] && recipes="$recipes,\"storage-test\""
  [ "$pulumi" = true ] && recipes="$recipes,\"pulumi-test\",\"release-chain\""
fi
# the whole list already carries leak-canary on master
[ "$sluis" = true ] && recipes="$recipes,\"leak-canary\""

# The LocalStack and OpenBao tier: sluis's adapters, storage's backends, the
# secrets layout move (internal/migrate, internal/secretstore: sluis, above) and deploy/
s3=false
if [ "$sluis" = true ] || [ "$storage" = true ] || [ "$pulumi" = true ]; then s3=true; fi

{
  echo "recipes=[$recipes]"
  echo "audit=$audit"
  echo "s3=$s3"
} >> "${GITHUB_OUTPUT:-/dev/stdout}"
