# Adapter details

What each adapter does beyond the port contract in [ports](ports.md): settings, mapping to the engine, limits, IAM and
the traps. Which adapters exist, what they need and which presets name them is [adapters](adapters.md) (generated from
the registry); the physical layout is [storage layout](storage-layout.md); the settings keys are in
[configuration](configuration.md).

## The OpenBao adapter

`internal/port/openbao` (`adapters.secrets.adapter: openbao`) keeps each secret as
a KV version 2 secret in an OpenBao mount, laid out as SSM is (layout v3, see
[configuration](openbao-secrets-adapter.md)). It
shares the client of the Export adapter: the same login (`jwt` or `kubernetes`,
the token file read again at every login), the same per-namespace token and the
same TLS-verified connection. Compare-and-swap is KV's own `cas`, atomic on the
server, which SSM's is not. It also implements `port.NamespacedSecrets`, so an
export entry's `namespace` is honoured on the default destination.

## The SSM adapter

`internal/port/ssm` (`adapters.secrets.adapter: ssm`, or the `aws-hybrid`,
`aws-serverless` and `k8s-aws` presets) keeps each secret as a SecureString
parameter in AWS SSM Parameter Store. It needs AWS and runs on kubernetes and
lambda.

**Settings:** `root` (the installation's, `/sluis/<instance>`: layout v3; a serve document's `secrets.root` supplies it and naming another here is refused; a v1 document that names none keeps `/sluis`), `kmsKeyId` (a customer-managed key id, ARN
or alias; unset is the AWS-managed `alias/aws/ssm`), `region`, `endpoint`
(LocalStack).

