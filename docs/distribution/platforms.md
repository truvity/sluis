---
title: Platforms
description: The distributions and architectures sluis v2 supports, the systemd floor of 252, the interim board exception, FIPS and the signing algorithms.
status: target
sidebar:
  order: 3
---

## Supported distributions and architectures

| Distribution | Version | systemd | Installations |
|---|---|---|---|
| Ubuntu | 26.04 LTS | 259 | Self-hosted servers and, once the images exist, the [boards](/runtimes/installations/#boards) |
| Rocky Linux | Latest stable, 10.x (10.2) | 257 | Self-hosted servers and, once the images exist, the boards |
| Amazon Linux | 2023 | 252 | EC2 installations, which are possible and not tested; see [Installations](/runtimes/installations/#other-installations) |

All of them are supported on **amd64 and arm64**. The packages come from the apt repository (Ubuntu) and the dnf repository (Rocky Linux and Amazon Linux); see [Packages](/distribution/packages/).

The [serverless](/runtimes/serverless/) and [kubernetes](/runtimes/kubernetes/) runtimes do not depend on a host distribution: Lambda runs zip packages on the managed runtime, and Kubernetes runs OCI images on distroless bases. An executable that needs cgo is built against the oldest supported glibc (Amazon Linux 2023's), so the same binary runs in a package and in an image.

## The systemd floor

The systemd floor is **252**, the version in Amazon Linux 2023, which is the oldest supported distribution. Ubuntu 26.04 has 259 and Rocky Linux 10.2 has 257, both above the floor. The generated units can rely on what 252 provides, such as `LoadCredentialEncrypted=` and `ExecPaths=`.

## Board images and the Ubuntu 22.04 exception

The [boards](/runtimes/installations/#boards) installation, and a one-board variant of it, run on arm64 single-board computers (2 GB RAM, eMMC). The boards run the vendor's Ubuntu 22.04 for now. This is a declared exception to the list above, not a supported platform.

On Ubuntu 22.04:

- systemd is 249, below the floor. It has `ExecPaths=` but no `LoadCredentialEncrypted=`, so units use plain `LoadCredential=`.
- The kernel is 5.10. `sluis doctor` warns on the randomness checklist, because VM-clone reseeding needs a kernel from 5.18. `sluis doctor` also checks that a hardware random generator is present or deliberately absent (the RK3566 one is disabled in mainline on purpose) and that entropy is initialised before the first key is generated.

**Backlog:** building Ubuntu 26.04 and Rocky Linux 10.2 board images. The image sources will live in a separate public repository. When those images exist the exception ends and the floor applies to boards.

## FIPS

- **No FIPS build in v2.0.** There is no FIPS mode and no certified build. Every platform above gets the same build: Go without forced FIPS mode, and Rust with current crates and rustls ([ADR 0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/)).
- **The algorithms stay within the FIPS-approved set**, so a FIPS build variant can be added later as a build option when a customer needs it. The variant gets its own build design, name and delivery when it is added.
- **BoringCrypto is not used**, in this build or in the variant.

## Algorithms across platforms

ES384 (P-384) is sluis's internal recommendation and default, and there is no RSA apart from the exceptions below. sluis supports the FAPI 2.0 set of signing algorithms, PS256, ES256 and EdDSA (Ed25519), as well. PS256 is RSA, so it is another exception to the no-RSA rule. Driver support has to be checked per algorithm: PS256 on a YubiKey takes about 2.3 s per signature, and PIV Ed25519 needs firmware 5.7 or newer. A cloud-federation key may use the algorithm its cloud requires, each in its own slot or key: P-384 for AWS, P-256 (ES256) for GCP, and RSA-2048 (RS256) for Azure Entra. An audience whose verifiers cannot take EC may get RS256 tokens signed with a separate RSA key. See [Keys](/concepts/keys/).
