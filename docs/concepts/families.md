---
title: Families of actors
description: "The three families of sluis actors, what each may hold, how work actors reach data, keys and audit, how actors are named, and units, transports and fusion."
status: target
sidebar:
  order: 1
---

sluis is not one program. An **actor** is a program with one responsibility, and every actor belongs to one of three **families**: work, data or keys. The family says what the actor may hold. Work actors hold nothing at rest; only the data and keys families hold credentials, so an installation has a few credentials to protect, not one set per actor. The record is [ADR 0002](/decisions/0002-families-of-actors/).

What runs where, what each actor calls and which kinds it may touch is generated from the actor registry and the kind registry: see [Actors](/reference/actors/), [Kinds](/reference/kinds/) and [Rules](/reference/rules/). The actors and their call graph are on [Components](/architecture/components/).

## The three families

| Family | Actors | What the family may hold |
|---|---|---|
| **Work** | `entry`, `signin`, `console`, `exchange`, `policy`, `signer`, `kv`, `ca`, `ssh-ca` and the adapters (`adapter-github`, `adapter-slack`, `adapter-upstream-oidc`) | **Nothing at rest.** No storage, key or export credential |
| **Data** | `store@`, `exporter@` | The credentials of storage (store@) and of export destinations (exporter@) |
| **Keys** | `key@` | The card PIN on a board, the KMS permission on AWS |

A port is never shared between two families.

```mermaid
flowchart LR
  work["work actors"] -- "data port" --> store["store@"]
  work -- "keys port" --> key["key@"]
  work -- "emit port" --> audit["audit"]
  exp["exporter@"] -- "watch" --> store
  exp -- "open" --> key
  store -- "four ports" --> classes["config, secret,<br/>durable, temp"]
```

