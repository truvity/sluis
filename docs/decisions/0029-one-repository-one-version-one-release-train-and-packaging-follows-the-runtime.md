# 0029 — One repository, one version, one release train, and packaging follows the runtime

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). This record continues the one-train rule of v1 ADR 0042 for v2, retires the build profiles of the early v2 design, and renames the package directory that earlier v2 notes called `roles`. Release signing and upload credentials: ADR 0030. Keys and the hand-off to root custody: [0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/). Kubernetes pod layout and the Kubernetes baseline: ADRs 0046 and 0047.
Rewritten in place on 2026-10-30 (realignment pass; then the package split by function, the Lambda, OCI and Helm packaging, one S3-compatible blob driver, and Rust executables on the same train).
Rewritten in place on 2026-10-30 (two open points of [issue #910](https://github.com/truvity/sluis/issues/910) decided: a package per driver executable with installation meta-packages, and the Lambda audit writer on reserved concurrency 1 with the blob fence; two points stay open).
Rewritten in place on 2026-10-31 (no `sluis-install-local` meta-package until a native local mode exists, decided by the maintainer).
Rewritten in place on 2026-10-31 (no FIPS build in the release, after [ADR 0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/); the Rust FIPS open point is closed).
Rewritten in place on 2026-11-01 (root ceremonies moved to the separate product [ritueel](https://ritueel.id): there is no ceremony helper and no `helpers` directory; the CLI has `sluis key request|install|status`; `sluis trust sync` checks a root list).

## Context

v1 ships from one repository on one version line, 1.x, and its Go module path has no major-version suffix. v1 ADR 0042 put the audit product, the storage module and the Pulumi libraries on the same train as the issuer, so that a change touching two of them is one pull request and one release. The early v2 design added four build profiles (`aws`, `selfhosted`, `kubernetes`, `dev`) that decided which parts went into a build.

v2 is a clean start on `main` ([0001](/decisions/0001-v1-and-v2-lines/)). It is a different set of processes: actors, drivers, a node agent, a host service and one command-line tool ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/), [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). Some of these executables are written in Go and some in Rust (ADR 0060). It runs in three runtimes ([0003](/decisions/0003-runtimes-are-runtime-kinds/)), and each runtime installs software in a different way: a package manager on a host, a function archive on a serverless platform, an image on a cluster. Four things have to be fixed together, because each one changes the others:

- how releases are numbered, and what the Go module path says;
- what a release contains, and for which runtime;
- which operating systems and architectures a package targets, and which systemd features the generated units may use;
- where users download from, and what is the record when two copies differ.

The executable is what carries over between runtimes, not the package. A package groups executables for a host's package manager; a Lambda function and a container each run exactly one of them. So the package split decides little about Lambda and images beyond names, and each runtime adds its own wrapping around the executable.

Some details of the platforms are hard constraints:

- The YubiKey driver talks to a smart-card service through C bindings (cgo, against pcsc-lite), so it cannot be cross-compiled on a plain build host.
- Lambda limits a function to five layers and 250 MB unzipped, with the code, the layers and the extensions counted together. Every process in one function shares the function's role, so the function is the credential scope. A driver extension must come from a layer. For sluis the layer count, not the size, is the binding limit.

This record does not set the compatibility rule between releases (what may break in a minor release). v1 ADR 0007 is the v1 rule for 1.x; the v2 rule is ADR 0053. The release has no FIPS build ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)). The planned record on a certified FIPS build (ADR 0054) is deferred until a FIPS build variant is added, when a customer needs it. How artifacts are signed is ADR 0030.

## Decision

### One repository, one version, one release train

- **Every artifact of a release carries the same version.** Packages, tarballs, Lambda zips, images, charts and the console bundle all carry it, and a release publishes all of them together. A component with no change still ships in the release. This continues v1 ADR 0042 for v2.
- **v2 versions are v2.x.y.** The module path of the main Go module ends in `/v2` (`github.com/truvity/sluis/v2`). Go requires the suffix from major version 2, and it also keeps v1 and v2 importable side by side while the two lines coexist. Any further Go module in the repository takes the same suffix, and all modules are tagged at one commit with one version, as v1 ADR 0042 did.
- **One build tool: GoReleaser v2, open-source edition.** A release contains Rust executables as well as Go ones, and they are built through the same train, with the same SBOM, signing and provenance. There is one build definition for every artifact below. GoReleaser's Rust builder is documented for simple projects, so a multi-binary Cargo workspace for two architectures is proved by a spike (S10, tracked with ADR 0060) before the first Rust code ships.
- **No FIPS build in the release.** The Rust executables are built on GoReleaser's open-source path with cargo-zigbuild, with current crates and rustls. A future FIPS build variant would need its own build design, decided when it is added ([0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)).
- **The YubiKey driver is built in a native per-architecture CI job** while it needs cgo, inside the userland of the oldest supported distribution, so it runs on every supported glibc. It needs cgo against pcsc-lite, so the amd64 build runs on amd64 and the arm64 build on arm64. The job's outputs join the rest of the release.
- **One command-line tool: `sluis`, with subcommands** for operators and users. Examples are `sluis config check`, `sluis doctor` and `sluis trust sync`. The actors, drivers, the node agent and the host service are separate executables, one per process, because each runs as its own user, unit, function or container ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). The key commands are `sluis key request`, `sluis key install` and `sluis key status` ([0031](/decisions/0031-intermediates-and-the-hand-off-to-root-custody/)); root ceremonies belong to [ritueel](https://ritueel.id), not to sluis.

### Packaging follows the runtime

There are no build profiles. The runtime decides the packaging, and every artifact is built for amd64 and arm64.

| Runtime | Packaging |
|---|---|
| bare-metal | `.deb` and `.rpm` packages, split by function, in signed apt and dnf repositories, and a tarball |
| serverless | A zip per executable, a function per actor instance, and at most three sluis layers per function |
| kubernetes | One multi-arch OCI image per process, and one Helm chart, with the images pinned by digest |

- **`dev` is not a build.** What the profile `dev` used to mean is the local installation ([0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)), which runs the published images in Docker.
- **The install directory is `/usr/libexec/sluis/{actors,drivers,integrations,node,vendor}/<short-name>`.** It is the same in a package, a tarball and an image. The directory that held the actors is called `actors`; earlier v2 notes called it `roles`, and this rename amends that package layout. File names inside it are unchanged.

### Packages for hosts

The deb and rpm packages are split by function, and every driver executable is a package of its own. A package is a grouping for a host, not a unit of execution.

| Package | Holds |
|---|---|
| `sluis` | The CLI, `sluis agent`, the `sluis trust sync` timer, and the `sluis key` subcommands |
| `sluis-node` | The node agent |
| `sluis-server` | The core actors and the first-party integrations |
| `sluis-driver-<name>` | One driver executable, with the system packages it needs |
| `sluis-audit-writer` | The audit writer and witness |
| `sluis-audit-search` | The indexer and reader |
| `sluis-quota` | `quota` |
| `sluis-host` | The SSH host service |
| `sluis-console` | The console's static bundle |
| `sluis-install-<installation>` | A meta-package per tested installation that installs from packages: the packages and drivers that installation runs |

- **One package per driver executable, with its system dependencies.** For example:
  - `sluis-driver-yubikey-piv` depends on pcscd and pcsc-lite;
  - `sluis-driver-pkcs11` depends on p11-kit;
  - `sluis-driver-rqlite` ships `rqlited` under `vendor/` and the `sluis-rqlite@.service` template;
  - `sluis-driver-aws` is the one Rust `aws` driver executable.

  A host then installs only the drivers it runs, and a driver's system dependency is declared once, in its own package. The driver packages replace `sluis-drivers` and `sluis-driver-yubikey`.
- **Meta-packages exist only for the bare-metal installations.** The lambda and kubernetes installations have no deb or rpm, so they have none. `sluis-install-boards` installs what the boards run. The local installation runs the published images under Docker ([0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)), so there is no `sluis-install-local` until a native local mode exists (decided 2026-10-31).
- **`sluis-server`, `sluis-audit-writer`, `sluis-audit-search` and `sluis-quota` depend on no driver package.** The drivers an installation chooses come from its meta-package or from the operator, so a host that runs only the audit log or only Quota does not install the core actors, and no host installs a driver it does not use.
- **`sluis-host` is a separate root host service, mainly for SSH.** It never listens on the network. It fetches a signed bundle, verifies it, writes it atomically and keeps the last good one. It ships as signed deb and rpm packages. There is no NixOS module.
- **The trust bundle reaches hosts through the generic `sluis` package.** It ships `sluis trust sync` as a oneshot service with a `sluis-trust-sync.timer`, which stays off until `sluis trust enroll --url … --fingerprint …` configures it. The sync fetches the bundle in SPIFFE format, checks its signature against the root list and the pinned roots, refuses a lower sequence number or a lower root-list version, writes the anchors and runs the distribution's trust update. Its unit may write only the anchors and the extracted store. **sluis generates no packages.** On Kubernetes the bundle reaches pods through a trust-manager source or a ClusterTrustBundle.

### Lambda

- **One function per actor instance, and one zip per executable.** An integration's zip may back more than one function when its actors differ in concurrency, for example a scheduled reconciler and an on-demand minter. Functions, schedules and grants are derived from the manifest, and a test checks that every declared event kind has a handler in some function.
- **One Rust `aws` driver executable per function, as an extension.** It implements every AWS port the function needs, so a function needs one driver extension, plus the `postgres` driver where an index is used.
- **At most three sluis layers per function:** the edge layer (for `entry` only), the drivers layer and the configuration layer. At least two layers stay free for the operator. There is no OpenTelemetry layer, because telemetry exports from the SDK directly. Configuration comes from a configuration layer that the installation builds, or through the AWS AppConfig API, never through the published AppConfig agent layer.
- **The function's executable is the package's binary, renamed `bootstrap`.** An invoke adapter in the kit maps the invocation onto the in-process handler. Only `entry` uses the Lambda Web Adapter, which is pinned and vendored into the edge layer; a function never references the public AWS layer ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). The other functions have no listener.
- **arm64 is the default, and both architectures are published.** One installation uses one architecture for all its functions, because layers must match. sluis publishes no public layer ARNs: the installation's own infrastructure code verifies the signature and checksum and publishes the layers into its own account.
- **Files are named `sluis-<kind>-<name>_<version>_<arch>.zip`,** for example `sluis-actor-entry_<version>_arm64.zip` and `sluis-driver-aws_<version>_arm64.zip`. The name is unique without a directory, because GitHub Releases is flat and some short names (`aws`) exist as a driver and as an integration.
- **The audit writer runs with a reserved concurrency of 1 on a standard SQS queue.** An event source mapping starts at a concurrency of 2, so the limit throttles it and messages return to the queue. Correctness does not come from the limit: every batch is created only if absent (`If-None-Match: *`), so a second writer cannot fork the log. The cost of the throttling is measured at the Lambda step of the delivery order ([0039](/decisions/0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel/)).
- **Not published for Lambda:** the YubiKey driver, `key-holder`, `sluis-node`, rqlite and every driver that needs a disk or a long-running process. With the cloud key service and the cloud state store, the platform does their job.

