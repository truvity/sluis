# Adapter details

Settings, engine mapping, limits and IAM of each adapter beyond the [port contract](ports.md). The adapter list is [adapters](adapters.md), the physical layout is [storage layout](storage-layout.md), the keys are in [configuration](configuration.md).

## The OpenBao adapter

`internal/port/openbao`, `adapters.secrets.adapter: openbao`. Settings, layout and policy are in [OpenBao Secrets adapter](openbao-secrets-adapter.md).

| Aspect | Behavior |
|---|---|
| Login | `jwt` or `kubernetes`; token file read at every login; one token per namespace |
| Compare-and-swap | KV `cas`, atomic on the server |

## The SSM adapter

`internal/port/ssm`, `adapters.secrets.adapter: ssm`, or the `aws-hybrid`, `aws-serverless` and `k8s-aws` presets. It opens the layout-v4 stores of `secrets` (`internal/secretstore`): one SecureString parameter per secret. Runs on Kubernetes and Lambda.

| Aspect | Behavior |
|---|---|
| `root` | `/sluis/<instance>`. `secrets.root` supplies it; naming another here is refused |
| `kmsKeyId` | Key id, ARN or alias. Unset is `alias/aws/ssm` |
| `region`, `endpoint` | `endpoint` is for LocalStack |
| Paths | [storage layout](storage-layout.md#ssm-the-ssm-secrets-adapter) |
| Size | 4 KiB standard; up to 8 KiB advanced (billed, never downgraded) |
| Update | Read, then write with Overwrite: not atomic, a writer in between is overwritten. Creation is atomic. The target lease serialises writers |

IAM for the sluis role, on the parameters of the root:

```json
{
  "Effect": "Allow",
  "Action": [
    "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath",
    "ssm:PutParameter", "ssm:DeleteParameter"
  ],
  "Resource": [
    "<the SSM parameter ARNs of /sluis/<instance>/internal/credentials/* and /external/*>"
  ]
}
```

| Grant | Applies to |
|---|---|
| Read-only `GetParameter`, `GetParameters`, `GetParametersByPath` on `/sluis/<instance>/internal/config/*` | The console function only. Controllers are denied |
| `kms:Decrypt`, `kms:Encrypt` on the key | Only when `kmsKeyId` is set |
| `ssm:GetParameterHistory` | The service role |
| Resources `<prefix>` and `<prefix>/*` | Every read grant: `GetParametersByPath` is authorised against the path itself |
| `ssm:GetParameter` (and `kms:Decrypt`) on the exact `/sluis/<instance>/external/<kind>/<id>` parameters | A consumer's External Secrets Operator. Never `internal/*` |

## The sqs adapter

`adapters.audit.settings`, or the `k8s-aws`, `aws-hybrid` and `aws-serverless` presets with a queue named there.

| Key | Meaning |
|---|---|
| `queueURL` | Required. The queue the audit writer Lambda consumes. A `.fifo` URL makes the tenant the message group and the record id the deduplication id |
| `region` | The queue's region; empty is the default chain's |
| `endpoint` | Emulator endpoint (LocalStack); empty on AWS |
| `timeout` | One send, retries included. Default `5s` |

| Aspect | Behavior |
|---|---|
| Wire format | That of `sink/sqssink` in truvity/audit: body is the canonical record, attribute `record-id` carries the id the writer dedupes on. Not imported |
| IAM | `sqs:SendMessage` on the queue (covers `SendMessageBatch`); `kms:GenerateDataKey` and `kms:Decrypt` for a customer-managed key. Consumer actions belong to the writer's role |
| Catalogue | No `RegisterCatalogue` call; one log line at start. `internal/audit/catalogue` ships in the writer's package, so a catalogue change with a version bump redeploys the writer. A mismatch is not refused at start |
| `block` actions | Return once SQS accepts, within `timeout`; the sign-in is refused otherwise |
| Other actions | `async`: in-memory queue, retried until SQS takes them |
| On Lambda | With `AWS_LAMBDA_FUNCTION_NAME` set, `Record` waits at most 3 seconds for the queue, then returns and never fails its caller. After a failed send it stops waiting until the queue empties. A handler may call `Trail.Flush(ctx)` |
| Loss | An outage that outlasts the container loses the `async` records made during it |

## The S3 Blob

`internal/port/s3blob`, chosen by `ports.blob`, which replaces the Blob of `ports.adapter`: State `legacy` or DynamoDB can pair with Blob S3. Credentials come from the platform. `ports.blob.s3.kmsKey` asks for SSE-KMS on every write.

One bucket and prefix: `reports/<target>` is the object `<prefix>/reports/<target>`.

| Operation | S3 | Maps to |
|---|---|---|
| `Read` | `GetObject`; version is the ETag | `NoSuchKey` is `ErrNotFound`; a missing bucket is `ErrUnavailable` |
| `Write` | `PutObject`; version is the new ETag | |
| `WriteIfVersion` | `PutObject` with `If-Match` | 412 and 409 `ConditionalRequestConflict` are `ErrConflict`; an absent object is `ErrNotFound`, told apart by `HeadObject` |
| `Delete` | `DeleteObject`; succeeds for an absent key | |
| `List` | `ListObjectsV2` under the prefix, every page, sorted | |

| Aspect | Behavior |
|---|---|
| ETag | MD5 of the body without SSE-KMS, so identical rewrites keep the version |
| `Replacer`, `ReaderAll` | Not implemented; callers use `Write` and `Delete` |
| `If-None-Match: *` | Not used; the port has no create-if-absent for blobs |
| Request checksum | Sent only where an operation requires it |
| Conformance | `porttest.RunGroups` `blob/`: a fake in `go test ./...`, LocalStack when `ACCESS_ROSTER_S3_URL` is set. `just test-s3` runs `hack/s3-conformance.sh`, also with SSE-KMS. A skipped or missing test fails it |

## The in-memory adapter

`internal/port/memory`, `ports.adapter: memory`. Implements every port in process memory for tests and the demonstration. Revisions are a counter and expiry uses an injectable clock.

## The DynamoDB adapter

`internal/port/dynamodb`, `ports.adapter: dynamodb`, `ports.dynamodb`. State, the transitional Index and Trigger over one table, shared by all replicas. Blob and Identity come from the legacy adapter unless `ports.blob` names S3. Item attributes are in [storage layout](storage-layout.md#dynamodb-the-dynamodb-state-index-and-trigger-adapter). The revision is a random 64-bit number drawn on every write.

| Operation | DynamoDB |
|---|---|
| `Get` | `GetItem` with `ConsistentRead`; expired is `ErrNotFound` |
| `Put` | `PutItem` |
| `Create` | `PutItem` with `attribute_not_exists(pk) OR (attribute_exists(expires) AND expires <= :now)`; failure is `ErrExists`; one taker of an expired record wins |
| `Update(rev)` | `PutItem` with `rev = :rev AND (attribute_not_exists(expires) OR expires > :now)` |
| `DeleteIfRevision` | `DeleteItem` with the same condition |
| `Delete` | `DeleteItem` |
| `PeekRevision` | `GetItem` projecting `rev`, not consistent (half a read unit) |
| `List` | Consistent `Query` with `begins_with(sk, :p)`, expiry filtered here, paged by `port.PageToken`. A prefix naming no single kind is a `Scan` |
| Index `Add`, `Remove`, `Members` | Item in the set's kind partition, sort key `<set id>/<member>`, lifetime of the `Add`; `Members` is one `Query` |
| `Watch`, Trigger | Polling, below |

| Aspect | Behavior |
|---|---|
| Conditional failures | `ReturnValuesOnConditionCheckFailure: ALL_OLD`: absent or expired is `ErrNotFound`, another revision is `ErrConflict`. No second call |
| Expiry | Judged by the adapter's clock to the second on every read and condition. Lifetimes round up to the second. `expires` also drives the engine's TTL delete |
| `Watch` | Lists the prefix once, then every poll interval (1 second, `WithPollInterval`) and reports differences. Two changes between polls are one event; a record written and removed between polls is none. Costs one `Query` per interval. Ends with its error after three failed polls in a row |
| Trigger | `Notify` writes `notify.<target>` (one-minute lifetime); `Subscribe` watches the prefix, so up to one poll interval of delay. Lambda uses the `invoke` adapter ([ADR 0029](../../decisions/0029-ticks-per-target-under-a-lease.md)) |
| Credentials | The table holds none. Without a secrets adapter (`adapters.secrets` or a preset) there is no Secrets port, and any `ports.adapter` but `legacy` refuses to start |
| Exporters | `StateExporter` and `IndexExporter` read items with their remaining lifetime for `sluis migrate`. The index export is a `Scan` |
| Table | `ports.dynamodb.create: false` (default) binds to an existing table with string `pk`, string `sk` and TTL on `expires`. `true` creates it (on-demand, TTL on) for tests. Start runs `DescribeTable`, also the readiness probe |
| IAM | `dynamodb:GetItem`, `PutItem`, `DeleteItem`, `Query`, `DescribeTable`; `Scan` for migration and dotless listing. `dynamodb:LeadingKeys` may restrict to the written families; `notify` and `lease` belong to every ticking role. A restriction listing issuer kinds must include `issuer-sso-cookie`, or every console asks for login again |
| Limits | A key over 1 KiB or with no first segment (`.x`) is `ErrUnsupported`. A value over 256 KiB is `ErrTooLarge`. A missing table, throttling and network faults are `ErrUnavailable` |
| Conformance | An in-memory fake of the API in `go test ./...`, LocalStack in CI; only `blob/` and `identity/` are skipped. `hack/dynamodb-conformance.sh` fails on any other skip and runs a migration memory to DynamoDB and back |

## The legacy adapter

`internal/port/legacy` implements the ports over ConfigMaps, Secrets and Valkey, writing the bytes the domain stores write. It is temporary and is deleted after the [migration](../../decisions/0031-a-generic-migration-tool.md).

| Port key or name | Location |
|---|---|
| `req.<id>`, `code.<id>`, `codesess.<id>`, `tok.<jti>`, `sso.<id>`, `rt.<hash>`, `rtrot.<hash>`, `keyring.<alg>:<kid>` | Valkey key the issuer writes (`issuer:request:<id>`, `issuer:code:<id>`, ...) under the installation prefix |
| `lease.<kind>:<workspace>` | `{<workspace>}:lease:<kind>`. `lease.<name>` is `lease:<name>`. Tick leases: `lease.github-tick:<org>`, `lease.github-links:all`, `lease.slack-tick:<workspace>` |
| Key containing `:` | Itself |
| `gh.org.<org>` | Entry `<org>.json` of ConfigMap `<release>-github-orgs`; the credential is a separate Secret |
| `snapshots/<workspace>` | `{<workspace>}:snapshot`, same gzip bytes |
| `reports/github/<key>`, `reports/slack/<key>` | Entry of `<release>-github-status`, `<release>-slack-status` |
| `ses.`, `sid.`, `ws.`, `gh.link.`, `app.`, `gate.`, `share.`, `cache.`, `dedupe.`, `notify.` | No object; `ErrUnsupported` |

| Deviation | Behavior |
|---|---|
| Revisions | SHA-1 of the stored bytes. Identical rewrites keep the revision (`revisions/change-on-identical-rewrite` is skipped). Swap is a Lua script in Valkey, or the ConfigMap `resourceVersion` |
| `Watch` | Polls by listing, every 2 seconds. Expiry shows as a delete |
| Lifetime | ConfigMap entries have none, so `gh.org.` accepts only permanent writes |
| Reports | Text only; invalid UTF-8 under `reports/` is `ErrUnsupported` |
| `Trigger` | In-process. Controllers see console writes in the mounted records, checked every 30 seconds |
| Tick leases | Exclusive across processes only with Valkey. Controllers have none, so leases live in memory and the chart refuses more than one replica |
| `List` | A scan (`SCAN` of every primary, or one ConfigMap `GET`); not for request paths |
