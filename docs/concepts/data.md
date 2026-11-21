---
title: Data
description: "How sluis keeps data: kinds with an address, a class, a treatment, a profile and one writer; configuration, secrets, KV, export, backup and restore."
status: target
sidebar:
  order: 2
---

Everything sluis keeps is a **kind**: a typed resource declared once in the kind registry. Each kind has an address, one storage class, a treatment, a profile and exactly one writer. The registry is the single source of the store's rules, the backup list, the deny matrix and the generated pages, so the security of the design rests on who may change which kind and who can read it. The records are [ADR 0004](/decisions/0004-the-data-model/) for the model and [ADR 0005](/decisions/0005-the-store/) for the store; the kinds themselves are on the generated [Kinds](/reference/kinds/) page and their rules on [Rules](/reference/rules/).

The mechanics are atoom's. The registry, the store and the signed configuration are its `state` block (planned) over its [store](https://github.com/truvity/atoom/blob/master/store/docs/store-port.md) and [source](https://github.com/truvity/atoom/blob/master/source/docs/source-port.md) ports; any product extends the registry, and audit declares its own kinds in it. This page explains what sluis declares and decides.

## Kinds

A kind names a Protobuf schema, a scope, one storage class, a treatment, a profile, its layers or links, its declared indexes, its rules (actor, kind and verb) and its one writer. A kind that is not in the registry cannot be read or written. Adding one is a reviewed change that names its consumer ([principle 8](/principles/#8-nothing-without-a-consumer)), and a new question about the data is a new declared index, not a query ([ADR 0004](/decisions/0004-the-data-model/#kinds)).

## The address

The address of an item is **`<boundary>/<tenant | ->/<kind>/<id>`**.

| Address | Kind | Class |
|---|---|---|
| `prod/-/front-host/default` | `FrontHost` | config |
| `prod/acme/tenant-client/portal` | `TenantClient` | durable |
| `prod/acme/session-chain/<id>` | `SessionChain` | temp |
| `prod/-/lease/adapter-github/sync` | `Lease` | temp |
| `prod/acme/data-key-wrap/data/1/board-a` | `DataKeyWrap` | durable |

- **Only two scope dimensions exist:** the [boundary](/concepts/boundaries/), always, and the [tenant](/concepts/boundaries/#tenants), only for durable and temp kinds. Config and secret kinds never have a tenant; a tenant never appears in config.
- **An installation is not a scope.** A setting that holds for a whole installation is one signed document that infrastructure code writes, with the same content, into every boundary's config source. No boundary reads another's.
- **Actor, client and limit are parts of the id,** never scopes.
- **An id never contains personal data:** it is a random value, or a keyed hash that key@ makes. A person is found by an opaque subject, never by an e-mail address.
- **A sealed value is bound to its full address,** so a value moved to another item, kind or tenant no longer opens ([ADR 0004](/decisions/0004-the-data-model/#the-address)).

## Storage classes

Every kind is in exactly one **storage class**, and where a thing is written decides its class ([principle 17](/principles/#17-where-a-thing-is-written-decides-its-class)).

| Class | Access | Where it lives | Written by | Backed up |
|---|---|---|---|---|
| **config** | Read-only, signed | A file, a ConfigMap or AWS AppConfig | Infrastructure code or the publishing pipeline, under review. sluis never writes config | No: the reviewed source is the copy |
| **secret** | Read-only | A systemd credential or AWS SSM | Infrastructure code | No |
| **durable** | Read-write, must survive | The durable engine behind store@ | One actor, through the store | Yes, when the kind is authoritative |
| **temp** | Read-write, may be lost | The temp engine behind store@ | One actor, through the store | Never |

- **Durable data must survive.** A kind that nothing else can recreate is authoritative, and only authoritative kinds are backed up.
- **Temp data may be lost,** at the price of a sign-in or a retry, and has a mandatory time to live with a built-in default and maximum. A fact that a re-sync rebuilds is temp; the directory facts keep 24 hours by default and 7 days at most.
- **Built-in is not a class.** It is what the binary carries: baseline and predefined policies, hard caps, default tunables, the supported algorithms, the kind registry itself. It changes only with a release and is the lowest layer of every layered kind ([ADR 0004](/decisions/0004-the-data-model/#built-in)).
- **A secret from a secret source is not a secret that sluis generates.** A secret that sluis generates or receives, such as a client secret or a provider token, is ordinary sealed or hashed durable data ([Treatment](#treatment)).
- **What is not data:** keys, opened data keys, sealed grants in flight, audit records and the audit index, the bootstrap file, the credentials of the holders, minted credentials, backup archives and exported copies ([ADR 0004](/decisions/0004-the-data-model/#what-is-not-data)).

## Treatment

Every kind has one treatment. **Sealed** is the default.

| Treatment | For | The store keeps | Examples |
|---|---|---|---|
| **hashed** | A compare-only secret, verified and never read back | The hash | The refresh-token hash of a `SessionChain`, `AuthCode`, `ClientSecret` |
| **sealed** | A value that is read back: a provider token, personal data, free text | The ciphertext | `Person`, `GitHubApp` |
| **plain** | A value that is neither secret nor personal | The value as it is | `Switch`, `MinterTarget` |

- **Treatment follows the data.** A kind that holds personal data or free text is sealed, or hashed when it is compare-only. Plain is only for values that are neither secret nor personal.
- **The store never seals or opens.** To write a sealed value, the actor asks key@ to seal it and then writes; to read, it asks key@ to open it. The plaintext exists only in that actor, for that call ([Keys](/concepts/keys/#seal-and-open)).
- **An exportable generated secret is stored twice:** a hash to verify it and a sealed copy to export it.
- **Audit redaction and the telemetry allowlist come from treatment:** a sealed or hashed value never appears in an audit record, a log line or a span.

## Profiles, layers and the direction rule

A **profile** says which layers may set a kind's value. It classifies settings, not protocol state.

| Profile | Layers | Written at run time by | Examples |
|---|---|---|---|
| **Fixed** | Built-in and config | Nobody. A change is a reviewed, signed document | Installation shape, trust, tunables, login domains |
| **Operable** | Built-in and config, and an operator at run time | An operator, routed to the owning actor. The kind may be boundary- or tenant-scoped | Log levels and their overrides, switches, holds |
| **Tenant-adjustable** | Built-in and config, and the tenant | A tenant admin, or a product for its tenant, inside bounds declared in config, with a grant sealed for that tenant | Policy links, token lifetimes, clients, branding |

The layers of a setting, lowest first, are built-in, config, operator and tenant. All four apply to policies only; every other kind uses a subset. The **direction rule** governs them ([principle 12](/principles/#12-a-loosening-is-temporary-a-tightening-is-durable)): a higher layer tightens freely; it loosens only as far as the layer below allows; a run-time loosening carries an expiry. The store checks the rule when a value is written, composes the layers on read, and returns the effective value with its **provenance**: which layer set each part ([ADR 0004](/decisions/0004-the-data-model/#layers-and-the-direction-rule)).

- **A tightening is durable and takes effect at once:** a revocation, a shorter lifetime, a paused job.
- **A loosening made at run time expires.** It is a temp kind with a built-in default and maximum, such as a debug log level (1 hour, at most 24) or a break-glass policy link.
- **A loosening is more permissive than what the layer below grants.** A link inside a client template is within the bound, so it is durable and needs no expiry. Branding has no looser or tighter direction, so it is exempt: the most specific layer wins, bounded by config.
- **A restore never loosens what was tightened after the archive was taken** ([Backup and restore](#backup-and-restore)).

## Layered and linked kinds

A **layered kind** is a setting split into one kind per layer, each with its own class and writer. The store composes the layers by a **combine rule** into one effective value.

| Group | Combine rule |
|---|---|
| Policies (built-in, `Policy`, links of the operator, `PolicyLink` of the tenant) | Union; a Cedar `forbid` anywhere wins |
| Token lifetimes (`TokenLifetime`, `TokenLifetimeTenant`) | The shortest wins |
| Sign-in methods | Intersection |
| Log settings (`LogSettings`, `LogLevelOverride`) | The unexpired override wins |
| Branding | The most specific wins |

**Linked kinds** refer to each other by id, each in one class, and the store does not join them. A chat app is the worked example:

| Kind | Class | Scope | Writer | What it holds |
|---|---|---|---|---|
| `SlackApp` | config | boundary | Infrastructure code | App id, scopes and redirect URL; made once by hand or by code |
| `SlackAppSecret` | secret | boundary | Infrastructure code | The app's client secret, from a secret source |
| `SlackConnection` | durable, sealed | boundary and tenant | `adapter-slack` | A tenant's workspace and bot token, made by the install flow |
| `SlackInstallState` | temp | boundary and tenant | `adapter-slack` | The state of an install in progress |

An app and a connection are two things. A provider app that serves the whole installation is config plus a secret. Every **connection** of a tenant to a provider (its directory, chat workspace, code host, or its account at a provider that sluis mints for) is a durable kind of that tenant with its credential sealed, made through the UI or the API. The operator's own company is a tenant, so its connections are durable too.

## One writer per kind

Every kind has exactly one writer. For a durable or temp kind it is an actor, named in the registry; the store refuses any other caller, and learns the caller from the transport, never from the request. For a config or secret kind it is infrastructure code or the publishing pipeline.

- **Two named exceptions:** the restore command of store@ writes back what it restores, and the self-scoped kinds `Lease`, `ReplayGuard` and `SpentGrant` have ids that start with the writing actor's name, so each actor writes only under its own.
- **The console writes two kinds,** `Switch` and `LogLevelOverride`, and routes every other write to the owner, which validates it with its own logic ([ADR 0027](/decisions/0027-the-console/#writes)).
- **The owner follows the subject.** `exchange` owns what a tenant has configured, `signin` owns people and their connections, and the adapter that completes a browser flow owns what the flow makes. key@ writes `DataKeyWrap`, and `ca` writes `TrustBundle`.
- **When two processes seem to need to write one value,** the value is split into two kinds, one per writer.

## The store

**store@ is the only holder of storage credentials.** It hosts atoom's `state` block for sluis, serves one generic store service to work actors and enforces the kind registry on every call ([ADR 0005](/decisions/0005-the-store/)). What the service and its four ports are is on [Store API](/reference/store-api/).

- **One port per storage class:** a config source, a secret source, a durable engine and a temp engine. The drivers are atoom's; sluis selects them by installation: rqlite on boards and locally, DynamoDB on Lambda, PostgreSQL through CloudNativePG on Kubernetes (planned). A boundary below L3 is served by its parent's engine under its prefix.
- **There is no query language and no SQL.** Actors use typed clients, and writes that must go together are one batch by one actor to its own kinds; a flow over several items is idempotent steps.
- **Rules:** an actor may use a verb on a kind, denied by default. The store also checks the schema, the version, the bounds and direction rule of a layered kind, and that the writer is the kind's one writer. Every write to a tenant-scoped kind carries a grant sealed for that tenant, which is the store's half of the two independent checks ([Keys](/concepts/keys/#sealed-grants)).
- **Time to live, the change log, watch and the lease with its fencing token** are the store port's. Only background jobs take a lease (a reconciler, a scheduled adapter, the backup job).

## Configuration

Config reaches the store as **signed config documents**, one per config kind, loaded as a whole snapshot. The format and the loading are the `state` block's; [Signed config](/reference/signed-config/) says what sluis supplies.

- **The signature is checked before a byte is parsed,** against publisher keys pinned in the bootstrap file. The set is validated as a whole, then swapped atomically.
- **A cold start refuses on any failure,** and keeps nothing between starts. A failed reload keeps the old snapshot in force.
- **A document may carry several signatures.** A regulated party's policy carries the publisher's and the party's, and the store requires both (planned).
- **Only the bootstrap file needs a restart.** It is the one local file store@ reads at start: the boundary, one destination per storage class and the publisher keys that anchor trust. It holds no credential ([bootstrap file](/reference/bootstrap-file/)).
- **Protobuf is the only schema source.** Each kind is a Protobuf message with protovalidate rules. JSON Schema is generated from it for editors and a binary validates a document against it before decoding ([ADR 0004](/decisions/0004-the-data-model/#schemas-are-protobuf)).
- **Durations are written with a unit,** one whole number and one of `s`, `m`, `h` or `d`, such as `15m` or `30d` ([Durations](/reference/bootstrap-file/#durations)).

### How `sluis config check` validates an installation

`sluis config check` runs offline, and the build runs the same code. It refuses a configuration that the registries do not allow:

- a signed document that fails its signature, its schema or a check that spans kinds and layers;
- a driver that does not fit the runtime, such as `rqlite` on serverless, and a combination of runtime, nodes and engine that the actor registry does not allow;
- a work unit that holds a credential, or a unit that fuses a holder with a work actor;
- a production installation with a software key, with a destination that overlaps a secret source, with no backup schedule or retention.

A check that cannot run is reported as not run, never as passed ([principle 4](/principles/#4-verify-never-trust-a-claim)).

## Secrets

Read-only secrets come from a **secret source** and are written by infrastructure code. The store fetches a value on first use and holds it in memory only, and it never appears in an audit fact, a log line or a span. A secret source holds its values in the clear for whoever may read it, so they pass through store@ in memory: a compromised store@ yields secret-source values and no sealed data.

sluis is a store of static secrets and not a general secrets engine. **KV** is a core service of stores that no tenant owns: policy decides by path, each store has a data key of its own, and External Secrets reads it through a subset of the Vault KV v2 API ([ADR 0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/)). It is planned and not built. It offers no dynamic secrets, leases or transit. Where a caller needs a credential for a vendor, sluis mints a scoped credential instead of storing the caller's own ([Integrations](/integrations/)).

## Export

**exporter@** publishes the kinds marked exportable to destinations outside sluis ([ADR 0009](/decisions/0009-export/)). It hosts atoom's `export` block (planned) over atoom's destination port; sluis plugs in the kinds, the rules and the credentials.

- **Six kinds are exportable, all public material:** `SigningRing`, `KeyCertificate`, `TrustAnchors`, `Crl`, `Krl` and `TrustBundle`. The mark is set in review, and exporter@ refuses any other kind. `ClientSecretExport` is retired; a generated secret that a consumer reads is a KV entry ([ADR 0047](/decisions/0047-kv-is-a-core-service-of-stores-that-no-tenant-owns/#secrets-that-sluis-generates-live-in-kv)).
- **A destination** is a config kind, `ExportDestination`: a bucket for public material, a store that a cluster reads through External Secrets, or, when an installation chooses it, a Kubernetes Secret written directly. A destination may not overlap a secret source.
- **exporter@ watches the exportable kinds,** opens sealed values at key@ with its own grant, writes with its own destination credential and audits every write. A full reconcile runs at start and daily.
- **sluis writes no Kubernetes object,** except an exported Secret written by exporter@ on the direct path. Clusters pull, and sluis exports.

## Backup and restore

A **backup** is a logical archive of the authoritative kinds, read through the store, and a job of exporter@ with a write-only bucket credential ([ADR 0010](/decisions/0010-backup-and-recovery/)). Planned: only the registry marks and the `BackupPlan` kind exist.

- **What the archive holds:** authoritative durable kinds, with sealed values still sealed and hashes still hashes, and the `DataKeyWrap` items. Config, secret and temp kinds are not in it, and neither is the audit log, which is protected by its lock, its witnesses and its store. Each archive is announced as the audit fact `backup.completed`.
- **The archive key** is wrapped by key@ for the restore role and for the backup recovery key ([Keys](/concepts/keys/#datakeywrap)). exporter@ never holds a key that opens an archive.
- **A restore is started by an operator only,** as the command `sluis restore` of store@. Maintenance mode comes first, the provenance of the archive is checked against `backup.completed`, and the restore writes back through the store, so the target may use another engine. Everyone signs in again.
- **A restore rolls forward from the audit log.** A tightening made after the archive is applied again; every recorded erasure and shred is applied before maintenance is lifted.
- **A regional outage is accepted,** and a yearly drill measures recovery. The targets before the first drill are targets, not measurements.
