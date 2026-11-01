# 0058 — Boundaries are enforced in three layers per port, by the driver, and every state value is encrypted

**Status:** Accepted
**Date:** 2026-10-30

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).
Rewritten in place on 2026-10-30 (threat IDs from the threat model added to the consequences).
Rewritten in place on 2026-10-30 (a narrow network rule per unit for the `per-actor` rqlite driver, and Garage as a supported backend of the blob driver with a `cloud-garage` credential driver, primary on the boards with an off-site copy of the audit log on R2).
Rewritten in place on 2026-10-31 (`cloud-garage` is Rust, as ADR 0060 now says, and the off-site copy of the audit log is an hourly job, as ADR 0021 decides).
Rewritten in place on 2026-10-31 (`key-holder` is Go with `piv-go`, after spike S4, as ADR 0060 now says).
Rewritten in place on 2026-10-31 (after the Garage spike, [issue #982](https://github.com/truvity/sluis/issues/982): the boards use R2 for blobs in the first release, and Garage is a later backend, blocked on conditional writes and prefix scoping upstream).
Rewritten in place on 2026-10-31 (after spike S5, [issue #943](https://github.com/truvity/sluis/issues/943): rqlite's memory on a board, and the CPU rather than the eMMC as the first limit).
Rewritten in place on 2026-11-01 (the root cards moved to the separate product [ritueel](https://ritueel.id): this record no longer mentions how a root card is used).

## Context

sluis divides an installation into boundaries. An environment boundary owns the machine plane, a company boundary owns the identity plane, and each boundary sits on an isolation ladder from L0 (tenant, code only) through L1 (keys), L2 (process) and L3 (store) to L4 (runtime). The boundary model and the ladder are ADR 0009. Every actor reaches its keys, records, blobs and messages through ports, and every port has drivers ([0002](/decisions/0002-actors-declare-ports-placement-is-derived/)). Drivers are services on Unix sockets that the platform starts ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)).

The question this record answers is where a boundary is enforced. A boundary that is enforced in one place fails as soon as that place has a bug. If only the actor's own code keeps `ca@development` away from production's records, a compromised actor ignores the rule. If only the backend keeps it, a misconfigured policy document opens it. If only physical separation keeps it, every boundary below L3 is unprotected.

Several facts about the backends shape the answer. All were checked on the dates given.

- **rqlite authorises per endpoint only.** There is nothing per table, per row or per database. A user with `execute` can also run `ATTACH` with an arbitrary path, and the file is created on the node's filesystem. On `query` connections `ATTACH` is refused since v10.6.3. Verified on rqlite v10.6.6 on 2026-10-30.
- **rqlite is light.** Measured on 2026-10-30 (v10.6.6, x86_64): about 7 to 9 MB of private memory per node when idle and 16 to 26 MB with a few thousand rows, plus about 30 MB of shared binary text once per host. Several independent clusters on the same three hosts worked side by side.
- **A YubiKey has one PIN.** Every process that can talk to the card can use every key on it, so the card cannot tell callers apart.
- **Garage has no Object Lock, no versioning, no bucket policies and no ACLs.** Its permissions are per key and per bucket (read, write, owner), with no prefixes. Its admin API v2 has scoped admin tokens, and its lifecycle support is partial. Read in Garage's documentation on 2026-10-30. Measured on Garage v2.5.0 on 2026-10-31 ([issue #982](https://github.com/truvity/sluis/issues/982)): `PutObject` ignores `If-None-Match` and `If-Match`, so an existing object is overwritten (upstream [issue 1052](https://git.deuxfleurs.fr/Deuxfleurs/garage/issues/1052), open); a key with write on a bucket writes every prefix (upstream [issue 1212](https://git.deuxfleurs.fr/Deuxfleurs/garage/issues/1212), open); and keys expire natively, but an expired key stays listed until it is deleted.
- **Cloudflare R2's long-lived tokens are scoped to a bucket only.** Prefix scoping exists only on temporary credentials, which are derived from a parent token. R2 has no OIDC federation. Verified on 2026-09-26.
- **Other backends do check callers natively:** DynamoDB with IAM conditions on the leading key, KMS with key policies and encryption-context conditions, PostgreSQL with roles, grants and row-level security, NATS with accounts and permission templates on scoped signing keys.

Until now the design answered the backends that cannot tell callers apart with mediators: `key-holder` for keys and `state-owner` for state ([0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)). A mediator is in the data path, it is the whole resource class as blast radius, and its access list is code. Once rqlite turned out to be cheap enough to run per actor, the state mediator was no longer needed.

The permission manifest is the spine that every generated policy comes from ([0005](/decisions/0005-three-zoom-levels-contracts-and-a-permission-manifest/)). Data origins and sensitivities are [0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/).

## Decision

### Three layers per port

Every port is protected in three layers. All three are generated from one manifest.

1. **Layer 1, the port library inside the actor.**
   - The scope `<boundary>/<kind>/…` comes from the actor's own identity, never from the request.
   - Values are encrypted (below).
   - Keys are addressed by purpose, never by a name the request supplies.
   - Blobs are created only if absent.
2. **Layer 2, the driver's or the backend's own check of the caller.** It checks the caller's platform identity: a peer uid, an IAM role, a ServiceAccount token or a client certificate. It never relies on what layer 1 claims.
3. **Layer 3, physical separation** at L3 and L4: a store, a bucket, a queue, an account or a cluster of its own.

Each layer must hold alone. A bug in one layer is caught by the next.

### Every state value is encrypted

- **Every value of the `state` port is encrypted in the port library.** The store sees ciphertext and record keys only.
- **The cipher is AES-256-GCM**, which is FIPS-compatible (ADR 0010).
- **There is one data key for each pair of boundary and record kind.** It is wrapped by the boundary's key-encryption key through the `keys` port (`wrap` and `unwrap`, [0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)).
- **Unwrap is allowed for the kind's writer and its declared readers only.** On `key-holder` this is its access list. On KMS it is the key policy plus a condition on the encryption context.
- **The additional authenticated data is (boundary, kind, key, version).** A value copied to another record, another kind or another boundary does not decrypt.
- **Record keys never contain personal data.** Keys stay in the clear so that the store can index and list them.
- Secrets still go through the `secrets` port, and sealing them is ADR 0034. This rule is the floor under every record, secret or not.

### One generator, a drift test, and test vectors per layer

- **One generator writes every layer's artefacts:** the library scopes, the access lists, the IAM and KMS policy documents (as output for the infrastructure code to apply), the systemd units and sockets, and the Kubernetes ServiceAccounts.
- **A drift test** compares what is deployed with what the generator writes. `sluis config check` refuses an installation whose layers disagree.
- **Isolation is tested per layer, with the other layers switched off.** Each layer has its own test vectors: for example, `ca@env-a` asking for env-b's key is refused, and actor X writing under actor Y's prefix is refused.
- **A driver whose backend has no per-caller check is allowed at L3 and above only, together with its layer-3 separation.** `sluis config check` enforces it.

### No mediator where the backend authorises natively

- **A backend that can tell callers apart is used with per-actor credentials and no mediator.** The driver runs `per-actor` ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)) and holds only its own actor's credential.
- **Mediators are only for backends that cannot tell callers apart.**
- **Credentials are brokered instead of mediating the data path:**
  - `sluis-node` issues tokens per uid;
  - the cloud drivers exchange them for cloud credentials;
  - `ca` issues database client certificates;
  - sluis issues NATS credentials.

### `key-holder` is the only mediator

The node's YubiKey is the one backend left that cannot tell callers apart.

- **One `key-holder` process serves one Unix socket per caller.** Each socket sits in a directory that only its caller can open, using systemd socket units with `SocketUser=` and `FileDescriptorName=`. In a container, only that caller's socket is mounted.
- **`key-holder` also checks the peer uid** of every connection, as a second factor.
- **Signing is content-aware for each purpose.** The `ca` purpose signs only a TBSCertificate that parses and stays within its constraints. The `ssh-ca` purpose signs only an OpenSSH certificate body. Token purposes sign only a JWS signing input. For this, a call carries the content to be signed and not only its digest. (inferred)
- **`key-holder` is written in Go with `piv-go`, which holds the card exclusively, so no other process on the node can use or reset it** ([0060](/decisions/0060-the-language-follows-the-executable/)). It has no network, runs under seccomp and keeps the PIN in locked memory. The PIN arrives only from an unlocked peer or through `sluis node unlock` ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)). Signatures are rate-limited, and each one is audited.
- **More cards per node are optional later.**

