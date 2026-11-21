# 0013 — One repository, one version, one release train, and packaging follows the runtime

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

v1 ships from one repository on one version line, 1.x, with no major-version suffix on its Go module path. v1 ADR 0042 put every v1 product on one train, so that a change touching two of them is one pull request and one release.

v2 is a clean start on `main` ([0001](/decisions/0001-v1-and-v2-lines/)) and a different set of processes: actors, a node agent, a host service and one command-line tool ([0002](/decisions/0002-families-of-actors/)), some in Go and some in Rust ([0012](/decisions/0012-the-language-follows-the-executable/)). It runs in three runtimes ([0011](/decisions/0011-runtimes-and-installations/)), and each installs software differently: a package manager on a host, a function archive on serverless, an image on a cluster. The executable is what carries over between runtimes, not the package.

The drivers are not sluis's. They are [atoom's](https://github.com/truvity/atoom/blob/master/docs/decisions/0010-distribution-per-driver-and-lambda.md), and atoom distributes each driver in every shape it can run in: a library, a tarball, a `.deb` and an `.rpm` package, an OCI image and a Lambda extension zip. One binary is one driver of one port. sluis consumes those drivers and packages only its own actors, its command-line tool and its chart. The audit pieces are [audit's](https://github.com/truvity/audit/blob/master/docs/decisions/0016-distribution-and-the-lambda-shape.md) and have their own packages.

Two platform facts are hard constraints. The YubiKey driver talks to the smart-card service through cgo against pcsc-lite, so it cannot be cross-compiled on a plain build host; atoom builds it, and key@, which links `piv-go`, has the same constraint. Lambda allows five layers and 250 MB unzipped per function, and every process in a function shares its role, so the function is the credential scope.

## Decision

### One version

- **Every artifact of a release carries the same version** and ships together: packages, tarballs, Lambda zips, images, the chart and the console bundle. A component with no change still ships. This continues v1 ADR 0042 for v2.
- **v2 versions are v2.x.y,** and the main Go module path ends in `/v2` (`github.com/truvity/sluis/v2`). Any further Go module takes the same suffix, and all modules are tagged at one commit.
- **Before v2.0.0 every release is a pre-release, `v2.0.0-alpha.N`,** tagged on `main`, each a full release of every artifact. Nothing is stable before v2.0.0: no compatibility promise holds between two alphas, images and charts carry no `latest` or other floating tag, and the GitHub release is marked as a pre-release.
- **One build tool: GoReleaser v2, open-source edition,** for Go and Rust executables alike, with one SBOM generator and one build definition. The Rust executables are built with cargo-zigbuild, current crates and rustls, as static musl binaries. GoReleaser only builds; publishing is a second step with the credentials. The images are built from the binaries of the same run, and the chart is packaged by truvity/ocictl from the same output, with every image pinned by digest. There is no FIPS build in 2.0 ([0006](/decisions/0006-keys-and-sealing/)).
- **A release is built once as a candidate.** The tag run publishes the candidate's bytes and builds nothing.
- **The build of key@ that links `piv-go` is built in a native job per architecture,** inside a pinned build userland (Ubuntu 22.04, glibc 2.35, which is a build image and not a supported platform), and a test fails the job when an executable asks for a glibc symbol newer than 2.34, the glibc of the oldest supported system, so it runs on every supported glibc (REQ-DEPLOY-032). The output joins the rest of the release.
- **One command-line tool, `sluis`, with subcommands** for operators and users, such as `sluis config check`, `sluis doctor`, `sluis trust sync` and `sluis key request|install|status`. Ceremonies belong to [ritueel](https://ritueel.id). Actors, the node agent and the host service are separate executables, one per process, because each runs as its own user, unit, function or container.
- **`sluis config check` runs the store executable of the same release as a helper** ([0012](/decisions/0012-the-language-follows-the-executable/#per-actor-table)). The CLI looks for it in this order: its `--helper` flag, the `SLUIS_STORE_HELPER` variable, the install path `../libexec/sluis/actors/store` beside the CLI's own directory, and `store` in that directory. So a host with the CLI and store packages uses the package's store, and a tarball that holds both executables in one directory works as it is unpacked. The CLI refuses a helper whose version is not its own. The helper is a Linux executable, so `config check` runs on Linux only.
- **atoom is consumed by release.** sluis pins atoom's libraries and drivers to one atoom release at a time ([atoom 0015](https://github.com/truvity/atoom/blob/master/docs/decisions/0015-versioning-and-consumption.md)); a tool moves the pins together and refuses a release whose CI is not green.

### Packaging per runtime

There are no build profiles: the runtime decides the packaging, and every artifact is built for amd64 and arm64. `dev` is not a build; it is the local installation, which runs the published images ([0011](/decisions/0011-runtimes-and-installations/#the-local-installation)).

| Runtime | Packaging of sluis's actors | State |
|---|---|---|
| bare-metal | `.deb` and `.rpm` packages split by holder and module, in apt and dnf repositories, and a tarball | Packages and tarballs built; the repositories are planned |
| serverless | An app zip per executable and a config layer, behind the Lambda Web Adapter | Planned |
| kubernetes | One multi-arch OCI image per process, and one Helm chart with the images pinned by digest | Images built; chart planned |

- **The install directory is `/usr/libexec/sluis/{actors,node,vendor}/<short-name>`,** the same in a package, a tarball and an image. The adapters are actors. atoom's drivers install at `/usr/bin/atoom-<id>` ([atoom's packages](https://github.com/truvity/atoom/blob/master/docs/packages.md)).
- **Host packages follow the foundation and the modules:**
  - **`sluis-cli`**: the command-line tool at `/usr/bin/sluis`, with no dependency on systemd, so that a workstation without it installs the CLI alone;
  - **`sluis`**: the foundation's `policy` and `entry`, with both units of `entry`, the identity front and the audit front, so that a host that runs audit reaches `entry` without a work actor; the `sluis trust sync` timer and their units. It depends on `sluis-cli` and on systemd;
  - **the foundation:** `sluis-store`, `sluis-key` and `sluis-exporter`, each with its actor and its units;
  - **the identity module:** `sluis-identity`, the work actors and the first-party adapters, and `sluis-console`, the console's static bundle (planned);
  - **`sluis-node`** (planned) and **`sluis-host`** (planned).
- **No package carries a driver.** A driver is a package of atoom, and the unit of its holder starts it; only the holder can open its socket ([0003](/decisions/0003-units-transports-and-fusion/#units)). A package depends on no driver package it does not use, so a host that runs the KMS driver installs no card driver and no pcscd. The meta-package of an installation lists the atoom driver packages that installation runs.
- **A module package depends on the foundation packages and on no other module.** A host that runs the foundation with audit only installs no work actor of the identity module.
- **Meta-packages exist only for the bare-metal installations that install from packages:** one for the boards. Local runs images, so it has no meta-package.
- **Every distribution form declares its runtime dependencies,** and the [runtime dependency matrix](/distribution/dependencies/) is the one list of them: a package's `Depends`, `Recommends` and `Suggests`, what a container image installs, what a Lambda layer or extension bundles, and what the boards deploy requires of the board image. A dependency sits on the narrowest package that needs it. A form that is not built yet is marked so in the matrix (REQ-DEPLOY-038).
- **Every package declares the groups its units join,** identically: a `g <name> <gid>` line in its own `sysusers.d` file for every group of `SupplementaryGroups=`, with the number of the uid plan. A user is still made by one package alone. A unit therefore starts on a host without the packages that own those groups, and no `Depends` is derived from the groups. The callee owns its sockets.
- **`sluis-host` is a separate root host service, mainly for SSH.** It never listens on the network; it fetches the signed SSH principals bundle ([0023](/decisions/0023-certificate-authorities/)), verifies it, writes it atomically and keeps the last good one.
- **The trust bundle reaches hosts through the `sluis` package.** `sluis trust enroll --url … --root-pin <spki-hash>` turns on the sync timer; the sync checks the bundle ([0022](/decisions/0022-workload-identity-and-host-enrolment/)) and the root list ([0024](/decisions/0024-root-custody-and-the-signing-api/)), writes the anchors and runs the distribution's trust update; its unit may write nothing else. sluis generates no per-installation packages. On Kubernetes the bundle reaches pods through a trust-manager source or a ClusterTrustBundle.
- **Lambda: every function runs behind the Lambda Web Adapter,** vendored and pinned, and no function has a native handler. A function is an app zip plus a config layer, plus one merged drivers layer only where a driver's language differs from the function's ([0011](/decisions/0011-runtimes-and-installations/#the-lambda-shape)). The drivers layers are atoom's extension zips, merged into one layer per function. The app zip holds the package's binary, one zip per executable, named `sluis-<kind>-<name>_<version>_<arch>.zip`. Functions, schedules, roles and invoke grants are derived from the actor registry ([0002](/decisions/0002-families-of-actors/)). arm64 is the default. sluis publishes no public layer ARNs: an installation verifies the zips and publishes the layers into its own account. The build fails when a function needs more layers than Lambda allows.
- **Images: one image per process,** holding one executable at the package's path, byte for byte, with the source package in a label. Bases are distroless static, and distroless with glibc for cgo binaries, each pinned by digest. Names are flat, `ghcr.io/truvity/sluis/<kind>-<name>` (for example `ghcr.io/truvity/sluis/actor-entry`), and no image is named `sluis`, because that name belongs to v1's images. Tags are the release version, and the digest is the reference; each release lists every image digest in a `release-images.txt` file. **One image set serves the local installation and Kubernetes**, and a release test compares the images' binaries with the packages' files by hash. Driver images are atoom's.
- **Helm: one `sluis` chart with a piece selector and the presets `install` and `kubernetes-dev`, and no subcharts,** so that a holder and its callers always come from one release. The chart is core: one release carries the packages, the images, the chart and the Lambda zips at one version, and upgrade and rollback are tested on every shape ([#876](https://github.com/truvity/sluis/issues/876)). There is no quota package and no quota preset. A holder's pod runs the holder's image with atoom's driver images as native sidecar containers. Operators such as cert-manager are prerequisites the chart checks for. The cert-manager issuer has a chart of its own, with its CRDs and RBAC. The chart version is the release version, at `ghcr.io/truvity/sluis/charts/sluis`. The console runs as its own Deployment behind the same route as `entry`.
- **The console is a separate artifact,** a bundle of static files built once per release and served same-origin with the API: a directory from `sluis-console` on bare-metal, the bucket behind the API gateway on serverless, its own Deployment on Kubernetes. The audit page that the console embeds is audit's bundle ([0027](/decisions/0027-the-console/)).

### Supported platforms

- **Operating systems:** Ubuntu 26.04 LTS, Rocky Linux (the latest stable, 10.x) and Amazon Linux 2023, on amd64 and arm64. The boards are aarch64 and run Ubuntu 26.04 and Rocky Linux 10 with SELinux enforcing, on current mainline-based kernels. The facts per distribution are on [Bare-metal](/runtimes/bare-metal/).
- **The systemd floor is 252,** the version in Amazon Linux 2023. Generated units may rely on what 252 provides, such as `LoadCredentialEncrypted=` and `ExecPaths=`, so unit credentials are encrypted at rest under the host key on every supported platform. The boards deploy refuses to write a credential on an older system, and `sluis doctor` warns where the kernel fails the randomness checklist. Key material is never affected: it stays in the card.
- **Serverless and Kubernetes have no host distribution.**

### Distribution

- **GitHub Releases is the immutable record,** and OCI images and charts are on the GitHub container registry. A release holds archives, packages, `SHA256SUMS`, SBOMs and the image list. Built.
- **An R2 bucket serves the same files without redirects:** `apt/` and `dnf/` repositories, `bin/<version>/` tarballs, `lambda/<version>/` functions and layers, `checksums/<version>/` and `keys/`, and a `latest` pointer. apt and dnf handle redirects unreliably, and a redirect is one more place to point a repository elsewhere. When a file on R2 and on the release differ, the release is right. Planned; see [Packages](/distribution/packages/) and the [roadmap](/roadmap/).
- **The website is a separate thing** ([0014](/decisions/0014-the-site-is-static-on-cloudflare-workers/)).

### Compatibility between releases

- **The rules of this section hold from v2.0.0.** Between two alphas anything may change, and the CHANGELOG says what did.
- **Compatibility follows semantic versioning.** A breaking change ships in a major release, or in a minor release that says so in the CHANGELOG, announces it and carries an upgrade note; stored data stays readable by the next release; protocol contracts are checked for breaking changes ([GOVERNANCE.md](https://github.com/truvity/sluis/blob/main/GOVERNANCE.md)).
- **The configuration schema and its identifiers are public contract.** A published `$id` never changes; an incompatible schema is a new version under a new path, beside the old one ([0004](/decisions/0004-the-data-model/)).
- **Removed configuration is refused, never silently ignored.** A key that no longer means anything fails validation with a message naming its replacement, as in v1 ADR 0007.
- **Artifacts of one release are built, tested and shipped together, and each also works with those of the previous release of the same major.** A chart and its images, a package and the atoom drivers it starts come from one tested set, and a rolling deployment runs two neighbouring releases at once ([0001](/decisions/0001-v1-and-v2-lines/)).

### Signing and checksums

- **A release carries checksums and SBOMs, and no signed artifact.** It has a `SHA256SUMS` list and a `.sha256` per file, and an SPDX JSON SBOM per archive and per image, made by syft, with the Rust crates recorded by `cargo auditable`. The maintainer signs each tag by hand.
- **Signing of release artifacts is planned and not scheduled:** GitHub build-provenance attestations and keyless Sigstore signatures made by the CI build with its GitHub OIDC token and no stored secret, and OpenPGP signatures after that. Sigstore says how a file was built, not who vouches for it, so it does not replace an OpenPGP release key, and `sluis-node` does not trust it alone.
- **Nothing is signed with OpenPGP before the roots exist.** The OpenPGP signatures wait until ritueel has held its first ceremony and its drill. From then on, releases are signed through sluis's own signing API with a release subkey held in a cloud key service ([0024](/decisions/0024-root-custody-and-the-signing-api/)): detached signatures first, then signed apt and dnf repository metadata.

## Consequences

- `sluis config check` needs two executables of one release, the CLI and store. A pipeline that checks config before it publishes installs both, on Linux.
- One tag produces everything, so no one needs a compatibility table between sluis's components. A small fix releases every artifact, including unchanged ones. The atoom drivers carry atoom's version, and the pin says which one a release was tested with.
- Every consumer of a v1 Go module changes its import path once, when it moves to v2; both lines can sit in one build graph during the migration.
- Two toolchains build one train. The Cargo workspace builds under GoReleaser with one build entry per binary, as the build spike showed.
- A native job per architecture builds the card-linked build of key@, and a failure in it blocks the release.
- A few dozen images per architecture are built and published; each digest names one thing, and a scanner reports on the binary a container actually runs.
- The Lambda limits are a build-time budget. Per-actor functions chain cold starts on a first request, and every data access of a work function is an invoke of store@'s function; provisioned concurrency on `entry`, `signer` and the holders is the lever if that is too slow.
- The vendored Web Adapter is part of the supply chain, pinned to a version and rebuilt into the release.
- Without signatures, nothing ties a release to the build that made it except the checksums and the signed tag. An apt or dnf repository cannot be verified by its signature until signing returns.
- GHCR has no OCI referrers API (measured 2026-11-01), so attestations and signatures, when they exist, are found through the fallback tag `sha256-<digest>`; a registry mirror must copy those tags too.
- R2 is a single provider for downloads; GitHub Releases is the second copy and wins a disagreement.
- Until the switch to atoom's per-driver packages, the tree's driver host executables are still built and packaged by sluis's release tooling. After the switch, sluis's package list has no driver in it.
- The dependencies of every form are in one matrix that package metadata, images and layers are checked against; until the repositories, chart and Lambda artifacts are built, their columns are a specification, and the matrix lists the gaps.
- The pages [Packages](/distribution/packages/) and the supported-platforms table on it describe this record; when they disagree, the page is fixed.

## Roads not taken

- **Keep the build profiles.** A profile mixed the runtime with the provider and the use, and a build that is not the one tested is the one that runs.
- **Independent versions per component or module.** A user would need a compatibility table between components released from one commit; v1 ADR 0042 rejected it for the same reason.
- **store@'s validation linked into the `sluis` binary.** The CLI is Go and store@ is Rust; linking Rust into the Go binary through cgo would give up its static cross-compiled build for one command.
- **One binary for everything, selected by subcommand.** An actor's unit could then execute every other actor's code; only the CLI is one binary.
- **ko for the images.** It builds Go only and puts the binary under its own path, so the Rust executables and the rule that an image holds the package's file byte for byte would not hold.
- **The four-segment image names `ghcr.io/truvity/sluis/<kind>/<name>`.** Not shown to work on GHCR, where the flat three-segment form is.
- **A separate Cargo release tool for the Rust executables.** It would be a second train with its own SBOM and signing to keep in step.
- **Driver packages built by sluis, or a driver private to one owner.** Two products would build the same driver twice and no kit would test either; a KMS-only host would install pcscd and the card driver. One binary per driver, built and tested in atoom, serves every consumer.
- **A driver package an operator starts as a service.** Only the holder may open a driver's socket, so a service of its own would be a second way to start the driver.
- **A native Lambda handler, or a container image for Lambda.** A native handler is a second entry point to keep in step, and layers do not apply to images, so the adapter and extensions would be baked into every image.
- **An image per package, or a distribution base image with the packages installed.** Containers would carry other actors' binaries, a shell and a package manager.
- **A chart per piece, or an umbrella chart with subcharts.** A holder and its callers could come from different releases, and subcharts cannot see the parent's generated schema.
- **Downloads through redirects, or from GitHub Releases alone.** apt and dnf handle redirects unreliably, and GitHub Releases is not an apt or dnf repository.
- **Lower the systemd floor to an older board image.** Every platform would give up encrypted credentials for one vendor image.
- **Dependencies written separately in each form's metadata.** The package, the image and the layer would drift apart, as the card path's packages already differ by family; one matrix is the list they are checked against.
- **Signing the first alphas with keyless Sigstore.** It would tie a release to its build, but the maintainer put signing of artifacts in the backlog with the staff-key work.

## Follow-ups

- The build system with one version, GoReleaser v2 and the per-architecture jobs: [#860](https://github.com/truvity/sluis/issues/860).
- The Helm chart, packaged by ocictl with digest-pinned images: [#876](https://github.com/truvity/sluis/issues/876).
- Board OS images for Ubuntu 26.04 and Rocky Linux 10.2 as an artifact of their own: [#881](https://github.com/truvity/sluis/issues/881).
- The distribution site with the apt and dnf repositories: [#873](https://github.com/truvity/sluis/issues/873).
- OpenPGP release signing and CI upload credentials: [#874](https://github.com/truvity/sluis/issues/874); the keyring package users verify with: [#1013](https://github.com/truvity/sluis/issues/1013); signed apt and rpm repository metadata: [#1074](https://github.com/truvity/sluis/issues/1074).
- The detail per artifact, what a patch, a minor and a major release may change in the schema, the APIs and the packages, written before v2.0.0: [#1715](https://github.com/truvity/sluis/issues/1715).
- Open: the Lambda zips, the vendored Web Adapter and the layer merge are not built; atoom's extension zips are step B9 of its plan.
