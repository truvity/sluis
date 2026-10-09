# Configuration reference

The configuration file of each binary: its shape, secrets, shared blocks and refusals. Keys per binary: [writer](configuration-writer.md), [observe and query](configuration-observe-query.md), [jobs](configuration-jobs.md). See also [emitter library](../../sdk/go/audit-emitter.md) and [chart values](chart-values.md).

One installation serves one application ([0053](../../decisions/0053-one-installation-per-service-or-product.md)).

## The configuration file

Only the file configures a process; no flag has an environment fallback ([0063](../../decisions/0063-one-validated-configuration-file.md)). Everything here is version 2.

| Command | Flags |
|---|---|
| `audit-writer`, `audit-observe`, `audit-query` | `--config <file>`, `--version`, `--help` |
| `audit verify`, `purge`, `clock-sync`, `migrate` | `--config <file>` replaces every other flag. Only `--json` may accompany it. A command line naming both a file and another flag is refused |
| Interactive `audit` commands | Their own flags, for a person at a keyboard |

The file is YAML. The binary's JSON Schema in `schemas/config/` validates it before anything starts. The schemas ship in the release.

| Binary or command | Schema |
|---|---|
| `audit-writer` | `audit-writer.schema.json` |
| `audit-observe` | `audit-observe.schema.json` |
| `audit-query` | `audit-query.schema.json` |
| `audit verify`, `purge`, `clock-sync`, `migrate` | `audit-verify.schema.json`, `audit-purge.schema.json`, `audit-clock-sync.schema.json`, `audit-migrate.schema.json` |