### Images

- **One image per process.** Each image holds one executable at the same path as in the package, byte for byte, with the source package named in a label. There is no image per package and no multi-binary image.
- **Base images:** distroless static for static binaries, and distroless with glibc plus a pinned pcsc-lite for cgo binaries, built against the oldest supported glibc.
- **Names:** `ghcr.io/truvity/sluis/<kind>/<name>`, with the kinds of the install directory (`actors`, `drivers`, `integrations`, `node`) plus the edge, the console and the adapters. An installation's own mirror may use its own path, and copies by digest.
- **One image set serves the local installation and Kubernetes.** The images carry the packages' binaries, and a release test compares their hashes ([0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/)).
- Tags are the release version only. The digest is the reference; the tag is for people.

### Helm

- **One `sluis` chart, with a piece selector and presets,** and no subcharts. The presets are `install`, `audit-install`, `quota-install` and `kubernetes-dev`. A writer and a reader therefore always come from the same release. This settles the open question of one chart or two. Operators such as a Postgres operator or cert-manager are prerequisites the chart checks for, not subcharts.
- **The cert-manager issuer has a chart of its own,** with its CRDs and RBAC, so the main chart stays free of them. The certificate sidecar is an image plus a documented container snippet.
- **The chart version is the release version,** and charts are published under `ghcr.io/truvity/sluis/charts/…`, a stream of their own, apart from v1's charts.
- **The console runs as its own Deployment on Kubernetes,** behind the same route as `entry`. It is a key-less listener and is named on the Listeners page.
- **The chart renders no Role for leases.** Leases live on the state store ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). A Kubernetes Lease driver is in the backlog.

