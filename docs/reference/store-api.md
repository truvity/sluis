---
title: Store API
description: "What store@ adds as the host of atoom's state block: the generic service, its four ports, the rules it enforces, its refusals and sealed values."
status: target
sidebar:
  order: 5
---

Every data call of a work actor goes to store@ over one generic service ([0005](/decisions/0005-the-store/)). The mechanics are not sluis's. They are atoom's: the [store port](https://github.com/truvity/atoom/blob/master/store/docs/store-port.md) (items, versions, time to live, indexes, conditional writes, the change log) and the `state` block (addresses, the kind registry, signed configuration, layers, watch). This page says what sluis adds as the host. Who may write which kind is the generated [Rules](/reference/rules/) page, and the kinds are on [Kinds](/reference/kinds/).

## Where each part lives

| Part | Home |
|---|---|
| Items, versions, expiry, indexes, conditional writes, the change log and its epoch, the durability a driver declares | atoom's [store port](https://github.com/truvity/atoom/blob/master/store/docs/store-port.md) |
| Named values from a config or a secret source | atoom's [source port](https://github.com/truvity/atoom/blob/master/source/docs/source-port.md) |
| Addresses, the kind registry that a product extends, signed configuration and its layers, layered reads, watch and its catching-up claim, the bindings per storage class | atoom's `state` block ([record 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)). Planned, step B7 of atoom's [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md). Open: the block has no page yet, so store@ implements these in `actors/store` and the sections below are the only description of the service until it does |
| Sealing and the envelope of a sealed value | atoom's `seal` block (planned, step B4). Open: no page yet. The purposes sluis declares are on [Keys](/reference/keys/) |
| The blob contract | Not store@'s. It is atoom's [blob port](https://github.com/truvity/atoom/blob/master/blob/docs/blob-port.md), used by audit |

## The service

`sluis.store.v2alpha1.StoreService` ([`store.proto`](https://github.com/truvity/sluis/blob/main/proto/sluis/store/v2alpha1/store.proto)) is served by store@ on a Unix socket, an invoke or a pod address, depending on the runtime ([0003](/decisions/0003-units-transports-and-fusion/#transports)).

| Method | What it does |
|---|---|
| `Get` | One item by address. A reader that holds a version may ask `if_none_match` and get "unchanged", which is how a reader re-reads cheaply on Lambda, where nothing is pushed. A read of a layered kind returns its provenance: for each field, the layer that set it and the version |
| `List` | The items of a kind in a tenant, or those that match a declared index. There is no query language: a new question is a new declared index, reviewed with the kind ([0004](/decisions/0004-the-data-model/)) |
| `Write` | At most 64 operations by one actor to its own kinds, all or none: `CREATE`, `UPDATE` and `DELETE` at a version, and `APPLY` for a writer that owns the whole item and replays idempotently. A flow over several items is idempotent steps with a recovery marker, never an interactive transaction |
| `Watch` | The changes of a kind from a resume point ([Watch](#watch)) |
| `GetSignedConfig` | The signed document of a config kind, unread ([below](#getsignedconfig)) |

- **The caller is learned from the transport,** never from a field: a peer uid, a Lambda role, a service-account token or a SPIFFE identity. The boundary is the store@ the client is connected to, so a request names none.
- **A kind is named, never a method.** A new kind adds no method and no generated code on the wire.
- **Actors use typed clients,** `store.Client[T Kind]` in Go and `Store<K: Kind>` in Rust, generated from the kind's Protobuf message ([0005](/decisions/0005-the-store/#typed-clients)). A client builds the address, and seals and opens a sealed kind at key@ ([Seal and open](/concepts/keys/#seal-and-open)). Both are built, and both pass one conformance suite ([0012](/decisions/0012-the-language-follows-the-executable/)).
- **Every call carries a deadline and a correlation id,** as every sluis call does ([Contracts and errors](/reference/contracts/#every-call)).

## The four ports downward

store@ has one port per storage class, and it is the only holder of the credentials behind them ([0005](/decisions/0005-the-store/#the-store-api)).

| Port | Class | atoom's contract | Drivers sluis selects |
|---|---|---|---|
| Config source | config | source port, read-only | `file`; `appconfig` (planned) |
| Secret source | secret | source port, read-only | A systemd credential on boards and locally; AWS SSM (planned) |
| Durable | durable | store port | `rqlite` on boards and locally, `dynamodb` on AWS, `postgres` on Kubernetes (planned) |
| Temp | temp | store port | The same three |

- **The drivers are atoom's.** sluis builds none and pins atoom's by release. Where a driver must run behind sluis's socket, peer credentials and command line, a thin host in `drivers/store/` registers atoom's library: the hosts for `file`, `credential` and `rqlite` are built. The AWS-backed drivers are Go libraries that a Lambda function mounts or carries as an extension of the function.
- **A driver has no owner.** Any actor's host may run an atoom driver, and the rules decide who may call it. For these four ports the rule is that only store@ opens the driver sockets, because it is the only holder of storage credentials; a work actor never sees one.
- **The platform starts a driver.** On a board it is a systemd service beside the store's unit, on Lambda an extension of the function (every function runs behind the Web Adapter, [0011](/decisions/0011-runtimes-and-installations/)), and on Kubernetes a native sidecar container ([0003](/decisions/0003-units-transports-and-fusion/)).
- **A driver declares its durability,** `strict` or `lossy`, in atoom's `Capabilities` call (planned, step B3). sluis refuses a `lossy` driver for the durable class. Every driver it selects declares `strict`, for the temp class too, because a temp kind holds rotation state, leases and replay guards. Until the field exists the choice follows each driver's documentation, and `sluis config check` refuses a driver that does not fit the runtime, such as `rqlite` on serverless.
- **The durable and temp ports may sit on one store.** They stay two ports because the classes differ in backup and in time to live ([Bootstrap file](/reference/bootstrap-file/#the-four-destinations)).

## Rules

store@ plugs sluis's rules into the block's rules hook. It checks them on every call, before it touches data, and denies by default ([0005](/decisions/0005-the-store/#rules)).

- **A rule is one line of the kind registry:** an actor may use a verb on a kind. A rule may limit an actor to ids under its own name, and may allow a watch (`watched_by`).
- **On every write it also checks** the schema of the kind, a TTL within the kind's maximum, the version, the bounds and the direction rule of a layered kind, the links the registry says are checked at write, and that the caller is the kind's one writer or the actor named in the id of a self-scoped kind.
- **A write to a tenant-scoped address carries a grant** sealed for that tenant. store@ has key@ open it and refuses the write unless it names the address's tenant and allows the verb. key@'s own writes of `DataKeyWrap` and `KeyRevocation` carry none ([0007](/decisions/0007-policy-and-sealed-grants/#two-independent-checks), [0041](/decisions/0041-a-compromised-key-is-revoked-then-replaced/)).
- **While `MaintenanceMode` is set, every write is refused** except those of the restore command ([0010](/decisions/0010-backup-and-recovery/#restore)).
- **A `Lease` carries a typed value** with one field the store reads, `expires_at`, so that store@ holds it to the kind's maximum (10 minutes; 30 seconds by default). Leases and their fencing token are atoom's. The typed message is planned; until then the value is opaque and store@ checks only the TTL asked for.

## Address

The address of an item is `<boundary>/<tenant | ->/<kind>/<id>` ([0004](/decisions/0004-the-data-model/#the-address)). Its encoding belongs to the `state` block. Until the block has a page, store@ and both kits follow these rules, held to one set of vectors ([`store-address-vectors.json`](https://github.com/truvity/sluis/blob/main/kits/conformance/kit/store-address-vectors.json)):

- a part is 1 to 128 characters of lower-case letters, digits, `-`, `_` and `.`, never `.` or `..`, and the whole address is at most 300 bytes;
- the tenant is `-` when the kind has none; the kind is the kebab-case form of its PascalCase name;
- an id has one or more parts separated by `/`, and never holds personal data: it is a random value (128 bits, base32) or a keyed hash that key@'s `Mac` makes ([0006](/decisions/0006-keys-and-sealing/#sign-and-mac)).

## Watch

The change log, its epoch, the resume point and the catching-up claim of a stream are the store port's and the `state` block's. What sluis decides:

- **Who may watch.** A watch is judged as a read is: the actor needs a `watched_by` rule on the kind, a tenant-scoped kind is watched under a tenant, and a kind that nobody may watch has no change log, so a watch over it is refused (`invalid_argument`, `address`). A read needs no grant today, so a watch needs none.
- **At most 16 watches per caller.** The 17th is refused with `resource_exhausted` and the reason `watch_limit`.
- **A config kind has no change log.** Its watch starts with the snapshot in force and then sends the changes of each swap, with no sequence to dedupe by.
- **A reader that decides authority** treats its state as unconfirmed until the stream says it is caught up, and trusts it only while a claim younger than 45 seconds stands. key@ does so for `KeyRevocation` ([REQ-DATA-123](/requirements/data/#REQ-DATA-123)).
- **A `List` that decides authority asks to be linearizable.** The default `List` is read from the leader without a quorum confirmation, the opposite of `Get`. A store that cannot give the level it was asked for refuses the call (`unavailable`) and never answers a weaker one.

## GetSignedConfig

It returns the signed document in force of a config kind, as the config source gave it to store@ and as store@ verified it ([Signed config](/reference/signed-config/)). The point is that a reader which must not rely on store@ for what a document says verifies it itself, under the publisher keys of its own copy of the bootstrap file. key@ does so for the `KeyPlan` and the `Boundary` ([0006](/decisions/0006-keys-and-sealing/#who-may-receive-a-wrap)).

- **It is a read of the kind.** Only the readers of the kind in the registry may call it. A kind that is not a config kind is `invalid_argument` (`address`, path `kind`), and a kind with no loaded document is `unavailable` (`engine`).
- **A self-scoped kind has no signed document for any caller** (`tunables`, `log-settings`, `integration`): the document holds every actor's items and an actor may read its own only, so the call is `permission_denied` (`rule`).
- **The document is at most 3 MiB.** It adds no edge to the call graph ([REQ-DATA-099](/requirements/data/#REQ-DATA-099)).

## Sealed values

A sealed kind's `value` is the envelope that key@ made. store@ validates its header and size and never opens it ([0005](/decisions/0005-the-store/#the-store-api)).

- **It checks** that the version is known, that the purpose is `data` (the purpose of every sealed kind but one) or `credential` for `StaffPasskey` and no other kind, and that the length reaches the minimum. A value that fails is `invalid_argument` with the reason `schema`.
- **The index entries of a sealed kind are keyed hashes or dates** that the writer computes, because store@ cannot read the value.
- **The layout of the envelope, its authenticated data and the test vector are the `seal` block's.** The purposes are sluis's: `data`, `evidence`, `cookie`, `grant`, `chain` and `credential` are declared to it ([Keys](/concepts/keys/#data-keys)).
- **A hashed kind has no envelope,** and a plain kind is kept as it is. A value from a secret source is neither: it passes through store@ in memory.

## Refusals

A refusal is a Connect error with the `Refusal` detail and a reason that names the rule ([Contracts and errors](/reference/contracts/#errors)). Every refusal of store@ is audited, on every method and for every reason below, as `store.refused`: it names the calling actor, the kind, the verb and the reason, and never a value of the request. The fact is best effort and counted when it is lost ([audit](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md)).

| Code | Reason | When |
|---|---|---|
| `invalid_argument` | `address`, `schema`, `ttl`, `index`, `batch` | The address does not fit the encoding, or a watch names a kind with no change log; the value fails the kind's schema; a TTL is above the kind's maximum; an index entry is undeclared; a batch is too large |
| `permission_denied` | `rule`, `writer`, `grant` | No rule allows this actor, kind and verb; the caller is not the kind's writer or the actor named in the id; the grant is missing, for another tenant or not allowing the verb |
| `not_found` | `absent` | No such item, or an expired one |
| `already_exists` | `taken` | A `CREATE` found the address in use |
| `aborted` | `version` | An `UPDATE` or `DELETE` named a version that is no longer current |
| `failed_precondition` | `bound`, `direction`, `link`, `maintenance`, `resume_point_expired` | A bound or the direction rule refuses the value; a link does not resolve; `MaintenanceMode` is set; the resume point is gone, not reached, or in another log |
| `resource_exhausted` | `watch_limit` | The caller already holds 16 watches |
| `unavailable` | `engine` | An engine or a source is down; store@ fails closed |

Refusals are counted past a rate, so that a caller with a socket cannot fill the shared spool and stop every signature: the first ten of each minute are facts one by one, and the rest are one summary fact ([0005](/decisions/0005-the-store/#rules)).
