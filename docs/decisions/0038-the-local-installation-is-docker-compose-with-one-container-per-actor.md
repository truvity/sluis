# 0038 — The local installation is Docker Compose with one container per actor and minimal drivers

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Delivery order: [0039](/decisions/0039-delivery-goes-local-first-then-boards-lambda-and-kubernetes-in-parallel/). How a developer reaches `entry` from outside the Compose network is ADR 0055; see the last Consequences item.

## Context

The first thing built is the local installation: sluis on a workstation, in Docker ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). It has two jobs. It is where the basic functionality is developed and tested, and it is what continuous integration and the integration tests run. It is not for production.

Earlier text described local as one container holding all actors under a supervisor of its own. That conflicts with rules already decided. Nobody execs a driver, and the platform starts every service ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). Each actor is its own user and has the right to run only its own binary ([0007](/decisions/0007-core-is-a-few-actors-with-one-responsibility-each/)). An in-image supervisor puts the exec right back. A local installation that is shaped differently from the others proves less about them.

Two further questions need an answer.

- **Which keys.** Signing is the hardest part of the design to get right, and a software key would leave it untested. The maintainer wants the host's YubiKey on a workstation ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). A CI runner has no YubiKey, and a software key there is acceptable if the code never lets it near production ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).
- **Which drivers.** A workstation should start fast and need nothing else running. The richer drivers (a replicated store, a cloud queue, an object lock) belong to the installations that need them.

A YubiKey is a smart card, reached through the smart-card service (`pcscd`). That service lets several programs share one card, so a program in a container can use the card while the host keeps using it.

## Decision

- **The local installation runs under Docker Compose, with one container per actor.** Every driver is a service of its own too, because the platform starts services and nobody execs one ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)). Compose is the platform that starts and orders them. The runtime is bare-metal, with one node.
- **Unix sockets on a shared volume.** Actors and drivers keep the same socket layout as everywhere (`/run/sluis/<actor>/`), here on a volume that the containers share. Each container runs as the uid of its actor, so the peer-credential checks and the access lists of the shared drivers work as on a host.
- **The images install the bare-metal packages** ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)), so local runs the same files as the boards and a virtual machine.
- **Minimal drivers:**

  | Port | Driver |
  |---|---|
  | keys | `yubikey-piv` through the host's `pcscd` on a workstation; `software` in continuous integration |
  | state | `embedded` (SQLite, exactly one node) |
  | lease | local lock |
  | blob | `fs` |
  | transport | `spool`, or `direct` |
  | index | `sqlite` |
  | witness | none (`sluis config check` warns, as it does for any installation without a witness) |

  A choice of drivers is a default of this installation and not a rule. `sluis config check` accepts any other combination the rules allow ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)).
- **The host's YubiKey through the host's `pcscd` socket.** The `key-holder` container mounts the host's `pcscd` socket and talks to the card through it. The host keeps using the card as well. Only the `key-holder` container gets the mount. The pcsc-lite client protocol in the image has to match the host daemon, and that is a known risk. The fallback is to run the `key-holder` natively on the host and mount its socket into the containers.
- **Software keys in continuous integration.** A job without hardware uses the `software` keys driver. The driver refuses to start when the environment is production, and it starts with a warning in staging ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)). A developer may also use it, for data that does not matter.
- **Used for development and the integration tests.** It is not a production installation, and the local installation proves behaviour and not availability.
- **It ships as an example install file.** The tested installations are example install files, not a configuration key ([0003](/decisions/0003-runtimes-are-runtime-kinds/)). The layers of configuration are decided separately: [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/).
- **Direct transport falls back to a local spool when the writer is down** ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)). Local has a disk, so `direct` and `spool` are both possible there.

## Consequences

- The local installation exercises the same call graph, the same socket layout and the same per-actor identity as the boards. A rule that a deployment breaks (an actor reaching a driver it has no access to) is found on a workstation.
- A developer needs a YubiKey for the full local installation. Without one, the `software` driver runs the same tests except those about the card.
- The Compose file is the local counterpart of the systemd units. It has to be generated from the permission manifest like the units are ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)), or the two will drift. This is work that must follow.
- Several containers instead of one cost start-up time and memory. That is accepted for fidelity.
- Peer credentials work across containers only if the uids are fixed and agree between images and the generated access lists. The images pin the uids.
- The host's `pcscd` socket is outside the isolation of the container, and any container given the mount can use the card. That is why only the `key-holder` container gets it.
- With a YubiKey the PIV signing speed is about eight signatures a second, which is enough for local tests but is a limit for a load test. Load tests run on installations that use cloud keys.
- Local does not exercise the platform controls of the other installations: IAM and key policies, systemd sandboxing, a replicated store, a compliance lock. Those need the boards, lambda and kubernetes installations.
- The `fs` blob has no lock mode, so tamper evidence on local comes from verification and not from the store. A test can rebuild the `sqlite` index and still verify the chain.
- A software key is extractable by anyone who can read the key's volume. Use it for development data only.
- The listener rule of [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/) still holds: `entry`'s public API listens on a loopback address and only a connector reaches it. How a developer or a test harness reaches that listener from outside the Compose network, by a published port or by an edge-connector container, is not decided here. It is ADR 0055.

## Roads not taken

### One container with all actors under a supervisor

- Pros: one image and one command. Fewer moving parts.
- Cons: the supervisor inside the image is something that starts processes, which the design takes away from actors. It does not follow the unit-per-actor model of the other installations, so a socket or an access-list mistake can pass local and fail elsewhere. (inferred from the platform-starts-services rule)
- Why not: one container per actor and per driver keeps local shaped like the others, at a small cost in start-up time.

### A software key for every local run

- Pros: nothing to buy or plug in. Works the same on every machine and in every job.
- Cons: the hardest part of the design, signing with a non-extractable key, stays untested until the hardware installations. The key is a file.
- Why not: the maintainer wants the key path exercised from the first installation. The software driver remains for continuous integration, where there is no card.

### Pass the USB device into the container

- Pros: no host service in between. The container owns the card.
- Cons: the host loses the card for the time, and two stacks for the card (the host's and the container's) can conflict. (inferred)
- Why not: `pcscd` already shares one card among programs, so the host's socket is the lighter way to give the container access.

### Run the `key-holder` on the host and mount only its socket

- Pros: the card never enters a container. The protocol version of the image no longer matters.
- Cons: the `key-holder` is no longer started by the same platform as everything else, and the host needs the sluis package for it. (inferred)
- Why not first: it is the fallback and stays one. The default keeps every actor and driver in Compose, and the protocol-match risk is stated.

### Richer local drivers (a replicated store, an object store, a queue)

- Pros: closer to the cloud installations.
- Cons: more to run on a workstation, slower start, and each one is a dependency a developer has to understand before the first test. (inferred)
- Why not: those drivers belong to boards, lambda and kubernetes, which are tested for them. Local uses the smallest driver of each port.

### Local on a native install with systemd units

- Pros: identical to the boards.
- Cons: it needs root and service users on the developer's machine, and is harder to reset between tests. (inferred)
- Why not: Compose gives per-actor isolation with a command to throw everything away. The bare-metal packages are what the images install, so the files are still the same.