### Drivers that ship

- **There is one S3-compatible blob driver.** The separate `r2` blob driver is removed. The blob driver gets its credentials from the `cloud` port:
  - on AWS, from STS through web identity;
  - on Cloudflare, from a `cloud-cloudflare@<boundary>` driver that mints R2 temporary credentials scoped to one bucket and prefix. It mints them locally, signed with a parent token's secret. The parent token is per boundary and is stored sealed.
- **The `cloud-cloudflare` driver is separate from the company integration `cloudflare@<company>`,** though they share a library. Long-lived R2 tokens can be scoped to a bucket only, and only temporary credentials can be scoped to a prefix. R2 has no OpenID Connect federation (checked 2026-09-26).

### The console

- **The console's single-page application is a separate artifact,** deployable on its own. It is a bundle of static files built once per release for every runtime, so the console can be redeployed without touching an actor. Each runtime's static host serves it on the same origin as the API ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)):
  - bare-metal: a directory, from the `sluis-console` package;
  - serverless: the bucket behind the API gateway;
  - kubernetes: its own Deployment, with its image pinned by digest like the others.

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

### Open points

These are tracked in [issue #910](https://github.com/truvity/sluis/issues/910):

- whether GHCR accepts three-segment names under the repository prefix (the fallback is `ghcr.io/truvity/sluis/<kind>-<name>`).

Decided on 2026-10-30: the Lambda audit writer uses a reserved concurrency of 1 on a standard queue with the blob fence (both above). Closed on 2026-10-31: whether a Rust FIPS build can be static, because the release has no FIPS build.

## Consequences

- One tag produces everything, so a reader never needs a compatibility table between components. The cost is that a small fix releases every artifact, including ones that did not change.
- Every consumer of a v1 Go module changes its import path once, when it moves to v2. Both lines can sit in one build graph during the migration.
- Two toolchains build the release. If the Cargo workspace does not build under GoReleaser, the train needs a special-case job, which the spike is there to avoid.
- The build needs two native jobs per release, one per architecture, for the YubiKey driver. The jobs' outputs are joined before publishing, so a failure in one architecture blocks the whole release.
- About thirty images per architecture are built, signed and attested. Each digest names one thing, and a scanner reports on the binary a container actually runs.
- Lambda limits (five layers, 250 MB) are a build-time budget. The build fails when a function's declared drivers need more than three sluis layers. Per-actor functions chain cold starts on a first request, and provisioned concurrency on `entry` and `signer` is the lever if that is too slow.
- The package split lets a host install only what it runs. A package per driver means more packages to build, sign and test, about one per driver executable, and the meta-packages are the short list an operator installs. A stand-alone audit or Quota host stays free of the core actors.
- On Lambda a throttled writer delays records, it does not lose or fork them. If the measured delay is too long, a FIFO queue with one message group or a lease on DynamoDB is the alternative, still with the blob fence. (the alternative is inferred from the earlier options)
- One blob driver means one driver to test for S3 and R2. Prefix scoping on R2 depends on temporary credentials, so the parent token is a secret per boundary that has to be sealed, rotated and backed up.
- The trust bundle reaches any host through one generic package and a timer, and no release has to carry a per-installation package.
- The package layout has one spelling of the directory. Everything generated from the permission manifest ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)) uses `actors`, and docs and units that still say `roles` are fixed.
- Because the systemd floor is 252, the board images are the only place where the weaker unit features apply, and `LoadCredential=` there protects secrets by file mode and a private state directory, not by encryption at rest. Key material is never affected, because it stays in the key hardware.
- Mixed distributions in one cluster share one package set. That is allowed and widens the test surface, because systemd, kernel and library versions differ.
- R2 is a single provider for downloads. GitHub Releases is the second copy, and it is the one to trust when they disagree.
- Packaging is checked by tests: each runtime's packages install and start, the images' binaries match the packages' by hash, and `sluis config check` agrees with what the runtime provides.
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

