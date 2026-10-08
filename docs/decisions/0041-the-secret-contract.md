# 0041 — The secret contract: two namespaces, one copy, typed documents for what leaves

**Status:** Proposed (the owner's decisions of 2026-10-08, to be confirmed before anything is built); once carried
out it supersedes [0034](0034-exports-go-to-openbao-directly.md) (the export copies and their schedule are retired),
amends [0036](0036-configuration-is-immutable-per-instance.md) (SSM layout v3 becomes v4) and
[0039](0039-the-issuer-generates-confidential-client-secrets.md) (where a generated secret and its previous value live)
**Date:** 2026-10-08

## Context

Layout v3 ([0036](0036-configuration-is-immutable-per-instance.md), [storage layout](../reference/storage-layout.md))
keeps every secret of an installation under one root, `/sluis/<instance>`, in two trees:

```text
/sluis/<instance>/private/config/<name>                  what an operator seeds or the stack generates; sluis reads
/sluis/<instance>/private/credentials/<kind>/<id>/<ref>  what sluis writes (a fresh <ref> per write)
/sluis/<instance>/export/<path>                          copies for consumers, made by the exports controller
```

The OpenBao Secrets adapter mirrors it under `<mount>/<root>/`. A consumer (an External Secrets Operator, a runner
scale set, a relying party) reads `export/<path>`, and the exports controller ([0034](0034-exports-go-to-openbao-directly.md),
`internal/exports`) re-derives each copy from State and Secrets on a schedule: at start, on a State watch, every
`interval` (an hour), and on Lambda every 15 minutes from an EventBridge schedule.

This has three costs. **Every value a consumer reads exists twice**: a generated client secret is the record
`private/credentials/oidc-client/<id>/secret` and the copy `export/<path>`, and a runner App's key is a `<ref>`
under `private/credentials/` and a copy. The two drift between passes, and a copy's freshness is an alert
(`SluisExportStale`) instead of a property. **The contract is undeclared**: the path is whatever the estate wrote in
`exports[].path`, a property may be renamed per export (`properties`), and nothing versions the shape a consumer
depends on. **The boundary is a convention**: whether something is "exported" depends on a policy entry, not on what
the value is, and the same kind of value lives in `private/` for one installation and in `export/` for another.

Separately, two infrastructure choices of the Lambda shape were reopened: the mTLS truststore has a bucket of its own
(`deploy/pulumi/lambda.go`, `newAPI`), and each installation's KMS keys are its own.

Fixed before the alternatives were compared (the owner's decisions, 2026-10-08):

1. Two namespaces, one copy. 2. A typed, versioned public contract. 3. One adapter interface per namespace, two
backends each. 4. Rotation R3. 5. One bucket (D56 b). 6. Any key per purpose; sharing is an estate's choice (D57 b, as
clarified the same day). 7. A migration path that keeps consumers working. 8. Rollout on one estate, then the second.

## Decision

### 1. Two namespaces, one copy (layout v4)

An installation's secrets live in exactly two namespaces under its root:

- **`internal/`**: values only sluis reads. Never exposed, and no reader is ever granted anything under it, on any
  backend. An OAuth provider's client id and secret, the state secret, the recovery password, the console session
  key, people's user tokens (GitHub links), directory credentials, an App's client secret, rotation bookkeeping.
- **`exportable/`**: values that by design leave sluis. This is the public contract (decision 2).

**The rule:** needed by something outside sluis → `exportable`; only sluis uses it → `internal`, **whoever wrote it**.
An operator-seeded confidential client secret is exportable (the relying party holds it too), exactly like a generated
one. Classification is by kind, never by whether a consumer is declared: being at an exportable address grants nobody
anything; the grants on exact addresses do (decision 3).

**There is no second copy and no export schedule.** sluis writes the exportable document as its own storage and reads
it back from there: the token endpoint reads a client's secret from `exportable/oidc-clients/<id>`, and a controller
reads a runner App's key from `exportable/github-runner-apps/<tier>/<org>`. What a consumer reads is what sluis uses,
by construction.

Layout v4, under `<root>` = `/sluis/<instance>` on SSM and `<root>` = `sluis` or `sluis/<instance>` under the KV mount
on OpenBao (as today):

```text
<root>/internal/config/<name>                   operator-seeded and stack-generated inputs (was private/config/)
<root>/internal/credentials/<kind>/<id>/<ref>   sluis-written internal credentials (was private/credentials/)
<root>/internal/rotation/<kind>/<id>            previous values and their grace expiry, rotation bookkeeping
<root>/exportable/<kind>/<id>                   one typed document per address: the public contract
```

Which value goes where (every row of [storage layout](../reference/storage-layout.md) is covered):

| Value | v4 address |
|---|---|
| Google OAuth client, state secret, recovery password, directory keys, Valkey password (`config/`) | `internal/config/<name>`, names unchanged |
| Console session key, directory credentials, an organisation's App key, the link App, GitHub links, Slack workspace credentials | `internal/credentials/<kind>/<id>/<ref>`, unchanged below the prefix |
| A catalogue Slack App's client secret | `internal/credentials/slack-app/<app>/<ref>` |
| A catalogue Slack App's bot token | `exportable/slack-apps/<app>` |
| A catalogue GitHub App's key, once installed | `exportable/github-apps/<app>` |
| A runner App's key, once installed | `exportable/github-runner-apps/<tier>/<org>` |
| A confidential client's secret, generated or operator-seeded (was `config/clients/<id>/secret` or `credentials/oidc-client/<id>/secret`) | `exportable/oidc-clients/<id>` |
| A generated client's previous secret, its expiry, `created`/`rotated`/`orphaned` | `internal/rotation/oidc-clients/<id>` |

**An App's key before installation.** A GitHub App created and not installed has a key and no installation id, so its
contract document cannot be complete. The key stays at `internal/credentials/<kind>/<id>/<ref>` (the pending key, as
today) until installation; the installation writes the exportable document, reads it back, and only then deletes the
pending credential. At steady state there is one copy.

**Instance names.** `internal` and `exportable` join `private` and `export` as names an instance may not take.

### 2. The public contract

An **address** is `exportable/<kind>/<id>`. The kinds and their ids:

| Kind | Id | Today's export source |
|---|---|---|
| `oidc-clients` | the client id | `oidc-client` |
| `github-apps` | the catalogue App id | `github-app` |
| `github-runner-apps` | `<tier>/<org>` | `runner-app` |
| `slack-apps` | the catalogue App id | `slack-app` |

The contract's kinds are plural nouns of their own; the internal record kinds (`internal/port/keys.go`) are unchanged
and are not part of the contract. An id segment that a path cannot hold is spelled `u-` and its bytes in hex, as the
credential layout already does.

**One JSON document per address, typed and versioned.** Every field is a JSON string (an App id is `"12345"`, as the
copies carry it today), so both backends hand a consumer the same text. The document carries `schema`, `"<kind>/v1"`,
and the fields of its kind:

| `schema` | Fields |
|---|---|
| `oidc-clients/v1` | `client-id`, `client-secret` |
| `github-apps/v1` | `app_id`, `installation_id`, `private_key` |
| `github-runner-apps/v1` | `github-app-id`, `github-installation-id`, `github-private-key` |
| `slack-apps/v1` | `bot_token` |

```json
{"schema":"oidc-clients/v1","client-id":"example-rp","client-secret":"…"}
```

**The field names are today's export names, kept.** They are inconsistent (kebab-case, snake_case, a `github-` prefix
on one kind), and a clean set would read better. Keeping them means a consumer's migration is one change per
ExternalSecret (its `remoteRef.key`) and nothing else; normalising is a `v2` of each kind whenever someone needs it,
by the rule below, not a reason to make every consumer change twice now. Consumers ignore `schema`.

