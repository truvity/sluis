---
title: Packages
description: The sluis release artifacts and where they live. The R2 layout, apt and dnf setup for amd64 and arm64, binaries, Lambda zips, OCI images and Helm charts.
status: target
sidebar:
  order: 1
---

A sluis release publishes every artifact from `https://distribution.sluis.id/`, a Cloudflare R2 bucket. GitHub Releases holds the same files as the immutable record. For how to check what you downloaded, see [Release signing](/distribution/release-signing/).

## Packaging per runtime

What you download depends on the [runtime](/runtimes/) you install on. There are no build profiles: the packaging is the build, and `dev` is not a build.

| Runtime | Packaging |
|---|---|
| [bare-metal](/runtimes/bare-metal/) | `.deb` and `.rpm` packages split by function, a package per driver executable, meta-packages per installation, and a tarball |
| [serverless](/runtimes/serverless/) | One function per actor and one zip per executable, with at most three layers of sluis's own |
| [kubernetes](/runtimes/kubernetes/) | One OCI image per process and one Helm chart with presets |

The local installation runs the same OCI images as Kubernetes, under Docker Compose. The images carry the packages' binaries, byte for byte, and a hash test checks it ([ADR 0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)). The packaging is agreed ([ADR 0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

## Packages by function

The `.deb` and `.rpm` packages are split by function, and every driver executable is a package of its own, so that a host installs only what it runs (decided 2026-10-30):

