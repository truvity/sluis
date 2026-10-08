#!/usr/bin/env bash
# Verify a published release of this repository, the way an estate's CI does
# before it installs anything from it.
#
#   hack/release-verify.sh v1.74.0
#
# Three checks, in this order, and the first failure stops the run:
#
#   1. SHA-256: every file listed in checksums.txt matches the downloaded
#      asset, and every other asset is named (an unlisted asset is only a
#      warning: the audit checksums, the signature bundle and the Nix flakes
#      are not in the combined file).
#   2. The cosign bundle of checksums.txt, keyless: the certificate must be
#      the one this repository's release workflow at a v* tag was issued, by
#      GitHub's OIDC issuer. The signature and the certificate are recorded
#      in Sigstore's public transparency log.
#   3. `gh attestation verify` for each asset (build provenance and, for the
#      SBOMs, the SBOM attestation), signed by the release workflow; then the
#      images: cosign signature and attestations on each digest.
#
# Environment:
#   REPO                          owner/name (default truvity/sluis)
#   RELEASE_VERIFY_DIR            verify the files in this directory instead of
#                                 downloading the release (used by the test)
#   RELEASE_VERIFY_SKIP_SIGNATURES=1  run check 1 only. The test uses it: it has
#                                 no release, no network and no OIDC token.
#   RELEASE_VERIFY_SKIP_IMAGES=1  leave the images out of check 3
#
# Needs: sha256sum; for the rest, gh (logged in) and cosign (both in devbox).
set -euo pipefail

version="${1:?usage: release-verify.sh VERSION (for example v1.74.0)}"
tag="v${version#v}"
repo="${REPO:-truvity/sluis}"
workflow="$repo/.github/workflows/release.yaml"
identity_regexp="^https://github.com/$repo/.github/workflows/release.yaml@refs/tags/v"
issuer=https://token.actions.githubusercontent.com

fail() { echo "release-verify: FAIL: $*" >&2; exit 1; }

if [ -n "${RELEASE_VERIFY_DIR:-}" ]; then
  dir="$RELEASE_VERIFY_DIR"
else
  dir="$(mktemp -d)"
  trap 'rm -rf "$dir"' EXIT
  gh release download "$tag" --repo "$repo" --dir "$dir"
fi
cd "$dir"
test -f checksums.txt || fail "checksums.txt is not among the assets"

# 1. Checksums. Read the file ourselves rather than `sha256sum -c`, which
# ignores a malformed line and, with --ignore-missing, a missing file.
listed=0
while read -r want name extra; do
  [ -n "$want" ] || continue
  [ -z "$extra" ] || fail "checksums.txt: unexpected line for $name"
  name="${name#\*}"
  test -f "$name" || fail "checksums.txt lists $name, which is not an asset"
  got="$(sha256sum "$name" | cut -d' ' -f1)"
  [ "$got" = "$want" ] || fail "$name: SHA-256 is $got, checksums.txt says $want"
  listed=$((listed + 1))
done < checksums.txt
[ "$listed" -gt 0 ] || fail "checksums.txt lists nothing"
echo "checksums: $listed files match checksums.txt"

shopt -s nullglob
for f in *; do
  case "$f" in
    checksums.txt|checksums.txt.sigstore.json) continue ;;
  esac
  grep -q "  \*\?$f\$" checksums.txt || echo "release-verify: warning: $f is not in checksums.txt" >&2
done

if [ "${RELEASE_VERIFY_SKIP_SIGNATURES:-}" = 1 ]; then
  echo "release-verify: signatures and attestations SKIPPED (RELEASE_VERIFY_SKIP_SIGNATURES=1)" >&2
  exit 0
fi

# 2. The signature on checksums.txt.
test -f checksums.txt.sigstore.json || fail "checksums.txt.sigstore.json is not among the assets"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp "$identity_regexp" \
  --certificate-oidc-issuer "$issuer" \
  checksums.txt || fail "checksums.txt: the cosign bundle does not verify"

# 3. Attestations: every asset but the bundle itself, which is the signature.
n=0
for f in *; do
  [ "$f" = checksums.txt.sigstore.json ] && continue
  gh attestation verify "$f" --repo "$repo" --signer-workflow "$workflow" > /dev/null \
    || fail "$f: no valid attestation from $workflow"
  n=$((n + 1))
done
echo "attestations: $n assets verified"

if [ "${RELEASE_VERIFY_SKIP_IMAGES:-}" != 1 ]; then
  version_tag="${tag#v}"
  for image in sluis/sluis sluis/resource-proxy audit/audit audit/audit-query \
    audit/audit-notary audit/audit-observe audit/audit-writer; do
    ref="ghcr.io/${repo%/*}/$image:$version_tag"
    cosign verify "$ref" \
      --certificate-identity-regexp "$identity_regexp" \
      --certificate-oidc-issuer "$issuer" > /dev/null \
      || fail "$ref: the cosign signature does not verify"
    gh attestation verify "oci://$ref" --repo "$repo" --signer-workflow "$workflow" > /dev/null \
      || fail "$ref: no valid attestation from $workflow"
    echo "image: $ref verified"
  done
fi
echo "release-verify: $tag ok"
