#!/usr/bin/env bash
# The OpenID Foundation conformance suite's Config profile against a deployed
# issuer: discovery and the key set. It needs no client, no secret and nobody at
# a browser, so it runs unattended; the two profiles that sign somebody in are
# run by a person (docs/guides/sluis/run-conformance.md). Not scheduled, and not
# on a tag: a tag is a build, not a deployment, so a run would certify the OLD
# issuer against the NEW version.
#
#   ISSUER=https://access.example.com hack/conformance-config.sh   (`just conformance-config <issuer>`)
#
# Needs docker (compose), curl and jq. The suite answers on a PUBLIC name that
# resolves to 127.0.0.1, which is how it gets a real certificate on a runner.
# The suite's log is kept in dist/conformance/, and the suite is stopped on
# exit.
#
# The images are public. A stale `docker login registry.gitlab.com` makes the
# pull fail with "HTTP Basic: Access denied": point DOCKER_CONFIG at a directory
# that holds `{}` and run again.
set -euo pipefail
cd "$(dirname "$0")/.."
issuer=${ISSUER:?usage: ISSUER=<issuer url> hack/conformance-config.sh}
suite=https://localhost.emobix.co.uk:8443
work=$(mktemp -d)
cleanup() {
  mkdir -p dist/conformance
  curl -sk "$suite/api/log" -o dist/conformance/runs.json || true
  (cd "$work/suite" && docker compose -f docker-compose-prebuilt.yml down -v) || true
  # The database's directory belongs to the container's user.
  docker run --rm -v "$work:/w" alpine rm -rf /w/suite 2>/dev/null || true
  rm -rf "$work" || true
}
trap cleanup EXIT

git clone --depth 1 https://gitlab.com/openid/conformance-suite.git "$work/suite"
(cd "$work/suite" && docker compose -f docker-compose-prebuilt.yml up -d)

for _ in $(seq 1 60); do
  [ "$(curl -sk -o /dev/null -w '%{http_code}' "$suite/api/runner/available" || true)" = 200 ] && break
  sleep 5
done
[ "$(curl -sk -o /dev/null -w '%{http_code}' "$suite/api/runner/available" || true)" = 200 ] || { echo "::error::the suite never came up" >&2; exit 1; }

echo "suite: $(curl -sk "$suite/api/server" | jq -r '.version // "unknown"')"

code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$issuer/.well-known/openid-configuration")
[ "$code" = 200 ] || { echo "::error::$issuer answered $code for its discovery document" >&2; exit 1; }

plan=$(jq -n --arg d "$issuer/.well-known/openid-configuration" '{
  description: "access-issuer - Config profile (CI)",
  server: {discoveryUrl: $d},
  client: {client_id: "conformance", client_secret: "unused-here"}}' |
  curl -sk -X POST "$suite/api/plan?planName=oidcc-config-certification-test-plan" \
    -H 'Content-Type: application/json' --data-binary @- | jq -r .id)
[ -n "$plan" ] && [ "$plan" != null ] || { echo "::error::the suite would not create a plan" >&2; exit 1; }

echo "plan: $suite/plan-detail.html?plan=$plan"
hack/conformance-run.sh "$plan"
