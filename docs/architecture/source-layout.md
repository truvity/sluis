---
title: Source layout
description: Which language each sluis component is written in, the shared kits every language implements, and the layout of the repository tree.
status: target
sidebar:
  order: 6
---

:::note[Proposed]
The languages and the layout below are the design as proposed, awaiting the maintainer's review. All language choices below are proposed except the cloud drivers and the `policy` actor, which are decided.
:::

## Languages

| Component | Language | Status |
| --------- | -------- | ------ |
| Actors other than `policy` (`entry`, `signer`, `ca`, `quota`, `writer`, `witness`, `indexer`, `reader`) | Go | Proposed |
| Most drivers (state, index, blob, transport, lease, keys for KMS and YubiKey) | Go. The YubiKey driver needs cgo against pcsc-lite, so it builds in a native per-architecture CI job | Proposed |
| Integrations | Go | Proposed |
| Cloud drivers (AWS, GCP, Azure) | Rust | Decided |
| `policy` actor and its Cedar engine | Rust, with the `cedar-policy` crate | Decided |
| Console | TypeScript | Proposed |
| `sluis` CLI (one `sluis` binary, with subcommands for operators and users) | Go | Proposed |

Rust is used for the aws, gcp and azure cloud drivers. There is no Rust JOSE driver; tokens are standard JWS/JWT, so any language verifies them against the published key set.

The `policy` actor is written in Rust with the `cedar-policy` crate, and Cedar is the policy language.

The command-line tool is one `sluis` binary with subcommands for operators and for users. An actor can be written in any language, because every contract is ConnectRPC and every integration is out of process. The language choice is per component; the contracts do not change.

## Shared kits

Parallel implementations of the same small kits, one per language, keep the languages interchangeable. Each is checked by the same conformance suite.

| Kit | What it is |
| --- | ---------- |
| ConnectRPC over Unix sockets | A server and client that read peer credentials (`SO_PEERCRED`) on every call and expose them to the call-graph interceptor |
| Config loader and validator | Loads the layered configuration (defaults, runtime, installation, install, environment overrides; the layering is proposed) and validates it against the generated JSON Schema, with strict unknown-field handling |
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
| `console/` | The TypeScript console |
| `manifest/` | The permission manifests, one per actor. They generate the IAM policies, key policies, shared-driver ACLs, systemd units, network rules and the deny-matrix tests |
| `deploy/` | Packaging for each artifact: `.deb` and `.rpm` packages, Helm charts, Lambda archives, infrastructure as code |
| `tests/installations/`, `tests/tutorials/` | Integration tests that run on the four tested installations (local, boards, lambda and kubernetes), and the tutorials as tests |
| `docs/`, `site/` | This documentation and the site that builds it |

One version covers the whole repository. See [Distribution](/distribution/) for what is built from the tree.

## Board OS images live elsewhere

Operating system images for the single-board computers are built in a **separate public repository**, not here. The boards run the vendor's Ubuntu 22.04 for now, as a declared exception; building Ubuntu 26.04 and Rocky Linux 10.2 images is a backlog task.

Next: [Architecture](/architecture/) lists the pages, and [Runtimes](/runtimes/) shows where each binary runs.