**Layout:** [storage layout](storage-layout.md#ssm-the-ssm-secrets-adapter) has every path. A consumer's External
Secrets Operator reads `<root>/export/*` and nothing else.

**Values:** a text value is stored as it is, so an export reads as the secret itself;
a value that is not text (control bytes, not UTF-8) or that begins with `sluis-b64:`
is stored as that marker and its base64 (so a binary value is at most about 6 KiB).
The tier is Intelligent-Tiering: standard (4 KiB, free) until a value needs more,
then advanced (8 KiB, billed), never downgraded. `MaxSecret` is enforced first.

**Versions** are SSM's parameter versions, a counter per write.

**Compare-and-swap is not atomic.** SSM has no conditional write on a version.
`PutIfVersion(v)` reads the version and then writes with Overwrite: a writer that
lands in between is overwritten (last writer wins). Creation (an empty version,
"only if absent") is atomic: `PutParameter` with Overwrite=false. The service's
writers of one secret are serialised by the target lease, which is a State
operation and atomic, so the window is not reachable in normal operation; a caller
that needs a true compare-and-swap on secrets must not use this adapter. The
conformance suite has no concurrent case, so no skip is needed; a test in the
package pins the documented outcome.

**IAM** the role of sluis needs, on the parameters of the root:

```json
{
  "Effect": "Allow",
  "Action": [
    "ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath",
    "ssm:PutParameter", "ssm:DeleteParameter"
  ],
  "Resource": [
    "<the SSM parameter ARNs of /sluis/<instance>/private/credentials/*>",
    "<the SSM parameter ARNs of /sluis/<instance>/export/*>"
  ]
}
```

(`GetParametersByPath` on the hierarchy itself is authorised by the `/*` resource
of each tree. The function that serves the console adds read-only `GetParameter`,
`GetParameters` and `GetParametersByPath` on `/sluis/<instance>/private/config/*`, the
secrets its document names; a controller is denied that tree.) With
`kmsKeyId` set, add `kms:Decrypt` and `kms:Encrypt` on that key; the AWS-managed
key needs nothing beyond the parameter permissions. **Consumers' ESO must read ONLY
`/sluis/<instance>/export/*`** (`ssm:GetParameter` and `ssm:GetParametersByPath` there, and
`kms:Decrypt` if a customer key is set), never `/sluis/<instance>/private/*`.

## The `sqs` adapter

Settings (`adapters.audit.settings`, or the preset's `k8s-aws`, `aws-hybrid` and
`aws-serverless` with a queue named there):

| Key | Meaning |
|---|---|
| `queueURL` | **required.** The queue the audit writer Lambda consumes. A URL ending in `.fifo` makes each record's tenant the message group and its id the deduplication id. |
| `region` | the queue's region; empty is the default chain's. |
| `endpoint` | an emulator's endpoint (LocalStack); empty on AWS. |
| `timeout` | one send, retries included. Default `5s`. |

The wire format is the one of `sink/sqssink` in truvity/audit: the body is the
canonical record, and the message attribute `record-id` carries its id, which
the writer dedupes on (a standard queue can deliver twice). It is not imported:
that module sits in the same repository as the receiver and the writer and
depends on sluis itself.

**IAM.** The role the process runs as (the Lambda's execution role, or the Pod
Identity role) needs `sqs:SendMessage` on the queue, and nothing else on it.
`SendMessageBatch` is authorised by the same action. If the queue is encrypted
with a customer-managed KMS key the role also needs `kms:GenerateDataKey` and
`kms:Decrypt` on that key. The consumer's `sqs:ReceiveMessage`,
`sqs:DeleteMessage` and `sqs:ChangeMessageVisibility` belong to the writer's role,
not sluis's.

**The catalogue travels with the writer.** There is no receiver on this path to
answer `RegisterCatalogue`, so sluis skips the registration: no call, no error,
no retry loop, and one log line at start (`audit records are published to SQS;
the catalogue is not registered ...`). The catalogue files (`internal/audit/catalogue`)
are in the writer Lambda's package, so a change to a catalogue document, with
its version bumped, redeploys the writer. Nothing refuses a mismatch at start,
because nothing is asked.

**Durability.** `Queued`: SQS has the message on several servers. Delivery modes
are the catalogue's, as for `connect`:

- A `block` action (a recovery sign-in) returns only once SQS has accepted it,
  within `timeout`, and refuses the sign-in when it has not.
- Every other action is `async`: it waits in the emitter's in-memory queue and
  is retried until SQS takes it, so an SQS outage never blocks a sign-in.
- **On Lambda** the process is frozen once the handler returns, and an in-memory
  queue would be lost with it. The trail is therefore synchronous there
  (`AWS_LAMBDA_FUNCTION_NAME` set): `Record` waits, for at most 3 seconds, until
  the record is on the queue, then returns whatever happened and never fails its
  caller. After a failed send the trail stops waiting (a degraded flag, cleared
  when the queue empties), so an outage costs one sign-in the bounded wait and
  the rest nothing; the record stays queued and is retried if the container is
  warm. A handler may also call `Trail.Flush(ctx)` before it returns.
  Records still queued at a freeze are the trade-off: an outage that outlasts the
  container loses the async records made during it, which is what `async` means.

## The S3 Blob

An adapter of the Blob port, in `internal/port/s3blob`. It is chosen by
`ports.blob`, which replaces the Blob of whatever `ports.adapter` brings, so
State `legacy` with Blob `s3` is a valid pair and so is State DynamoDB with
Blob S3. It takes its credentials from the platform (Pod Identity, IRSA, a
Lambda role): no key is configured. It passes the conformance suite on
LocalStack; its status is in [adapters](adapters.md) and [capabilities](capabilities.md).

**S3 Blob.** One bucket and one key prefix; a name `reports/<target>` is the
object `<prefix>/reports/<target>`.

| Port operation | S3 | Maps to |
|---|---|---|
| `Read` | `GetObject`; the version is the ETag | `ErrNotFound` for `NoSuchKey`; a missing bucket is **not** `ErrNotFound` but `ErrUnavailable` |
| `Write` | `PutObject`; the version is the new ETag | |
| `WriteIfVersion` | `PutObject` with `If-Match: "<etag>"`, atomic in S3 | 412 (the ETag moved) and 409 (`ConditionalRequestConflict`, a concurrent conditional write) are `ErrConflict`; an absent object is `ErrNotFound` (a store that answers 412 for an absent key is told apart by a `HeadObject`) |
| `Delete` | `DeleteObject`, which succeeds for an absent key | |
| `List` | `ListObjectsV2` under the prefix, every page, sorted | |

Without SSE-KMS an ETag is the MD5 of the body, so rewriting identical bytes
keeps the version; as with the legacy adapter, a compare-and-swap cannot tell
A, B, A from a blob that never moved, and nothing depends on it. `Replacer` is
**not implemented**: S3 has no multi-object write, so replacing a family is not
atomic, and the port's fallback (`Write` and `Delete`) is what a caller uses.
`ReaderAll` is not implemented either: on S3 it is a listing and a `GET` per
object, no cheaper than the caller doing it. The port has no create-if-absent
for a blob, so `If-None-Match: *` is not used. `ports.blob.s3.kmsKey` asks for
SSE-KMS on every write; the request checksum is sent only where an operation
requires it, which keeps S3-compatible stores working.

**Conformance.** `porttest.RunGroups` runs the `blob/` group against the
adapter: against a fake in `go test ./...`, and against LocalStack (S3) when
`ACCESS_ROSTER_S3_URL` is set. `just test-s3` starts the pinned
LocalStack with `docker run`; CI runs the same script
(`hack/s3-conformance.sh`) as the `s3` job, which **fails if a test skipped or
did not run**. It also runs the Blob suite with SSE-KMS on.

## The in-memory adapter

`internal/port/memory` implements every port in process memory, with the
semantics above (revisions are a counter, expiry is judged by an injectable
clock, `Watch` delivers put, delete and expiry, `Trigger` coalesces, and `Secrets` holds the
values in process memory). It is what tests and the demonstration use, and what `ports.adapter: memory` selects.

## The DynamoDB adapter

`internal/port/dynamodb` implements State, the transitional Index and Trigger over
one DynamoDB table (`ports.adapter: dynamodb`, `ports.dynamodb`:
[configuration](configuration.md)), with `aws-sdk-go-v2` and the
platform's credentials (Pod Identity, IRSA, a Lambda role: none is configured).
It is shared by every replica and process, and Blob and Identity are not its:
with it `internal/store` takes them from the legacy adapter, unless `ports.blob`
names the S3 adapter. A DynamoDB table holds no credential, so the domain
stores need a secrets adapter beside it. It passes the suite on
LocalStack; its status is in [adapters](adapters.md).

The item attributes (`pk`, `sk`, `lkey`, `v`, `rev`, `expires`, `k`) are in [storage layout](storage-layout.md#dynamodb-the-dynamodb-state-index-and-trigger-adapter).
The revision is a random 64-bit number drawn on every write.

| Operation | DynamoDB |
|---|---|
| `Get` | `GetItem` with `ConsistentRead`; an expired item is `ErrNotFound` |
| `Put` | `PutItem`, unconditional |
| `Create` | `PutItem` with `attribute_not_exists(pk) OR (attribute_exists(expires) AND expires <= :now)`; a failed condition is `ErrExists`, and of several takers of an expired record exactly one wins |
| `Update(rev)` | `PutItem` with `rev = :rev AND (attribute_not_exists(expires) OR expires > :now)` |
| `DeleteIfRevision` | `DeleteItem` with the same condition |
| `Delete` | `DeleteItem` |
| `PeekRevision` (optional, `port.RevisionPeeker`) | `GetItem` projecting `rev` only, not consistent (half a read unit); an expired item is `ErrNotFound` |
| `List` | `Query` on the prefix's partition with `begins_with(sk, :p)`, consistent, filtered for expiry here, paged by a token naming the last key (`port.PageToken`); a prefix that names no one kind is a `Scan` |
| `Watch`, Trigger | polling, below |
| Index `Add`/`Remove`/`Members` | an item in the set's kind partition with `<set id>/<member>` as sort key and the lifetime of the `Add`, so the lifetime is the member's; `Members` is one `Query` |

- **Conditional failures need no second call.** `Update` and `DeleteIfRevision`
  ask for `ReturnValuesOnConditionCheckFailure: ALL_OLD`: no old item, or an
  expired one, is `ErrNotFound`; an item with another revision is `ErrConflict`.
- **A revision is random, not a counter.** A counter that a delete resets would
  give a record that went A, deleted, A the revision it had the first time, and a
  stale `Update` would succeed. A fresh 64-bit number changes on every write, an
  identical rewrite included, and a stale one is `ErrConflict` whatever happened
  to the key in between: no assertion is skipped for State, Index or Trigger.
- **Expiry is judged by the adapter's clock**, to the second, on every read and in
  every condition, so the engine's lazy TTL (an expired item may stay a day or
  more) is not relied on. A lifetime is rounded up to the second: a record lives
  up to a second longer than asked, never shorter. `expires` is also what the
  engine's TTL deletes by, so storage is reclaimed in the end.
- **`Watch` polls.** DynamoDB has no cheap change feed (a stream needs a consumer,
  a second IAM surface and shard handling), so a watch lists its prefix once before
  it returns, then every poll interval (one second; `WithPollInterval`) and reports
  what differs: a key whose revision moved is a put, a key that is gone (deleted, or
  expired by the clock) is a delete. Two changes to one key between polls are one
  event, and a record written and removed between two polls is none, which the
  port's contract (at-least-once, no completeness, reconcile by listing) allows. A
  watch costs one `Query` per interval on its prefix, and ends with its error
  after three failed polls in a row.
- **The Trigger is a polling watch too.** `Notify` writes `notify.<target>` with a
  one-minute lifetime and `Subscribe` watches that prefix, so a notification crosses
  processes and replicas tick on a shared table, with up to one poll
  interval of delay. This is the decision for the Kubernetes deployment on AWS. The
  asynchronous `lambda:Invoke` of [0029](../decisions/0029-ticks-per-target-under-a-lease.md)
  is the Lambda platform's Trigger and a
  different adapter (`invoke`), where the invocation is the delivery and there is no
  process to subscribe; it is not part of this one.
- **Exporters.** `StateExporter` and `IndexExporter` read the live items with their
  lifetime left from `expires`, so `sluis migrate` can read from, and write
  to, a DynamoDB side (the way back is the rollback). The index export is a `Scan`.
- **The table.** `ports.dynamodb.create: false` (the default) binds to the table the
  infrastructure code made, which needs a string `pk`, a string `sk` and TTL on
  `expires`; `true` makes it (on-demand billing, TTL on) for a test or a
  development installation. Start checks the table with `DescribeTable`, which is
  also the readiness probe, so a missing table stops the start naming the key.
- **IAM.** `dynamodb:GetItem`, `PutItem`, `DeleteItem` and `Query` on the table,
  `Scan` for migration and a dotless listing, `DescribeTable`; `dynamodb:LeadingKeys`
  may restrict a role to the families it writes (`notify` and `lease` belong to
  every role that ticks).
- **Limits.** A key over 1 KiB, and a key with no first segment (`.x`), are
  `ErrUnsupported`; a value over 256 KiB is `ErrTooLarge` as everywhere (an item may
  hold 400 KiB). A table that is not there, throttling and the network are
  `ErrUnavailable`.
- **Conformance.** The suite runs over an in-memory fake of the DynamoDB API in
  `go test ./...` (the fake understands only the expressions the adapter sends) and
  over LocalStack in CI, each assertion on a table of its own, with only the other
  ports' assertions (`blob/`, `identity/`) skipped, each with its
  reason. `hack/dynamodb-conformance.sh`, which `just test-s3` and the `s3` job run,
  fails if any other test skipped or did not run, and also runs a migration from
  memory into DynamoDB and back.

## The legacy adapter

`internal/port/legacy` is temporary: it implements the ports over today's
ConfigMaps, Secrets and Valkey, writing exactly the bytes the domain stores
write, and is deleted when the migration of
[0031](../decisions/0031-a-generic-migration-tool.md) has run. Its package
documentation holds the whole mapping; in short:

| Port key or name | Where it lives today |
|---|---|
| `req.<id>`, `code.<id>`, `codesess.<id>`, `tok.<jti>`, `sso.<id>`, `rt.<hash>`, `rtrot.<hash>`, `keyring.<alg>:<kid>` | the Valkey key the issuer writes (`issuer:request:<id>`, `issuer:code:<id>`, ...), under the installation's prefix |
| `lease.<kind>:<workspace>` | `{<workspace>}:lease:<kind>`, the hub's refresh lease; `lease.<name>` is `lease:<name>`. The controllers' tick leases are `lease.github-tick:<org>`, `lease.github-links:all` and `lease.slack-tick:<workspace>` (0029) |
| a key containing `:` | itself: the legacy namespace the issuer's own state is written in |
| `gh.org.<org>` | the `<org>.json` entry of the `<release>-github-orgs` ConfigMap (the record; the credential is a separate Secret) |
| `snapshots/<workspace>` | `{<workspace>}:snapshot`, the same gzip bytes, for the cache's lifetime |
| `reports/github/<key>`, `reports/slack/<key>` | an entry of `<release>-github-status`, `<release>-slack-status` |

Every other key of the layout (`ses.`, `sid.`, `ws.`, `gh.link.`, `app.`,
`gate.`, `share.`, `cache.`, `dedupe.`, `notify.`) has no object of its own
today, and is `ErrUnsupported` (which is why the pending-share hand-off, the
State-backed input cache and the gates of 0029 wait for an adapter that can hold
them; the controllers keep the first as a notification between ticks, the
second in memory and the third in the report): a session is one Valkey value that does not carry
its person, a link is a Secret entry keyed by account id, and presenting either
under the layout's key would write different bytes.

Where a port operation cannot be met exactly, the adapter does the closest safe
thing, and the conformance suite names the exception:

- **Revisions are the SHA-1 of the stored bytes.** Neither Valkey nor a
  ConfigMap entry has a per-key version, and adding one would change what is
  written. A rewrite of identical bytes keeps its revision
  (`revisions/change-on-identical-rewrite` is skipped), and a compare-and-swap
  cannot tell a record that went A, B, A from one that never moved. The swap is
  atomic: a Lua script in Valkey, the ConfigMap's own `resourceVersion` for an
  entry.
- **`Watch` polls** the prefix by listing it (every two seconds by default) and
  reports differences; an expiry is seen as a delete when a poll finds the record
  gone.
- **A ConfigMap entry has no lifetime**, so `gh.org.` accepts only the permanent
  writes the layout allows. Report entries are text: a blob of invalid UTF-8 under
  `reports/` is `ErrUnsupported`.
- **`Trigger` is in-process.** A notification reaches this process's
  subscribers. A controller subscribes and ticks the target it names, but the
  console that calls `Notify` is another process, so a controller still learns
  of a console write from the records it mounts, looked at every 30 seconds (an
  operator's request for a pass names its target and ticks only it; any other
  change runs a sweep), and its sweep is still its interval. The key-value watch
  of the layout replaces the poll.
- **Tick leases are exclusive across processes only with a Valkey.** The
  controllers are configured with none, so with this adapter a controller's
  leases are held in its own memory and the chart refuses more than one replica.
- **Listing is a scan** (`SCAN` of every primary, or one `GET` of the ConfigMap),
  for an operator or a watcher and not a request path.

What still does not go through the ports, with the default adapter: every
domain store. They stay behind their own interfaces, written by `internal/kube`,
which only `internal/app`, `internal/githubroster/app` and the legacy adapter
import, until an installation sets `ports.adapter` to one that holds the keys.
