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
- **A `.sha256` per file**, and a **`SHA256SUMS`** file for the release, signed once with cosign (`.sig`) and once with OpenPGP (`.asc`). The one OpenPGP signature verifies with either of the two release certificates (see [why](#why-two-primaries-and-one-shared-subkey)).
- **cosign keyless signing** through GitHub OIDC. There is no long-lived cosign private key; the signature is tied to the workflow identity that ran in the repository.
- **OpenPGP-signed apt and dnf metadata.** There are two primary keys, offline, one on each card of a pair of YubiKeys. Once a signing installation exists, the release is signed with an **expiring signing subkey** that both primaries certify, so one signature verifies under either. See [The OpenPGP release key](#the-openpgp-release-key).
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

# Example: OpenPGP, with the certificate of either release primary
gpgv --keyring ./release-a.gpg SHA256SUMS.asc SHA256SUMS

sha256sum -c SHA256SUMS --ignore-missing
```

The certificate identity pins the repository, so a signature made by any other repository's workflow fails. The release OpenPGP public keys and the cosign public key are in `keys/`.

:::note[Proposed]
Where the cosign certificate is stored (a `.pem` beside the `.sig`, or inside a bundle) is not yet decided. The commands show the first form. The OpenPGP signature file is named `SHA256SUMS.asc` here, and that name is a proposal too.
:::

### apt and dnf packages

The package manager verifies the repository for you.

- **apt** verifies `InRelease` (or `Release` with `Release.gpg`) against the key named by `signed-by`. A `NO_PUBKEY` or `BADSIG` error means the key or the metadata is not the one you set up.
- **dnf** verifies the metadata with `repo_gpgcheck=1` and each package with `gpgcheck=1`.

Verify the key itself when you add it: a primary key's fingerprint is stable across subkey changes, so compare it with the copy attached to the GitHub release. See [Packages](/distribution/packages/) for the setup.

When a signing subkey is rotated, the public key files in `keys/` are updated with the new subkey, under the same primary keys. Refresh your copy of the public key when a repository starts failing after the old subkey's expiry date.

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

- **Two primary keys, one per root card.** Each card of a pair of YubiKeys generates its own primary key on the card, so no primary key ever exists outside hardware. No key is ever copied between the cards, so each card keeps its attestation. Both primaries are authoritative; the second card is not a spare. The same two cards hold the two [roots](/concepts/keys/#the-roots-and-the-ceremony).
- **Each node holds one signing subkey S<sub>i</sub>, bound under both primaries.** The subkey carries a binding signature and a back-signature under each primary. Every release gets **one signature**, and it verifies with certificate A or certificate B.
- **Creating or rotating S<sub>i</sub> needs both cards**, because each primary signs the binding and S<sub>i</sub> signs a back-signature to each.
- **Expiry.** The primaries expire in 2 years and are extended at each yearly drill. The subkeys last 2 years, staggered so that they do not all expire together.
- **Revocation.** Revoke a subkey under **both** primaries. `gpg` and `gpgv` ignore a revocation made under only one, so a verifier that holds a single certificate would still accept the subkey.
- **The maintainer's personal key certifies each release key**, so a user who already trusts the maintainer's key has a path to the release identity.
- **Nothing is signed before phase 2** of the [roadmap](/roadmap/). The guides and tests show how to generate each primary key on its own YubiKey, as a key ceremony, so the ceremony is something anyone can read and rehearse. The ceremony is also planned as a feature of sluis itself (`sluis ceremony`) for the keys on the cards.

### How releases are signed

- **Target.** sluis signs as a service. CI proves its GitHub OIDC identity to sluis, and a node card signs with S<sub>i</sub>. Sigstore provenance is published alongside. CI holds no release signing key of its own.
- **Until a v2 signing installation exists**, releases are signed offline, directly with the root cards' primaries, using `sluis ceremony`, with a touch for each release. There is no dedicated release card.

### Why two primaries and one shared subkey

Several signing keys are ordinary in OpenPGP practice. Checked on 2026-10-29:

- **Debian** signs a stable release's `Release` file with two keys, the archive key of the time and a per-release key, and the keyring package ships every active key. See the [archive signing keys](https://ftp-master.debian.org/keys.html), and the [SecureApt history](https://wiki.debian.org/SecureApt), where a transition signed with both the old and the new key was meant to work for clients that knew either one. These are background precedents for several active keys, not a design that sluis copies.
- **Arch Linux** has five master keys, and a packager key is valid when at least three of them have signed it. See the [master keys](https://archlinux.org/master-keys/).
- **Key transitions** keep the old and the new key valid together for a while, and the old key signs the new one. See the [Apache guide](https://infra.apache.org/key-transition.html) and the [Riseup template](https://help.riseup.net/en/security/message-security/openpgp/key-transition).

sluis uses two primary keys, one on each card, because a second card that is a spare is a copy, and a copy has lost its attestation. Two primaries exist so that a YubiKey hardware failure removes neither the ability to verify a release nor the ability to sign one. Loss and theft of a card fall to the lost-card procedure in [Keys](/concepts/keys/#a-lost-root-card).

Both primaries certify **one shared signing subkey**, instead of each primary having its own subkey and every release being signed twice. The reason is that one signature then verifies under either root. A verifier that holds only certificate A, only certificate B, or both gets a clean success from the same file. With two signatures in separate files, a verifier would have to know which file matches the certificate it holds, and with two signatures in one file `gpg` and `gpgv` exit with status 2 when one of them comes from a key the verifier does not have.

The choice was tested on 2026-10-29, with software keys, on these verifiers: `gpg` 2.4.8, `gpgv` 2.4.8, `sqv` 1.5.0, apt 2.6 (which verifies with `gpgv`), apt 3.0 (which verifies with `sqv`) and rpm 6.0 (which verifies with rpm-sequoia). Each was given certificate A, certificate B, and A and B together, in both import orders. All accepted the one signature every time, with no warning and no merge of the two certificates.

What the shared subkey costs, and what to do about it:

- **No threshold.** One signature counts once, not once per primary. A rule such as "both A and B must have endorsed this release" cannot be expressed with a shared subkey.
- **A compromise is shared.** Compromise of the subkey is compromise of both identities at signing time. The subkey sits on a node card that signs without a touch, so the controls that limit it are that card's PIN, its attestation, the staggered 2-year expiry and revocation.
- **Revoke under both primaries.** Sequoia-based verifiers (`sqv`, apt 3, rpm 6) enforce a revocation made under either primary. `gpg` and `gpgv` do not enforce one that is made under a single primary, and `gpgv` prints a plain "Good signature". A revocation published under only one primary can leave a verifier that holds only the other one trusting the subkey.
- **Both cards at creation and rotation**, as above.

Packaging notes, from the same tests:

- **`gpgv` needs a binary keyring.** It cannot read an armored file. apt takes either a binary `.gpg` or an armored `.asc` in `Signed-By`.
- **rpm imports armored keys only.**
- **apt's `Release.gpg` must be armored**, whatever the key structure.

To check a release, a verifier needs the signature file and the certificate of either primary:

```sh
# Example: the verifier holds certificate A
gpgv --keyring ./release-a.gpg SHA256SUMS.asc SHA256SUMS

# Example: the verifier holds certificate B
gpgv --keyring ./release-b.gpg SHA256SUMS.asc SHA256SUMS
```

:::note[Proposed]
The name of the signature file is not decided. The commands use `SHA256SUMS.asc` as a stand-in. The same file is published for both primaries; there is no second signature file.
:::

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

CI has no static upload key, and no release signing key.

1. The release workflow gets a GitHub OIDC token.
2. It exchanges that token with sluis.
3. sluis mints a short-lived Cloudflare R2 token scoped to the distribution bucket.
4. CI uploads the files with that token, and the token expires.

Only the workflow identity that the policy names can obtain the token, so a fork or a pull request can't publish. The same workflow signs with cosign keyless and asks GitHub for the provenance attestations.

## What a signature does not prove

- A signature shows who published the file and that it wasn't changed. It doesn't show that the code is free of bugs.
- The OpenPGP signing subkey is on a node card that signs without a touch. If a node is compromised during the subkey's validity, a bad repository can be signed. The offline primary keys limit that to the subkey's life, and revoking the subkey under both primaries ends it. The cosign identity, the GitHub attestation and the immutable release are independent checks that don't depend on that subkey.
- A new install trusts the key it first sees. Compare the fingerprint on a different channel, for example the copy in the repository's `SECURITY.md`.
