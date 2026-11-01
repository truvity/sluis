# 0019 — One repository, one version, one release train, and packaging follows the runtime

**Status:** Accepted
**Date:** 2026-11-01

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

v1 ships from one repository on one version line, 1.x, with no major-version suffix on its Go module path. v1 ADR 0042 put every v1 product on one train, so that a change touching two of them is one pull request and one release. The early v2 design added build profiles (`aws`, `selfhosted`, `kubernetes`, `dev`) that chose which parts went into a build.

v2 is a clean start on `main` ([0001](/decisions/0001-v1-and-v2-lines/)) and a different set of processes: actors, drivers, a node agent, a host service and one command-line tool ([0003](/decisions/0003-core-actors-with-one-responsibility-each/)), some in Go and some in Rust ([0014](/decisions/0014-the-language-follows-the-executable/)). It runs in three runtimes ([0018](/decisions/0018-runtimes-installations-and-delivery-order/#runtime-kinds)), and each installs software differently: a package manager on a host, a function archive on serverless, an image on a cluster. The executable is what carries over between runtimes, not the package.

Two platform facts are hard constraints. The YubiKey driver talks to the smart-card service through cgo against pcsc-lite, so it cannot be cross-compiled on a plain build host. Lambda allows five layers and 250 MB unzipped per function, and every process in a function shares its role, so the function is the credential scope; for sluis the layer count is the binding limit.

## Decision

### One version

- **Every artifact of a release carries the same version** and ships together: packages, tarballs, Lambda zips, images, charts and the console bundle. A component with no change still ships. This continues v1 ADR 0042 for v2.
- **v2 versions are v2.x.y,** and the main Go module path ends in `/v2` (`github.com/truvity/sluis/v2`). Any further Go module takes the same suffix, and all modules are tagged at one commit.
- **One build tool: GoReleaser v2, open-source edition,** for Go and Rust executables alike, with one SBOM and one build definition. The Rust executables are built with cargo-zigbuild, current crates and rustls. There is no FIPS build in 2.0 ([0012](/decisions/0012-cryptography-and-authority-keys/#algorithms)).
- **The YubiKey driver is built in a native job per architecture,** inside the userland of the oldest supported distribution, so it runs on every supported glibc. The jobs' outputs join the rest of the release.
- **One command-line tool, `sluis`, with subcommands** for operators and users, such as `sluis config check`, `sluis doctor`, `sluis trust sync` and `sluis key request|install|status`. Root ceremonies belong to [ritueel](https://ritueel.id). Actors, drivers, the node agent and the host service are separate executables, one per process, because each runs as its own user, unit, function or container.

### Packaging per runtime

There are no build profiles: the runtime decides the packaging, and every artifact is built for amd64 and arm64. `dev` is not a build; it is the local installation, which runs the published images ([0018](/decisions/0018-runtimes-installations-and-delivery-order/#the-local-installation)).

| Runtime | Packaging |
|---|---|
| bare-metal | `.deb` and `.rpm` packages split by function, in apt and dnf repositories, and a tarball |
| serverless | A zip per executable, a function per actor instance, at most three sluis layers per function |
| kubernetes | One multi-arch OCI image per process, and one Helm chart with the images pinned by digest |

- **The install directory is `/usr/libexec/sluis/{actors,drivers,integrations,node,vendor}/<short-name>`,** the same in a package, a tarball and an image.
- **Host packages:** `sluis` (the CLI, `sluis agent`, the `sluis trust sync` timer), `sluis-node`, `sluis-server` (the core actors and first-party integrations), `sluis-driver-<name>` (one per driver executable, with its system dependencies, for example `sluis-driver-yubikey-piv` on pcscd and `sluis-driver-rqlite` shipping `rqlited` under `vendor/`), `sluis-audit-writer`, `sluis-audit-search`, `sluis-quota`, `sluis-host` and `sluis-console`. `sluis-server`, the audit packages and `sluis-quota` depend on no driver package, so a host that runs only the audit log or only Quota installs neither the core actors nor an unused driver.
- **Meta-packages exist only for the bare-metal installations that install from packages:** `sluis-install-boards`. Local runs images, so there is no `sluis-install-local`.
- **`sluis-host` is a separate root host service, mainly for SSH.** It never listens on the network; it fetches the signed SSH principals bundle ([0008](/decisions/0008-certificate-authorities-for-x509-ssh-and-databases/#ssh)), verifies it, writes it atomically and keeps the last good one.
- **The trust bundle reaches hosts through the `sluis` package.** `sluis trust enroll --url … --root-pin <spki-hash>` turns on `sluis-trust-sync.timer`; the sync checks the bundle ([0007](/decisions/0007-workload-identity-and-host-enrolment/#the-trust-bundle)) and the root list ([0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/#root-pins-and-the-root-list)), writes the anchors and runs the distribution's trust update; its unit may write nothing else. sluis generates no per-installation packages. On Kubernetes the bundle reaches pods through a trust-manager source or a ClusterTrustBundle.
- **Lambda:** one function per actor instance and one zip per executable, named `sluis-<kind>-<name>_<version>_<arch>.zip`. Functions, schedules and grants are derived from the manifest. Each function has one Rust `aws` driver extension that implements every AWS port it needs (plus `postgres` where an index is used). The three sluis layers are the edge layer (`entry` only), the drivers layer and the configuration layer, so at least two stay free for the operator. The executable is the package's binary renamed `bootstrap`, behind the kit's invoke adapter; only `entry` uses the Lambda Web Adapter, pinned and vendored, because a loopback listener beside a key is forbidden ([0004](/decisions/0004-drivers-listeners-and-boundary-enforcement/#listeners)). arm64 is the default. sluis publishes no public layer ARNs: an installation verifies the zips and publishes the layers into its own account. Not published for Lambda: the YubiKey driver, `key-holder`, `sluis-node`, rqlite, and every driver that needs a disk or a long-running process.
- **Images:** one image per process, holding one executable at the package's path, byte for byte, with the source package in a label. Bases are distroless static, and distroless with glibc and a pinned pcsc-lite for cgo binaries. Names are `ghcr.io/truvity/sluis/<kind>/<name>`; tags are the release version, and the digest is the reference. **One image set serves the local installation and Kubernetes**, and a release test compares the images' binaries with the packages' files by hash.
- **Helm:** one `sluis` chart with a piece selector and the presets `install`, `audit-install`, `quota-install` and `kubernetes-dev`, and no subcharts, so a writer and a reader always come from one release. Operators such as a Postgres operator or cert-manager are prerequisites the chart checks for. The cert-manager issuer has a chart of its own, with its CRDs and RBAC; the certificate sidecar is an image plus a documented container snippet. The chart version is the release version, under `ghcr.io/truvity/sluis/charts/…`. The console runs as its own Deployment behind the same route as `entry`.
- **One S3-compatible blob driver** for S3 and R2. It takes credentials from the `cloud` port: STS through web identity on AWS, and on Cloudflare a `cloud-cloudflare@<boundary>` driver that mints R2 temporary credentials scoped to one bucket and prefix, signed locally with a per-boundary parent token stored sealed. The driver is separate from the company integration `cloudflare@<company>`, though they share a library.
- **The console is a separate artifact,** a bundle of static files built once per release and served on the same origin as the API: a directory from `sluis-console` on bare-metal, the bucket behind the API gateway on serverless, its own Deployment on Kubernetes.

### Supported platforms

- **Operating systems:** Ubuntu 26.04 LTS, Rocky Linux (the latest stable, 10.x) and Amazon Linux 2023, on amd64 and arm64.
- **The systemd floor is 252,** the version in Amazon Linux 2023. Generated units may rely on what 252 provides, such as `LoadCredentialEncrypted=` and `ExecPaths=`.
- **Serverless and Kubernetes have no host distribution.**
- **A board image older than the floor is a declared exception,** not a supported platform: its units use plain `LoadCredential=` where `LoadCredentialEncrypted=` is missing, and `sluis doctor` warns where its kernel fails the randomness checklist. The exception ends when board images for the supported distributions exist.

### Distribution

- **Everything is served from an R2 bucket, without redirects:** `apt/` and `dnf/` repositories, `bin/<version>/` tarballs, `lambda/<version>/` functions, layers and extensions, `checksums/<version>/` and `keys/`, and a `latest` pointer. apt and dnf handle redirects unreliably, and a redirect is one more place to point a repository elsewhere.
- **GitHub Releases is the immutable record.** Each release holds the same files; when a file on R2 and on the release differ, the release is right.
- **OCI images and charts are on the GitHub container registry.** The website is a separate thing ([0020](/decisions/0020-the-site-is-static-on-cloudflare-workers/)).

### Compatibility between releases

- **Compatibility follows semantic versioning.** A breaking change ships in a major release, or in a minor release that says so in the CHANGELOG, announces it and carries an upgrade note; stored data stays readable by the next release; protocol contracts are checked for breaking changes ([GOVERNANCE.md](https://github.com/truvity/sluis/blob/main/GOVERNANCE.md)).
- **The configuration schema and its identifiers are public contract.** A published `$id` never changes; an incompatible schema is a new version under a new path, beside the old one ([0010](/decisions/0010-configuration-and-data-pkl-schema-origins-and-sensitivities/#pkl-and-json-schema)).
- **Removed configuration is refused, never silently ignored.** A key that no longer means anything fails validation with a message naming its replacement, as in v1 ADR 0007.
- **All artifacts of one version are compatible with each other, and only with each other.** A writer and a reader, a chart and its images, a package and its drivers come from one release.

### Signing and checksums

- **Nothing is signed before the roots exist.** Until the root ceremony and its drill have run in ritueel, a release carries checksums only.
- **From then on, releases are signed through sluis's own signing API** with a release subkey held in a cloud key service ([0013](/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api/#signed-releases)): detached signatures first, then signed apt and dnf repository metadata.

## Consequences

- One tag produces everything, so no one needs a compatibility table between components. A small fix releases every artifact, including unchanged ones.
- Every consumer of a v1 Go module changes its import path once, when it moves to v2; both lines can sit in one build graph during the migration.
- Two toolchains build one train. If the Cargo workspace does not build under GoReleaser, the train needs a special-case job.
- Two native jobs per release build the YubiKey driver, and a failure in either blocks the release.
- About thirty images per architecture are built and published; each digest names one thing, and a scanner reports on the binary a container actually runs.
- The Lambda limits are a build-time budget: the build fails when a function's drivers need more than three sluis layers. Per-actor functions chain cold starts on a first request; provisioned concurrency on `entry` and `signer` is the lever if that is too slow.
- A package per driver means more packages to build and test; the meta-package is the short list an operator installs.
- Prefix scoping on R2 depends on temporary credentials, so the parent token is a per-boundary secret that has to be sealed, rotated and backed up.
- The board exception is the only place where unit credentials are protected by file mode and a private state directory instead of encryption at rest. Key material is never affected: it stays in the card.
- R2 is a single provider for downloads; GitHub Releases is the second copy and wins a disagreement.
- Until the first signed release, a user checks downloads against checksums fetched from GitHub Releases, and an apt or dnf repository cannot be verified by its signature.
- The pages [Packages](/distribution/packages/) and [Platforms](/distribution/packages/#supported-platforms) describe this record; when they disagree, the page is fixed.

## Roads not taken

- **Keep the build profiles.** A profile mixed the runtime with the provider and the use, and a build that is not the one tested is the one that runs.
- **Independent versions per component or module.** A user would need a compatibility table between components released from one commit; v1 ADR 0042 rejected it for the same reason.
- **One binary for everything, selected by subcommand.** An actor's unit could then execute every other actor's code; only the CLI is one binary.
- **A separate Cargo release tool for the Rust executables.** It would be a second train with its own SBOM and signing to keep in step.
- **Drivers inside `sluis-server`, or one `sluis-drivers` package.** A stand-alone audit or Quota host would install the core actors, or every host every driver and its system dependencies.
- **A container image for Lambda.** Layers do not apply to images, so the adapter and extensions would be baked into every image.
- **An image per package, or a distribution base image with the packages installed.** Containers would carry other actors' binaries, a shell and a package manager.
- **A chart per piece, or an umbrella chart with subcharts.** A writer and a reader could come from different releases, and subcharts cannot see the parent's generated schema.
- **Downloads through redirects, or from GitHub Releases alone.** apt and dnf handle redirects unreliably, and GitHub Releases is not an apt or dnf repository.
- **Lower the systemd floor to the boards' image.** Every platform would give up encrypted credentials for one vendor image.

## Follow-ups

- The build system with one version, GoReleaser v2 and the per-architecture jobs: [#860](https://github.com/truvity/sluis/issues/860).
- Prove that GoReleaser builds the multi-binary Cargo workspace for both architectures before the first Rust code ships: [#947](https://github.com/truvity/sluis/issues/947).
- OCI images and Helm charts with digest-pinned images, including whether GHCR accepts three-segment names under the repository prefix (the fallback is `ghcr.io/truvity/sluis/<kind>-<name>`): [#876](https://github.com/truvity/sluis/issues/876).
- Board OS images for Ubuntu 26.04 and Rocky Linux 10.2, which end the board exception: [#881](https://github.com/truvity/sluis/issues/881).
- Release signing and CI upload credentials: [#874](https://github.com/truvity/sluis/issues/874); signed apt and rpm repository metadata: [#1074](https://github.com/truvity/sluis/issues/1074).
- The detail per artifact, what a patch, a minor and a major release may change in the schema, the APIs and the packages, written before the first v2 release: [#1022](https://github.com/truvity/sluis/issues/1022).