### A separate build tool for the Rust executables

- Pros: Cargo's own release tooling, with no reliance on GoReleaser's Rust builder.
- Cons: a second release definition, with its own signing, SBOM and provenance, and two trains to keep in step.
- Why not: one train builds everything. The spike checks that GoReleaser can carry the Cargo workspace.

### Drivers and integrations inside `sluis-server`, or one `sluis-drivers` package

- Pros: fewer packages to build, sign and version.
- Cons: with everything in `sluis-server`, a stand-alone audit or Quota host installs the core actors too. A shared `sluis-drivers` package installs every driver on every host, and a driver's system dependency either lands on hosts that do not need it or needs a package of its own anyway. `sluis-drivers` was the first version of this record.
- Why not: one package per driver executable declares its own dependencies, and meta-packages per installation keep the install short.

### A package per integration

- Pros: the smallest possible install.
- Cons: dozens more packages to sign and version, for executables that need no system dependency. (inferred)
- Why not: the integrations stay in `sluis-server`. Only drivers, which carry system dependencies, are split one per package. (inferred)

### A FIFO queue or a lease for the Lambda audit writer

- Pros: one writer at a time by construction, with no throttling.
- Cons: a FIFO queue limits throughput per message group, and a lease adds a DynamoDB round trip to every batch. (inferred)
- Why not: the blob fence already makes a second writer harmless, so a reserved concurrency of 1 on a standard queue is enough. The throttling is measured at the Lambda step.

