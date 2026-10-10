#!/usr/bin/env bash
# release-verify must fail on a tampered asset, and on a Lambda zip that is
# not what it must be (hack/check-zips.sh). A local fixture stands in for a
# release: a tarball, the Lambda zips of hack/release-zips.txt, and the
# checksums.txt written for them.
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

# zipof NAME CONTENT: sluis-NAME_1.0.0_linux_arm64.zip holding bootstrap = CONTENT.
zipof() {
  python3 -I - "$dir/sluis-$1_1.0.0_linux_arm64.zip" "$2" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as z:
    z.writestr("bootstrap", sys.argv[2])
PY
}
sums() { (cd "$dir" && sha256sum sluis_1.0.0_linux_amd64.tar.gz sluis-*_1.0.0_linux_arm64.zip > checksums.txt); }

printf 'one\n' > "$dir/sluis_1.0.0_linux_amd64.tar.gz"
fixture() {
  zipof issuer 'ELF sluis-module=issuer ELF'
  zipof cloudflare 'ELF sluis-module=cloudflare ELF'
  zipof backup 'ELF sluis-module=backup ELF'
  zipof lambda 'ELF unpinned ELF'
  sums
}
fixture

run() { "$root/hack/release-verify.sh" v1.0.0 2>&1; }

out="$(run)" || { echo "test-release-verify: an intact release failed: $out" >&2; exit 1; }

expect_failure() {
  if out="$(run)"; then echo "test-release-verify: $1: verification passed" >&2; exit 1; fi
  case "$out" in *"$2"*) ;; *) echo "test-release-verify: $1: failed, but not with '$2': $out" >&2; exit 1 ;; esac
}

printf 'tampered\n' >> "$dir/sluis_1.0.0_linux_amd64.tar.gz"
expect_failure "tampered asset" "SHA-256"
printf 'one\n' > "$dir/sluis_1.0.0_linux_amd64.tar.gz"

# A module zip that is not pinned, or pinned to another module, or a legacy
# zip that carries a pin, would run as a module it was not built for.
zipof issuer 'ELF no pin ELF'; sums
expect_failure "an unpinned module zip" "is not pinned to its module"
zipof issuer 'ELF sluis-module=issuer sluis-module=backup ELF'; sums
expect_failure "a zip with another module's pin" "carries the pin of module backup"
fixture
zipof lambda 'ELF sluis-module=issuer ELF'; sums
expect_failure "a pinned legacy zip" "carries the pin of module issuer"
fixture

# Every zip is in checksums.txt, or the pin is not a pin.
(cd "$dir" && grep -v sluis-cloudflare checksums.txt > c && mv c checksums.txt)
expect_failure "a zip missing from checksums.txt" "is not in checksums.txt"
fixture

rm "$dir/sluis-backup_1.0.0_linux_arm64.zip"
sums
expect_failure "a module with no zip" "exactly one sluis-backup"
fixture

rm "$dir/sluis-lambda_1.0.0_linux_arm64.zip"
# The zip is listed in checksums.txt and no longer an asset.
expect_failure "missing asset" "not an asset"
fixture

rm "$dir/checksums.txt"
expect_failure "no checksums.txt" "checksums.txt is not among"

echo "release-verify: tamper tests ok"
