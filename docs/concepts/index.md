---
title: Concepts
description: The ideas behind sluis v2 and how they relate, with a map. A page each for roles, drivers, integrations, keys, identity, configuration, data and audit.
status: target
sidebar:
  order: 0
---

The concepts explain the model that the rest of the documentation relies on. Read them in order, or jump to the one you need from the map.

```mermaid
flowchart TB
  shape["Shape<br/>local, single node, cluster,<br/>EC2, Lambda, Kubernetes"]
  manifest["Permission manifest<br/>one per role"]
  roles["Roles<br/>central, signer, policy, ca,<br/>notary, audit, quota"]
  drivers["Drivers<br/>technology behind a port"]
  integ["Integrations<br/>six roles, called by core"]
  keys["Keys<br/>mode by shape"]
  ident["Identity<br/>workload tokens, cloud exchange"]
  config["Configuration<br/>Pkl at build, JSON Schema at runtime"]
  data["Data<br/>one writer per record kind"]
  audit["Audit<br/>outbox, batches, notary"]

  shape -->|"selects"| drivers
  shape -->|"selects"| keys
  config -->|"chooses"| shape
  config -->|"generates"| manifest
  manifest -->|"limits"| roles
  manifest -->|"generates ACLs for"| drivers
  roles -->|"use ports of"| drivers
  roles -->|"call"| integ
  drivers -->|"hold"| keys
  drivers -->|"hold"| data
  keys -->|"sign"| ident
  roles -->|"get identity from"| ident
  roles -->|"every success"| audit
  audit -->|"stored in"| data
  audit -->|"sealed with"| keys
```

## The pages

| Page | The question it answers |
|---|---|
| [Roles](/concepts/roles/) | Which processes make up sluis, what does each hold, and why are the signer and the policy separate? |
| [Drivers](/concepts/drivers/) | How does a role reach keys, state and storage without knowing the technology, and who may use which driver? |
| [Integrations](/concepts/integrations/) | How do vendors plug in, and why can they never decide? |
| [Keys](/concepts/keys/) | Which key backs which purpose on each shape, and what algorithm? |
| [Identity](/concepts/identity/) | How does a process on a node prove who it is to a cloud? |
| [Configuration](/concepts/configuration/) | How is sluis configured, validated and explained? |
| [Data](/concepts/data/) | Who may write which record, and how are writes made safe? |
| [Audit](/concepts/audit/) | What is recorded, and why can the trail be trusted? |

For the same system drawn as boxes and sockets, see [Architecture](/architecture/).