**A breaking change is a new schema version beside the old one.** `v1` lives at `exportable/<kind>/<id>`; version
`N ≥ 2` lives at `exportable/<kind>.vN/<id>` (a dot is a legal path character and no kind has one). A release that
introduces `vN` writes both documents for the same value, in the same operation, until a later release stops writing
the old one under [0007](0007-breaking-changes-inside-1x.md)'s deprecation rules. A field added to a document is not
breaking; a field removed, renamed or re-typed is.

**Consumers read single fields.** External Secrets' `remoteRef: {key: <address>, property: <field>}` selects one field
of a JSON parameter on the AWS Parameter Store provider and one key of a KV secret on the Vault/OpenBao provider, so
one ExternalSecret shape works on both backends. To be verified on both providers in the first rollout.

**Each kind is pinned by a test.** Per kind and version: a JSON Schema in the repository and a golden document. The
encoder's output for a fixture must equal the golden, every field the schema names must be present and a string, and
the address a kind encodes to is pinned. Changing a field fails the test unless the schema version moves.

### 3. One adapter interface per namespace, two backends

Two ports replace `port.Secrets`' `export/` special case and `port.Export`:

```go
// Exportable is the public contract: typed documents at addresses.
type Exportable interface {
    Get(ctx context.Context, addr Address) (Document, string, error)       // the version; ErrNotFound
    Put(ctx context.Context, addr Address, doc Document) (string, error)   // identical document: no write, same version
    PutIfVersion(ctx context.Context, addr Address, doc Document, version string) (string, error) // "" = create-only
    List(ctx context.Context, kind string) ([]Address, error)              // addresses, never values
    Versioned
}

// Internal is today's Secrets port rooted at internal/: Get, Put, PutIfVersion, Delete, List.
type Internal interface {
    port.Secrets
    Versioned
}

// Versioned is native version access, for audit and rollback only.
type Versioned interface {
    Versions(ctx context.Context, path string) ([]VersionInfo, error)       // newest first: version and time, no value
    GetVersion(ctx context.Context, path string, version string) ([]byte, error)
}
```

