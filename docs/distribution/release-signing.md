---
title: Release signing
description: How sluis releases are signed and how to verify each artifact type, with per-file checksums, cosign keyless, OpenPGP metadata and build provenance.
status: target
sidebar:
  order: 2
---

Every sluis artifact can be checked against more than one signature, made by different keys and published in different places, so one compromised channel doesn't let a bad artifact through.

## What a release does

- **One version per repository.** The whole repository has one version, and every artifact of a release carries it.
- **GoReleaser v2**, open-source edition, builds the release. Rust builds are included. The YubiKey driver builds in a native per-architecture CI job because it needs cgo against pcsc-lite.
- **A `.sha256` per file**, and a **`SHA256SUMS`** file for the release, signed twice: a cosign signature (`.sig`) and an OpenPGP signature (`.asc`).
- **cosign keyless signing** through GitHub OIDC. There is no long-lived cosign private key; the signature is tied to the workflow identity that ran in the repository.
- **OpenPGP-signed apt and dnf metadata.** The primary key is offline, on a hardware key. CI signs with an **expiring signing subkey** certified by that primary key.
- **Build provenance attestations**, made by GitHub, for every artifact.
- **Two copies.** R2 serves the files; GitHub Releases is the immutable record.

## How to verify

### A single file

Download the file and its `.sha256`, then check it.

```sh
# Example
sha256sum -c sluis_linux_arm64.tar.gz.sha256
```

This catches a damaged download. It doesn't prove who made the file, because the `.sha256` comes from the same place.

### A release as a whole

Check the signed `SHA256SUMS`, then check each file against it. There are two independent signatures; verify at least one, and prefer both.

```sh
# Example: cosign keyless
cosign verify-blob \
  --certificate-identity-regexp '^https://github.com/truvity/sluis/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature SHA256SUMS.sig \
  --certificate SHA256SUMS.pem \
  SHA256SUMS

# Example: OpenPGP
gpg --verify SHA256SUMS.asc SHA256SUMS

sha256sum -c SHA256SUMS --ignore-missing
```

The certificate identity pins the repository, so a signature made by any other repository's workflow fails. The release OpenPGP public key and the cosign public key are in `keys/`.

:::note[Proposed]
Where the cosign certificate is stored (a `.pem` beside the `.sig`, or inside a bundle) is not yet decided. The commands show the first form.
:::

### apt and dnf packages

The package manager verifies the repository for you.

- **apt** verifies `InRelease` (or `Release` with `Release.gpg`) against the key named by `signed-by`. A `NO_PUBKEY` or `BADSIG` error means the key or the metadata is not the one you set up.
- **dnf** verifies the metadata with `repo_gpgcheck=1` and each package with `gpgcheck=1`.

Verify the key itself when you add it: the primary key's fingerprint is stable across subkey changes, so compare it with the copy attached to the GitHub release. See [Packages](/distribution/packages/) for the setup.

When CI rotates the expiring signing subkey, the public key file in `keys/` is updated with the new subkey, under the same primary key. Refresh your copy of the public key when a repository starts failing after the old subkey's expiry date.

### Binaries and Lambda zips

Verify `SHA256SUMS` as above, then check the file against it. Build provenance confirms what built the file:

```sh
# Example
gh attestation verify sluis_linux_arm64.tar.gz --repo truvity/sluis
```

### OCI images and Helm charts

Images and charts are signed by cosign keyless, and each carries a provenance attestation. Use the digest, not a tag.

```sh
# Example
cosign verify ghcr.io/truvity/sluis-central@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/truvity/sluis/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/truvity/sluis-central@sha256:<digest> --repo truvity/sluis
```

A Helm chart pins its images by digest, so verifying the chart and installing it gives the images that were released with it.

## How CI uploads

CI has no static upload key.

1. The release workflow gets a GitHub OIDC token.
2. It exchanges that token with sluis.
3. sluis mints a short-lived Cloudflare R2 token scoped to the distribution bucket.
4. CI uploads the files with that token, and the token expires.

Only the workflow identity that the policy names can obtain the token, so a fork or a pull request can't publish. The same workflow signs with cosign keyless and asks GitHub for the provenance attestations.

## What a signature does not prove

- A signature shows who published the file and that it wasn't changed. It doesn't show that the code is free of bugs.
- The OpenPGP signing subkey is on CI. If CI is compromised during its validity, a bad repository can be signed. The offline primary key limits that to the subkey's life, and the cosign identity, the GitHub attestation and the immutable release are independent checks that don't depend on that subkey.
- A new install trusts the key it first sees. Compare the fingerprint on a different channel.