| Rule | Detail |
|---|---|
| `apiVersion` | `audit.truvity.github.io/<kind>/v2`. `<kind>` is the schema name, such as `audit-writer-lambda` |
| Versions | A binary reads N and N-1 (ADR 0067). Version 1 is deprecated, converted on load with a warning, and read for one more minor ([v0.13 upgrade](../../guides/audit/upgrade/v0.13.md)). Another version or kind is refused |
| Schemas | Version 2: `schemas/config/<kind>.schema.json`, `$id` `https://truvity.github.io/sluis/schemas/audit/v2/config/<kind>.schema.json`. Version 1: frozen in `schemas/config/v1/` |
| Errors | An unknown key, a missing required key or a wrong type is a start-up error naming the path. Rules a schema cannot say run after it ([Refusals](#refusals)) |
| Regeneration | `just audit-config-schemas` regenerates the schemas from `internal/config/schema/schema.go`. The chart validates the same files when it renders |
| Durations | Go notation: `30s`, `2m`, `168h` |
| `AUDIT_CONFIG` | Where the file is. The six binaries and four `audit` jobs read it. `--config` wins. For an `audit` job it counts only on a command line with no option of its own |
| Lambda default | Without flag or variable: `/opt/audit/audit.yaml`, then `/var/task/audit.yaml` (kept one release after the move to the layer) |

### Secrets

A `...Secret` field holds a secret's name, never the secret. The file's `secrets` block says how to find it. An absent or empty secret is an error naming the field, source and root, never a value.

<!-- generated: config-secrets -->
| key | holds |
|---|---|
| `database.passwordSecret` | the Postgres password |
| `bucket.credentialsSecret.accessKeyID`, `.secretAccessKey` | the static credentials of an S3-compatible store |
| `openbao.tokenSecret` | an OpenBAO token |
<!-- /generated -->

```yaml
secrets:
  source: file                      # env (the default), file or ssm
  root: /etc/audit/secrets          # a directory (file) or a parameter path (ssm); not with env
```

| `source` | A name is | `root` |
|---|---|---|
| `env` | The name of an environment variable | Refused |
| `file` | A path under `root`, one file per secret, read as is except one trailing newline: a mounted Kubernetes Secret | An absolute directory, required |
| `ssm` | A SecureString parameter `<root>/<name>` in AWS Systems Manager Parameter Store, read decrypted with the process's identity | A path starting with `/` and not ending in one, required |

| Rule | Detail |
|---|---|
| Names | For `file` and `ssm`: relative, segments of letters, digits, `.`, `_`, `-`, no `..`. A `root` has no empty, `.` or `..` segment |
| AWS Lambda | Use `ssm`. `source: env` is refused when `AWS_LAMBDA_FUNCTION_NAME` is set, including a converted version-1 `...Env`. The Pulumi library renders the block and grants `ssm:GetParameter(s)` on the root only ([AWS](../../guides/audit/operate/aws-store-secrets-in-ssm.md)) |
| Kubernetes | Use `file`. The chart's `secretFiles` projects each Secret key as the named file |
| Database URL | A password in it is refused |
| Files | Token, key and root files are referenced by path: `tokenFile`, `jwtFile`, `rootFile`, `keyFile.path`, `caFile` |

### Telemetry

The file holds none. The `OTEL_*` variables configure telemetry. The chart's `telemetry.otlp` renders them ([telemetry](telemetry.md#the-chart-sets-the-environment)); on Lambda see [AWS](../../guides/audit/operate/aws-send-lambda-telemetry.md).

### Health: `/healthz` and `/readyz`

`audit-writer`, `audit-query` and `audit-observe` serve both on `listen`. Checks run at every probe, in parallel, within 2 seconds. A 503 names the failed checks, not their errors.

| Endpoint | Probe | Passes when | On failure |
|---|---|---|---|
| `/healthz` | Liveness | The process is up, and a stream or queue consumer has not stopped on its own | Restarts the pod |
| `/readyz` | Readiness | The database answers (writer with `database`, query with the `postgres` searcher, observe); the catalogue registry reads (writer with `database`); the archive lists (query with `s3scan`, observe's catalogues); a consumer runs. A receiver with neither is ready when up | Removes the pod from the Service, no restart |

### Evidence: the writer's start-up record

`audit.writer.started` carries these as `data` (schema `writer-started.json`). The writer refuses a file that changed between its two reads, before and after validation.

| Field | Value |
|---|---|
| `config_file` | The file read |
| `config_digest` | `sha256:` and the SHA-256 of the file's bytes, as `sha256sum` prints |
| `deployment_digest`, `workloads_digest` | Digests of the named documents |
| `catalogues_digest` | Digest of each file's relative name and digest, in name order |
| `layer` | Lambda: the layer version ARN from `AUDIT_CONFIG_LAYER` |
| `function_version` | Lambda: `AWS_LAMBDA_FUNCTION_VERSION`. An unknown field is left out |

### Documents the file points at

The file names four documents by path. The first three carry `apiVersion` and have a schema in `schemas/config/`: `audit-deployment`, `audit-grants`, `audit-workloads` (`.schema.json`).

| Key | Document | In the chart |
|---|---|---|
| `deployment` | The profile configuration: the framework profiles each profile composes, and `externalIdentifiersAreOpaque` | `/etc/audit/deployment.yaml`, from `profiles` and `externalIdentifiersAreOpaque` |
| `workloads` | Trusted issuers and which service account speaks for which source ([Workload identity](configuration-writer.md#workload-identity)) | `/etc/audit/workloads.yaml`, from `workloadIdentity` |
| `grants` | The query service's issuers, presets and rules ([Grants file](configuration-observe-query.md#grants-file)) | `/etc/audit/grants.yaml`, from `query.grants` |
| `catalogues` | A directory of catalogue documents registered at start-up | `/etc/audit/catalogues`, from `catalogues` |

## Shared blocks

<!-- generated: config-shared-blocks -->
These blocks recur under several keys. They follow the shared fragments of the
component contract (`postgres.json`, `bucket.json`, `listen.json`,
`nats.json`).

**`database`** (a PostgreSQL connection)

| key | type | default | meaning |
|---|---|---|---|
| `url` | string, required | | a `postgres://` or `postgresql://` URL without a password. A URL with one is refused, and one that does not parse is refused |
| `passwordSecret` | string | none: the connection needs no password | secret by reference: the name of the secret holding the password |
| `maxConnections` | integer, at least 1 | 10 | the pool size of this process. Size it against the server's limit divided by the number of processes |

**`bucket`** (an object store addressed by the S3 API)

| key | type | default | meaning |
|---|---|---|---|
| `name` | string, required | | the bucket, which exists already |
| `region` | string | the SDK's own resolution | the region the bucket is in |
| `endpoint` | URI | the SDK's own resolution for the region | override the API endpoint, for a store that is not AWS |
| `ca` | path | the system trust store | a CA bundle trusted for that endpoint, mounted by the platform |
| `pathStyle` | boolean | false | address the bucket as a path rather than a host, for a certificate that does not cover a bucket subdomain |
| `credentialsSecret.accessKeyID`, `.secretAccessKey` | strings, both required if the block is present | none: the SDK's ambient credentials, which is what a workload identity provides | secret by reference: the names of the secrets holding static credentials |

**`archive`** (what the process adds to the deployment's presets). Where the archive is -- the bucket, prefix,
region and endpoint of each install preset -- and the Object Lock it is written under (compliance for the
`attested` preset, none for the others) are the **deployment document's `presets`**
([0068](../../decisions/0068-storage-is-configured-per-preset.md), [profiles](profiles.md#presets-and-their-storage)).
The processes that open the archive take it: `audit-writer` (mode `writer`), the writer Lambda, `audit-query`, `audit-observe`, `audit-notary` and `audit verify`. The receiver, `audit migrate`, `audit purge` and `audit clock-sync` open no archive. The process names the deployment (`deployment`) and may carry:

| key | type | default | meaning |
|---|---|---|---|
| `stateRoot` | string | none | the root of the installation's state store, below which a preset's `credentials` address is read. Needed when a preset is at an endpoint with `credentials` |
| `sluisRoot` | string | none | the SSM root of the sluis installation (`/sluis/<instance>`) below which a preset's `credentials_ref` is read with the process's identity. Exclusive with `sluisDir` |
| `sluisDir` | path | none | the directory a secrets operator projects a preset's `credentials_ref` into, read at `<sluisDir>/<credentials_ref>`. Exclusive with `sluisRoot` |
| `ca` | path | the system trust store | a CA bundle for a store whose certificate is not signed by a public root |
| `kmsKey` | string | the bucket's default encryption | the key objects are encrypted with where a preset names no `key_alias`. Only the writer and the notary have it |

**`listen`**: `address` (string, `host:port` such as `:8080`, required when the
block is present). Both servers default it to `:8080`.

**`sink`** (the writer a process records through)

| key | type | default | meaning |
|---|---|---|---|
| `url` | string | | the writer's base URL. Exactly one of `url` and `sqs` |
| `sqs` | `queueUrl` (required), `region`, `fifo` | | the ingest queue of a writer that runs elsewhere, such as the writer Lambda: records are sent to the queue and acknowledged `queued`. There is no credential in the file: the pod's own identity is it (EKS Pod Identity, or IRSA through the service account's annotation), and its role needs `sqs:SendMessage` on the queue. `tokenFile` is refused with it, and `expect` is `queued` and may be left out |
| `tokenFile` | path | none: no token, which only an anonymous trial install accepts | the file holding the bearer token, read afresh on every request; in a cluster the pod's projected service-account token |
| `expect` | `logged`, `queued` or `archived` | none: the client claims nothing | what the writer at `url` is configured to give. A client cannot learn that until it writes, so the file says (`sink.Client.Expecting`); the process's `require` is checked against it at start-up and against every acknowledgement afterwards |

**`openbao`** (how a process reaches an OpenBAO transit engine)

| key | type | default | meaning |
|---|---|---|---|
| `address` | string, required | | the server, for example `https://openbao.example.com:8200` |
| `mount` | string | `transit` | where the transit engine is mounted |
| `namespace` | string | the root namespace | the namespace the engine and the auth mount are in |
| `caFile` | path | the system roots | a PEM bundle trusted beside them |
| `login.mount`, `.role`, `.jwtFile` | strings, all required | | sign in with a JWT: the auth mount, the role on it, and the file holding the pod's projected token, read at every login. Nothing is stored |
| `tokenFile` | path | | sign in with a token read from a file on every call |
| `tokenSecret` | string | | secret by reference: the name of the secret holding a token |

Exactly one of `login`, `tokenFile` and `tokenSecret`.

**`keys`** (where the keys live, by purpose: the storage port's shape, [the adapter block](../storage/adapter-block.md))

| key | type | default | meaning |
|---|---|---|---|
| `adapter` | `kms`, `transit` or `local` | | the key service: `kms` (AWS KMS, keys by alias), `transit` (OpenBao) or `local` (a root file; development) |
| `seal` | a key name, or `{key, context}` | | the notary's seal key: asymmetric ECC P-384 (ES384). It signs and takes no encryption context. The notary takes `keys.seal`: exactly one of `seal` and `signer` |
| `pseudonym` | a key | | wraps the per-tenant pseudonym secrets. Provisioned only for an installation that pseudonymises (the `attested` preset, or a profile that needs pseudonyms) |
| `conceal` | a key | | identities sealed where the law requires them to be recoverable |
| `archive` | a key | | the key the archive's objects are encrypted with (for S3, the SSE-KMS alias) |
| `instance` | string | | names the installation in the default encryption context `{instance, purpose}`; bound into ciphertexts, so choose something stable and non-secret |
| `state` | `{root, address}` | | with `kms`: where the wrapped per-tenant secrets behind `pseudonym` are kept (the installation's SSM store). Nothing in it is usable without the pseudonym key |
| `openbao` | `openbao` | | with `transit`: the engine and how to sign in |
| `rootFile` | path | | with `local`: a file of 32 bytes |
| `provider` | `none`, `local` or `transit` | | **deprecated**: the first releases' shape, still loaded. Use `adapter` and a key per purpose. Unset, or `none`, means no pseudonyms, no key material and no resolve ([0055](../../decisions/0055-no-pseudonymisation-keys-by-default.md)). The `local` and `transit` blocks beside it are deprecated with it |

A key is an alias or a transit key name; an ARN or key id is refused. The archive's own `credentials` (`{root, address}`)
read `{accessKeyID, secretAccessKey}` from an `internal/` address, for an archive on an endpoint
([archive on R2](../../guides/audit/operate/archive-on-r2.md)).
<!-- /generated -->

## Refusals

Every refusal is a start-up error naming the key. The grants file has its own, under [Grants file](configuration-observe-query.md#grants-file).

| Refused by the schema |
|---|
| `workloads` and `anonymousWrites` together, or neither |
| `mode: receiver` with `archive`, `catalogues`, `keys` or `consume`, or with neither `stream` nor `forward` |
| `forward` with none or several of `nats`, `sqs`, `log`; `consume` with none or both of `nats`, `sqs`; `forward` in a writer |
| `require: archived` in a receiver; `forward.log` without `require: logged`; `require` on an emitter with no `sink` |
| A `writer` without `archive` |
| `keys.provider` `local` without `local`, `transit` without `transit`, or either block beside another provider |
| An `openbao` with none or several of `login`, `tokenFile`, `tokenSecret` |
| `secrets` with `root` and source `env`; source `file` or `ssm` with a root that is not an absolute directory or SSM path |
| A `signer` with none or several of `keyFile`, `kmsKey`, `transit` |
| A database URL with a password |
| A query service with the `postgres` searcher and no `database`, or `s3scan` and no `archive` |

| Refused after the schema | Reason |
|---|---|
| A `require` the chain can never give (the guard); an emitter `require` with no `sink.expect` or a weaker one | Start-up guard |
| `forward.sqs.fifo: true` on a URL not ending in `.fifo` | URL and flag disagree |
| `stream.ackWait` not longer than `roll.interval` | The stream would offer unacknowledged records to another writer |
| `replicas` above 1 without `database` | In-process deduplication misses a redelivery landing on another replica |
| `replicas` above 1 with local keys and no `keys.local.dir` | Each replica would mint its own keys and pseudonyms |
| `keys` on the query service without `archive`, or with the local provider and no `keys.local.dir` | Resolve opens what the writer sealed in the archive |
| A `database.url` that does not parse | Invalid URL |
| A profile demanding a stricter Object Lock than its preset's bucket gives, or whose preset is not under `presets` | The writer names the profile and preset |
| A profile name containing `/` | It is a key component |