An `Address` is `{Kind, ID}`; a `Document` is `{Schema string; Fields map[string]string}`, encoded by a typed value
per kind (`OIDCClientV1`, …), and `Put` refuses a document whose schema names another kind than the address. The
exportable port has no `Delete`: removal is an operator's tool (`sluisctl` or the backend's CLI), as removal of a copy
is today. Both ports are implemented on **SSM Parameter Store** and **OpenBao KV version 2**; the two namespaces of an
installation are on the same backend in every shape the code builds today (see open question 4).

**Mapping.**

| | SSM | OpenBao KV v2 |
|---|---|---|
| exportable address | SecureString `<root>/exportable/<kind>/<id>` holding the document's JSON (sorted keys, no HTML escaping, as `secretsexport.Encode` writes today) | key `<root>/exportable/<kind>/<id>`; its data are the document's fields, `schema` included, one KV field each (as the OpenBao adapter already stores an `export/` object) |
| internal path | SecureString `<root>/internal/<path>`, values as today (text, or `sluis-b64:` and base64) | key `<root>/internal/<path>`, one field `value` or `value_b64`, as today |
| version | SSM's parameter version | KV's version |
| `PutIfVersion` | create-only is atomic (`Overwrite=false`); with a version it is read-then-write, made safe by the target's lease, as today | `options.cas`, atomic |
| versions | `GetParameterHistory`; a version is read with the `<name>:<version>` selector. SSM keeps the newest 100 and drops the oldest on the next write unless it is labelled, so sluis never labels | `metadata/<key>` and `data/<key>?version=N`; the mount's (or key's) `max_versions`, the estate's setting |
| size | 8 KiB (advanced tier when needed, as today) | the mount's limit |
| key | `kmsKeyId` per namespace: one for `internal`, one for `exportable`, the same key or two (decision 6) | the server's own |

A fixed exportable address is replaced in place, unlike an internal credential's fresh `<ref>`. The `<ref>` scheme
guarded against a writer that lost a compare-and-swap overwriting the winner; for exportable writes the target's
tick lease and `PutIfVersion` over the version that was read do that instead, and on SSM the lease is what makes the
read-then-write safe (already the rule for every SSM writer).

**Permissions.**

- **SSM.** sluis's role: `GetParameter`, `GetParameters`, `GetParametersByPath`, `PutParameter`, `DeleteParameter`,
  `GetParameterHistory` on `<root>/internal/credentials/*`, `<root>/internal/rotation/*` and `<root>/exportable/*`;
  read only on `<root>/internal/config/*`. A consumer's role: `ssm:GetParameter` on the **exact** parameter of each
  address it reads, and `kms:Decrypt` through SSM with `kms:EncryptionContext:PARAMETER_ARN` equal to those same
  parameter ARNs. No `GetParametersByPath`, no wildcard, nothing under `internal/`.
