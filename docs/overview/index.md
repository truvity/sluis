---
title: Overview
description: What sluis is, who it is for, what it does and what it leaves out, and how v2 relates to v1. Start here, then follow the map of the documentation.
status: target
sidebar:
  order: 1
---

sluis is an open-source identity and access broker for people, robots and external systems. It sits between the identity providers that already know who someone is and the systems that need a credential before they let that someone in.

## Who it is for

sluis is for engineers who run the identity layer of a small organisation, a platform team or a self-hosted lab, and who want it to be small, auditable and cheap to keep running.

- **Operators** install it from signed packages on one single-board computer, three of them, a pair of AWS instances, or as AWS Lambda functions. The same roles and the same configuration run on every one of these [shapes](/shapes/local/).
- **Platform and security engineers** write the policy that says who may get which credential, and read the audit trail that says who did.
- **Integrators** extend it with new sign-in providers, directory sources and credential minters, as separate processes, in any language that speaks ConnectRPC.

## What it does

- It **federates sign-in** from upstream identity providers, so people use the accounts they already have.
- It **issues OIDC tokens** and **exchanges tokens** (RFC 8693): a robot or a CI job presents a token from the system it runs on and receives a sluis token for the system it wants to reach.
- It **mints short-lived downstream credentials**: cloud API tokens, GitHub installation tokens and AWS credentials. Nothing it hands out is meant to outlive the task.
- It **reconciles directory facts** from workspace directories, GitHub teams and chat workspaces, so that policy can say "members of this team" without sluis owning the team.
- It **writes a tamper-evident audit trail**. Every success is recorded, sealed into batches and chained by a notary that nobody else can impersonate. See [Audit](/concepts/audit/).

Behind these sits one idea: the part of sluis that holds a key is small and cannot be talked into anything, and the part that decides is separate from the part that signs. See [Roles](/concepts/roles/).

## What it does not do

- **It does not store secrets.** There is no key-value store, no transit engine and no lease system for other people's secrets. Use a secrets manager for that.
- **It does not write Kubernetes objects.** sluis exports what a cluster needs and the cluster pulls it. cert-manager and trust-manager remain clients of sluis; sluis does not create their resources.

These two are agreed non-goals. They keep the blast radius of a compromised sluis process to identity, not to everything a secrets manager would also hold.

## v1 and v2

v1 is the sluis that runs today. It is a set of many AWS Lambda functions. v2 is a from-scratch rewrite on the `main` branch that shares no history with v1.

- v2 has to reach **parity with v1** as it is at cutover. The [features page](/features/) lists what that means feature by feature.
- Once at parity, v2 adds features that v1 does not have, device login among them.
- v2 is not tied to Lambda. The roles, drivers and configuration that run as Lambda functions also run as systemd units on a single board, and as pods on Kubernetes.
- The two lines coexist. v1 keeps its agreed roadmap and fixes until the maintainer's own production deployments run v2. The reasoning is in [ADR 0001](/decisions/) and the plan on the [roadmap](/roadmap/). The documentation of v1 is at [/v1/](/v1/).

## How v2 is organised

Three ideas hold the design together:

1. **One permission manifest per process role.** It is derived from what each use case touches and what each component owns, and it generates the IAM policies, key policies, driver access lists, systemd units, network rules and the tests that prove a role cannot do what its manifest does not say. See [Architecture](/architecture/).
2. **Ports and drivers.** A role asks for a capability (keys, state, blob) through a port and does not know which technology is behind it. The technology is a driver, which is a separate service started by the platform. See [Drivers](/concepts/drivers/).
3. **Integrations return facts or carry out effects that core has already authorized. They never decide.** See [Integrations](/concepts/integrations/).

## Map of the documentation

| Section | What you find there | Start with |
|---|---|---|
| [Concepts](/concepts/) | The model: roles, drivers, integrations, keys, identity, configuration, data and audit. | [Concepts map](/concepts/) |
| [Architecture](/architecture/) | The same system at three zoom levels: context, components and processes, with the deployment and listener details and two interactive explorers. | [Architecture](/architecture/) |
| [Shapes](/shapes/) | How sluis is deployed: local, single node, cluster, AWS EC2, AWS Lambda, Kubernetes. | [Single node](/shapes/single-node/) |
| [Tutorials](/tutorials/) | Step-by-step installs that run in CI on the supported shapes. | [Single-node install](/tutorials/single-node-install/) |
| [Distribution](/distribution/) | Packages, release signing and supported platforms. | [Packages](/distribution/packages/) |
| [Open decisions](/open-decisions/) | The points of the design the maintainer has not decided, each with options and the current lean. | [Open decisions](/open-decisions/) |

Every page says how settled it is. A badge beside the title shows whether it describes a design that is not built yet, something partly built or something shipped. Inside a page, plain prose is agreed, a **Proposed** note is a design awaiting the maintainer, and an **Open decision** note is undecided. The rules are in [Contributing](/contributing/).
