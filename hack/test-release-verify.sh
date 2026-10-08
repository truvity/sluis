#!/usr/bin/env bash
# release-verify must fail on a tampered asset. A local fixture stands in for
# a release: two files and the checksums.txt written for them.
#
# The cosign and `gh attestation` parts are NOT exercised here: they need a
# published release, the network and a Sigstore identity, none of which a
# test has (RELEASE_VERIFY_SKIP_SIGNATURES=1 leaves them out). They run for
# real in an estate's CI and in `just release-verify VERSION`.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT
export RELEASE_VERIFY_DIR="$dir" RELEASE_VERIFY_SKIP_SIGNATURES=1

printf 'one\n' > "$dir/sluis_1.0.0_linux_amd64.tar.gz"
printf 'two\n' > "$dir/sluis-lambda_1.0.0_linux_arm64.zip"
(cd "$dir" && sha256sum sluis_1.0.0_linux_amd64.tar.gz sluis-lambda_1.0.0_linux_arm64.zip > checksums.txt)

run() { "$root/hack/release-verify.sh" v1.0.0 2>&1; }

out="$(run)" || { echo "test-release-verify: an intact release failed: $out" >&2; exit 1; }

expect_failure() {
  if out="$(run)"; then echo "test-release-verify: $1: verification passed" >&2; exit 1; fi
  case "$out" in *"$2"*) ;; *) echo "test-release-verify: $1: failed, but not with '$2': $out" >&2; exit 1 ;; esac
}

printf 'tampered\n' >> "$dir/sluis_1.0.0_linux_amd64.tar.gz"
expect_failure "tampered asset" "SHA-256"

printf 'one\n' > "$dir/sluis_1.0.0_linux_amd64.tar.gz"
rm "$dir/sluis-lambda_1.0.0_linux_arm64.zip"
expect_failure "missing asset" "not an asset"

rm "$dir/checksums.txt"
expect_failure "no checksums.txt" "checksums.txt is not among"

echo "release-verify: tamper tests ok"