### State on rqlite: a cluster per boundary and owning actor

- **There is one rqlite cluster for each pair of boundary and owning actor.** There is no `state-owner`.
- **The owning actor gets an `execute` user. Each declared reader gets a `query`-only user.** One writer per record kind is then enforced by the backend.
- **Each rqlite unit is sandboxed by systemd** so that it can write only its own data directory. That contains `ATTACH`, which an `execute` user can otherwise use to create files anywhere the rqlite user can write.
- **Node-to-node and client traffic use mutual TLS**, with a certificate for each cluster. A node of one cluster then cannot join another.
- **Clusters run from a systemd template**, `sluis-rqlite@<boundary>-<actor>`, with a port scheme for their HTTP and Raft ports.
- **A `per-actor` rqlite driver reaches its cluster through a narrow network rule per unit**, not through `PrivateNetwork=yes`: `IPAddressDeny=any`, `IPAddressAllow=` for its own cluster only, and `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, plus a client certificate for that cluster. It moves to a Unix socket if rqlite adds one ([0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/)).
- **The memory figures above come from a desktop.** On a board, spike S5 measured about 9 MiB per cluster under write load, with the binary's pages (about 22 MiB) paid once per board (2026-10-31, [0060](/decisions/0060-the-language-follows-the-executable/)).

### Blob: one S3-compatible driver, with credentials from the `cloud` port

- **There is one blob driver for object stores, `s3-compatible`.** The separate `r2` blob driver is removed.
- **Its credentials come from the `cloud` port:**
  - on AWS, STS through web identity, with IAM conditions on the prefix;
  - on Cloudflare, a `cloud-cloudflare@<boundary>` driver that mints R2 temporary credentials scoped to one bucket and a prefix. It mints them locally, as an HS256 token signed with the parent token's secret, so no call to Cloudflare is needed for each credential.
- **The parent token is per boundary, and it is stored sealed.**
- **`cloud-cloudflare@<boundary>` is separate from the company integration `cloudflare@<company>`.** They share a library, not a credential.
- **Layer 3 is a bucket per boundary**, with Object Lock on S3.

### Garage: a later backend of the same blob driver

- **On the boards, blobs are on R2 in the first release,** through `s3-compatible` with R2 temporary credentials from `cloud-cloudflare@<boundary>`, as on any R2 store (decided 2026-10-31). The lock mode is `removable`, so the peer witnesses still carry the audit log's tamper evidence.
- **Garage is a later backend of `s3-compatible`, blocked upstream.** The spike ([issue #982](https://github.com/truvity/sluis/issues/982)) found no conditional writes on Garage v2.5.0 (upstream [issue 1052](https://git.deuxfleurs.fr/Deuxfleurs/garage/issues/1052)) and no prefix-scoped keys (upstream [issue 1212](https://git.deuxfleurs.fr/Deuxfleurs/garage/issues/1212)). Garage is excluded from the blob backends until its conditional writes land, because the blob store is the audit log's write-once fence ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)), and it waits for prefix scoping too.
- **When Garage lands, its design is the one below.**
  - **Its credentials come from a `cloud-garage@<boundary>` driver,** written in Rust by rule R5 of [0060](/decisions/0060-the-language-follows-the-executable/). It holds a scoped admin token, and creates a key per (boundary, actor) with an expiry through Garage's admin API. The admin token is stored sealed. (that it is sealed is inferred from the parent token's rule)
  - **A bucket per actor,** because Garage's permissions are per bucket and have no prefixes. Layer 2 is the key with rights on its own bucket only. Layer 3 is the bucket per actor, and a Garage cluster per boundary.
  - **The lock mode is `none`.** Garage has no Object Lock and no versioning, so the audit log's tamper evidence comes from the peer witnesses.
  - **The audit log has an off-site copy on R2.** An hourly job of the audit installation copies new log objects from Garage to an R2 bucket, so losing the boards loses at most the last hour of the log ([0021](/decisions/0021-the-audit-log-is-sealed-batches-in-a-blob-with-a-lock-mode/)). (that the copy uses `cloud-cloudflare` credentials is inferred)

### PostgreSQL

- **Each actor kind has a fixed schema. Grants separate the kinds.**
- **Row-level security separates instances of one kind** (boundaries and tenants) through group-role membership: `pg_has_role(current_user, 'boundary_' || boundary, 'member')`. Tables use `FORCE ROW LEVEL SECURITY`.
- **Actors never own tables.** A migration role owns them.
- **Policies never trust session variables.**
- **Views are `security_invoker`, and there is no `SECURITY DEFINER` function.**
- **The tenant column is part of every unique key.**
- **Each role has its own connection pool.**
- **PostgreSQL 15 or newer** is required.
- **At L3 and above, each boundary has its own database**, and row-level security stays on.
- **Clients authenticate with certificates from `ca`** (the common name is the role) or with IAM on RDS and Aurora ([0033](/decisions/0033-database-access-uses-short-lived-credentials/)).

### NATS JetStream

- **Layer 2 of the `nats-jetstream` transport driver is NATS itself:** accounts and permission templates on scoped signing keys. The broker refuses a publish outside the template, so the driver needs no mediator and runs `per-actor`.
- **The mapping:** one operator per environment, an account per tenant, the company as an account tag, the user named `<actor>@<boundary>`, and a scoped signing key per role.
- The key that NATS requires is an exception in ADR 0010.

### Leases

- **Leases live on the state store**, as compare-and-set with a fencing token. They are independent of the Raft leader. The named `leader` lease stays available.
- **Where the target store can fence, it fences.** A PostgreSQL write checks the token in the same transaction. Blob batches are created with `If-None-Match: *`, so the audit log cannot fork. With one log per node, the boards' audit writers need no lease.
- **External side effects rely on the lease plus idempotent reconcilers.**
- **Session advisory locks are not used alone**, because they give no fencing token.
- **The Kubernetes Lease driver goes to the backlog.**
- **A PostgreSQL or CloudNativePG state driver** is a question for the Kubernetes records (ADR 0047).

### The drivers, layer by layer

| Port | Driver | Layer 2: the backend's own check | Layer 3, and notes |
|---|---|---|---|
| keys | `software` | `key-holder` | Refused in production ([0011](/decisions/0011-authority-keys-sign-in-place-and-never-leave-their-store/)) |
| keys | `yubikey-piv` | `key-holder`: a socket per caller, peer uid, content-aware signing | |
| keys | `aws-kms` | Key policy plus a condition on the encryption context | |
| keys | `pkcs11`, YubiHSM 2 (planned) | `key-holder` plus the HSM's authentication keys and domains | ADR 0052 |
| keys | `pkcs11`, Nitrokey HSM 2 (planned) | `key-holder` plus the PIN | ADR 0052 |
| state | `embedded` | A file per uid, one per actor | |
| state | `rqlite` | A cluster per actor, plus rqlite users (`execute` for the owner, `query` for readers) | |
| state | `dynamodb` | IAM `dynamodb:LeadingKeys` per actor role | A table per boundary; at L4 in the boundary's own account |
| state | `postgres` | Grants and row-level security, as above | A database per boundary at L3 and above |
| lease | all drivers | On the state store, independent of the Raft leader | |
| blob | `fs` | A directory per uid, mode 0700 | |
| blob | `s3-compatible` | IAM prefix conditions on AWS; R2 temporary credentials with prefixes through `cloud-cloudflare` | A bucket per boundary; Object Lock on S3 |
| blob | `s3-compatible` on Garage (later) | A key per bucket, per (boundary, actor), with an expiry, from `cloud-garage` | A bucket per actor; a Garage cluster per boundary; lock mode `none` |
| transport | `direct-unix` | The peer uid, checked by the receiver | |
| transport | `direct-mtls` | The SPIFFE ID in the client certificate | |
| transport | `spool` | A directory per producer uid | |
| transport | `sqs` | Queue policy per sender role | A queue per boundary |
| transport | `nats-jetstream` | NATS accounts plus permission templates | An operator per environment, an account per tenant |
| index | `sqlite` | A file per indexer uid; the reader opens it read-only | |
| index | `rqlite` | As for state | |
| index | `postgres` | Grants and row-level security, as above | |
| witness | `peer` | The node's identity, over mutual TLS | |
| witness | `external` | The witness's own key | |
| secrets | `sealed-state` | Inherits from `state` and `keys` | |
| export | `age` | Encrypted to the recipient's key | |
| cloud | web identity | Tokens per uid from `sluis-node`; the trust policy pins `sub` to `<actor>@<boundary>` | |
| cloud | platform role | – | |
| cloud | GCP and Azure federation | – | |
| cloud | `cloud-cloudflare` | R2 temporary credentials | |
| cloud | `cloud-garage` (later) | A scoped admin token that creates keys per (boundary, actor) with an expiry | A Garage cluster per boundary |

A dash means that the decision names no layer-2 check for that driver yet.

## Consequences

- **A compromised actor has to defeat three independent mechanisms** to reach another boundary's records: its own library, the backend's check of its platform identity, and, at L3 and above, a store it has no route to. A bug in the generated policy documents is caught by layer 1, and a bug in layer 1 is caught by the backend.
- **A store-level reader learns little.** A backup, a database administrator or a process that bypasses the port sees ciphertext and record keys. A value moved between records does not decrypt, because the additional authenticated data names its place. The cost is an unwrap per data key and an AEAD per value, and the rule that record keys carry no personal data, which some kinds have to be designed for.
- **AES-GCM with one data key per boundary and kind has a usage limit.** Kinds with a high write rate need their data key rotated before it is reached. (inferred)
- **One writer per record kind is now enforced by the backend** on every state driver: an rqlite cluster that only the owner can write, a DynamoDB leading key per role, PostgreSQL grants, a file per actor. It no longer depends on a mediator's code.
- **`state-owner` is gone, and so is its hop.** The cost is more rqlite clusters: one per boundary and owning actor, each with three nodes on a cluster, its own ports, certificates, users file, backups and upgrades. rqlite shipped three security fixes in October 2026, so each release means a rolling upgrade of every cluster. On the boards neither memory nor the eMMC is the first limit: spike S5 (2026-10-31) found the CPU saturated at about 430 commits/s in total while the eMMC was about half busy, so the budget is about 400 commits/s per board, summed over its clusters.
- **rqlite's users file is static.** A change of users needs a restart, which is acceptable because owners and readers come from the manifest and change only with a release. (inferred)
- **`key-holder` is the one mediator left**, and its socket layout, peer-uid check and content checks are the code to review hardest. On KMS there is no content-aware signing, so the key policy per actor is layer 2 there. (inferred)
- **Content-aware signing changes the `keys` contract for `key-holder`.** A sign call carries the content to be signed. (inferred)
- **Credential brokering puts minting on the hot path of start-up**, not of every read. A broker that is down stops new credentials, not existing ones. (inferred)
- **One blob driver serves AWS and Cloudflare.** The difference between them moves to the `cloud` port, where credentials are scoped to a prefix on both. A bucket can therefore be shared between actors safely, which a long-lived R2 token could not do.
- **In the first release the boards' blobs depend on the internet.** Every batch goes to R2, so an outage of the link or of R2 delays the log, and R2's lock can be removed by an account administrator, so the witnesses carry the log's tamper evidence. (inferred)
- **Garage, when it lands, gives the boards a blob store of their own, at a cost.** A bucket per actor means more buckets, the log has no lock, so the witnesses carry its tamper evidence, and the off-site copy on R2 is one more job to run and monitor. (inferred)
- **A unit with a `per-actor` rqlite driver has a network stack**, limited by its per-unit address rule and by the per-cluster certificate rather than by a namespace.
- **PostgreSQL needs more set-up:** a migration role, a group role per boundary, a pool per role and version 15 or newer. Self-run PostgreSQL clients get certificates from `ca`.
- **This record changes three accepted records.** [0002](/decisions/0002-actors-declare-ports-placement-is-derived/) and [0008](/decisions/0008-drivers-are-unix-socket-services-and-only-the-public-api-listens-on-tcp/) name `state-owner` as a mediating driver, and [0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/) has `state-owner` enforce one writer per record kind, runs one rqlite cluster per installation behind it, and lists a Kubernetes Lease driver. Where they disagree with this record, this record is the reasoning, and they are realigned in place during the design phase.
- **The driver page and the requirements catalogue follow.** The per-driver table belongs on the drivers page, and the isolation test vectors are requirements with one clause each.
- **Threats addressed:** T-8, T-9, T-10, T-11, T-12, T-13, T-14, T-24 and T-25, see the [threat model](/security/threat-model/).

## Roads not taken

### Keep `state-owner` in front of one rqlite cluster per boundary

- Pros: works with rqlite as it is. One policy point, easy to audit. Fewer clusters to run.
- Cons: the isolation between actors is code, not something the backend enforces. The mediator is in every state call's path and is the whole state class as blast radius. Its access list is one more thing that can be wrong.
- Why not: rqlite is cheap enough to run a cluster per owning actor, so the backend can enforce one writer per kind and no mediator is needed.

### etcd for state, with prefix roles and certificate users

- Pros: native prefix permissions with users authenticated by client certificate, the closest analogue to DynamoDB's leading keys. Mature, with native compare-and-set.
- Cons: a second data store beside rqlite, which the index still needs. Roles and users are managed through etcd's `root` user, so adding an actor is a privileged control-plane step. No SQL.
- Why not: a cluster per owning actor gives the same separation with the store the index already uses.

### One rqlite cluster per boundary, with a column for the actor

- Pros: the fewest clusters.
- Cons: rqlite cannot restrict a user to rows or tables, so every actor with `execute` can rewrite every record of the boundary, and `ATTACH` can write files.
- Why not: it leaves the backend with no per-caller check, which this record allows only with a mediator or at L3 per actor.

### Isolation in one layer only

- Pros: less to generate, less to test.
- Cons: the single layer becomes the boundary. Library-only isolation does not stop a compromised actor. Backend-only isolation depends on policy documents being right. Physical separation alone leaves L0 to L2 unprotected.
- Why not: the point of the boundary model is that one mistake does not cross a boundary.

### Encrypt only the values marked secret or personal

- Pros: less cryptography on each read and write. A store administrator can read non-sensitive records when debugging. (inferred)
- Cons: the classification has to be right for every field of every kind, and a mistake leaks in the clear. A value can be moved between records without detection. (inferred)
- Why not: encrypting every value makes the store's own access control the second line, not the only one, whatever the classification says. (inferred)

### Mediate YubiKey access with one socket and an access list

- Pros: one socket, one listener, a simpler unit. (inferred)
- Cons: every caller connects to the same socket, so the peer-uid check and the access list are the only separation. Mounting one socket into a container gives that container a path to every purpose.
- Why not: a socket per caller in its own directory adds a filesystem check before `key-holder`'s own, and a container gets only its own socket.

### A key-holder process per caller, each with its own card

- Pros: no shared process at all.
- Cons: one card per caller per node, which the hardware and the slots cannot provide today. (inferred)
- Why not: more cards per node remain an option for later. One process with a socket per caller, a peer-uid check and content-aware signing covers the need now.

### Keep the `r2` blob driver beside an `s3` driver

- Pros: each driver tuned to its store.
- Cons: two drivers for one protocol. Long-lived R2 tokens are bucket-wide, so a driver holding one has access to the whole bucket.
- Why not: R2 speaks the S3 API, and temporary credentials scoped to a prefix give it the same layer 2 as IAM conditions on AWS.

### Garage behind prefixes in a shared bucket

- Pros: fewer buckets, as on S3 and R2.
- Cons: Garage's keys have no prefix scope, so every actor with a key on the bucket could write every prefix (measured on v2.5.0, 2026-10-31).
- Why not: a bucket per actor gives Garage a layer-2 check of its own.

### Garage as the boards' blob store in the first release

- Pros: a blob store on the boards themselves, with no internet on the write path; native key expiry and scoped admin tokens; about 73 MiB of memory at peak on a board with LMDB (measured 2026-10-31).
- Cons: no conditional writes (`If-None-Match: *` is accepted and the object is overwritten), no prefix-scoped keys, a `write` right that includes delete, and about 170 ms per admin API call from its token check (all measured on v2.5.0, 2026-10-31, [issue #982](https://github.com/truvity/sluis/issues/982)).
- Why not: the maintainer moved Garage to later on 2026-10-31, after the spike. R2 is the boards' blob store in the first release.

### PostgreSQL isolation through session variables

- Pros: a common pattern. One pool for every tenant.
- Cons: a caller that can run SQL can set the variable. A pooled connection can carry another tenant's setting.
- Why not: a policy must rest on the authenticated role, through group-role membership, not on something the session can change.

### The Kubernetes Lease as the lease on Kubernetes

- Pros: native to the platform. No state store needed for leader election.
- Cons: the chart would need RBAC for leases. The fencing token would not be checked by the stores that the holder writes to, and it is a lease driver for one runtime. (the last two are inferred)
- Why not: leases on the state store work on every runtime, and fencing belongs in the store that is written. The Kubernetes Lease driver stays in the backlog.
