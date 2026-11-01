---
title: Source layout
description: Which language each sluis component is written in, the shared kits every language implements, and the layout of the repository tree.
status: target
sidebar:
  order: 6
---

## Languages

**One executable, one language.** The language of each executable follows a set of rules, agreed on 2026-10-30 ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)):

| Rule | Reading |
|---|---|
| R1 | One executable is written in one language |
| R2 | Rust at the unauthenticated edge, when no Go-only protocol library is needed |
| R3 | Rust for the trusted base, with one named exception: `key-holder` is Go |
| R4 | Go where the deciding library is Go-only, or best in Go |
| R5 | A driver follows its backend's best SDK: AWS, GCP and Azure are Rust |
| R6 | Go by default, except on the smallest board tier (R11) |
| R7 | No Rust where the only library for a security function is experimental or unaudited |
| R8 | No FIPS build in v2.0; when a FIPS variant is added, Rust uses `aws-lc-rs` in FIPS mode, mirroring [ADR 0010](/decisions/0010-cryptography-uses-p384-by-default-with-named-exceptions/) |
| R9 | The kits of both languages pass one conformance suite |
| R10 | Contracts are on the wire, so a choice can be reversed |
| R11 | On the smallest board tier an actor prefers Rust, unless a serious trade-off is named; the list per actor is in ADR 0060 |

| Executable | Language |
|---|---|
| `entry` (slim) | Rust |
| `sluis-node` | Rust |
| `sluis-host` | Rust |
| The AWS driver and the other cloud drivers | Rust |
| `policy` and its Cedar engine (the `cedar-policy` crate) | Rust |
| `key-holder`, with `piv-go` for YubiKey PIV | Go (an exception to R3, after spike S4); PKCS#11 only for the later YubiHSM 2 and Nitrokey drivers |
| `cloud-cloudflare` | Go, sharing the library of the `cloudflare@<company>` integration (an exception to R5) |
| `session`, `exchange`, `console`, `quota`, `signer`, `ca`, `ssh-ca` | Go |
| The audit actors (`writer`, `witness`, `indexer`, `reader`) | Go |
| Integrations | Go |
| The `sluis` CLI and `sluis agent` | Go |
| The cert-manager issuer | Go |
| Console | TypeScript |

Every Rust choice is **gated by its spike**, and the languages are fixed from the spikes before the first code of step 1 ([Roadmap](/roadmap/#spikes-before-code)): Connect for Rust over Unix sockets and a slim `entry` built in both languages decide `entry`; Spike S4 is done for YubiKey PIV (2026-10-31): `piv-go` holds the card exclusively, which is what one `key-holder` per node needs, while `ykcs11` reset the card when a second client appeared and cached the PIN in its own library, so `key-holder` is Go. Spike S5 is done (2026-10-31): the boards' memory fits with Go or with Rust actors. The FIPS spike with `aws-lc-rs` is done; with no FIPS build in v2.0 it no longer gates the AWS drivers, which stay Rust, and its result is kept for a later FIPS variant. When a Rust choice fails its spike, its fallback is chosen then from the written-down candidates ([ADR 0060](/decisions/0060-the-language-follows-the-executable/)).

There is no Rust JOSE driver; tokens are standard JWS/JWT, so any language verifies them against the published key set.

The command-line tool is one `sluis` binary with subcommands for operators and for users, including `sluis key request`, `install` and `status`. An actor can be written in either language, because every contract is ConnectRPC and every integration is out of process. The language choice is per executable; the contracts do not change.

## Shared kits

Parallel implementations of the same small kits, one per language, keep the languages interchangeable. Each is checked by the same conformance suite.

| Kit | What it is |
| --- | ---------- |
| ConnectRPC over Unix sockets | A server and client that read peer credentials (`SO_PEERCRED`) on every call and expose them to the call-graph interceptor |
| Config loader and validator | Loads the layered configuration (defaults, runtime, install file, environment overrides; the layering is proposed) and validates it against the generated JSON Schema, with strict unknown-field handling |
| CLI conventions | One command-line convention: `config check`, `config explain`, `doctor`, and a uniform `--help` |
| Conformance suite | Shared vectors and behavioural tests that every kit and driver must pass, whatever its language |

Configuration validation uses `santhosh-tekuri/jsonschema/v6` in Go, the `jsonschema` crate in Rust and `ajv` in TypeScript, with one shared set of conformance vectors. The schema is generated from the Pkl sources at build time; there is no Pkl at run time.

## The repository tree

```text
proto/          ConnectRPC contracts
schema/         Pkl sources and the generated JSON Schema
kits/
  go/  rust/  ts/  conformance/
actors/<actor>/
drivers/<driver>/
integrations/<integration>/
node/agent/
cli/sluis/
host/sluis-host/
console/
manifest/       permission manifests, one per actor
deploy/
  packages/  helm/  lambda/  iac/
tests/
  installations/  tutorials/
docs/  site/
```

| Directory | Holds |
| --------- | ----- |
| `proto/` | The contracts every other directory implements or calls. A contract change is reviewed here first |
| `schema/` | The Pkl sources that define the configuration shape, and the JSON Schema (draft 2020-12) generated from them. The schema is part of the public API |
| `kits/` | The shared kits above, one directory per language, plus the conformance suite |
| `actors/`, `drivers/`, `integrations/` | One directory per component, matching the layout under `/usr/libexec/sluis/` that the [packages](/architecture/processes/#packaging) install. The directory is called `actors` (agreed; it amends the layout, which first called it `roles`) |
| `node/agent/` | The node agent (`sluis-node`) |
| `cli/sluis/` | The one `sluis` binary, with subcommands for operators and users |
| `host/sluis-host/` | The SSH host service ([SSH](/integrations/ssh/)) |
| `console/` | The TypeScript console |
| `manifest/` | The permission manifests, one per actor. They generate the IAM policies, key policies, shared-driver ACLs, systemd units, network rules and the deny-matrix tests |
| `deploy/` | Packaging for each artifact: `.deb` and `.rpm` packages, Helm charts, Lambda archives, infrastructure as code |
| `tests/installations/`, `tests/tutorials/` | Integration tests that run on the four tested installations (local, boards, lambda and kubernetes), and the tutorials as tests |
| `docs/`, `site/` | This documentation and the site that builds it |

One version covers the whole repository. See [Distribution](/distribution/) for what is built from the tree.

## Board OS images live elsewhere

Operating system images for the single-board computers are built in a **separate public repository**, not here. The boards run the vendor's Ubuntu 22.04 for now, as a declared exception; building Ubuntu 26.04 and Rocky Linux 10.2 images is a backlog task.

Next: [Architecture](/architecture/) lists the pages, and [Runtimes](/runtimes/) shows where each binary runs.
