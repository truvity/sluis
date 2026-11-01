---
title: Distribution
description: How sluis v2 is built, versioned, signed and delivered, from one repository version to signed packages, binaries, Lambda zips, images and charts.
status: target
sidebar:
  order: 0
---

Distribution describes what a release contains, where it is served from, and how to verify every artifact.

## At a glance

- **One version for the whole repository.** Every artifact of a release carries the same version. A release is built with GoReleaser v2, open-source edition, which includes the Rust builds. The YubiKey driver needs cgo against pcsc-lite, so it is built in a native per-architecture CI job.
- **Packaging per runtime, for amd64 and arm64:**
  - bare-metal: `.deb` and `.rpm` packages in signed apt and dnf repositories, and a tarball;
  - serverless: a Lambda zip per actor, with layers and extensions;
  - kubernetes: multi-arch OCI images on GHCR and a Helm chart as an OCI artifact, with images pinned by digest.
  - There are no build profiles. `dev` is not a build; it is how the local installation is run.
- **Two homes.** `distribution.sluis.id` is a Cloudflare R2 bucket in the same account as the sluis.id zone. Everything is served from R2 with no redirects. GitHub Releases keeps the same files as the immutable record, with GitHub build provenance attestations.
- **Signed every way a user checks it.** A `.sha256` per file, a signed `SHA256SUMS`, cosign keyless signatures, OpenPGP-signed apt and dnf metadata, and provenance attestations.
- **No static upload key.** CI exchanges its GitHub OIDC token with sluis, which mints a short-lived R2 token.
- **The website is separate.** `sluis.id` is served as static assets on Cloudflare Workers, deployed by GitHub Actions, with a small live status API beside it ([ADR 0020](/decisions/0020-the-site-is-static-on-cloudflare-workers/)). Only distribution is on R2.

## Pages

| Page | What it covers |
|---|---|
| [Packages](/distribution/packages/) | The R2 layout, apt and dnf setup, binaries, Lambda zips, OCI images and Helm charts |
| [Release signing](/distribution/release-signing/) | How artifacts are signed, how to verify each type, and how CI uploads |
| [Platforms](/distribution/platforms/) | Supported distributions, architectures, the systemd floor, board images and FIPS |

## Why R2 and no redirects

apt and dnf handle redirects unreliably, and a redirect chain is one more place for a repository to be pointed somewhere else. Serving every path directly from one bucket keeps the URL a user pins and the file they get the same thing. GitHub Releases is the second copy, and the one to compare with if the first is in doubt.
