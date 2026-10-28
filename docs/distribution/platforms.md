---
title: Platforms
description: The distributions and architectures sluis v2 supports, the systemd floor of 252, the interim board exception, and the FIPS builds.
status: target
sidebar:
  order: 3
---

## Supported distributions and architectures

| Distribution | Version | systemd | Shapes |
|---|---|---|---|
| Ubuntu | 26.04 LTS | 259 | Self-hosted servers and, once the images exist, the boards (T1, T2) |
| Rocky Linux | Latest stable, 10.x (10.2) | 257 | Self-hosted servers and, once the images exist, the boards (T1, T2) |
| Amazon Linux | 2023 | 252 | [T3](/shapes/aws-ec2-single/) and [T4](/shapes/aws-ec2-cluster/) |

All of them are supported on **amd64 and arm64**. The packages come from the apt repository (Ubuntu) and the dnf repository (Rocky Linux and Amazon Linux); see [Packages](/distribution/packages/).

AWS Lambda and Kubernetes do not depend on a host distribution: Lambda runs zip packages on the managed runtime, and Kubernetes runs OCI images.

## The systemd floor

The systemd floor is **252**, the version in Amazon Linux 2023, which is the oldest supported distribution. Ubuntu 26.04 has 259 and Rocky Linux 10.2 has 257, both above the floor. The generated units can rely on what 252 provides, such as `LoadCredentialEncrypted=` and `ExecPaths=`.

## Board images and the Ubuntu 22.04 exception

The two board shapes, [T1](/shapes/single-node/) and [T2](/shapes/cluster/), run an arm64 single-board computer (2 GB RAM, eMMC). The boards run the vendor's Ubuntu 22.04 for now. This is a declared exception to the list above, not a supported platform.

On Ubuntu 22.04:

- systemd is 249, below the floor. It has `ExecPaths=` but no `LoadCredentialEncrypted=`, so units use plain `LoadCredential=`.
- The kernel is 5.10. `sluis doctor` warns on the randomness checklist, because VM-clone reseeding needs a kernel from 5.18. `sluis doctor` also checks that a hardware random generator is present or deliberately absent (the RK3566 one is disabled in mainline on purpose) and that entropy is initialised before the first key is generated.

**Backlog:** building Ubuntu 26.04 and Rocky Linux 10.2 board images. The image sources will live in a separate public repository. When those images exist the exception ends and the floor applies to boards.

## FIPS builds

- **Default build.** FIPS mode is on by default. It runs Go's FIPS 140-3 module in `fips140=on` mode, using the latest module. In this mode TLS refuses X25519 and ChaCha20, and key generation runs pairwise consistency tests. Both behaviours are acceptable for sluis.
- **Certified build.** A separately pinned build uses the CMVP-certified module, certificate #5247, module v1.0.0, for installs that need the certificate.
- **BoringCrypto is not used.**

:::note[Proposed]
How the certified build is named and delivered (its own repository path, tarballs or both) is not yet decided.
:::

## Algorithms across platforms

EC P-384 is used everywhere, with no RSA, apart from two exceptions. A cloud-federation key may use the algorithm its cloud requires, each in its own slot or key: P-384 for AWS, P-256 (ES256) for GCP, and RSA-2048 (RS256) for Azure Entra. An audience whose verifiers cannot take EC may get RS256 tokens signed with a separate RSA key. See [Keys](/concepts/keys/).