**Audit is not a family.** It is the product of [truvity/audit](https://github.com/truvity/audit), built on atoom's ports. Every sluis actor has an emit port that hands its audit records to audit's emitter, and audit's pieces hold their own credentials under audit's own rules ([Audit](/concepts/audit/)).

## Work actors

A work actor is a program with one responsibility: `entry` parses and routes, `signin` signs people in, `console` is the operator API, `exchange` exchanges tokens and enrols machines, `policy` decides, `signer` signs, `kv` keeps static secrets, `ca` and `ssh-ca` issue certificates, and an adapter serves one vendor. [Components](/architecture/components/#actors) has the table with what each calls, and the [actor registry](/reference/actors/) has the rest. The verifiers of proofs are not actors: they are verifiers inside `exchange` ([Identity](/concepts/identity/)).

`signer`, `policy`, `ca` and `ssh-ca` run per [boundary](/concepts/boundaries/). `entry`, which faces the internet, holds nothing and has no outbound network. It is written in a memory-safe language, parses only closed parameter lists, is fuzzed, loads no plugin and runs on a read-only file system. Where each actor listens, by runtime, is in [ADR 0003](/decisions/0003-units-transports-and-fusion/#listeners): on a board no actor has a TCP listener.

### The console

The browser holds no token: its session is an HttpOnly cookie that `signin` has key@ seal. `console` checks the operator's delegation and routes each write to the actor that owns the kind. The store refuses any other writer, and the console writes only two kinds itself, `Switch` and `LogLevelOverride` ([ADR 0002](/decisions/0002-families-of-actors/#the-console), [ADR 0027](/decisions/0027-the-console/#writes)). Its audit page is audit's, embedded.

### Why the signer and the policy are separate

In v1 the issuer is one function that serves sign-in, token exchange and minting, and touches 25 resources: too broad for least privilege. v2 splits it:

- `entry` parses and routes and holds nothing, so a bug in request parsing reaches no key, no secret and no stored value.
- `exchange` and `signin` own the protocol steps and ask `policy` before anything is issued.
- `signer` signs only what its signing policy allows, so a compromised `entry` or `exchange` cannot ask it for an arbitrary token.
- `policy` decides. It has no outbound network and writes nothing, so it cannot be made to read or send anything. Its output is a **sealed grant**: key@ seals it, and the store, `ca`, `signer`, key@ and the minters open it.

To obtain a credential, an attacker needs a grant that `policy` sealed **and** a holder that accepts it; to forge a token, `signer` and its signing policy must agree ([ADR 0007](/decisions/0007-policy-and-sealed-grants/#sealed-grants)).

```mermaid
flowchart LR
  user(["Caller"]) -->|"sign-in, exchange"| entry["entry<br/>holds nothing"]
  entry -->|"route"| xchg["exchange"]
  xchg -->|"may this subject?"| policy["policy<br/>no outbound network"]
  policy -->|"sealed grant"| xchg
  xchg -->|"sign claims"| signer["signer"]
  xchg -->|"grant + request"| minter["adapter or ca<br/>(minter)"]
```

## Credential holders

The holders are exactly three kinds of unit: store@ and exporter@ (data) and key@ (keys). No work actor is a holder, and no other unit of sluis is one.

| Holder | Credential at rest | What it does with it |
|---|---|---|
| `store@` | The credentials of its four storage ports: the config source, the secret source, the durable engine and the temp engine | Reads and writes items for callers whose rule allows it. It never seals or opens a value ([Data](/concepts/data/#the-store)) |
| `exporter@` | The credentials of its export destinations | Writes the exportable kinds to destinations outside sluis, and takes the backup ([Data](/concepts/data/#export)) |
| `key@` | The card PIN on a board, the KMS permission on AWS | Seals, opens, signs and makes keyed hashes ([Keys](/concepts/keys/)) |

- **A holder never shares a unit with a work actor,** and key@ never shares one with `entry`.
- **On Lambda every function has a role.** Only the holders' roles reach storage, keys or export destinations; a work function's role lets it invoke other functions and hand records to audit's transport.
- **A holder's compromise is bounded by what it holds.** A flaw in store@ yields ciphertext, hashes, plain values and the values of secret sources, but no plaintext of a sealed value. A flaw in key@ yields keys, but not the data they protect, since the data is in store@ ([threat model](/security/threat-model/)).

store@, exporter@ and key@ are thin hosts of blocks of [atoom](https://github.com/truvity/atoom): `state`, `export` and `seal` ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)). The block holds the mechanics; sluis supplies the rules and the placement.

## The foundation and the layers

The data and keys families, store@, exporter@ and key@, are the **foundation**: every installation runs them, and their binaries do not change with what runs above them. Above it sluis has a **core** that every installation runs and **add-ons** that bring their own actors, kinds and key purposes; no core actor calls an add-on ([ADR 0044](/decisions/0044-sluis-is-a-core-and-add-ons/)). A layer is a grouping, not a fourth family: each actor stays in one family ([ADR 0002](/decisions/0002-families-of-actors/#the-foundation-and-the-layers)).

## Ports of a work actor

A work actor has three ports in common, and the ports of its own job.

| Port | Goes to | Used for |
|---|---|---|
| **Data port** | store@ | Configuration, secrets and data, all as kinds through the one generic store service, with typed clients |
| **Keys port** | key@ | Seal, Open, Sign and Mac |
| **Emit port** | audit's emitter | Every flow is audited, so every actor has it |
| Ports of its job | The edge, a vendor, the web | An HTTP listener (`entry` only), provider APIs (adapters), outbound fetch |

An actor has no configuration loader, no secret source and no storage driver of its own, and it never sees a driver. A holder has ports of its own downward: store@ has four, one per storage class; exporter@ has one per destination; key@ has one for its card or KMS. A driver sits behind each of them. Drivers are [atoom's](https://github.com/truvity/atoom); the store and key rules decide who may call one ([ADR 0005](/decisions/0005-the-store/#drivers)). The ports are the same on every installation; what differs is the transport of each hop and the driver behind a holder's port.

## Naming

Every actor is named **`<actor>@<boundary>`**, and the name is generated from the actor registry. The [boundary](/concepts/boundaries/) is an environment such as `prod` or a customer boundary. The same name appears in every layer that enforces it, so a unit, a role, a socket and an audit field match without a lookup table:

| Layer | Example for `ca@prod` |
|---|---|
| systemd unit | `sluis-ca@prod.service` |
| Unix user | `sluis-ca-prod` |
| Socket | `/run/sluis/ca/prod.sock` |
| Lambda function and IAM role | `sluis-ca-prod` |
| Kubernetes pod and service account | `ca-prod` |
| Audit field | `actor=ca@prod` |
| SPIFFE ID | `spiffe://prod.example.com/sluis/ca` |

An adapter is named `adapter-<vendor>@<boundary>`, such as `adapter-github@prod`. One adapter runs for a vendor in a boundary, and connections tell the tenants apart ([ADR 0025](/decisions/0025-integrations/#adapters-and-connections)). In prose, `store@`, `key@` and `exporter@` mean the holder of the boundary under discussion.

## Units and transports

A **unit** is what an installation starts for one actor. A **transport** is how a call between two units travels. The steps and the checks are the same on every installation ([principle 19](/principles/#19-one-model-many-transports)); only the unit and the hop differ.

| Installation | The unit | The transport | The caller is known by |
|---|---|---|---|
| Boards, and the local installation | A systemd unit with its own Unix user (a container with a pinned uid locally) | A Unix socket | The peer uid |
| Lambda | A function, one per actor and one per holder, behind the Lambda Web Adapter | A Lambda invoke | The caller's IAM role |
| Kubernetes | A pod with its own service account, one per actor and per holder | In-cluster gRPC | A projected service-account token whose audience the callee checks, behind a NetworkPolicy |

A holder's unit contains its drivers: separate binaries that the platform starts, and that only the owner's unit can open. The call graph is declared once in the actor registry and enforced twice: by one interceptor in every process, and again by the transport. Where an actor runs is derived from the registry, and `sluis config check` refuses a combination it does not allow ([ADR 0003](/decisions/0003-units-transports-and-fusion/#units)).

Every contract is Protobuf served over ConnectRPC, even when caller and callee share a process, so moving a component into its own unit changes configuration and not code ([ADR 0002](/decisions/0002-families-of-actors/#contracts)).

### The fusion rule

On a small machine memory is tight, so an installation may **fuse** work actors into one unit. Fusion is an explicit choice, never the default:

> A holder never shares a unit with a work actor, key@ never shares one with `entry`, and `entry` never shares one with an actor that signs.

The store applies the union of the fused actors' rules to the unit, so fusion never grants what no member was allowed. `ca` and `ssh-ca` stay separate. `sluis config check` refuses a configuration that breaks the rule, and the build runs the same check ([ADR 0003](/decisions/0003-units-transports-and-fusion/#fusion)). On Lambda no function links store@ into a work actor.

### Cells

A **cell** is one replica of each actor, scheduled together on one node: on Kubernetes by pod affinity, on a board the board itself. Services prefer the local cell, so a call to store@ or key@ stays on the node when a replica there is ready ([ADR 0003](/decisions/0003-units-transports-and-fusion/#cells)).

## Platform pieces

A platform piece is a program an installation runs that belongs to no family: the edge connector (`cloudflared` on boards, the pinned Lambda Web Adapter on Lambda, an edge relay container on Kubernetes, each behind the Cloudflare edge), the host's card service, the engines (rqlite, DynamoDB, PostgreSQL through CloudNativePG) and the node agent `sluis-node`. The edge connector holds no key. The card is reached only through key@, and the engines only through store@'s drivers. `sluis-node` issues each unit a short-lived workload token and asks key@ to sign it ([Identity](/concepts/identity/#how-an-actor-reaches-a-cloud)). See [Runtimes](/runtimes/) for each runtime and the four tested installations.
