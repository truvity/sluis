---
title: Packages
description: The sluis release artifacts and where they live. The R2 layout, apt and dnf setup for amd64 and arm64, binaries, Lambda zips, OCI images and Helm charts.
status: target
sidebar:
  order: 1
---

A sluis release publishes every artifact from `https://distribution.sluis.id/`, a Cloudflare R2 bucket. GitHub Releases holds the same files as the immutable record. For how to check what you downloaded, see [Release signing](/distribution/release-signing/).

## The R2 layout

```text
apt/       dists/stable/{InRelease,Release,Release.gpg}
           dists/stable/main/binary-{amd64,arm64}/
           pool/
dnf/       {x86_64,aarch64}/{repodata/, packages}
           sluis.repo
bin/       <version>/<component>_linux_{amd64,arm64}.tar.gz
lambda/    <version>/{functions,layers,extensions}/<name>-{x86_64,arm64}.zip
checksums/ <version>/SHA256SUMS, .sig (cosign), .asc (OpenPGP)
keys/      the release OpenPGP public key, the cosign public key
oidc/      nodes/<node>/.well-known/openid-configuration, jwks.json
latest     pointer to the newest version
```

- Everything is served directly from R2. There are no redirects.
- `oidc/` is not a release artifact. It holds the workload-identity discovery documents and JWKS of the nodes, public keys only, at `https://distribution.sluis.id/oidc/nodes/<node>/`. See [Identity](/concepts/identity/).
- `latest` is a pointer to the newest version. Pin a version in anything you automate.

## apt (Debian and Ubuntu)

The repository has one suite, `stable`, and one component, `main`, for `amd64` and `arm64`.

```sh
# Example
curl -fsSLo sluis-release.asc https://distribution.sluis.id/keys/sluis-release.asc
gpg --show-keys --fingerprint sluis-release.asc    # compare the fingerprint before you continue
gpg --dearmor < sluis-release.asc | sudo tee /usr/share/keyrings/sluis.gpg > /dev/null

echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/sluis.gpg] https://distribution.sluis.id/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/sluis.list
sudo apt update && sudo apt install sluis
```

`$(dpkg --print-architecture)` prints `amd64` or `arm64`. The key is scoped to this one repository by `signed-by`.

## dnf (Rocky Linux and Amazon Linux)

```sh
# Example
sudo curl -fsSLo /etc/yum.repos.d/sluis.repo https://distribution.sluis.id/dnf/sluis.repo
sudo dnf install sluis
```

The repo file points at the architecture directory with `$basearch`, which is `x86_64` or `aarch64`:

```ini
# Example: /etc/yum.repos.d/sluis.repo
[sluis]
name=sluis
baseurl=https://distribution.sluis.id/dnf/$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://distribution.sluis.id/keys/sluis-release.asc
```

`gpgcheck=1` verifies each package and `repo_gpgcheck=1` verifies the repository metadata. The metadata is OpenPGP-signed; see [Release signing](/distribution/release-signing/).

## Binaries

Each component is a tarball for each architecture at `bin/<version>/<component>_linux_{amd64,arm64}.tar.gz`, with its `.sha256` beside it. Use a tarball when you manage the units yourself. The layout inside a package or tarball is the same:

```text
/usr/libexec/sluis/{roles,drivers,integrations,node,vendor}/<short-name>
```

## Lambda zips

Lambda is zip only, so layers and extensions combine. The zips are published under `lambda/<version>/` for `x86_64` and `arm64`:

- `functions/`: one zip per process role;
- `layers/`: shared layers, including the vendored Lambda Web Adapter (pinned v1.1.0), which is built into sluis's own layer and not referenced from the public AWS layer;
- `extensions/`: the drivers that run as Lambda extensions.

A function has at most five layers and 250 MB unzipped, including layers and extensions. See [AWS Lambda](/shapes/aws-lambda/).

## OCI images

Multi-arch images (amd64 and arm64) are published to GHCR. Pull by digest.

```sh
# Example
docker pull ghcr.io/truvity/sluis-entry@sha256:<digest>
```

## Helm charts

Charts are OCI artifacts. A chart pins every image it deploys by digest, and the digests come from the GoReleaser output through `ocictl helmctl`, so a chart installs exactly the images that were released with it.

```sh
# Example
helm pull oci://ghcr.io/truvity/charts/sluis --version <version>
```

## GitHub Releases

GitHub Releases is the immutable record. Each release holds the same files as the R2 version directory (tarballs, zips, checksums, signatures) and the GitHub build provenance attestations. If a file on R2 and a file on the release differ, trust the release.
