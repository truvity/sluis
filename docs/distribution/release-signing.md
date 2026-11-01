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
- **A `.sha256` per file**, and a **`SHA256SUMS`** file for the release, signed once with cosign (`.sig`) and once with OpenPGP (`.asc`). The OpenPGP signature verifies against the release keyring (see [The OpenPGP release key](#the-openpgp-release-key)).
- **cosign keyless signing** through GitHub OIDC. There is no long-lived cosign private key; the signature is tied to the workflow identity that ran in the repository.
- **OpenPGP-signed apt and dnf metadata.** The release is signed by sluis's signing API with an **expiring signing subkey** that a primary bound; the primaries are held by ritueel. See [The OpenPGP release key](#the-openpgp-release-key).
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

Check the signed `SHA256SUMS`, then check each file against it. There are two independent signatures, cosign and OpenPGP; verify at least one, and prefer both.

```sh
# Example: cosign keyless
cosign verify-blob \
  --certificate-identity-regexp '^https://github.com/truvity/sluis/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --signature SHA256SUMS.sig \
  --certificate SHA256SUMS.pem \
  SHA256SUMS

# Example: OpenPGP, with the release keyring
gpgv --keyring ./sluis-release-keyring.gpg SHA256SUMS.asc SHA256SUMS

sha256sum -c SHA256SUMS --ignore-missing
```

The certificate identity pins the repository, so a signature made by any other repository's workflow fails. The release keyring and the cosign public key are in `keys/`.

:::note[Proposed]
Where the cosign certificate is stored (a `.pem` beside the `.sig`, or inside a bundle) is not yet decided. The commands show the first form. The OpenPGP signature file is named `SHA256SUMS.asc` here, and that name is a proposal too.
:::

### apt and dnf packages

The package manager verifies the repository for you.

- **apt** verifies `InRelease` (or `Release` with `Release.gpg`) against the key named by `signed-by`. A `NO_PUBKEY` or `BADSIG` error means the key or the metadata is not the one you set up.
- **dnf** verifies the metadata with `repo_gpgcheck=1` and each package with `gpgcheck=1`.

Verify the key itself when you add it: a primary key's fingerprint is stable across subkey changes, so compare it with the copy attached to the GitHub release. The keyring package carries every active primary and every current subkey binding; use it, not one primary's certificate. See [Packages](/distribution/packages/) for the setup.

A signing subkey lasts about 13 months. Each year its expiry is extended with a new binding over the same subkey, and each extension ships in a new keyring package. The key itself changes only on an event or after three years at most. Refresh your keyring when a repository starts failing after the old subkey's expiry date.

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

- **The primaries are ritueel's.** There are two primary keys, one on each root card, held and used only at ritueel's yearly ceremony. They are redundancy, not two-person control. How they are made, why there are two, how the cards alternate and what to do when one is lost are taught at [ritueel.id](https://ritueel.id/concepts/openpgp-release-keys/). sluis does not hold a primary and does not run that ceremony.
- **A signing subkey does the work.** It is generated on the signing key store (a node's YubiKey, or a cloud key service), bound by the primary of the year, and carries a back-signature that `key-holder` makes on the card ([ADR 0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/)). It lasts about 13 months, and each year a new binding over the same subkey extends its expiry, so years overlap. The subkey itself is replaced only on an event or after three years at most ([ADR 0012](/decisions/0012-cryptography-and-authority-keys/#rotation)). sluis warns 60, 30 and 14 days before it expires.
- **Users verify with the keyring package,** which carries both primaries and every current subkey binding. A verifier that holds only one primary's certificate cannot check a signature from a subkey that the other primary bound.
- **A subkey is revoked by the primary of the year.** The revocation reaches users through a new keyring package.
- **The maintainer's personal key certifies each release key** is an open question, tracked with the publication research (ritueel issue 28).

### How releases are signed

sluis signs as a service. CI proves its GitHub OIDC identity to the signing API, policy decides which repository, branch or workflow may use which key, sluis signs in place and records the signature in the audit log, and the key is never shown. CI holds no release signing key of its own. The project's own releases are the first user, signed with a subkey held in a cloud key service. Sigstore provenance is published alongside.

The output is an ASCII-armoured detached signature, delivered in stages:

| Stage | What is signed |
|---|---|
| First version | A detached `.asc` signature over a file or a digest, such as `SHA256SUMS` |
| v1 parity | The `Release.gpg` and `InRelease` files of an apt repository, and the `repomd.xml.asc` of an rpm repository |
| Later | The header signature of each rpm package, and a stand-in for `gpg` that git can call, for CI |

The design is in [ADR 0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/).

### Packaging notes

Measured on 2026-10-29 and 2026-10-31 with `gpg` and `gpgv` 2.2.27 to 2.4.8, `sqv` 1.1.0 to 1.5.0, apt 2.4 to 3.0 and rpm 6.0 (rpm-sequoia): every verifier accepts ECDSA P-384 release signatures. The details of what was tested are at [ritueel.id](https://ritueel.id/concepts/openpgp-release-keys/).

- **`gpgv` needs a binary keyring.** It cannot read an armored file. apt takes either a binary `.gpg` or an armored `.asc` in `Signed-By`.
- **rpm imports armored keys only.**
- **apt's `Release.gpg` must be armored**, whatever the key structure. A binary detached signature fails with `NODATA` (apt 2.4, checked again on 2026-10-31).
- **No encryption subkey in the release certificate.** `gpg`'s card key generation adds an ECDH subkey with an AES-192 key wrap, and rpm-sequoia prints a policy warning about it on every install, although the signature verifies.

To check a release, a verifier needs the signature file and the release keyring:

```sh
# Example
gpgv --keyring ./sluis-release-keyring.gpg SHA256SUMS.asc SHA256SUMS
```

:::note[Proposed]
The name of the signature file is not decided. The commands use `SHA256SUMS.asc` as a stand-in. There is one signature file, and it verifies against the keyring.
:::

:::note[Proposed]
How a user finds and verifies the public key is under research. The plan is to use several independent channels, so one compromised channel cannot replace the key:

- keys.openpgp.org;
- the Web Key Directory, direct method, on sluis.id (static files, served as assets on Cloudflare Workers, [ADR 0020](/decisions/0020-the-site-is-static-on-cloudflare-workers/));
- DNS `OPENPGPKEY` records (RFC 7929), if DNSSEC is enabled on the zone;
- `SECURITY.md` in the repository;
- the GitHub release;
- the site.

The exact set of channels is not decided. To check a signature, a user can use `gpgv`, Sequoia `sqv`, `cosign`, `gh attestation verify` or `slsa-verifier`.
:::

## How CI uploads

CI has no static upload key, and no release signing key.

1. The release workflow gets a GitHub OIDC token.
2. It exchanges that token with sluis.
3. sluis mints a short-lived Cloudflare R2 token scoped to the distribution bucket.
4. CI uploads the files with that token, and the token expires.

Only the workflow identity that the policy names can obtain the token, so a fork or a pull request can't publish. The same workflow signs with cosign keyless and asks GitHub for the provenance attestations.

## What a signature does not prove

- A signature shows who published the file and that it wasn't changed. It doesn't show that the code is free of bugs.
- The OpenPGP signing subkey is on a card or in a key service that signs without a touch. If the signer is compromised during the subkey's validity, a bad repository can be signed. The primary keys limit that to the subkey's life, and revoking the subkey with the primary of the year ends it. The cosign identity, the GitHub attestation and the immutable release are independent checks that don't depend on that subkey.
- A new install trusts the key it first sees. Compare the fingerprint on a different channel, for example the copy in the repository's `SECURITY.md`.
