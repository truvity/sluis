# 0047 — KV is a core service of stores that no tenant owns: policy decides by path, each store has its own data key, and External Secrets reads it through a Vault KV v2 subset

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #798](https://github.com/truvity/sluis/issues/798).

## Context

An installation on a few boards needs a key-value store for static secrets, like the key-value stores of OpenBao and AWS SSM, with an audit record of every access. External Secrets (ESO) must read it, from sluis itself and not from a cloud parameter store, so that a board installation needs no cloud for its consumers.

The design said the opposite until now. [0009](/decisions/0009-export/) says sluis is not a secrets engine for anyone else: a secret leaves sluis only through an export, and only public material is exportable. A platform that keeps its secrets in OpenBao and a parameter store needs a replacement for both, and the replacement should sit under the same policy, keys and audit as everything else sluis holds.

Five facts shape the answer.

- **A store belongs to no tenant.** Staff, workloads and tenants' identities all read the same stores, so access is many-to-many and policy relates identities to stores.
- **ESO is the consumer.** It gets a sluis identity and reads under policy. There is no export of a KV secret.
- **Access to a secret is audited.** A success that cannot be recorded does not happen. A denial is recorded on a best-effort basis and counted.
- **Staff may read values,** with step-up on the stores or subtrees that policy names.
- **The first consumer reads through Vault's KV v2 calls,** and nothing more of Vault is needed.

## Decision

### What KV is

