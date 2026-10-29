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
- **OpenPGP-signed apt and dnf metadata.** There are two primary keys, offline, one on each card of a pair of YubiKeys, and each release is signed once with each. CI signs with **expiring signing subkeys** certified by those primary keys. See [The OpenPGP release key](#the-openpgp-release-key).
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
cosign verify ghcr.io/truvity/sluis-entry@sha256:<digest> \
  --certificate-identity-regexp '^https://github.com/truvity/sluis/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/truvity/sluis-entry@sha256:<digest> --repo truvity/sluis
```

A Helm chart pins its images by digest, so verifying the chart and installing it gives the images that were released with it.

## The OpenPGP release key

The OpenPGP key is a **dedicated sluis release identity**, not anyone's personal key.

- **Each card of a pair of YubiKeys generates its own primary key on the card**, so no primary key ever exists outside hardware. No key is ever copied between the cards, so each card keeps its attestation. There are two primary keys, and both are authoritative; the second card is not a spare.
- **Every release is signed twice.** Each primary key has its own signing subkey, and the release is signed once with each, so either fingerprint verifies every release.
- **CI signs with expiring subkeys.** Each subkey is certified by its primary key. When one expires, a new subkey is published under the same primary key.
- **The maintainer's personal key certifies each release key**, so a user who already trusts the maintainer's key has a path to the release identity.
- **Nothing is signed before phase 2** of the [roadmap](/roadmap/). The guides and tests show how to generate each primary key on its own YubiKey, as a key ceremony, so the ceremony is something anyone can read and rehearse. The ceremony is also planned as a feature of sluis itself (`sluis pki` or `sluis ceremony`) for the keys on the cards.

:::note[Proposed]
How a user finds and verifies the public key is under research. The plan is to use several independent channels, so one compromised channel cannot replace the key:

- keys.openpgp.org;
- the Web Key Directory, direct method, on sluis.id (static files work on GitHub Pages);
- DNS `OPENPGPKEY` records (RFC 7929), if DNSSEC is enabled on the zone;
- `SECURITY.md` in the repository;
- the GitHub release;
- the site.

The exact set of channels is not decided. To check a signature, a user can use `gpgv`, Sequoia `sqv`, `cosign`, `gh attestation verify` or `slsa-verifier`.
:::

## How CI uploads

CI has no static upload key.

1. The release workflow gets a GitHub OIDC token.
2. It exchanges that token with sluis.
3. sluis mints a short-lived Cloudflare R2 token scoped to the distribution bucket.
4. CI uploads the files with that token, and the token expires.

Only the workflow identity that the policy names can obtain the token, so a fork or a pull request can't publish. The same workflow signs with cosign keyless and asks GitHub for the provenance attestations.

## What a signature does not prove

- A signature shows who published the file and that it wasn't changed. It doesn't show that the code is free of bugs.
- The OpenPGP signing subkey is on CI. If CI is compromised during its validity, a bad repository can be signed. The offline primary keys limit that to the subkey's life, and the cosign identity, the GitHub attestation and the immutable release are independent checks that don't depend on that subkey.
- A new install trusts the key it first sees. Compare the fingerprint on a different channel, for example the copy in the repository's `SECURITY.md`.