- **OpenBao.** sluis's policy: `create`, `read`, `update`, `patch` on `<mount>/data/<root>/internal/*` and
  `<mount>/data/<root>/exportable/*`, `read` and `list` on the matching `metadata/` paths, and `delete` only where
  internal credentials are deleted today. A consumer's policy: `read` on `<mount>/data/<root>/exportable/<kind>/<id>`
  for each exact address, and nothing else (no `list`, no `metadata/`).
- **`internal/` is never granted to a reader** on either backend. The library's read-policy helper takes a list of
  addresses and emits only exact grants.

### 4. Rotation R3

**`exportable/` holds the current value only.** The previous value and its grace expiry live in
`internal/rotation/<kind>/<id>`. The order of a rotation:

1. write `internal/rotation/<kind>/<id>` with `previous` = the current value as read, its expiry and the bookkeeping;
2. write the new current document at `exportable/<kind>/<id>` with `PutIfVersion` over the version read in step 1.

A failure between the two leaves the old value current and a `previous` equal to it: nothing is broken, and a retry
starts again from step 1. Both steps run under the target's lease (for clients, the per-client lock of
`internal/clientcreds`).

**Only the verifier needs the previous value**, so only it reads `internal/rotation/`:

- **OIDC client secrets**: sluis is the verifier. The token endpoint accepts the current secret, or the previous one
  until its expiry. It reads both documents and caches them **as a pair**; a presented secret that matches neither is
  checked once more against a fresh, uncached read of both before it is refused, so a replica that cached the
  rotation document before step 1 cannot refuse the old secret during the overlap. The v3 record's `created`,
  `rotated` and `orphaned` move to the rotation document.
- **GitHub App keys**: GitHub is the verifier and holds both public keys during the overlap. sluis adds the new key,
  writes it as current, waits the overlap, and deletes the old key at GitHub; the rotation document records the old
  key's fingerprint and when to delete it, never the old private key (see open question 5).
- **Slack bot tokens**: Slack's token rotation handles the overlap (see open question 6).

**Native versions are kept on both backends for audit and rollback only.** Nothing depends on them: no read path
consults an older version, and losing history loses no function. Rolling a value back is an operator's act (read a
version, write it as current), audited like a rotation.

### 5. One bucket for the truststore (D56 b)

The Lambda shape's mTLS truststore moves into the installation's blob bucket, under `truststore/client-ca.pem`.

- **The bucket policy denies writes to the prefix** to every principal but the deployer: `s3:PutObject`,
  `s3:DeleteObject`, `s3:DeleteObjectVersion`, `s3:PutObjectAcl` and `s3:PutObjectTagging` on `<bucket>/truststore/*`,
  with `ArnNotEquals aws:PrincipalArn` the deployer role ARNs the estate passes. The function's role keeps its grant on
  `<bucket>/*` for its own prefixes; the explicit deny wins over it.
- **The object stays versioned, and the custom domain pins its version** (`TruststoreVersion`), as today. The blob
  bucket therefore becomes versioned whenever an API is declared. Its own objects (`reports/`, `snapshots/`) gain a
  lifecycle rule that expires noncurrent versions after a day, so versioning does not grow the bill; `truststore/` has
  none.
- **Encryption.** The truststore object is written with SSE-S3 explicitly, whatever the bucket's default, until it is
  verified that API Gateway reads an SSE-KMS truststore (a rollout check).

**The library.** `NewStorage` owns the bucket policy (S3 takes one per bucket), so `StorageArgs` gains the deployer
role ARNs and the truststore prefix's deny, and turns versioning on when asked for a truststore. `APIArgs.TruststoreBucketName`
is deprecated in the release that ships this (accepted, ignored, a warning naming the migration) and removed in a later
minor release marked **Breaking:**. The outputs `TruststoreBucketName` and `TruststoreURI` keep their names and point
at the blob bucket.

