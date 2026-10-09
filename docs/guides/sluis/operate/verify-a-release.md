# Verify a release

Check that a release is the one this repository's release workflow built. `hack/release-verify.sh` implements the procedure; copy it into your CI.

## What a release carries

| What | Where | Made by |
|---|---|---|
| `checksums.txt`, the SHA-256 of every archive, SBOM and bundle of both products | release asset | goreleaser, combined by the `audit` job |
| `checksums.txt.sigstore.json`, a keyless cosign bundle over `checksums.txt` | release asset | the `attest` job |
| a build-provenance attestation for every release asset | GitHub attestations | the `attest` job |
| `<archive>.sbom.spdx.json` per archive, and an SBOM attestation binding it | release asset, attestation | syft, `attest-sboms` |
| per image digest: a cosign signature, a provenance attestation and an SPDX SBOM attestation; per OCI Helm chart: a signature and provenance | the registry | `attest-oci` |

Signing is keyless. The short-lived certificate names `.github/workflows/release.yaml` at the release tag, and the signature is recorded in the public Sigstore transparency log (Rekor).

## Steps

1. Install `gh` (logged in, with a token that reads the repository) and `cosign` 3 or later.

2. Run the script on a tag.

   ```sh
   just release-verify v1.74.0
   # or, without the repository's toolchain:
   hack/release-verify.sh v1.74.0
   ```

   It downloads the release and fails on the first of these:

   1. An asset whose SHA-256 differs from `checksums.txt`, or a listed file that is not an asset.
   2. A `checksums.txt.sigstore.json` that does not verify `checksums.txt` with `--certificate-identity-regexp '^https://github.com/truvity/sluis/.github/workflows/release.yaml@refs/tags/v'` and `--certificate-oidc-issuer https://token.actions.githubusercontent.com`.
   3. An asset with no attestation from `truvity/sluis/.github/workflows/release.yaml`.
   4. An image or Helm chart whose signature or attestation fails. The list is `hack/release-images.txt`: copy it beside the script. `RELEASE_VERIFY_SKIP_IMAGES=1` skips them.

   The identity is pinned to a `v*` tag of the release workflow. A run from another ref or a fork cannot match it.

3. To verify one image by hand:

   ```sh
   ref=ghcr.io/truvity/sluis/sluis:1.74.0
   cosign verify "$ref" \
     --certificate-identity-regexp '^https://github.com/truvity/sluis/.github/workflows/release.yaml@refs/tags/v' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   gh attestation verify "oci://$ref" --repo truvity/sluis \
     --signer-workflow truvity/sluis/.github/workflows/release.yaml
   ```

Deploy by digest: verify the tag once, resolve its digest and pin it.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `checksums.txt.sigstore.json is not among the assets` | the release predates signing, or the `attest` job has not finished |
| `no valid attestation` for a listed file | `gh` is not logged in, or the file was replaced after the release |
| a mismatch on one archive only | the asset was re-uploaded: take the release again and report it if it still differs |