- **KV is a core service.** It is built on sluis's `store@`, `key@`, `policy` and the audit emitter, and one new work actor, `kv`, holds no credential at rest ([principle 16](/principles/)). No actor of the core needs `kv` to start or to issue a token.
- **Nothing of KV is built yet.** It is planned for the core, on the local installation first, and the [roadmap](/roadmap/) says where.
- **The mechanics are atoom's.** A KV store is a set of items in atoom's `state` block, and each value is sealed under a per-store data key by atoom's `seal` block ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md), and the row for KV storage and encryption in [atoom 0013](https://github.com/truvity/atoom/blob/master/docs/decisions/0013-what-moves-from-sluis.md)). This record says what KV adds: its kinds, its keys, its rules, its audit and its protocol. KV adds kinds to sluis's registry ([0004](/decisions/0004-the-data-model/)).
- **A KV store is what Vault calls a mount.** It is named, boundary-wide and declared in signed config as the kind `KvStore`. The name is 1 to 32 characters of `a-z`, `0-9`, `-` and `_`, with no `/`. It is not `store@`, the data service.
- **A secret is a path in a store,** at most 16 segments of `A-Za-z0-9._-` and 512 bytes, case-sensitive. A **version** is an immutable JSON object of string keys, numbered from 1 for each path.
- **The semantics of Vault KV v2 are kept for what static secrets need.** A write creates version n+1. A write may carry `cas`: `0` means "only if absent" and `n` means "only if the current version is n". A store or a path may require `cas`. Versions beyond `max_versions` (default 10, at most 100) are pruned by the write that exceeds it. A soft delete keeps the sealed value and sets `deletion_time`, an undelete restores it, a destroy removes the sealed value for good, and deleting the metadata destroys the whole secret. A soft-deleted value is destroyed after `soft_delete_retention` (default 30 days), because a store that can be erased needs the bound. Metadata is `created_time`, `updated_time`, `current_version`, `oldest_version`, `max_versions`, `cas_required`, `delete_version_after`, a `custom_metadata` map of strings and a table of the state of each version.
- **Not in KV:** dynamic secrets, leases, transit, response wrapping, namespaces and Vault's own ACL policies ([REQ-KV-025](/requirements/kv/#REQ-KV-025)).
- **No key material is a KV secret.** An authority key, a wrapping key and a data key stay out of every kind ([REQ-DATA-094](/requirements/data/#REQ-DATA-094)).

### Stores are boundary-wide, and a path is a hierarchy for policy

- **Stores stay flat.** A store is the unit of separation: its key, settings, managed prefixes, audit filter and erasure. A store inside a store is not built, because it would need its own key, settings and quota, and a rule for what a parent's settings do to a child.
- **Inside a store, a path is a hierarchy for policy.** Every prefix of a path is a node (`a`, `a/b`, `a/b/c`). Nodes exist only for policy and are not stored. A rule on a node applies to everything below it, through Cedar's `in`.
- **A store may name groups,** `groups: [...]` in its config, so that one rule covers several stores ("all stores of the infra group"). A group is a label and owns nothing.
- **Many-to-many comes from policy.** An identity of any tenant can be related to any store, node or group by a rule, and delegation by template link lets a store's admin hand out access within the bounds the operator declared ([0007](/decisions/0007-policy-and-sealed-grants/)).
- **The policy schema gains three entity types and the KV actions:** `KvGroup`, `KvStore in [KvGroup]` and `KvNode in [KvNode, KvStore]`; the actions `kv.read`, `kv.list`, `kv.metadata.read`, `kv.write`, `kv.metadata.write`, `kv.delete`, `kv.undelete` and `kv.destroy`, all on a node. A `Workload` gains `cluster`, `namespace`, `serviceaccount` and `tenant`. A workload's `tenant` is declared by the trust configuration for its issuer and is never reported by the workload ([0022](/decisions/0022-workload-identity-and-host-enrolment/)). `kv.list` is asked on the folder, so a rule on a subtree covers listing inside it.
- **Step-up is policy, not a flag.** The `Authentication` context carries `auth_age`, and a rule requires a fresh sign-in on a subtree. A store has no field that asks for step-up.

```text
// ESO in cluster c1 reads one subtree of the store "infra" with the identity of one ServiceAccount.
permit (principal is Workload,
        action in [Action::"kv.read", Action::"kv.list", Action::"kv.metadata.read"],
        resource in KvNode::"infra/apps/app1")
when { principal.verifier == "k8s-sa-token" && principal.cluster == "c1" &&
       principal.namespace == "app1" && principal.serviceaccount == "eso-app1" };

// Staff may read the whole store, but the production subtree needs a sign-in younger than five minutes.
permit (principal is Person, action in [Action::"kv.read", Action::"kv.list", Action::"kv.metadata.read"],
        resource in KvStore::"infra")
when { principal has grants && principal.grants.contains(Grant::"all:kv-infra:reader") };
forbid (principal is Person, action, resource in KvNode::"infra/prod")
unless { context has auth_age && context.auth_age < 300 };
```

A namespaced `SecretStore` with a `serviceAccountRef` per app lets policy tell apps apart. A `ClusterSecretStore` reads with one identity for every namespace, so policy cannot.

### Storage and encryption

- **Three kinds** are added to the registry: `KvStore` (config, boundary, plain), `KvSecret` (the metadata of one path) and `KvVersion` (the value of one version). The last two are durable, at the boundary scope with tenant `-`, sealed, written by `kv` alone, authoritative and backed up. One item per version makes a destroy the delete of one row. This is the one exception to the rule that durable kinds belong to a tenant: a tenant's exit does not delete them.
- **The addresses are keyed hashes.** A path has capitals and slashes, which an address part does not, so the id of a `KvSecret` is `<store>.<h>`, with `h` the base32 of `Mac(mac, "sluis-kv-path-v1" || store || 0x00 || path)`, and a `KvVersion` adds `.<n>`. The path itself is inside the sealed envelope and is checked on open.
- **A list reads an index of hashed ancestors.** `KvSecret` declares an index `by_ancestor`; the writer adds one entry per ancestor folder, each a keyed hash of the store and the folder. An index never holds a path in the clear.
- **A write is one transaction:** the condition on the version of the metadata, the new version, the updated metadata and the delete of the pruned versions. A condition that fails is retried by `kv` unless the caller sent `cas`, and then it is the Vault error. Reads are linearizable.
- **Each store has one random data key of its own,** a key purpose `secret` that sluis declares to the `seal` block, with the store as its scope. The key encrypts nothing but the values of its store, so `key@` can refuse a grant for store A on store B's key, a store is erased by deleting its wraps, and rotation and the budget of seals are counted per store. The wrap id is `secret/<store>/<generation>/<recipient>` ([0006](/decisions/0006-keys-and-sealing/)).
- **The sealed grant names stores.** For purpose `secret`, `policy` seals a grant that names `stores: [<store>]` and the operations `seal` and `open`, and `key@` checks that the store of the key is one the grant names, besides the caller's rule. This keeps the two independent checks of [principle 18](/principles/) for a store that no tenant owns ([0007](/decisions/0007-policy-and-sealed-grants/)).
- **A rewrap job re-seals live versions under the newest generation** of a store's key, so that an old generation can be retired. It is idempotent and resumable, a command of `kv`, and an automatic pass after a key event.
- **Erasing a store deletes its items and its wraps.** A backup holds ciphertext only, and the wraps age out of the archive with its retention, so a deleted store is unreadable everywhere.
- **A tenant's exit does not erase what its identities wrote into a store.** Their access ends by policy, and the entries stay until a store admin removes them. This is the price of stores that no tenant owns, and the threat model states it.
- **The first limits are** 256 KiB for a value, 512 bytes and 16 segments for a path, 64 pairs of custom metadata, 10,000 secrets for a store and 100 versions for a secret. They come from the design, not from a measurement, and a measurement on a board is planned before KV ships ([principle 9](/principles/)); values pass through Raft, which is why the cap on a value is small.

### Audit

- **sluis emits through audit's emitter** ([audit 0002](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md)) and declares the KV actions in its audit catalogue ([audit 0015](https://github.com/truvity/audit/blob/master/docs/decisions/0015-configuration-kinds.md)). Every request is one audit fact. The actions are `kv.login`, `kv.read`, `kv.list`, `kv.metadata.read`, `kv.write`, `kv.metadata.write`, `kv.delete`, `kv.undelete`, `kv.destroy`, `kv.rewrap`, `kv.sweep` and `kv.deny`. The fields are the store, the path, the versions touched, the `cas` sent and its outcome, the result, a closed reason code, the caller's subject and client, the token's `jti`, the policy decision and the `role` label sent at login.
- **A success uses the required emit** of [audit's security class](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md) ([REQ-KV-015](/requirements/kv/#REQ-KV-015)): the call fails with 503 when its record is not durable. For a destroy the emit comes first, because a destroy that cannot be recorded must not happen. For the other writes the store write comes first and a failed emit fails the call afterwards; a retry of a `cas` write is idempotent, and without `cas` it makes a new version, as Vault does. When the spool of one board is full or absent, that board's `kv` answers 503, the other boards still serve, and ESO keeps the Kubernetes Secrets it already synced.
- **A denial is best effort and counted:** a flood of denials must not stop allowed operations, and a denial discloses nothing.
- **A record never holds a value,** a key name inside a value, a hash or the length of a value, a ciphertext or a token. A fingerprint would let a reader guess a low-entropy secret offline and tells nothing the version number does not. A test greps every record of a full run for the values it wrote.
- **The record's tenant is the caller's tenant** (a person's `org`, a workload's declared tenant, `-` for a boundary-level caller), sealed under that tenant's `evidence` key ([audit 0011](https://github.com/truvity/audit/blob/master/docs/decisions/0011-evidence-encryption-and-pseudonyms.md)). A tenant's auditor sees what its own identities did in shared stores and nothing else. The operator's audit grant names every tenant, and the operator filters by `store`.

### External Secrets reads a Vault KV v2 subset

`kv` serves the paths below on a host of its own, `kv.sluis.example`, so the edge rules (no cache, a stricter rate limit, no body logging) differ from the issuer's. `entry` routes `/v1/` to `kv` and limits sizes and rates; the protocol lives in `kv`. The list was read from ESO's Vault provider on 2026-11-12 (main at `a7c496c6`): it makes no `sys/` call at all, and the version of KV is declared in the store spec. A later ESO release can add calls, and the ESO job below is the guard.

| # | Request | What the facade answers |
|---|---|---|
| 1 | `GET /v1/auth/token/lookup-self` | `data.type` `service`, `data.ttl` in seconds, `data.expire_time` in RFC 3339, `entity_id`, `renewable` false |
| 2 | `PUT /v1/auth/token/revoke-self` | A 2xx; the token's `jti` is refused until it expires |
| 3 | `PUT /v1/auth/jwt/login` | `auth.client_token`, built as below |
| 4 | `GET /v1/<store>/data/<p>[?version=N]` | `data.data` and `data.metadata`; a soft-deleted version is a 404 with a body that carries `deletion_time` |
| 5 | `GET /v1/<store>/metadata/<p>` | `created_time`, `current_version`, `delete_version_after`, `custom_metadata`; 404 for no secret |
| 6 | `LIST /v1/<store>/metadata/<p>/` (`GET ...?list=true`) | `data.keys`, folders ending in `/`; 404 for none |
| 7 | `PUT /v1/<store>/metadata/<p>` | Writes `custom_metadata` (ESO sets `managed-by`) |
| 8 | `PUT /v1/<store>/data/<p>` | Writes a version; `options.cas` when the store spec requires it |
| 9 | `DELETE /v1/<store>/data/<p>` | Soft deletes the latest version |
| 10 | `DELETE /v1/<store>/metadata/<p>` | Destroys the secret |

- **Level 1 is these requests, about fourteen handlers.** Level 2 adds `PATCH` and `POST` forms, per-version delete, undelete and destroy, metadata and config reads and writes, subkeys, and the `sys/` calls that the `vault kv` command and Pulumi's Vault provider use. It comes when a consumer needs it ([principle 8](/principles/)). Until then a stack reads a secret with `sluis kv get`. ESO's webhook provider, a new native ESO provider and a full simulation of Vault are not built.
- **ESO's `path` is one segment, the store.** Everything after it is the secret path. A multi-segment `path` is refused with a clear error, because `data` or `metadata` could be a path segment. A non-empty `X-Vault-Namespace` is refused, so nothing is routed silently wrong; `X-Vault-Token` and `Authorization: Bearer` are both read. Errors are `{"errors": [...]}` with 400, 403, 404 or 5xx.
- **The only login is the JWT one.** ESO requests a ServiceAccount token for the audience `https://kv.sluis.example` that lives 10 minutes, and `kv` calls `exchange` with it as the subject token (RFC 8693, type `urn:ietf:params:oauth:token-type:jwt`, audience `kv`). The verifier `k8s-sa-token` of [0022](/decisions/0022-workload-identity-and-host-enrolment/) checks the token against the cluster's key set declared in `ExchangeTrust`, and `policy` decides `token.exchange` for the workload. **The sluis access token is the Vault token.** A login is `kv.login`, a required fact; the `role` is recorded and given to policy as context, and no decision rests on it. AppRole and a static token are not offered, because a secret ID is a long-lived client secret.
- **The `k8s-sa-token` verifier and the declared tenant of a workload are the slice of workload exchange that KV needs.** Both are planned and are built with KV; the rest of workload exchange follows its own consumers ([0022](/decisions/0022-workload-identity-and-host-enrolment/)).
- **Offline verification cannot see a deleted ServiceAccount,** so the KV client's `ttl_cap` is 15 minutes, and a removed workload keeps access for at most the token's life plus that. The verifier accepts an identical login within 60 seconds, because the Vault client retries a failed login with the same body.
- **Staff reach KV through `sluis kv`,** with the token of `sluis token --audience kv` ([0017](/decisions/0017-identity-and-sign-in/)). `sluis kv put` takes values from standard input or a file and not from the arguments by default.
- **Three layers of tests keep the facade honest:** unit tests in `kv` (the `cas` matrix, pruning, soft delete, destroy, list, limits, the binding to the address, a grant for store A that cannot open store B's key, no value in the audit log); a differential suite against a pinned OpenBao dev server, replaying the 14 recordings that ESO's OpenBao provider ships (Apache-2.0 test data, with its licence notice kept); and an ESO job on a standard GitHub runner in a kind cluster, with a pinned ESO chart, a namespaced `SecretStore`, an `ExternalSecret` that extracts and finds, and a `PushSecret`, which also asserts one `kv.read` record per read.

### Secrets that sluis generates live in KV

- **There is no export of a KV secret.** `exporter@` publishes public material only, and a secret that sluis generates or receives for a named consumer is written by its owner actor into a KV entry. The consumer, ESO, reads it with its own sluis identity under policy.
- **A store's `managed` list names prefixes and their only writer,** `[{prefix, writer}]`, for example `oidc/` written by `exchange`. A rule at `kv`'s socket, generated from the actor registry, lets only that actor write there ([principle 17](/principles/)), and the Vault facade cannot write under a managed prefix at all. Everyone else, staff and ESO included, reads under Cedar like any path. The actor calls `kv` over the local socket and never holds the store's key.
- **One mark per kind says that its secret is published to KV** and where (`kv_path`). Without the mark the secret is readable outside sluis nowhere. The mark takes the place of "marked exportable" in [0009](/decisions/0009-export/), and a secret still needs a named consumer ([principle 8](/principles/)).
- **The generated client secrets of clients that a consumer reads move to KV** as `{client_id, client_secret}` under a managed prefix, written by `exchange`. `ClientSecret`, the hash that `signin` verifies at the token endpoint, stays. A new version is a new secret; the old verifies during its overlap through the hash, and consumers read the latest. The sealed copy `ClientSecretExport` is retired, and the registry loses it, in the change that makes `exchange` write the entry ([0021](/decisions/0021-clients/)).
- **The GitHub App documents** that the runner and Git-read consumers need arrive in KV the same way, written by the GitHub adapter, when that add-on exists.
- **Credentials that sluis can mint are not stored.** Cloudflare tokens, GitHub installation tokens and SSH and database certificates are minted on request ([0009](/decisions/0009-export/)). A static token that a consumer needs until the minter exists is an unmanaged entry that the operator writes and deletes when the minter arrives.
- **Sluis's own start-up secrets stay out of KV:** the upstream sign-in client secret, session and cookie keys, storage credentials and anything an actor needs to start. They stay in the secret source, which is read-only and written by infrastructure code, and in sealed kinds, and must not depend on KV ([principle 17](/principles/)).

## Consequences

- An operator can run the secrets of a platform on sluis alone, with an audit record of every read, and ESO needs no store of record besides sluis. The price is a service that holds the most valuable data sluis stores, so it is a target for an attacker: it has a data key per store, two independent checks and a required audit fact.
- A success that cannot be recorded fails. A full spool on one board stops that board's `kv`, which is the same trade as token issuance ([REQ-AUDIT-015](/requirements/audit/#REQ-AUDIT-015)).
- Shared stores break the tenant boundary on purpose. The consequences are accepted and stated: no erasure of a tenant's entries by its exit, and audit records that sit in the caller's tenant, so only the operator sees all of a store.
- KV needs a slice of the core's identity work first: the verifier for foreign tokens in `exchange` (today it verifies a trusted-issuer proof and then refuses it) and the declared tenant in `ExchangeTrust`. The same slice serves the other workload consumers.
- The policy schema is no longer fixed: its next version adds the KV types. A general schema extension is part of block extraction; KV pulls the first piece forward.
- `key@` gains a store scope for data keys, and the sealed grant gains a field. That is the largest change KV makes to the other services.
- The word "store" means both `store@` and a KV store. The glossary names "KV store", and "vault" appears only in the wire paths of the compatibility front.
- A later ESO release can add calls the facade does not answer. The ESO job in CI runs again on each bump of the chart, which is the guard.

## Roads not taken

- **A store owned by one tenant.** It cannot express many-to-many access, and shared stores of an operator would need a tenant to stand for them.
- **Nested stores.** A nested store needs its own key, settings and quota and a rule for what a parent's settings do to a child; a path node gives policy all it needs.
- **Flat stores with rules by string pattern only.** There is no way to say "everything under `infra/prod`" except by `like`, and a new path would escape the rule.
- **One boundary-wide data key for all stores.** The separation between stores would rest on the rule and the address binding alone, with no erasure of one store and one budget of seals for all.
- **A key derived from one boundary key for each store.** Nothing is wrapped and no store can be erased on its own, and the grant check would still need the `stores` field.
- **ESO's webhook provider.** It has no ServiceAccount login, so a token refresher would run in each cluster, and it has no `dataFrom.find`, no metadata and no `cas`.
- **A new native ESO provider for sluis.** It takes months upstream and still needs a front underneath.
- **A full simulation of Vault.** It has no end, its ACL policies and leases clash with Cedar and short-lived tokens, and it goes against [0009](/decisions/0009-export/).
- **Export to a store of record or to a Kubernetes Secret.** Another copy of each secret and another credential in `exporter@`, while ESO can read the original under policy.
- **An AppRole login.** A secret ID is a long-lived client secret, against [0022](/decisions/0022-workload-identity-and-host-enrolment/).
- **A flag on a store that asks for step-up.** A second place for a rule that policy already holds; `auth_age` in a rule does it.
- **A fingerprint of a value in the audit record.** It would let a reader guess a low-entropy secret offline.
- **Start with the boundary-wide key and move to a key per store later.** It would touch every row at the move.
- **A cloud parameter store as ESO's store for good.** ESO reads from sluis itself, so a board installation needs no cloud for its consumers.

## Follow-ups

- The pieces, in the order of the design: the registry rows and the schema; `kv` on the local installation; the store scope of `key@` and the `stores` field of the grant; the facade at Level 1 and its differential suite; `sluis kv`; the slice of workload exchange; the ESO job; the managed prefix of `exchange`; and a measurement on three boards (latency, a list of 10,000 secrets, a value of 256 KiB, a full spool) ([#798](https://github.com/truvity/sluis/issues/798), [#1714](https://github.com/truvity/sluis/issues/1714)).
- The registry change that retires `ClientSecretExport` and leaves six exportable kinds goes with the managed prefix of `exchange` ([#798](https://github.com/truvity/sluis/issues/798)).
- Level 2 of the facade, when a consumer needs it ([#798](https://github.com/truvity/sluis/issues/798)).