**Migration.** One `pulumi up` creates the object in the blob bucket and updates the domain's truststore URI and
version in place; after the mTLS checks pass, the old bucket (created with `Protect`) is unprotected, every version
emptied, and deleted.

### 6. Keys: any key per purpose; sharing is an estate's choice (D57 b)

**sluis requires nothing about how keys are shared.** The library and the binary accept a key per purpose, by ARN or
alias: the parameter key of `internal/`, the parameter key of `exportable/`, the blob bucket's key (or none, SSE-S3),
the symmetric key of the wrapped key ring, and the asymmetric signing keys. An estate may pass a different key for
every purpose, or one key for several, with or without aliases. Where an estate passes no key, the library does for
that purpose what it does today. The library never creates a shared key; it emits the IAM grants and the conditions it
needs, and exports the key-policy statements an estate merges into a shared key's policy (as `WrappedKeyPolicyStatements`
does today), for example a statement reserving decryption under `PARAMETER_ARN` like `<root>/internal/*` to sluis's
roles.

**Encryption context is available, and optional where sluis chooses it.**

- **Where AWS sets it, it is always there.** SSM puts `PARAMETER_ARN` in every SecureString's context, so `internal/*`
  versus `exportable/*` is a condition on that key; S3 SSE-KMS puts `aws:s3:arn` (the object ARN, or the bucket ARN
  with an S3 Bucket Key). Neither can be turned off or extended by sluis.
- **Where sluis makes the call, it already sends one.** The `kms-wrapped` key ring wraps and unwraps under
  `{purpose: sluis-signing, alg, kid}` (`internal/issuer/wrapped.go`), and the library's grants and key policy pin it.
  That stays on, always: it binds a ciphertext to its key-ring entry, and the key policies already shipped rely on it.
- **New and optional: the instance.** `signingKey.kmsWrapped.instanceContext: true` adds `sluis:instance: <instance>`
  to the wrapped key ring's context. **Off by default**: it matters only when two installations share one symmetric
  key and must not open each other's ring, and turning it on changes the context of new entries, which a key policy
  written for three keys would refuse. It is a code change: each key-ring entry records the context it was made under,
  new entries use the configured one, and an existing entry is opened with the context it records for as long as it
  is unwrapped at all (a new key is generated at least every 7 days, so within days). No `ReEncrypt` is needed. The library's context-keys list
  admits `sluis:instance` when the option is set.

**An example deployment, not a requirement.** Our estates run two shared keys, one symmetric and one asymmetric, under
several aliases, with permissions split by context: SSM's `PARAMETER_ARN` separates `internal/` from `exportable/` and
one installation from another; the bucket ARN separates blob objects; the wrapped key ring's `purpose` (and, between
installations, `sluis:instance`) separates signing. The limits of that choice are accepted consequences, stated below.

### 7. Migration v3 → v4

**A command, not a start-time upgrade.** `sluis migrate secrets-layout --to v4` (with `--dry-run`, a JSON report that
names paths and never values, `--kms-key-internal`/`--kms-key-exportable`, and `--overwrite`), the same shape as
`sluis migrate ssm-layout` ([0031](0031-a-generic-migration-tool.md)). Rejected: an upgrade at start. On Lambda every
cold start would race it, the running role would need write grants on both layouts permanently, and the move would
happen at the next deploy instead of when an operator chose it. The server binary runs it because it holds the store
credentials; `sluisctl` speaks to the API with a person's token and holds none.

**What it does.** For every v3 value, it writes the v4 address of the table in decision 1 (an operator-seeded
`config/clients/<id>/secret` becomes the document `{schema, client-id, client-secret}`; a generated client's v3 record
splits into the exportable document and the rotation document; a Slack App's credential splits into the internal
client secret and the exportable bot token; App ids come from the State record), encrypts it under the target key,
reads it back and compares. It writes where the destination is absent, refuses one that holds another value unless
`--overwrite`, and deletes nothing. `--delete-v3` is a separate, later run that deletes only v3 values whose v4 address
holds the same value.

**The order, which keeps every consumer working throughout:**

