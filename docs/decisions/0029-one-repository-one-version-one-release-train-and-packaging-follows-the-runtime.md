# 0029 — One repository, one version, one release train, and packaging follows the runtime

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). This record continues the one-train rule of v1 ADR 0042 for v2, retires the build profiles of the early v2 design, and renames the package directory that earlier v2 notes called `roles`. Release signing and upload credentials: ADR 0030. Kubernetes pod and chart layout: ADRs 0046 and 0047.

## Context

v1 ships from one repository on one version line, 1.x, and its Go module path has no major-version suffix. v1 ADR 0042 put the audit product, the storage module and the Pulumi libraries on the same train as the issuer, so that a change touching two of them is one pull request and one release. The early v2 design added four build profiles (`aws`, `selfhosted`, `kubernetes`, `dev`) that decided which parts went into a build.

v2 is a clean start on `main` ([0001](/decisions/0001-v1-and-v2-lines/)). It is a different set of processes: actors, drivers, a node agent and one command-line tool ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/), [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). It runs in three runtimes ([0003](/decisions/0003-runtimes-are-runtime-kinds/)), and each runtime installs software in a different way: a package manager on a host, a function archive on a serverless platform, an image on a cluster. Four things have to be fixed together, because each one changes the others:

- how releases are numbered, and what the Go module path says;
- what a release contains, and for which runtime;
- which operating systems and architectures a package targets, and which systemd features the generated units may use;
- where users download from, and what is the record when two copies differ.

Two details of the platform are hard constraints. The YubiKey driver talks to a smart-card service through C bindings (cgo, against pcsc-lite), so it cannot be cross-compiled on a plain build host. And Lambda limits a function to five layers and 250 MB unzipped, with the code, the layers and the extensions counted together.

This record does not set the compatibility rule between releases (what may break in a minor release). v1 ADR 0007 is the v1 rule for 1.x; the v2 rule is ADR 0053. The name and delivery of the separately pinned, certified FIPS build are ADR 0054; the FIPS build modes themselves are in [0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/). How artifacts are signed is ADR 0030.

## Decision

### One repository, one version, one release train

- **Every artifact of a release carries the same version.** Packages, tarballs, Lambda zips, images and charts all carry it, and a release publishes all of them together. A component with no change still ships in the release. This continues v1 ADR 0042 for v2.
- **v2 versions are v2.x.y.** The module path of the main Go module ends in `/v2` (`github.com/truvity/sluis/v2`). Go requires the suffix from major version 2, and it also keeps v1 and v2 importable side by side while the two lines coexist. Any further Go module in the repository takes the same suffix, and all modules are tagged at one commit with one version, as v1 ADR 0042 did.
- **One build tool: GoReleaser v2, open-source edition.** The Rust builds are produced through it too. There is one build definition for every artifact below.
- **The YubiKey driver is built in a native per-architecture CI job.** It needs cgo against pcsc-lite, so the amd64 build runs on amd64 and the arm64 build on arm64. The job's outputs join the rest of the release.
- **One command-line tool: `sluis`, with subcommands** for operators and users. Examples are `sluis config check`, `sluis doctor` and `sluis ceremony`. The actors, drivers and the node agent are separate executables, one per process, because each runs as its own user, unit, function or container ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)).

### Packaging follows the runtime

There are no build profiles. The runtime decides the packaging, and every artifact is built for amd64 and arm64.

| Runtime | Packaging |
|---|---|
| bare-metal | `.deb` and `.rpm` packages in signed apt and dnf repositories, and a tarball |
| serverless | A zip per actor, with layers and extensions |
| kubernetes | A multi-arch OCI image and a Helm chart, with the images pinned by digest |

- **`dev` is not a build.** What the profile `dev` used to mean is the local installation ([0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)), which runs the bare-metal packaging in Docker.
- **Serverless is zip only.** A zip lets layers and extensions combine. Functions, layers and extensions are published for `x86_64` and `arm64`. The Lambda Web Adapter, pinned to one version, is vendored into sluis's own layer, so a function never references the public AWS layer ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). Zips are built to fit five layers and 250 MB unzipped.
- **Kubernetes images and charts are referenced by digest.** The chart is built from the release output, so it installs exactly the images released with it. Chart layout and the Kubernetes baseline are ADR 0047.
- **The install directory is `/usr/libexec/sluis/{actors,drivers,integrations,node,vendor}/<short-name>`.** It is the same in a package and in a tarball. The directory that held the actors is called `actors`; earlier v2 notes called it `roles`, and this rename amends that package layout. File names inside it are unchanged.

### Supported platforms

- **Operating systems:** Ubuntu 26.04 LTS, Rocky Linux (the latest stable release, 10.x) and Amazon Linux 2023.
- **Architectures:** amd64 and arm64, on all of them.
- **The systemd floor is 252**, the version in Amazon Linux 2023, the oldest supported system. Ubuntu 26.04 has 259 and Rocky Linux 10.2 has 257. Generated units may rely on what 252 provides, such as `LoadCredentialEncrypted=` and `ExecPaths=`.
- **Serverless and kubernetes have no host distribution.** Lambda runs the zips on the provider's managed runtime, and a cluster runs the images.
- **The board exception.** The boards run the vendor's Ubuntu 22.04 for now. This is a declared exception, not a supported platform. Its systemd is 249, which has `ExecPaths=` but no `LoadCredentialEncrypted=`, so units there use plain `LoadCredential=`. Its kernel is 5.10, so `sluis doctor` warns on the randomness checklist. Building Ubuntu 26.04 and Rocky Linux 10.2 board images is a backlog task. When the images exist the exception ends and the floor applies to boards.

