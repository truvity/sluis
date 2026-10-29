---
title: Concepts
description: "The ideas behind sluis v2 with a map: actors, ports and drivers, integrations, keys, identity, configuration, data and audit."
status: target
sidebar:
  order: 0
---

The concepts explain the model that the rest of the documentation relies on. Read them in order, or jump to the one you need from the map.

```mermaid
flowchart TB
  env["Environment<br/>bare-metal, serverless, kubernetes"]
  inst["Installation<br/>environment + nodes +<br/>dependencies + drivers"]
  manifest["Permission manifest<br/>one per actor"]
  actors["Actors<br/>entry, signer, policy, ca,<br/>quota, audit actors"]
  drivers["Ports and drivers<br/>technology behind a port"]
  integ["Integration actors<br/>six kinds, called by core"]
  keys["Keys<br/>mode by preset"]
  ident["Identity<br/>workload tokens, cloud exchange"]
  config["Configuration<br/>Pkl at build, JSON Schema at runtime"]
  data["Data<br/>one writer per record kind"]
  audit["Audit<br/>transport, batches, notary"]

  env -->|"part of"| inst
  inst -->|"selects"| drivers
  inst -->|"selects"| keys
  config -->|"describes"| inst
  config -->|"generates"| manifest
  manifest -->|"limits"| actors
  manifest -->|"generates ACLs for"| drivers
  actors -->|"use ports of"| drivers
  actors -->|"call"| integ
  drivers -->|"hold"| keys
  drivers -->|"hold"| data
  keys -->|"sign"| ident
  actors -->|"get identity from"| ident
  actors -->|"every success"| audit
  audit -->|"stored in"| data
  audit -->|"sealed with"| keys
```

## The vocabulary

| Term | Meaning | Example |
|---|---|---|
| **Actor** | A process with one responsibility. It declares the ports it requires. | `signer` requires keys, state, transport |
| **Port** | An abstract interface, with capability flags | `blob` (lock mode: none, removable, compliance), `state` (cas) |
| **Driver** | An implementation of a port. It declares its requirements. | `s3`, `rqlite`, `sqs` |
| **Environment** | Only the runtime kind: how processes are started and what they can rely on | bare-metal, serverless, kubernetes |
| **Nodes** | How many nodes an installation has: 1, N, or elastic (serverless) | |
| **Dependency** | Something outside sluis that a driver needs | an AWS account, a Cloudflare account, a YubiKey, a NATS service |
| **Installation** | One concrete deployment: environment + nodes + available dependencies + one driver per port | "bare-metal, 3 nodes, AWS account" |
| **Preset** | A named, tested installation, used in docs, tutorials and the test matrix | T1 Single board |
| **Stage** | An install boundary such as development, staging or production. A boundary is a stage or a regulated entity. | production |

## The pages

| Page | The question it answers |
|---|---|
| [Actors](/concepts/actors/) | Which processes make up sluis, what does each hold and need, and why are the signer and the policy separate? |
| [Ports and drivers](/concepts/drivers/) | How does an actor reach keys, state and storage without knowing the technology, which driver fits which environment, and who may use which driver? |
| [Integrations](/concepts/integrations/) | How do vendors plug in, and why can they never decide? |
| [Keys](/concepts/keys/) | Which key backs which purpose in each preset, and what algorithm? |
| [Identity](/concepts/identity/) | How does a process on a node prove who it is to a cloud? |
| [Configuration](/concepts/configuration/) | How is an installation configured, validated and explained? |
| [Data](/concepts/data/) | Who may write which record, and how are writes made safe? |
| [Audit](/concepts/audit/) | What is recorded, and why can the trail be trusted? |

For the same system drawn as boxes and sockets, see [Architecture](/architecture/), or open the [architecture explorer](/architecture/explorer/).
