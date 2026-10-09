# 0041 — The secret contract: internal and external, one storage module, keys by purpose

**Status:** Accepted (2026-10-08). Once
carried out it amends [0034](0034-exports-go-to-openbao-directly.md) (the export copies, their schedule and the
recovery bundles are retired), [0036](0036-configuration-is-immutable-per-instance.md) (layout v3 becomes v4) and
[0039](0039-the-issuer-generates-confidential-client-secrets.md) (where a generated secret and its previous value
live). Its companion [0042](0042-one-repository-one-release-train.md) moves audit into this repository and decides
how audit is installed.
**Date:** 2026-10-08

Layout v3 ([0036](0036-configuration-is-immutable-per-instance.md), [storage layout](../reference/storage-layout.md))
keeps an installation's secrets under `private/config/`, `private/credentials/` and `export/`, and the exports
controller ([0034](0034-exports-go-to-openbao-directly.md)) re-derives every `export/` copy on a schedule. Every value
a consumer reads therefore exists twice and the two drift between passes; the path and the field names of a copy are
whatever an estate wrote in `exports:`, so nothing versions the shape a consumer depends on; and whether a value may
leave sluis is a policy entry rather than a property of the value. Separately, each installation's KMS keys are its
own, named by ARN, and the Lambda shape's mutual-TLS truststore has a bucket of its own. This record replaces all of
that with the design below.

## Decisions

1. **Two namespaces, one copy (layout v4).** An installation's secrets live under `<root>/internal/…`, read by sluis
   only and never granted to anyone, and `<root>/external/<kind>/<id>`, the public contract. The rule: a value needed
   by something outside sluis is external; a value only sluis uses is internal, **whoever wrote it** (an
   operator-seeded confidential client secret is external, exactly like a generated one). Each value is stored once,
   and sluis reads an external value from its external address, so what a consumer reads is what sluis uses. The
   exports controller, `ports.export`, the `exports:` policy section, the `exports` event and its schedule, and the
   recovery bundles are retired.