### Distribution

- **Everything is served from an object-storage bucket on R2, without redirects.** apt and dnf handle redirects unreliably, and a redirect is one more place for a repository to be pointed elsewhere. The bucket serves these paths:
  - `apt/` and `dnf/`: signed repositories for the packages;
  - `bin/<version>/`: tarballs;
  - `lambda/<version>/`: functions, layers and extensions;
  - `checksums/<version>/` and `keys/`: checksums, signatures and public keys;
  - `latest`: a pointer to the newest version.
- **GitHub Releases is the immutable record.** Each release holds the same files, with GitHub build provenance attestations. When a file on R2 and the file on the release differ, the release is right.
- **OCI images and charts are on the GitHub container registry,** not on R2.
- **The website is a separate thing.** Only distribution is on R2.

## Consequences

- One tag produces everything, so a reader never needs a compatibility table between components. The cost is that a small fix releases every artifact, including ones that did not change.
- Every consumer of a v1 Go module changes its import path once, when it moves to v2. Both lines can sit in one build graph during the migration.
- The build needs two native jobs per release, one per architecture, for the YubiKey driver. The jobs' outputs are joined before publishing, so a failure in one architecture blocks the whole release.
- Lambda limits (five layers, 250 MB) are a build-time budget. A new driver packaged as an extension has to fit, and the build fails when it does not.
- The package layout has one spelling of the directory. Everything generated from the permission manifest ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)) uses `actors`, and docs and units that still say `roles` are fixed.
- Because the systemd floor is 252, the board images are the only place where the weaker unit features apply, and `LoadCredential=` there protects secrets by file mode and a private state directory, not by encryption at rest. Key material is never affected, because it stays in the key hardware.
- Mixed distributions in one cluster share one package set. That is allowed and widens the test surface, because systemd, kernel and library versions differ.
- R2 is a single provider for downloads. GitHub Releases is the second copy, and it is the one to trust when they disagree.
- Packaging is checked by tests: each runtime's packages install and start, and `sluis config check` agrees with what the runtime provides.
- The site pages [Packages](/distribution/packages/) and [Platforms](/distribution/platforms/) describe this record. When they disagree, this record is the reasoning and the page is fixed.

## Roads not taken

### Keep the build profiles (`aws`, `selfhosted`, `kubernetes`, `dev`)

- Pros: smaller binaries for each deployment. Familiar to v1 users.
- Cons: a profile mixes the runtime with the provider and the use. Each new platform adds a profile. A build that is not the one you test is the one you run.
- Why not: the runtime is the part that decides how software is installed, so packaging follows it. Everything else is a driver choice at run time ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)), checked by `sluis config check`.

### Independent versions per component or per module

- Pros: a component that did not change keeps its version. Smaller releases.
- Cons: a user needs a compatibility table between components released from one commit. Every cross-cutting change becomes several releases and pins.
- Why not: v1 ADR 0042 rejected the same option for the same reason, and one tag is easier to reason about and to roll back.

### Stay on the v1 line and the v1 module path

- Pros: no import change for consumers.
- Cons: v2 shares no history and no contract with v1, so a v1.x number would say the wrong thing. Go's import compatibility rule expects one path to keep one API, so an incompatible API belongs under a new path. (inferred)
- Why not: v2 is a separate line ([0001](/decisions/0001-v1-and-v2-lines/)), and a major version and a path suffix say so.

### One binary for everything, selected by subcommand

- Pros: one artifact. A simpler package.
- Cons: an actor's unit would be allowed to execute a binary that contains every other actor, and the binary would carry every dependency. (inferred from the process-per-actor model)
- Why not: only the command-line tool is one binary. Each actor is its own executable, so its unit, function or container has the right to run that one file only ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)).

### Cross-compile the YubiKey driver

- Pros: a single build host. No arm64 runner.
- Cons: the driver links against pcsc-lite through cgo, so a cross build needs a matching C toolchain and library for each target. (inferred)
- Why not: a native job per architecture gives a build that is the one that runs, at the cost of two jobs.

### A container image for Lambda

- Pros: one packaging format for serverless and kubernetes. A larger size limit.
- Cons: layers do not apply to an image, so the vendored web adapter and the extensions would have to be baked into every image. (inferred)
- Why not: a zip lets layers and extensions combine, and the size budget is a tested constraint on the build.

### Serve downloads through redirects, or from GitHub Releases alone

- Pros: no second host. A redirect to a CDN can be cheap.
- Cons: apt and dnf handle redirects unreliably, and a redirect chain is one more place a repository can be pointed somewhere else. GitHub Releases is not an apt or dnf repository. (The second reason is inferred.)
- Why not: serving every path directly from one bucket keeps the URL a user pins and the file they get the same thing. GitHub Releases stays as the second copy.

### Lower the floor to the boards' systemd

- Pros: the vendor board images would be supported platforms. No exception to document.
- Cons: the floor would drop to 249, which has no `LoadCredentialEncrypted=`. Every platform would give up encrypted credentials for the sake of one vendor image.
- Why not: the floor is the oldest version in the supported list, 252. The boards are a declared exception until their images move to a supported distribution.