1. **Release with `secrets.layout`.** `v3` (the default: today's behaviour), `transition` (read v4 first, fall back to
   v3; every write goes to v4 and then to the v3 path; the exports controller still runs, so `export/` stays current),
   `v4` (v4 only).
2. **Grants first.** The estate adds the v4 grants (sluis's role on `internal/`, `exportable/`; each consumer on its
   exact addresses) beside the v3 ones, and the key-policy statements if it shares keys.
3. **`transition`, then backfill.** Deploy with `secrets.layout: transition`, then run the migration (`--dry-run`
   first). From here v4 is complete and current.
4. **Flip consumers.** Each ExternalSecret changes `remoteRef.key` from `<root>/export/<path>` to
   `<root>/exportable/<kind>/<id>`; the property is unchanged. One change per ExternalSecret, at the estate's pace.
5. **`v4`.** Deploy with `secrets.layout: v4` and remove `exports:` and `ports.export`. The exports controller is not
   started, the Lambda `exports` event and its schedule are removed, and no v3 path is written again. Rolling back to
   `transition` from here first runs the migration in reverse (`--to v3`), since v3 stopped being written.
6. **Clean up.** `--delete-v3`, then remove the v3 grants and each consumer's `export/` grant.

A later minor release removes `transition`, `exports:`, `ports.export`, `port.Export` and its adapters, the
`{"kind":"exports"}` event, the `export` lease, `SluisExportFailing` and `SluisExportStale`, marked **Breaking:**. A
policy row's `secret: <name>` for a confidential client is accepted in `v4` with a warning (the secret is at
`exportable/oidc-clients/<id>`), and refused in that same later release.

### 8. Rollout

First on one estate, the reference estate; the second follows only once the first is confirmed. **Confirmed on the
first estate** means every one of these was observed, on the record:

- The migration's dry run reported no conflict, the real run wrote as many v4 values as the v3 count, and every one
  read back equal.
- Every consumer's ExternalSecret is `Ready` from its v4 address, and the value it synced hashes the same as the
  value it synced from v3. `remoteRef.property` verified on the Parameter Store provider and, where the estate has
  one, on the Vault/OpenBao provider.
- A consumer's role is refused reading any `internal/` value (AccessDenied from SSM, 403 from OpenBao), refused an
  exportable address it was not granted, and on SSM refused `kms:Decrypt` for an `internal/` parameter by the key's
  `PARAMETER_ARN` condition.
- A client-secret rotation with an overlap: the relying party picked up the new value, sign-ins never failed,
  `sluis.client_secret.auth{previous}` fell to zero before the expiry, and a failure injected between the two writes
  left the old secret working.
- The custom domain serves mutual TLS from `truststore/` in the blob bucket at the pinned version; the function's role
  is refused `PutObject` and `DeleteObject` there; the old truststore bucket is gone.
- Signing is unbroken across the key-ring change: the JWKS kept every key a live token names, and, where
  `instanceContext` was turned on, new entries carry it and old ones still open until they retire.
- `secrets.layout: v4` ran for seven days with no write to a v3 path (CloudTrail on SSM, the audit device on OpenBao),
  no exports controller, and the v3 values deleted.

## Consequences

- **One copy, read by sluis itself.** A consumer reads exactly what sluis uses, so "the copy is stale" stops existing
  as a failure mode, along with its alerts, its lease and its schedule.
- **The contract is in the repository.** Addresses, field names and schema versions are pinned by tests and changed
  under [0007](0007-breaking-changes-inside-1x.md). An estate no longer chooses an export's path or renames its fields.
- **The boundary is structural.** "May a reader see this?" is answered by the namespace, and grants are on exact
  addresses, so adding a consumer never widens what another consumer sees.
- **Rotation costs a second read.** The token endpoint reads two documents per client (cached as a pair) instead of
  one record, and a refused secret costs one uncached re-read.
- **Exportable values are replaced in place.** The `<ref>` scheme's protection is now the lease and `PutIfVersion`;
  on SSM that remains a read-then-write that relies on the lease, as every SSM writer does today.
- **A Kubernetes installation whose confidential client secrets came from a projected file** must seed them into the
  exportable store instead: the token endpoint reads `exportable/oidc-clients/<id>`, not a `secrets.source` name. A
  `file` or `env` source keeps delivering `internal/config` names.