2. **A typed, versioned contract for what leaves.** There are three kinds: `oidc/<client>` (fields `client-id`,
   `client-secret`), `github/<app>` (`app_id`, `installation_id`, `private_key`; a runner App is
   `github/runner-<tier>-<org>`, so a catalogue App's name may not start with `runner-`) and `slack/<app>`
   (`bot_token`). Each document carries `"schema": "<kind>/v1"` and is pinned by a JSON Schema and a golden test. A
   breaking change is a new address, `external/<kind>.v2/<id>`, written beside the old one for a deprecation window.
   No field is renamed per consumer: a consumer names its own Secret keys through `remoteRef.property`.

3. **One backend per installation, grants on the consumer's side.** An installation's `internal/` and `external/` are
   on one backend. All of `external/` lives in one AWS account under one key alias, or in one OpenBao namespace, and
   each consumer is granted its exact addresses on its own side (its own role's policy, its own OpenBao policy). Being
   at an external address grants nobody anything.

4. **Storage in three layers, in a shared module.** The module `github.com/truvity/sluis/storage` holds two packages,
   `state` and `keys`, and imports neither sluis nor audit. `state.Store` is untyped bytes (`Get`, `GetRev`, `Put`
   only if unchanged, `Delete`, `List`, `Child(prefix, options)`), implemented on SSM Parameter Store, OpenBao KV
   version 2, and S3-compatible blob storage (an endpoint and credentials, so R2 works). `state.Value[T]` is one typed
   key with `Get`, `Put` and `Rotating(grace)`, built on the backend's native versions. sluis keeps `Internal` and
   `External` types with domain methods over them. sluis's existing DynamoDB State port is called **State (DynamoDB)**
   in the documentation from now on, so the two are never confused.

5. **A rotation is one `Put`.** `Rotating(grace)` returns the current value and, while the current revision is younger
   than the grace period, the revision before it, read from native versions. There is no rotation bookkeeping and no
   protection layer. The OIDC token check accepts the current secret or the previous one while `Rotating` returns it;
   consumers mirror the latest version only. GitHub App keys and Slack tokens are rotated from the console in a few
   clicks, like create and install, with operator steps where the vendor has no API. Slack token rotation stays off: a
   reinstall replaces the token.

6. **One GitHub App model.** Catalogue, runner and roster Apps are one model with an `export` property that places the
   App's key at `external/github/<app>` or under `internal/`. A pending key (an App created and not installed) stays
   internal until the App is installed.

7. **Keys by purpose, named by alias.** Code asks for a key by a short purpose: sluis `sign` (the token-signing key
   ring); audit `seal`, `pseudonym`, `conceal` and `archive`. The installation's configuration maps each purpose to a
   KMS key alias or an OpenBao transit key name, `keys: {adapter: kms, sign: alias/sluis-<instance>-sign}`, with the
   long form `{key, context}` only to change the default context. sluis's and audit's configuration names aliases
   only, never ARNs. The estate's infrastructure code creates keys, aliases and key policies; the libraries take alias
   names.

8. **Encryption context is on by default.** Where sluis or audit makes the KMS call, the context is
   `{instance, purpose}` unless the configuration says `off` or gives an explicit map. Where AWS sets the context
   (SSM's `PARAMETER_ARN`, S3's bucket ARN) it is always there. A wrapped signing-ring entry keeps the context it was
   made under (today's `{purpose: sluis-signing, alg, kid}`), so nothing is re-wrapped.

9. **Sharing keys is the estate's choice.** Any key per purpose works: one key per purpose, one for several, one per
   estate. The reference deployment shares two keys per estate, one symmetric and one asymmetric, and accepts what
   that costs (Consequences). On KMS, audit's pseudonyms use an HMAC data key per purpose and tenant, wrapped under the
   symmetric key with the context `{purpose, tenant}`. On OpenBao transit there is one key per purpose by default; a
   derived key with a context is optional.

10. **Three deployment shapes.** **AWS** (the default): Lambda, API Gateway, ACM and Route 53, in
    `deploy/pulumi/edge/aws`. **AWS behind Cloudflare**: the separate module `deploy/pulumi/edge/cloudflare`, with DNS
    and ACM validation in Cloudflare, Authenticated Origin Pulls, and mutual TLS whose truststore is one versioned
    object under `truststore/` in the installation's blob bucket, writable only by the infrastructure apply
    identities. **Kubernetes with OpenBao**: the charts behind the cluster's gateway (no truststore), DynamoDB and S3
    kept, OpenBao KV and transit through a JWT login with the projected ServiceAccount token, and HTTP to the in-cluster
    audit writer. `TruststoreBucketName` is deprecated, then removed. The core library installs audit by default.

11. **Blobs stay in a bucket.** The installation's blob bucket keeps reports and directory snapshots, because Lambda's
    `/tmp` is per instance and a DynamoDB item caps at 400 KB. It is S3 or S3-compatible (R2); a compressed write sends
    a precomputed `ChecksumSHA256`, because R2 refuses the SDK's chunked checksum together with `Content-Encoding`.

12. **Backup and restore are whole-installation.** A configured target (an S3-compatible bucket) and an optional extra
    key (a KMS alias or a transit key). The scope is State (DynamoDB), `internal/`, `external/` and the blobs.
    `sluis backup` runs on demand and from a scheduled backup event; `sluis restore --from <backup>` restores into an
    empty installation and reads every value back. There is no per-secret command.

13. **Migration by command, rollout estate by estate.** `sluis migrate secrets-layout --to v4` is one step of an
    ordered move: the estate adds the grants, the command backfills v4 with read-back, consumers switch, v3 writes
    stop, and `--delete-v3` removes the old values. An estate that never ran v3 migrates its legacy store straight into v4.
    The reference estate migrates first and must pass the checks under Rollout; the second estate follows.

## Details for implementers

### Layout v4

`<root>` is `/sluis/<instance>` on SSM and `sluis` or `sluis/<instance>` under the KV mount on OpenBao, as today.

```text
<root>/internal/config/<name>                   operator-seeded and stack-generated inputs (was private/config/)
<root>/internal/credentials/<kind>/<id>/<ref>   sluis-written internal credentials (was private/credentials/)
<root>/external/<kind>/<id>                     one typed document per address: the public contract
<root>/external/<kind>.v<N>/<id>                version N ≥ 2 of a kind, beside version 1
```

Below `internal/`, the paths are sluis's own business and not a contract; this record keeps today's names so the
migration is a prefix move. A later release may move an internal credential to a fixed address under `Value[T]`
without a contract change.

| Value | v4 address |
|---|---|
| Google OAuth client, state secret, recovery password, directory keys, Valkey password | `internal/config/<name>`, names unchanged |
| Console session key, directory credentials, the link App, GitHub links, Slack workspace credentials | `internal/credentials/<kind>/<id>/<ref>` |
| A GitHub App (catalogue, runner or roster) with `export: false`, or any App not yet installed | `internal/credentials/<kind>/<id>/<ref>` |
| A catalogue GitHub App with `export: true`, once installed | `external/github/<app>` |
| A runner App, once installed (`export: true` is its default) | `external/github/runner-<tier>-<org>` |
| A catalogue Slack App's client secret | `internal/credentials/slack-app/<app>/<ref>` |
| A catalogue Slack App's bot token | `external/slack/<app>` |
| A confidential client's secret, generated or operator-seeded (was `config/clients/<id>/secret` or `credentials/oidc-client/<id>/secret`) | `external/oidc/<client>` |

`internal` and `external` join `private` and `export` as names an instance may not take. An id segment a path cannot
hold is spelled `u-` and its bytes in hex, as the credential layout already does.

### Documents

One JSON document per address. Every field is a JSON string (an App id is `"12345"`), so both backends hand a
consumer the same text, and `schema` is a field like the others.

```json
{"schema":"oidc/v1","client-id":"example-rp","client-secret":"…"}
{"schema":"github/v1","app_id":"12345","installation_id":"67890","private_key":"<PEM>","webhook_secret":"…"}
{"schema":"slack/v1","bot_token":"…"}
```

`webhook_secret` of `github/v1` is optional: it is present only for a catalogue App whose entry declares a `webhook`
(see *Amendment: a webhook secret*). A consumer that verifies deliveries (Argo CD, Kargo) reads it with
`remoteRef: {key: github/<app>, property: webhook_secret}`, and one that does not ignores it.

Per kind and version the repository holds a JSON Schema and a golden document. The encoder's output for a fixture
must equal the golden, every field the schema names must be present and a string, and the address the kind encodes to
is pinned; changing a field fails the test unless the schema version moves. Adding a field is not breaking; removing,
renaming or re-typing one is, and takes a `vN` address written beside the old one in the same operation until a later
release stops writing the old one under [0007](0007-breaking-changes-inside-1x.md).

A consumer reads single fields with External Secrets' `remoteRef: {key: <address>, property: <field>}` and names the
key of its own Secret in the ExternalSecret's `data[].secretKey` or template. `property` selects a field of a JSON
parameter on the Parameter Store provider and a key of a KV secret on the Vault/OpenBao provider, so one
ExternalSecret shape works on both; the first rollout verifies it on both providers.

### The `state` package

```go
package state

type Revision struct {
    ID       string    // the backend's version: SSM's and KV's integer, S3's version id
    Prev     string    // the revision before it, "" for the first
    Modified time.Time // when this revision was written
}

type Store interface {
    Get(ctx context.Context, key string) ([]byte, Revision, error)          // ErrNotFound
    GetRev(ctx context.Context, key string, rev string) ([]byte, Revision, error)
    Put(ctx context.Context, key string, value []byte, ifRev string) (Revision, error) // "" = create-only; ErrConflict
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]string, error)              // keys, never values
    Child(prefix string, opts ...Option) Store                              // e.g. the key alias for the subtree
}

type Value[T any] struct{ /* store, key, codec */ }

func NewValue[T any](s Store, key string, c Codec[T]) Value[T]
func (v Value[T]) Get(ctx context.Context) (T, Revision, error)
func (v Value[T]) Put(ctx context.Context, x T, ifRev string) (Revision, error) // identical value: no write
func (v Value[T]) Rotating(ctx context.Context, grace time.Duration) (cur T, prev *T, err error)
```

`Rotating` returns `prev` when the current revision's `Modified` is within `grace` of now and a previous revision
exists. `Put` of a value identical to the current one writes nothing, so a no-op cannot push the previous revision
out of reach. Two rotations inside one grace period leave only the latest previous value; that is accepted.

| | SSM Parameter Store | OpenBao KV v2 | S3-compatible |
|---|---|---|---|
| value | SecureString; a document is its JSON (sorted keys, no HTML escaping) | one KV field per document field, `schema` included; an internal value is one field `value` or `value_b64`, as today | an object |
| revision | the parameter's version | the KV version | the object's version id (versioning on) |
| previous revision | `GetParameterHistory`, or `<name>:<version>`; SSM keeps 100 and sluis never labels a version, so a write never blocks | `data/<key>?version=N`; the mount's `max_versions` must be at least 2 | `ListObjectVersions` |
| `Put` if unchanged | create-only is atomic (`Overwrite=false`); an update is read-then-write, safe under the caller's lease as every SSM writer is today | `options.cas`, atomic | `If-None-Match: *` / `If-Match`, atomic |
| key | the `Child`'s KMS alias (`internal/` and `external/` may use the same alias or two) | the server's own | SSE-KMS alias or SSE-S3; R2 its own |

The conformance suite runs every backend through the same tests, including the lease rule on SSM and a refusal at
start when an OpenBao mount reports `max_versions: 1`.

### sluis's `Internal` and `External`

```go
type Internal struct{ s state.Store } // rooted at <root>/internal
func (i Internal) RecoveryPassword() state.Value[string]
func (i Internal) StateSecret() state.Value[[]byte]
func (i Internal) OAuthProvider() state.Value[OAuthClient]
// … one method per internal value

type External struct{ s state.Store } // rooted at <root>/external
func (e External) OIDC(client string) state.Value[OIDCv1]
func (e External) GitHubApp(name string) state.Value[GitHubv1]          // refuses a name starting with "runner-"
func (e External) GitHubRunnerApp(tier, org string) state.Value[GitHubv1]
func (e External) SlackApp(name string) state.Value[Slackv1]
```

These replace `port.Secrets`' `export/` special case, `port.Export` and its adapters. The token endpoint reads
`External.OIDC(client)`; the runner controller reads `External.GitHubRunnerApp(tier, org)`.

### Rotation

- **OIDC client secrets.** sluis is the verifier. The token check calls `Rotating(grace)` (the grace is
  [0039](0039-the-issuer-generates-confidential-client-secrets.md)'s overlap) and caches the pair. A presented secret
  that matches neither is checked once more against a fresh, uncached read before it is refused, so a replica that
  cached the pair before the rotation cannot refuse the old secret during the overlap. 0039's `created` and `rotated`
  are the first and current revisions' `Modified`, and `orphaned` is derived from policy (an address with no client
  row); none of them is stored.
- **GitHub App webhook secrets** do not overlap, and the order of a rotation is what keeps deliveries from failing
  (*Amendment: a webhook secret*, below).
- **GitHub App keys.** GitHub is the verifier and accepts every key the App has. GitHub's public API is not known to
  create or delete an App's private key, and how GitHub hands over a new key during a console flow is to verify. The
  console guides the operator: generate a key in the App's settings, hand it to the console, which checks it by
  minting an App JWT and calling `GET /app`, writes it with one `Put`, and, once consumers have mirrored it, shows the
  old key's fingerprint for the operator to delete in GitHub.
- **Slack bot tokens.** The catalogue manifests keep `token_rotation_enabled: false`. A reinstall from the console
  replaces the token with one `Put`.
- **Consumers** mirror the latest version only; an ExternalSecret's `refreshInterval` shorter than the grace is what
  keeps a relying party inside the overlap.
- **Rollback** of a value is an operator's act: read a revision, `Put` it as current, audited like a rotation.

### Keys

```yaml
keys:
  adapter: kms                          # or openbao (transit)
  sign: alias/sluis-<instance>-sign     # short form: default context {instance, purpose}
  # sign: {key: alias/…, context: off}  # long form: off, or an explicit map
```

`sign` replaces `signingKey.kmsWrapped.keyId`: the symmetric key that wraps each ring entry. The direct `kms` signing
adapter's key lists take aliases as well. audit's configuration has the same block with `seal` (asymmetric: seals are
ES384 signatures), `pseudonym`, `conceal` (keeps the identifier behind a pseudonym readable where the law requires) and
`archive` (the archive objects' SSE-KMS key, whose context S3 sets).

- **Context.** A symmetric call sluis or audit makes carries `{instance: <instance>, purpose: <purpose>}`. `Sign` has no
  context, so `seal` and the direct `kms` adapter carry none. A wrapped ring entry records the context it was made
  under; an entry that records none is today's and is opened with `{purpose: sluis-signing, alg, kid}`. New entries use
  the configured context, and the ring turns over within `rotateEvery` (at most 7 days) plus `retain`, so no
  `ReEncrypt` is ever needed. The library's key-policy statements admit both contexts for that turnover.
- **Pseudonyms on KMS.** One HMAC data key per pseudonymisation purpose (today's `keys.Purpose`, a profile's name) and
  tenant, generated with `GenerateDataKey` under the `pseudonym` key with context `{purpose, tenant}` and kept wrapped
  in the writer's key directory. Erasure destroys the wrapped key.
- **OpenBao transit.** One transit key per purpose by default. A derived key (`derived: true`) with a context is
  optional; whether an ACL's `allowed_parameters` can pin the `context` parameter is to verify.
- **Aliases only.** An alias resolves in the caller's account. The Pulumi library resolves an alias to its key at
  deploy time for the IAM statements it emits; the configuration it renders names the alias.

### Grants

- **SSM.** sluis's role: `GetParameter`, `GetParameters`, `GetParametersByPath`, `GetParameterHistory`, `PutParameter`,
  `DeleteParameter` on `<root>/internal/credentials/*` and `<root>/external/*`; read only on `<root>/internal/config/*`.
  A consumer's role: `ssm:GetParameter` on the exact parameter of each address it reads, and `kms:Decrypt` with
  `kms:EncryptionContext:PARAMETER_ARN` equal to those parameters. No path reads, no wildcard, nothing under
  `internal/`.
- **OpenBao.** sluis's policy: `create`, `read`, `update`, `patch` on `<mount>/data/<root>/internal/*` and
  `<mount>/data/<root>/external/*`, `read` and `list` on the matching `metadata/`, `delete` where internal credentials
  are deleted today. A consumer's policy: `read` on `<mount>/data/<root>/external/<kind>/<id>` for each exact address,
  in the namespace that holds `external/`, and nothing else.
- **KMS.** The key policy of a key that holds `internal/` reserves decryption under `PARAMETER_ARN` like
  `<root>/internal/*` to sluis's roles; the library exports that statement for an estate to merge, as it exports
  `WrappedKeyPolicyStatements` today.

### Deployment shapes

- **AWS** (`deploy/pulumi/edge/aws`): API Gateway's custom domain, an ACM certificate validated in Route 53, and the
  Route 53 records. No mutual TLS.
- **AWS behind Cloudflare** (`deploy/pulumi/edge/cloudflare`, a Go module of its own so the core library does not
  depend on the Cloudflare provider): the Cloudflare records (proxied), the ACM certificate's validation records in
  Cloudflare, Authenticated Origin Pulls on the zone, and mutual TLS on the custom domain whose truststore holds the
  origin-pull CA.
  - The truststore is `truststore/client-ca.pem` in the blob bucket, versioned, and the custom domain pins its version
    (`TruststoreVersion`). The bucket becomes versioned; a lifecycle rule expires noncurrent versions of `reports/`
    and `snapshots/` after a day, and none for `truststore/`.
  - The bucket policy denies `PutObject`, `DeleteObject`, `DeleteObjectVersion`, `PutObjectAcl` and
    `PutObjectTagging` on `truststore/*` to every principal whose `aws:PrincipalArn` is not one of the apply
    identities the estate passes: the CD role, the operator's admin role and the break-glass role at first, the CD
    and break-glass roles later. The explicit deny wins over the function's grant on its own prefixes.
  - The object is written with SSE-S3 until it is verified that API Gateway reads an SSE-KMS truststore.
  - API Gateway reads a truststore only from S3, so an installation whose blobs are on R2 keeps one small, versioned S3
    bucket for the truststore alone, with the same policy.
  - `StorageArgs` gains the apply identities and the truststore deny (`NewStorage` owns the bucket policy).
    `APIArgs.TruststoreBucketName` is accepted with a warning in the release that ships this and removed in a later
    minor release marked **Breaking:**; `TruststoreBucketName` and `TruststoreURI` outputs keep their names.
- **Kubernetes with OpenBao**: the chart behind the cluster's gateway; State (DynamoDB) and the blob bucket through
  workload identity ([0030](0030-workload-identity-on-both-platforms.md)); KV and transit through OpenBao's JWT auth
  method with the projected ServiceAccount token; audit records over HTTP to the in-cluster writer.
- The core library installs audit with every installation unless told otherwise
  ([0042](0042-one-repository-one-release-train.md)).

### Blobs, backup and restore

The S3-compatible `state.Store` serves the blob bucket, so reports and snapshots use the same backend code as R2 and
S3. A write with `Content-Encoding` computes the SHA-256 itself and sends `ChecksumSHA256`, never the SDK's chunked
trailer checksum. R2 credentials are an `internal/config/<name>` input.

A backup is one directory per run under the target, `<instance>/<timestamp>/`, holding State (DynamoDB) items, every
`internal/` and `external/` value at its current revision, and the blobs, with a manifest of keys and hashes (never
values in the manifest). With an extra key, the payload is encrypted under a data key from it, context
`{instance, purpose: backup}`. The backup event is `{"kind":"backup"}` from an EventBridge schedule on Lambda and a
CronJob running `sluis backup` on Kubernetes. `sluis restore --from <backup>` refuses an installation that holds any
State item or secret, writes everything, and reads every value back before it reports success. Revision history is
not restored.

### Code this implies

- Remove `internal/exports`, `port.Export` and its adapters, `ports.export`, `exports:` in policy, the
  `{"kind":"exports"}` event, the `export` lease, `SluisExportFailing` and `SluisExportStale` (after the transition).
- Replace `LambdaArgs.ParameterKeyArn` and the other `…Arn` key inputs (`SigningKeyArns`, `WrappedSigningKeyArn`,
  `State.KeyArn`) with alias inputs; audit's `Archive.KeyArn` likewise. `secrets.kmsKeyId` takes an alias.
- The wrapped key ring records each entry's context; `ssm:GetParameterHistory` joins sluis's grants.
- Catalogue validation refuses a GitHub App named `runner-…`; the GitHub App records gain `export`.
- The documentation renames sluis's State port to "State (DynamoDB)" wherever `state` could mean the new package.

## Consequences

- **One copy, read by sluis itself.** "The copy is stale" stops being a failure mode, with its alerts, its lease and
  its schedule.
- **The contract is in the repository.** Addresses, fields and schema versions are pinned by tests and change under
  [0007](0007-breaking-changes-inside-1x.md). An estate no longer chooses a path or renames a field.
- **The boundary is structural.** "May a reader see this?" is answered by the namespace, and grants are on exact
  addresses, so adding a consumer never widens what another sees.
- **Consumers change their key and, for GitHub and runner Apps, their properties.** The runner kind's fields become
  `app_id`, `installation_id`, `private_key`; a consumer keeps its own Secret's key names through `secretKey` or a
  template.
- **Rotation depends on native versions.** An OpenBao mount with `max_versions: 1` would lose the overlap and is
  refused at start; SSM's 100 versions are ample because only one previous revision is ever read.
- **Values are replaced in place.** On SSM an update stays a read-then-write that relies on the caller's lease.
- **Operator-seeded client secrets are documents.** The operator writes `{schema, client-id, client-secret}` with the
  backend's own CLI, or lets sluis generate the secret (0039). A Kubernetes installation whose client secrets came
  from a projected file seeds them into the store instead.
- **Backups replace the recovery bundles.** A restore needs an empty installation and brings back no revision
  history, so an overlap in progress at backup time is not restored.
- **The truststore bucket goes away** (except beside R2), the blob bucket becomes versioned where mutual TLS is
  declared, and the apply identities become an input of the storage component.
- **Accepted consequences of the reference deployment's two shared keys:**
  - disabling or scheduling the deletion of a shared key stops every purpose on it at once, signing included;
  - a caller that sends no context (OpenBao's `awskms` seal) needs a statement with no context condition for its role,
    as broad as the key policy lets it be;
  - signers on one asymmetric key cannot be separated by context, because `Sign` has none, only by principal;
  - an alias is a name, not a permission: a grant or key policy names the key, and every alias of a key reaches the
    same material.

## Migration

`sluis migrate secrets-layout --to v4` (with `--dry-run`, a JSON report that names paths and never values, and
`--overwrite`) is a command, not a start-time upgrade, in the shape of `sluis migrate ssm-layout`
([0031](0031-a-generic-migration-tool.md)). It reads v3 or, for an estate that never ran v3, the legacy store, and
writes each value to its v4 address: an operator-seeded `config/clients/<id>/secret` becomes an `oidc/v1` document, a
generated client's v3 record becomes the current revision of its document, App ids come from State (DynamoDB). It
writes where the destination is absent, refuses one that holds another value unless `--overwrite`, reads every value
back, and deletes nothing.

1. **Release with `secrets.layout`**: `v3` (the default), `transition` (read v4 first and fall back to v3; every write
   goes to v4 and then to v3; the exports controller still runs) and `v4`.
2. **Grants.** The estate adds sluis's v4 grants and each consumer's exact external addresses beside the v3 ones, and
   the key aliases and policy statements of the purposes.
3. **`transition`, then backfill** (`--dry-run` first). From here v4 is complete and current.
4. **Consumers switch** `remoteRef.key` (and, for Apps, `property`) to the external address, at the estate's pace.
5. **`v4`.** Deploy with `secrets.layout: v4` and remove `exports:` and `ports.export`; no v3 path is written again.
   Going back to `transition` from here first runs the migration in reverse (`--to v3`).
6. **`--delete-v3`** deletes v3 values whose v4 address holds the same value; then the v3 and `export/` grants go.

A later minor release removes `transition` and the code listed under "Code this implies", marked **Breaking:**. A
policy row's `secret: <name>` is accepted in `v4` with a warning and refused in that release.

## Rollout

The reference estate first; the second estate starts only when every check below was observed on the reference
estate and recorded:

- **People and agents.** A person signs in and refreshes; an agent-class client
  ([0040](0040-agent-class-sessions.md)) signs in and refreshes; tokens verify against the JWKS across a ring entry
  made under the new context.
- **A rotation through `Rotating`.** A client secret is rotated with one `Put`; the relying party picks up the new
  value, no sign-in fails during the grace, and the previous secret is refused after it.
- **ExternalSecrets synced from `external/`.** Every consumer is `Ready` from its v4 address, the synced value hashes
  the same as the v3 one, and `property` works on the Parameter Store provider and on OpenBao.
- **No parameter on the AWS-managed key.** Every SecureString under `<root>/` is encrypted under the configured alias,
  none under `aws/ssm`.
- **Refusals.** A consumer's role is refused any `internal/` value and any external address it was not granted.
- **An audit record in its prefix.** A sign-in produces a record in the destination prefix it belongs to
  ([0042](0042-one-repository-one-release-train.md)).
- **Previews clean.** After the migration, the estate's infrastructure preview and its rendered documents show no
  change; `secrets.layout: v4` has run for seven days with no write to a v3 path.
- **Mutual TLS** (the Cloudflare shape) is served from `truststore/` at the pinned version, and the function's role is
  refused a write there.

## Amendment: a webhook secret (2026-10-09)

A catalogue App may declare a `webhook` (a URL, or a Kargo receiver) so GitHub delivers its events to a consumer such
as Argo CD (`POST /api/webhook`, one secret, `X-Hub-Signature-256`) or Kargo. The decisions:

- **sluis generates the secret** (32 random bytes, as for a generated client secret) and sets it with
  `PATCH /app/hook/config` as the App, right after the manifest conversion. The conversion's own `webhook_secret` is
  not kept: it is one more copy of a value this service would then have to keep consistent. The App is created with
  an active webhook, because GitHub has no API to switch an existing App's webhook on.
- **It is kept with the App's key.** Pending, beside the pending key; once installed and `export: true`, as the
  optional `webhook_secret` of `github/v1` at `external/github/<app>`, which is where a consumer reads it. A write of
  the key never drops it, and the store compares and keeps all four fields.
- **Rotation has no overlap, so its order is the design.** Argo CD and Kargo each hold one secret, and GitHub signs with
  one. A rotation therefore (1) keeps the new secret as a new revision, where the consumers read it; (2) sends the new
  target a signed `ping` until it answers 2xx within a bounded wait, which proves the consumer reloaded; and only then
  (3) tells GitHub the new secret, and for a Kargo receiver the URL derived from it (its path is
  `hex(sha256(project + receiver + secret))`, so the URL moves with the secret), in the single PATCH. A failure before
  GitHub is told restores the previous revision and leaves deliveries exactly as they were; a failure of the PATCH
  itself does the same. Each step is audited (`roster.catalogue_app.webhook_changed`), never with the secret or the
  derived URL.
- **Drift** is read from `GET /app/hook/config`: its URL, content type and whether a secret is set. GitHub masks the
  secret, so it cannot be compared, only replaced; rotating is the repair for all of them.

Rejected: *keeping two secrets for an overlap* (neither consumer accepts a second one); *telling GitHub first* (every
delivery between the PATCH and the consumer's reload would fail signature verification and be lost, and GitHub does
not retry on its own); *a secret declared in values* (a durable credential in git, which this contract exists to
avoid).

## Rejected alternatives

- **Keep the export copies and add a schema.** The drift, the schedule and the second copy are the problem.
- **Rotation bookkeeping documents** (`internal/rotation/<kind>/<id>`, the earlier draft). Only one previous revision
  is ever needed, and every backend keeps it natively; a second document to keep consistent is the copy problem again.
- **Previous and current together in the external document.** A consumer would be handed a value it must never use.
- **Per-consumer field renames** (`exports[].properties`). The consumer already names its own Secret keys.
- **Mixed backends** (`internal/` on SSM, `external/` on OpenBao). Two failure domains for one installation, and a
  consumer's grant would depend on which half a value is in.
- **An address or namespace per consumer.** That is a copy per consumer again.
- **One address per field.** A document would no longer change atomically; an App's id and key could be read out of
  step.
- **Normalise the v1 field names across kinds.** Every consumer changes once now; a `v2` can do it later.
- **A start-time layout upgrade.** On Lambda every cold start would race it, and the running role would need write
  grants on both layouts for good.
- **Context off by default** (the draft's opt-in `instanceContext`). A shared key is the common case; a context that
  must be remembered is one that is forgotten.
- **ARNs in configuration.** They tie a rendered document to one key's identity; an alias is what an estate renames.
- **Keys created by the library per installation.** Key sharing is the estate's decision, made in its own code.
- **Narrow the function's S3 grant instead of a bucket deny.** An identity policy attached later could widen it.
- **A per-secret backup or seed command.** The backend's CLI already does that, and a whole-installation backup is what
  recovery needs.
