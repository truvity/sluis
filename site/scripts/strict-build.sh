#!/usr/bin/env bash
# A strict build: `astro build` must exit 0 and print no warning and no error.
# Astro has no --strict, and a warning that nobody reads is how a page ends up
# without its styles or a collection without its entries. The links validator
# (starlight-links-validator) fails the build itself on a broken internal link.
#
# usage: site/scripts/strict-build.sh   (from anywhere)
set -euo pipefail
cd "$(dirname "$0")/.."

log=$(mktemp)
trap 'rm -f "$log"' EXIT

rm -rf dist
# NO_COLOR keeps the log greppable; the build's own exit status is checked below.
status=0
NO_COLOR=1 ASTRO_TELEMETRY_DISABLED=1 yarn build 2>&1 | tee "$log" || status=$?
[ "${PIPESTATUS[0]}" -eq 0 ] || status=${PIPESTATUS[0]}
[ "$status" -eq 0 ] || { echo "strict-build: astro build exited $status" >&2; exit "$status"; }

if grep -n -i -E '\[(warn|warning|error)\]|\bwarning:' "$log" >&2; then
  echo "strict-build: the build printed the lines above; a strict build prints none" >&2
  exit 1
fi
echo "strict-build: ok, no warnings"