| Package | Holds |
|---|---|
| `sluis` | The CLI, `sluis agent`, `sluis trust sync` with its timer ([Machine identity](/concepts/machine-identity/#trust-sync)), and the `sluis ceremony` subcommands with their OpenPGP-card helper |
| `sluis-node` | The node agent |
| `sluis-server` | The core actors and the first-party integrations |
| `sluis-driver-<name>` | One driver executable, with the system packages it needs |
| `sluis-audit-writer` | The audit writer and the witness |
| `sluis-audit-search` | The indexer and the reader |
| `sluis-quota` | The quota engine |
| `sluis-host` | The SSH host service ([SSH](/integrations/ssh/)) |
| `sluis-console` | The console's single-page application, as a static bundle |
| `sluis-install-<installation>` | A meta-package per bare-metal installation, such as `sluis-install-boards`, which pulls in what that installation runs |

Examples of driver packages and what they bring:

| Package | Dependencies and contents |
|---|---|
| `sluis-driver-yubikey-piv` | Depends on pcscd and pcsc-lite |
| `sluis-driver-pkcs11` | Depends on p11-kit |
| `sluis-driver-rqlite` | Ships `rqlited` under `vendor/` and the `sluis-rqlite@.service` template |
| `sluis-driver-aws` | The one Rust `aws` driver executable |

No package depends on a driver package except the meta-packages. The lambda and kubernetes installations have no `.deb` or `.rpm`, so they have no meta-package, and the local installation runs the images under Docker, so there is no `sluis-install-local` until a native local mode exists (decided 2026-10-31, [ADR 0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

The ceremonies need no package of their own. `sluis ceremony …` runs a Rust helper that drives the OpenPGP cards, shipped in the `sluis` package under `/usr/libexec/sluis/helpers/`. On the offline live USB system the same `sluis` runs with the same configuration: a ceremony is prepared online, run offline, and completed and published online ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)).

The packages ship systemd **template units** only, `sluis-<actor>@.service` and `sluis-<actor>@.socket`, and a systemd generator that reads `/etc/sluis/install.yaml` and creates the instances ([Bare-metal](/runtimes/bare-metal/#what-runs-where)). sluis does not generate packages per installation.

The **console's single-page application** is a separate artifact: a bundle of static files with the release's version, deployable on its own. Each runtime's static host serves it on the same origin as `entry`'s API: a directory that `entry` serves on bare-metal, the bucket behind the API gateway on serverless, a static container behind the ingress on Kubernetes ([ADR 0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

The **audit console** is a bundle of its own, built from the same source tree on the same release train, and served by the audit installation's static host on the audit front's origin ([ADR 0062](/decisions/0062-audit-front-is-a-slim-entry-the-reader-as-its-api-and-a-console-of-its-own/)). Its package and image are named with the build ([#985](https://github.com/truvity/sluis/issues/985)).

## The R2 layout

```text
apt/       dists/stable/{InRelease,Release,Release.gpg}
           dists/stable/main/binary-{amd64,arm64}/
           pool/
dnf/       {x86_64,aarch64}/{repodata/, packages}
           sluis.repo
bin/       <version>/<component>_linux_{amd64,arm64}.tar.gz
lambda/    <version>/{functions,layers,extensions}/sluis-<kind>-<name>_<version>_{arm64,x86_64}.zip
checksums/ <version>/SHA256SUMS, .sig (cosign), .asc (OpenPGP)
keys/      the release OpenPGP public key, the cosign public key
oidc/      nodes/<node>/.well-known/openid-configuration, jwks.json
latest     pointer to the newest version
```

- Everything is served directly from R2. There are no redirects.
- `oidc/` is not a release artifact. It holds workload-identity discovery documents and JWKS, public keys only. By default an installation's workload tokens come from its boundary's issuer, so `oidc/nodes/<node>/` is used only by a fleet that chooses per-node issuers. See [Identity](/concepts/identity/).
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

Each component is a tarball for each architecture at `bin/<version>/<component>_linux_{amd64,arm64}.tar.gz`, with its `.sha256` beside it. Use a tarball when you manage the units yourself. The layout inside a package or tarball is the same, and the directory that held the actors was renamed from `roles` to `actors`:

```text
/usr/libexec/sluis/{actors,drivers,integrations,node,helpers,vendor}/<short-name>
```

## Lambda zips

Lambda is zip only, so layers and extensions combine. The zips are published under `lambda/<version>/` for `arm64`, the default, and `x86_64`. Each file is named `sluis-<kind>-<name>_<version>_<arch>.zip`, the same on R2 and in the GitHub release, so no two files collide:

- `functions/`: **one zip per executable**, and one function per actor. The function's handler is the `.deb`'s binary, renamed `bootstrap`;
- `layers/`: at most three layers of sluis's own: the edge layer, with the vendored Lambda Web Adapter (pinned v1.1.0) that only `entry` uses; the drivers layer; and the configuration layer. There is no OpenTelemetry layer, and AppConfig is read through its API;
- `extensions/`: the drivers, as **one Rust `aws` executable** that implements every AWS port for its function.

A function has at most five layers and 250 MB unzipped, including layers and extensions, so two layers are left to the operator. The zips are verified and published into the installation's own account by its infrastructure code; sluis publishes no public layer ARNs. See [Serverless](/runtimes/serverless/).

## OCI images

There is **one image per process**: each actor, driver and integration, and the edge relay and the console, is an image of its own, so a container can run its one executable only. Multi-arch images (amd64 and arm64) are published to GHCR as `ghcr.io/truvity/sluis/<kind>/<name>`. Pull by digest.

- The base is distroless `static`, or distroless with glibc and a pinned `pcsc-lite` for an executable that needs cgo, built against the oldest supported glibc.
- The executable sits at the same path as in the `.deb` (for example `/usr/libexec/sluis/actors/entry`), byte for byte, and an OCI label names the package it comes from.
- The same images run the local installation and Kubernetes.

```sh
# Example
docker pull ghcr.io/truvity/sluis/actors/entry@sha256:<digest>
```

## Helm charts

Charts are OCI artifacts. A chart pins every image it deploys by digest, and the digests come from the GoReleaser output through `ocictl helmctl`, so a chart installs exactly the images that were released with it. A chart's version is the release version.

- **One `sluis` chart** with a piece selector and presets, `install`, `audit-install`, `quota-install` and `kubernetes-dev`, and no subcharts ([OD-30](/open-decisions/#od-30), decided). The console runs as its own Deployment.
- **The cert-manager issuer** has a chart of its own, with its CRDs and RBAC. The certificate sidecar is an image plus a snippet.

```sh
# Example
helm pull oci://ghcr.io/truvity/sluis/charts/sluis --version <version>
```

## GitHub Releases

GitHub Releases is the immutable record. Each release holds the same files as the R2 version directory (tarballs, zips, checksums, signatures) and the GitHub build provenance attestations. If a file on R2 and a file on the release differ, trust the release.
