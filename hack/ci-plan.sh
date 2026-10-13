#!/usr/bin/env bash
# What a pull request has to run, from the files it changes.
#
#   EVENT=pull_request BASE=<base sha> hack/ci-plan.sh   (writes to $GITHUB_OUTPUT)
#
# CI runs groups of recipes, one job each (the `ci-*` recipes of the Justfile).
# On master everything runs. On a pull request each changed file belongs to one
# area, the first that matches, and an area runs the groups that can see it:
#
#   audit/, charts/audit/             audit, the audit service tests, e2e
#   docs/, *.md                       docs
#   ts/, frontend/, package.json ...  web
#   storage/                          go-lint (lint, storage-test), the S3 tier
#   deploy/                           go-lint (lint, pulumi-test, release-chain), the S3 tier
#   Justfile, devbox, .github/, hack/, .goreleaser.yaml, go.mod/go.sum
#                                     everything: they are shared by all
#   anything else (Go, charts, proto, config ...)   every sluis group and the S3 tier
#
# `docs` always runs: it holds leak-canary, which reads the whole tree, and the
# workflow adds the pinned-action and component-contract checks to it.
#
# storage/ and deploy/ are modules of their own that the root build, test and
# lint never see, so `lint` (which runs golangci-lint in each of them) is in
# their plan: a pull request that touches only deploy/pulumi has to be linted
# (2026-10-10: #508 and #532 put findings on master that way). hack/test-ci-plan.sh
# holds this table, and that every go.mod outside audit/ is linted by `lint`.
#
# Outputs: groups (a JSON list, the matrix of the groups job), services (the
# scripts the services job runs, space-separated, empty to skip it) and audit
# (true runs the e2e job).
#
# CI_PLAN_FILES (newline-separated paths) replaces the git diff: for the test.
set -euo pipefail

sluis=false audit=false web=false storage=false pulumi=false

if [ "${EVENT:-}" != "pull_request" ]; then
  sluis=true audit=true
else
  while IFS= read -r f; do
    case "$f" in
      audit/* | charts/audit/*) audit=true ;;
      docs/* | *.md) ;;
      ts/* | frontend/* | package.json | yarn.lock | .yarnrc.yml) web=true ;;
      storage/*) storage=true ;;
      deploy/*) pulumi=true ;;
      Justfile | devbox.* | .github/* | hack/* | .goreleaser.yaml | go.mod | go.sum | lefthook.yml) sluis=true audit=true ;;
      *) sluis=true ;;
    esac
  done < <(if [ -n "${CI_PLAN_FILES+x}" ]; then printf '%s\n' "$CI_PLAN_FILES"; else git diff --name-only "${BASE:?}" HEAD; fi)
fi

groups='"docs"'
if [ "$sluis" = true ]; then
  groups="$groups,\"go-build\",\"go-test\",\"go-lint\",\"web\""
else
  # one go-lint, however many modules ask for it
  if [ "$storage" = true ] || [ "$pulumi" = true ]; then groups="$groups,\"go-lint\""; fi
  [ "$web" = true ] && groups="$groups,\"web\""
fi
[ "$audit" = true ] && groups="$groups,\"audit\""

# The LocalStack and OpenBao tier: sluis's adapters, storage's backends, the
# secrets layout move (internal/migrate, internal/secretstore: sluis, above) and
# deploy/; and audit's tests that skip without Postgres, LocalStack or OpenBao.
services=""
if [ "$sluis" = true ] || [ "$storage" = true ] || [ "$pulumi" = true ]; then
  services="hack/s3-conformance.sh hack/dynamodb-conformance.sh hack/storage-conformance.sh hack/keys-conformance.sh hack/openbao-conformance.sh"
fi
[ "$audit" = true ] && services="${services:+$services }hack/audit-service-tests.sh"

{
  echo "groups=[$groups]"
  echo "services=$services"
  echo "audit=$audit"
} >> "${GITHUB_OUTPUT:-/dev/stdout}"