### Cross-compile the YubiKey driver

- Pros: a single build host. No arm64 runner.
- Cons: the driver links against pcsc-lite through cgo, so a cross build needs a matching C toolchain and library for each target. (inferred)
- Why not: a native job per architecture gives a build that is the one that runs, at the cost of two jobs.

### A container image for Lambda

- Pros: one packaging format for serverless and kubernetes. A larger size limit.
- Cons: layers do not apply to an image, so the vendored web adapter and the extensions would have to be baked into every image. (inferred)
- Why not: a zip lets layers and extensions combine, and the size budget is a tested constraint on the build.

### One extension layer per port driver on Lambda

- Pros: each driver is published and updated on its own.
- Cons: `entry` would need about six layers, more than Lambda allows, and nothing would be left for the operator.
- Why not: one Rust `aws` driver per function implements every AWS port it needs.

### The Lambda Web Adapter in every function

- Pros: one handler shape for every function.
- Cons: it puts a loopback TCP listener in `signer`, `policy` and `ca`, beside their keys, which [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/) forbids.
- Why not: only `entry` uses it. The others use the kit's invoke adapter.

### Public layer ARNs published by sluis

- Pros: an installation could reference a layer without publishing it.
- Cons: a function would depend on an account sluis runs, and Lambda code signing cannot verify sluis's signatures.
- Why not: the installation verifies the zips and publishes them into its own account.

### An image per package, or one multi-binary image

- Pros: fewer images to build and sign.
- Cons: every container would carry other actors' binaries, against the rule that a container may run one file only, and the image would change when any member changes.
- Why not: one image per process.

### A distribution base image with the packages installed

- Pros: literally the same packages in an image. One recipe.
- Cons: a shell and a package manager in every container, a larger image, and vulnerabilities in code that no actor runs.
- Why not: distroless images carry the packages' binaries, and a hash test proves they are the same files.

### Local builds its own images from the packages

- Pros: the earlier wording of [0038](/decisions/0038-the-local-installation-is-docker-compose-with-one-container-per-actor/) would stand.
- Cons: two image sets for the same binaries, and the local installation would not test the images Kubernetes runs. It would be a dev build by another name.
- Why not: one image set serves both.

### A chart per piece, or an umbrella chart with subcharts

- Pros: each chart is small, and an audit chart reads naturally for an audit installation.
- Cons: the install document would be cut into several schemas, and a writer and a reader could come from different releases unless every chart refused a peer of another version. Subcharts split the values and cannot see the parent's generated schema.
- Why not: one chart with presets, and no subcharts.

### Images and charts under the organisation's flat paths

- Pros: short names, matching the organisation's existing chart namespace.
- Cons: a release token cannot create a package outside the repository's prefix, a flat name is ambiguous between a driver and an integration, and v1 already publishes its 1.x charts there.
- Why not: every v2 artifact lives under the repository's prefix.

### A separate `r2` blob driver

- Pros: R2's own behaviour in its own driver.
- Cons: two blob drivers that speak the same S3 API, and R2 credentials held inside a blob driver rather than by the port that issues cloud credentials.
- Why not: one S3-compatible blob driver, with credentials from the `cloud` port.

### Serve downloads through redirects, or from GitHub Releases alone

- Pros: no second host. A redirect to a CDN can be cheap.
- Cons: apt and dnf handle redirects unreliably, and a redirect chain is one more place a repository can be pointed somewhere else. GitHub Releases is not an apt or dnf repository. (The second reason is inferred.)
- Why not: serving every path directly from one bucket keeps the URL a user pins and the file they get the same thing. GitHub Releases stays as the second copy.

### Lower the floor to the boards' systemd

- Pros: the vendor board images would be supported platforms. No exception to document.
- Cons: the floor would drop to 249, which has no `LoadCredentialEncrypted=`. Every platform would give up encrypted credentials for the sake of one vendor image.
- Why not: the floor is the oldest version in the supported list, 252. The boards are a declared exception until their images move to a supported distribution.
