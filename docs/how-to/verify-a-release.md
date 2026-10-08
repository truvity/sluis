# Verify a release

## Purpose

Check that a published release is the one this repository's release workflow built, before an installation or a CI
pipeline takes anything from it. The release is checksummed, signed and attested; this page is the procedure, and
`hack/release-verify.sh` is the one implementation of it. An installation's CI copies that script.

## What a release carries

| what | where | made by |
| -- | -- | -- |
| `checksums.txt`, the SHA-256 of every archive, SBOM and bundle of both products | release asset | goreleaser, the sluis and audit lines combined by the `audit` job |
| `checksums.txt.sigstore.json`, a keyless cosign bundle over `checksums.txt` | release asset | the `attest` job, after the lines are combined |
| a build-provenance attestation for every release asset | GitHub attestations | the `attest` job |
| `<archive>.sbom.spdx.json`, an SPDX SBOM per archive, and an SBOM attestation binding it to the archive | release asset, attestation | syft in goreleaser, `attest-sboms` |
| for every image digest: a cosign signature, a build-provenance attestation and an SPDX SBOM attestation | the registry, beside the image | `attest-images` |

Nothing is signed with a long-lived key. The signing certificate is short-lived and names the workflow that asked for it
(`.github/workflows/release.yaml` at the release tag), issued against the workflow's GitHub OIDC token. Keyless means the
certificate and the signature are recorded in Sigstore's public transparency log (Rekor), so that the fact that this
workflow signed this digest is public and cannot be rewritten afterwards. The log holds the repository, the workflow and
the tag, nothing secret.

## Verify

You need `gh` (logged in; a token that can read the repository) and `cosign` 3 or later.

```sh
just release-verify v1.74.0
# or, without the repository's toolchain:
hack/release-verify.sh v1.74.0
```

It downloads the release and fails on the first of:

1. an asset whose SHA-256 differs from `checksums.txt`, or a file `checksums.txt` lists that is not an asset;
2. a `checksums.txt.sigstore.json` that does not verify `checksums.txt` with
   `--certificate-identity-regexp '^https://github.com/truvity/sluis/.github/workflows/release.yaml@refs/tags/v'` and
   `--certificate-oidc-issuer https://token.actions.githubusercontent.com`;
3. an asset with no attestation from `truvity/sluis/.github/workflows/release.yaml`
   (`gh attestation verify <file> --repo truvity/sluis --signer-workflow ...`);
4. an image whose signature or attestation does not verify. `RELEASE_VERIFY_SKIP_IMAGES=1` leaves the images out.

The identity is pinned to a `v*` tag of the release workflow, not to a branch: a workflow run from another ref, or from a
fork, cannot produce a certificate that matches.

## Verify one image by hand

```sh
ref=ghcr.io/truvity/sluis/sluis:1.74.0
cosign verify "$ref" \
  --certificate-identity-regexp '^https://github.com/truvity/sluis/.github/workflows/release.yaml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify "oci://$ref" --repo truvity/sluis \
  --signer-workflow truvity/sluis/.github/workflows/release.yaml
```

Deploy by digest, not by tag: verify the tag once, resolve its digest, and pin that digest.

## Troubleshooting

| symptom | cause |
| -- | -- |
| `checksums.txt.sigstore.json is not among the assets` | the release predates signing, or the `attest` job has not finished; a release is complete when it has |
| `no valid attestation` for a file the release lists | `gh` is not logged in, or the file was replaced after the release |
| a mismatch on one archive only | the asset was re-uploaded; take the release again, and report it if it still differs |
