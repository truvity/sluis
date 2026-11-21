---
title: Runtime dependencies
description: "The one matrix of what sluis needs outside its executables, in every form: packages, the boards deploy, container images, Kubernetes and Lambda."
status: partial
sidebar:
  order: 2
---

A sluis executable is static or nearly so, but the host or the image around it is not: the card driver needs a card service, a deploy needs `tar` on the board, a package needs a systemd that knows encrypted credentials. This page is the **one list** of those dependencies and of how each distribution form satisfies each of them. Package metadata, image contents and layer contents are checked against it ([0013](/decisions/0013-release-train-packaging-and-distribution/#packaging-per-runtime), `REQ-DEPLOY-038`); a change that adds a dependency to the code or the deploy adds its row here in the same pull request.

## What is built today

Read on 2026-11-18, against `main` and the release `v2.0.0-alpha.1` of 2026-11-08. The package columns of the matrix are the specification for packages that are built but not published yet. They are not an inventory.

| Form | Built today? | Where it is defined |
|---|---|---|
| Packages (`.deb`, `.rpm`) | Partly. `tools/release/packages` builds and checks them for amd64 and arm64: the executables, the exact dependencies between the packages and the system dependencies of `sluis-cli` (`ca-certificates`, `xdg-utils`) and `sluis` (`systemd`). The units, users and directories are in them, and an install tier proves them on Ubuntu 26.04 and Rocky Linux 10 (the CI job `install`); the meta-package is not in them yet, and the tree still packages its driver host executables until the switch to atoom's packages ([0013](/decisions/0013-release-train-packaging-and-distribution/#consequences)). `v2.0.0-alpha.1` predates the packages, so no release has published them: the release job does, from the bytes of a release candidate | [Packages](/distribution/packages/#how-the-packages-are-built) |
| Boards deploy (executables, generated units and a stage over ssh) | Yes | `deploy/boards/`, [Bare-metal](/runtimes/bare-metal/#deploy-and-prove-on-three-boards) |
| Tarballs, checksums, SBOMs | `.goreleaser.yaml` builds every executable but the card-linked ones for amd64 and arm64, with an SPDX SBOM for each archive, and CI runs it when a change can affect the release, and on every push to main. `v2.0.0-alpha.1` published the archives, `SHA256SUMS`, the SBOMs and the audit catalogue on the GitHub release | [Binaries](/distribution/packages/#binaries) |
| Container images | The release images are built by the dry run of every pull request, one per actor and, until the switch to atoom's images, per driver host executable but the card driver, and the engine ([OCI images](/distribution/packages/#oci-images)); `deploy/local/Dockerfile` and `deploy/local/Dockerfile.rqlite` still build the same for a local installation from source. `v2.0.0-alpha.1` published them to GHCR, by digest, listed in its `release-images.txt` | [Local](/runtimes/local/), [OCI images](/distribution/packages/#oci-images) |
| Kubernetes chart | No ([#876](https://github.com/truvity/sluis/issues/876)) | [Kubernetes](/runtimes/kubernetes/#packaging-and-the-baseline) |
| Lambda zips, layers, extensions | No | [Lambda zips](/distribution/packages/#lambda-zips), [Serverless](/runtimes/serverless/) |

## How to read the matrix

- **Depends** is a package that does not work without the dependency, so the package manager installs it. **Recommends** is installed by default and may be left out. **Suggests** is never installed on its own. In an rpm these are `Requires`, `Recommends` and `Suggests`.
- **A dependency sits on the narrowest package that needs it.** The card service is a dependency of atoom's card driver package, not of `sluis`, so a KMS-only host installs no pcscd ([0013](/decisions/0013-release-train-packaging-and-distribution/#packaging-per-runtime)).
- **Board image** means the operating system image of the board must carry it. The boards deploy installs no package of the card path: it checks, and refuses naming the packages of the board's family before it writes a file. The one package it installs is rqlite.
- **Image** means the container image carries it. An image holds one executable at the package's path; a library goes into the base layer, and a daemon runs outside the container, on the host.
- **n/a** means the form never needs it.

## The matrix

The package cell reads `deb / rpm`. The rpm column covers Rocky Linux 10 and Amazon Linux 2023; a card is not used on Amazon Linux 2023. A driver's own dependencies are declared by its atoom package, not by a sluis package; the matrix keeps one row for the card driver because the boards deploy checks them.

| Dependency | Needed by | Packages (deb / rpm) | Boards deploy | Container image | Kubernetes | Lambda |
|---|---|---|---|---|---|---|
| **The card driver's system dependencies**: the smart-card service `pcscd` / `pcsc-lite`, the client library `libpcsclite1` / `pcsc-lite-libs`, a CCID driver `libccid` / `pcsc-lite-ccid`, `polkitd` / `polkit` (pcscd 2.x on Ubuntu 26.04 and Rocky 10 asks it about every client), and glibc `libc6 (>= 2.34)` / `glibc >= 2.34` (the card driver is the one cgo executable, every other one is static) | the `yubikey-piv` card driver, which is atoom's | Declared by atoom's `atoom-driver-key-yubikeypiv`: Depends on pcscd, libpcsclite and glibc, Recommends the CCID driver ([atoom packages](https://github.com/truvity/atoom/blob/master/docs/packages.md#what-runs-where)). No sluis package declares them, so a KMS-only host installs no pcscd. Open: atoom's package does not name polkit | Board image; the deploy checks the card packages of the board's family and refuses, naming them, before it writes a file. The native build and a test hold the executable to the symbols of glibc 2.34 | atoom's card image: a glibc base with the one library; the card service runs on the host and its socket is mounted | n/a: no card on Kubernetes | n/a: no card on Lambda |
| **systemd 252 or later**, with `systemd-creds`, `systemd-sysusers` and `systemd-tmpfiles` | every unit: `LoadCredentialEncrypted=`, the generator, users and directories | Depends `systemd (>= 252)` / `systemd >= 252` on `sluis` (the CLI package `sluis-cli` has none) | Board image; the deploy refuses a board below 252 or without `systemd-creds` | n/a: no systemd | n/a | n/a |
| **tar** | the boards deploy: the stage reaches the board as a tar stream | n/a: the package manager unpacks its own packages | Board image; `boards-deploy` refuses a board without it, before it sends anything. Ubuntu 26.04 has it; the Rocky 10 image of the boards lacks it | n/a | n/a | n/a |
| **audit** (`ausearch`) | the denial check of an SELinux board (`boards-avc`, run at the end of a deploy and a smoke) | deb: n/a (AppArmor). rpm: Recommends `audit` on `sluis-install-boards` | Board image on an SELinux board; the check fails when SELinux is enforcing and `ausearch` is missing. The Rocky 10 image lacks it | n/a | n/a | n/a |
| **policycoreutils-python-utils** (`semanage`, `audit2why`) | work on the SELinux policy only; the deploy does not call them | rpm: Suggests `policycoreutils-python-utils`. deb: n/a | Operator's choice. The Rocky 10 image lacks it | n/a | n/a | n/a |
| **policycoreutils** (`restorecon`) and `selinuxenabled` | the labels after a deploy | Base system on Rocky 10; no dependency declared | Base system; the deploy warns when `restorecon` is missing | n/a | n/a | n/a |
| **rqlite 10.6.6** (`rqlited`) | the `rqlite` store driver, one node per board | atoom's `atoom-driver-store-rqlite` holds the executable only. Open: whether a sluis package carries `rqlited` under `vendor/` | `boards-rqlite` installs the upstream `.deb` or `.rpm`, pinned by sha256 in `deploy/boards/rqlite.pins`, and refuses a package whose digest differs | The pinned release archive (version and sha256 in the `rqlite-fetch` recipe of the Justfile) on a distroless glibc base, in `Dockerfile.rqlite`; the release builds the same as the image `vendor-rqlite` | n/a: DynamoDB is the engine | n/a: DynamoDB is the engine |
| **curl** | the boards script `boards.sh`, which asks the board's own rqlite node how many data-key wraps it holds (the guard of a key switch) | n/a | Board image. Ubuntu 26.04 has no curl by default, and nothing declares or checks it: **a gap** (see below) | n/a | n/a | n/a |
| **ca-certificates** and the trust update | `sluis trust sync` writes the anchors and runs `update-ca-certificates` or `update-ca-trust`; `adapter-upstream-oidc` and `exchange` make HTTPS calls with the system roots | Depends `ca-certificates` / `ca-certificates` on `sluis-cli` and on the package that holds an actor with HTTPS calls (`sluis-identity`: `adapter-upstream-oidc`, `exchange`), each directly, so the chain does not rest on a transitive dependency | Base system | The base carries the system roots; the bundle reaches pods and containers by a mount | The bundle comes from a trust-manager source or a ClusterTrustBundle | n/a |
| **OpenSSH server** (`sshd`) | `sluis-host` writes the principals bundle that `sshd` reads; `sshd` stays stock | Recommends `openssh-server` / `openssh-server` on `sluis-host` | Board image; key-only ssh is already what the deploy uses | n/a | n/a | n/a |
| **xdg-open** | the `sluis` CLI opens the browser for a sign-in | Suggests `xdg-utils` / `xdg-utils` on `sluis-cli` | n/a: workstation | n/a | n/a | n/a |
| **Edge connector** | `entry`, the one process with a public listener | Not a dependency: the operator installs `cloudflared` from its vendor | The operator installs `cloudflared` | The local installation's forwarder is Caddy, pinned by digest in the Compose file; it is a test service, not a sluis component | The edge relay image of the chart, or a `cloudflared` sidecar | The pinned, vendored Lambda Web Adapter, which runs in front of every function |
| **nftables** (`nft`) | the per-uid egress rules generated from the actor registry (not built), and the rqlite firewall of the boards (`boards-firewall`), which loads one table with `nft -f` | Depends `nftables` / `nftables` on the package that ships the rules, once they ship (not built) | `boards-deploy` does not touch the firewall, but it refuses a board without `/usr/sbin/nft` before it sends anything (as for `tar`), naming `apt-get install nftables` or `dnf install nftables`; `boards-firewall` checks it again before it changes anything. Neither image of the boards has the package: it is installed by hand (MEASURED 2026-11-07: nftables 1.1.6 on Ubuntu 26.04.1, 1.1.5 on Rocky 10.2), and it leaves `nftables.service` disabled and inactive, which the firewall does not need. It also needs `systemd-run` for the dead-man timer (part of systemd) | n/a | n/a: a NetworkPolicy per callee, which needs a CNI that enforces it | n/a |
| **Cluster prerequisites** | the chart: Gateway or Ingress, cert-manager, a CNI that enforces NetworkPolicy, native sidecars, the `restricted` Pod Security level | n/a | n/a | n/a | Checked by the chart before it installs ([Kubernetes](/runtimes/kubernetes/#packaging-and-the-baseline)) | n/a |
| **Drivers layer, configuration layer** | a holder's drivers on AWS (AppConfig, SSM, DynamoDB, KMS), where a driver's language differs from the function's | n/a | n/a | n/a | n/a | The drivers layer is atoom's extension zips merged into one layer per function ([atoom 0010](https://github.com/truvity/atoom/blob/master/docs/decisions/0010-distribution-per-driver-and-lambda.md)); the configuration layer is sluis's and carries the signed config. Not built |

The `audit` and `policycoreutils` rows serve the default mandatory access control of each family. [REQ-DEPLOY-033](/requirements/deploy/#REQ-DEPLOY-033) wants sluis to run with no SELinux or AppArmor denial under the distribution's own policy (SELinux enforcing on Rocky, AppArmor on Ubuntu); it is about compatibility, because the default policy does not confine sluis. [REQ-DEPLOY-042](/requirements/deploy/#REQ-DEPLOY-042) is the later step: the .rpm would ship an SELinux module and the .deb an AppArmor profile.

Three things have no row because nothing on a host satisfies them: the services a driver talks to (an AWS account, DynamoDB, NATS JetStream, PostgreSQL, a Cloudflare account) are the installation's dependencies ([Runtimes](/runtimes/#installations-nodes-and-dependencies)), the libraries a Rust or Go executable links in (SQLite is bundled, TLS is rustls) are in its file, and the shell tools the boards scripts run on a board (`bash`, `timeout`, `getent`, `sudo`) are part of every base image of a supported system.

## Floors and pins

| Dependency | Value | Where it is enforced |
|---|---|---|
| systemd | 252 (Amazon Linux 2023) | `credential_floor` in `deploy/boards/remote-lib.sh`; [Supported platforms](/distribution/packages/#supported-platforms) |
| glibc, for the card driver | 2.34 (Amazon Linux 2023). The build userland is Ubuntu 22.04 (glibc 2.35) and is not a supported platform | `TestNativeExecutablesStayWithinTheGlibcFloor`, `REQ-DEPLOY-032` |
| rqlite | v10.6.6, pinned by sha256 per asset | the `rqlite-fetch` recipe of the Justfile for the local installation; `deploy/boards/rqlite.pins` for the boards |

A change to a floor or a pin changes this page and the code in one pull request.

## Gaps

These are known and not fixed here. Each is a line to close, not a decision.

| Gap | Effect | How it closes |
|---|---|---|
| **No chart and no Lambda artifact exists, and the packages declare only the dependencies of `sluis`**, so no dependency of the Kubernetes and Lambda columns is declared in any file but the images' Dockerfiles | Those columns are a specification. A dependency cannot be missing from a file that does not exist | The build system ([#860](https://github.com/truvity/sluis/issues/860)) writes the definitions and layers from this page; `REQ-DEPLOY-038` stays partial until they exist |
| **`curl` on the board** is used by `boards.sh` (the data-key count of a key switch) and is neither declared nor checked. Ubuntu 26.04 has no curl by default | On such a board the guard cannot read the count and refuses the switch (it fails closed), with an error about the command rather than the missing package | Read the count with the bash-only request that `boards-rqlite` already uses for rqlite's HTTP API, so the board needs no curl; until then install `curl` on the board |
| **The card driver has no container image** | The local installation's opt-in development card cannot run in a container yet | atoom builds the card driver's image ([atoom packages](https://github.com/truvity/atoom/blob/master/docs/packages.md#the-images)); pcscd, the CCID driver and polkit stay on the host |
| **The deploy does not check `libpcsclite1` / `pcsc-lite-libs`** on its own | None in practice: pcscd depends on it on both families | Add it to the check if a board image ever ships pcscd without it |
| **rqlite has one source today**: the upstream package that `boards-rqlite` installs. Atoom's driver package holds no engine | None yet | Open: decide whether a sluis package carries `rqlited` under `vendor/`; the deploy then drops `boards-rqlite` for it |

## Where the metadata lives

The package dependencies are declared in `tools/release/packages`: what the plan writes (`Externals` in `tools/release/pkgs/shared.go`) and the list a package may depend on (`allowedExternal` in `tools/release/pkgs/check.go`) are separate on purpose, and a test holds the systemd floor to the boards deploy's. Only `sluis-cli` (`ca-certificates`, Suggests `xdg-utils`), `sluis` (`systemd >= 252`) and `sluis-identity` (`ca-certificates`) have system dependencies so far; the card driver's are atoom's package's. The other metadata is read from the code and checked against this page:

- `check_card_prerequisites` in `deploy/boards/remote-lib.sh` names the card packages per family: `pcscd`, `libccid` and `polkitd` on Debian and Ubuntu, `pcsc-lite`, `pcsc-lite-ccid` and `polkit` on RHEL and Rocky.
- `credential_floor` in the same file is the systemd floor.
- `deploy/release/Dockerfile.*` (generated, one per image) and `deploy/local/Dockerfile` and `deploy/local/Dockerfile.rqlite` are the images; their bases are pinned by digest, and a test holds the local Dockerfiles to the same pins as the release ones.
- `deploy/boards/Dockerfile.native` and `native-build.sh` are the build userland of the card-linked builds, not a form that sluis ships.

`tools/gen/units` holds a test (`REQ-DEPLOY-038`) that fails when the card packages, the systemd floor, the glibc floor or the rqlite version in those files differ from this page.

Facts on this page were read from the repository and from the pages it links on 2026-11-06. The tar, audit and policycoreutils-python-utils facts of the Rocky 10 image were measured on 2026-11-06 (Rocky 10.2); the absence of curl on Ubuntu 26.04 is UNVERIFIED here.