- **The blob bucket becomes versioned** where an API is declared, with a lifecycle rule for its own prefixes, and the
  deployer's role ARNs become an input of the storage component.
- **New SSM action.** sluis's role needs `ssm:GetParameterHistory` for version access.
- **Limits of sharing keys, accepted by an estate that shares them (the example deployment above):**
  - disabling or scheduling the deletion of a shared key affects every purpose on it at once, signing included;
  - a service that sends no encryption context (OpenBao's `awskms` seal, for example) needs a statement with no
    context condition, which is as broad as the key policy lets it be;
  - asymmetric `Sign` has no encryption context, so signers on one asymmetric key cannot be separated by context, only
    by principal;
  - an alias is not a permission boundary: a grant or key policy names the key, and every alias of a key reaches the
    same material.
- **The honest boundary.** A value an operator copies by hand out of `exportable/` into another system is outside this
  contract, and the disaster-recovery bundles have no place in it yet (open question 1).

## Alternatives considered

- **Keep the export copies, add a schema.** Rejected: the drift, the schedule and the second copy are the problem, and
  a schema does not remove them.
- **Store previous and current together in the exportable document** (v3's record shape, exposed). Rejected: a consumer
  would be handed a value it must never use, and the rotation bookkeeping is sluis's alone.
- **Normalise the field names now.** Rejected for v1: every consumer would change its property as well as its key, for
  no function. A `v2` can do it under the versioning rule.
- **One address per field** (an SSM parameter per field). Rejected: a document would no longer change atomically, and
  a GitHub App's id, installation and key could be read out of step.
- **Start-time layout upgrade.** Rejected in decision 7.
- **Rely on native versions for the previous value.** Rejected: SSM's history is capped and labelled versions block
  writes, KV's `max_versions` is the estate's setting, and a rotation that depends on either breaks when someone tunes
  it. Versions stay audit and rollback only.
- **Narrow the function's S3 grant instead of denying in the bucket policy.** Rejected as the only control: an
  identity policy attached later could widen it again; the bucket's explicit deny holds whatever is attached.

## Open questions for the owner

1. **Disaster-recovery bundles.** `bundle` exports (`workspace-credentials`, `github-apps`, `github-links`,
   `github-runner-apps`, `github-catalogue-apps`, `slack-credentials`, `slack-records`) copy internal values out for
   restore. With one copy and no schedule, are they retired (recovery from the backends' own backups), or kept as a
   separate operator-run backup command outside this contract?
2. **Exports into another OpenBao namespace.** An export entry may name a `namespace` (a tier's runner App sent to
   another namespace). A v4 document lives in sluis's own namespace; does a consumer in a sibling namespace get a
   grant there, or does the address need a namespace of its own?
3. **`properties` renames.** A consumer that reads a renamed property today must change its `property` as well as its
   `key`. Accept that, or carry renames as per-consumer aliases?
4. **Mixed backends.** A deployment may keep its Secrets on SSM and send exports to OpenBao (`ports.export: openbao`).
   Do its consumers move to SSM, or may `exportable/` sit on another backend than `internal/`?
5. **GitHub App key rotation.** No code in this repository creates or deletes a GitHub App's private key, and GitHub's
   public REST API is not known to offer either (to verify). If it does not, the add and delete steps are an
   operator's in GitHub's UI and sluis records and switches; is that the intended shape?
6. **Slack bot tokens.** The catalogue Apps' manifest sets `token_rotation_enabled: false`
   (`internal/slackapp/catalogue`), so Slack's rotation does not apply today, and turning it on makes a bot token
   expire every 12 hours, which an hourly consumer refresh would outrun. Keep rotation off (a reinstall replaces the
   token), or turn it on with sluis refreshing and consumers refreshing faster?
7. **Operator-seeded client secrets.** The operator must now write a JSON document (`schema`, `client-id`,
   `client-secret`) instead of a bare value. Is the backend's CLI enough, or should `sluisctl` write it?
8. **The deployer principal.** Which role ARNs count as "the deployer" for the truststore deny on each estate (the CI
   role alone, or also a break-glass role)?
