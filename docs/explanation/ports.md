# Ports

The target shape of sluis's storage, signalling and identity edges, and
the contract each adapter must meet. The reasoning is in the records
[0026](../decisions/0026-two-platforms-permanently-kubernetes-and-aws-lambda.md)
to [0034](../decisions/0034-exports-go-to-openbao-directly.md); this
page is the specification. Which adapter exists today is in
[../capabilities.md](../reference/capabilities.md).

**Status: the ports and their adapters are built, the domain stores are on them;
DynamoDB has run on LocalStack and not yet on AWS.** The interfaces, an in-memory
adapter, a temporary `legacy` adapter and a DynamoDB adapter for State, Index and
Trigger, and an S3 Blob exist, and every domain store (workspaces and their
credentials, GitHub organisations and Apps, a person's GitHub link, the Slack
records) has an implementation on State and Secrets (the credentials)
([The domain stores](#the-domain-stores)). With `ports.adapter`
`legacy`, the default, the running service still keeps its state as described in
[sluis.md](design.md#the-store) and
[../operations/high-availability.md](../how-to/high-availability.md); with any
other adapter it keeps the domain records here. This
page is what an adapter is built and tested against; the current layout stays
true until the migration in
[0031](../decisions/0031-a-generic-migration-tool.md) has run.

A port is a small Go interface in the service, a set of semantics every adapter
shares, and a conformance suite. It is **not a framework**
([0024](../decisions/0024-reconciler-rails-are-shared-pieces-not-a-framework.md)):
business code names a port and never an adapter, and an adapter contains no
business rule.

| Port | What it is for | Kubernetes | AWS Lambda |
|---|---|---|---|
| [State](#state) | records, sessions, tokens, leases, gates, caches, counters (no credential: those are [Secrets](#secrets)) | Kubernetes objects, DynamoDB | DynamoDB |
| [Blob](#blob) | status reports, directory snapshots | S3 | S3 |
| [Trigger](#trigger) | a change becomes a tick | KV watch | asynchronous `lambda:Invoke` |
| [Secrets](#secrets) | dynamic secrets (the credentials of the domain stores) and the exports | memory, OpenBao, Kubernetes | SSM |
| [Export](#export) | copies a secret out of the service, into a store a consumer reads | OpenBao KV | OpenBao KV |
| [Inputs](#inputs) | policy, configuration, operator-managed secrets | mounted ConfigMaps and Secrets | file in the image, or a parameter store |
| [Identity](#identity) | proves a workload to the issuer, and the service to the cloud | ServiceAccount token, AWS federation | the same |
| [Audit sink](#audit-sink) | records what the service did | `http` | `sqs` |

## Adapters, presets and the platform

An adapter is chosen by name, **per concern**. The concerns are `state` (sessions
are State under `ses.`, with a lifetime), `secrets` (dynamic secrets, and the
exports under `export/`), `blobs`, `signing`, `trigger`, `schedule` and `audit`.
Each adapter registers a descriptor in `internal/port` (`port.Register`): its
name and concern, what it needs (AWS, Kubernetes, OpenBao), the runtimes it works
on (`kubernetes`, `lambda`, `process`), its status (`implemented` or
`on-request`) and a factory from its settings. `port.Catalogue` lists the
adapters that are planned and not built; `port.Default.Matrix()` is the registry
plus the catalogue, which is what the table below is generated from.

**Only what Truvity and hive need is built.** Both run the `aws-hybrid` preset:
sluis on Lambda, DynamoDB state, SSM secrets, KMS token signing, S3 blobs, SQS
audit, EventBridge ticks and an asynchronous invoke for "run a pass now". Every
other adapter is *on request*: it is in the matrix, and start refuses it.

### Choosing a preset

Four questions, in this order:

```text
AWS?        no  -> Kubernetes?   no  -> server
                                 yes -> OpenBao?  yes -> k8s-openbao
                                                  no  -> k8s-minimal
            yes -> Kubernetes?   no  -> aws-serverless
                                 yes -> sluis on Lambda?  yes -> aws-hybrid
                                                          no  -> k8s-aws
```

The answers are the `platform` block (`aws`, `kubernetes`, `openbao`, `runtime`,
`replicas`); `preset` names one outright. A **modifier** overrides one concern
(sessions to Valkey, say, is `adapters.state: {adapter: valkey}`).

| Concern | `server` | `k8s-minimal` | `k8s-openbao` | `aws-serverless`, `aws-hybrid` | `k8s-aws` |
|---|---|---|---|---|---|
| state | postgres | kubernetes | kubernetes | dynamodb | dynamodb |
| secrets | store | kubernetes | openbao | ssm | ssm (or openbao) |
| blobs | postgres | off | off | s3 | s3 |
| signing | generated | file | transit | kms-wrapped | kms-wrapped |
| trigger | http | watch | watch | invoke | dynamodb |
| schedule | ticker | ticker | ticker | eventbridge | ticker |
| audit | log | log | log | sqs | connect |

`k8s-aws` (sluis as a pod on Kubernetes with AWS storage) is built end to end:
every adapter it names exists. Its secrets are SSM, and `adapters.secrets:
{adapter: openbao, settings: {...}}` replaces them with OpenBao, which needs no
`platform.openbao` answer (naming the adapter is the answer). `aws-eks` is its
deprecated name: it resolves to it, and start logs a warning.

### Resolution

An explicit override wins over the preset, and the preset over what the
decision tree derives from the `platform` answers: `adapters.<concern>` (with its
`settings`), then the legacy keys written beside a preset (`ports.adapter`,
`ports.blob`), then `preset`, then the preset the `platform` leads to. **With
none of `platform`, `preset` and `adapters`, nothing changes:** the `ports` keys
decide, `ports.adapter: legacy` is the default, and the table is the legacy one
(state `legacy` or the named adapter, signing `file`, schedule `ticker`, audit
`connect` when `audit.writer` is set and `log` otherwise).

### Start-up validation

Start is refused, naming every problem, when an adapter needs a platform answer
that is false (only checked when `platform` or `preset` is given); cannot run on
the current runtime (`legacy` on `lambda`; the runtime is
`platform.runtime`, the preset's, or `lambda` when `AWS_LAMBDA_FUNCTION_NAME` is
set); is process-local (`memory`) while `platform.replicas` is above 1; is a
secrets adapter that is not a secret store while the platform has one; is
unknown; or is planned. The resolved table is then logged once (`adapters
resolved`, one attribute per concern) and exposed as the gauge
`sluis_adapter_info{concern,adapter} 1`.

### The matrix

`implemented` adapters; the *on request* ones are planned and not built.

| Concern | Implemented | On request |
|---|---|---|
| state | dynamodb, legacy (until the kernel cutover), memory | kubernetes, postgres, valkey |
| secrets | memory, ssm | openbao, kubernetes, store |
| blobs | s3, memory, legacy | postgres, off |
| signing | file, kms | generated, transit |
| trigger | memory, legacy, dynamodb (invoke: later) | watch, http |
| schedule | ticker (eventbridge: later) | |
| audit | connect, log, sqs | |

### Secrets

`port.Secrets` (`internal/port/secrets.go`) is whole values under slash-separated
paths, each with a version: `Get(path) (value, version)`, `Put`, `PutIfVersion`
(`ErrConflict` when the version moved, `ErrNotFound` when gone, an empty version
means "only if absent"), `Delete` and `List(prefix)` (names, never values, by whole
segments). A value is at most `MaxSecret` (8 KiB, an SSM advanced parameter's
limit). Exports live under `export/` (`port.ExportPrefix`). The suite is
`porttest.RunSecrets`; the `memory` and `ssm` adapters pass it, and an adapter for a
real store runs it against the engine.

The secrets concern is wired by `internal/store`: an adapter that configuration
chose (a preset, the platform answers or `adapters.secrets`) is built from its
settings and is `Set.Secrets`. With nothing chosen (the `ports` keys alone, which
is every deployment before the presets) there is no Secrets port, as before.

### The OpenBao adapter

`internal/port/openbao` (`adapters.secrets.adapter: openbao`) keeps each secret as
a KV version 2 secret in an OpenBao mount, laid out as SSM is (layout v3, see
[configuration](../reference/configuration.md#the-openbao-secrets-adapter)). It
shares the client of the Export adapter: the same login (`jwt` or `kubernetes`,
the token file read again at every login), the same per-namespace token and the
same TLS-verified connection. Compare-and-swap is KV's own `cas`, atomic on the
server, which SSM's is not. It also implements `port.NamespacedSecrets`, so an
export entry's `namespace` is honoured on the default destination.

### The SSM adapter

`internal/port/ssm` (`adapters.secrets.adapter: ssm`, or the `aws-hybrid`,
`aws-serverless` and `k8s-aws` presets) keeps each secret as a SecureString
parameter in AWS SSM Parameter Store. It needs AWS and runs on kubernetes and
lambda.

**Settings:** `root` (the installation's, `/sluis/<instance>`: layout v3; a serve document's `secrets.root` supplies it and naming another here is refused; a v1 document that names none keeps `/sluis`), `kmsKeyId` (a customer-managed key id, ARN
or alias; unset is the AWS-managed `alias/aws/ssm`), `region`, `endpoint`
(LocalStack).

**Layout:** a port path `p` is the parameter `<root>/private/<p>`, except a path
under `export/`, which is `<root>/export/<rest>`. The domain stores keep a
credential at the port path `credentials/<kind>/<id>/<ref>`, and the operator's
secrets are `<root>/private/config/...`: the whole of it, with the DynamoDB layout,
is [reference/storage-layout.md](../reference/storage-layout.md). A consumer's External Secrets
Operator reads `<root>/export/*` and nothing else.

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

## State

A key-value store with a lifetime on every record, a revision on every write,
and no relations. Keys are strings of the form `a.b.c`; a prefix ends at a `.`.
A value is an opaque byte string up to 256 KiB (the smaller of the two engines'
limits, with headroom); larger content is a [blob](#blob).

### Operations

| Operation | Meaning | Errors |
|---|---|---|
| `Get(key)` | the value and its revision | `ErrNotFound` if absent **or expired** |
| `Put(key, value, ttl)` | write unconditionally; returns the new revision | `ErrTooLarge` |
| `Create(key, value, ttl)` | write only if absent (an expired record is absent) | `ErrExists` |
| `Update(key, value, ttl, rev)` | compare-and-swap: write only if the revision is still `rev` | `ErrConflict` if it moved, `ErrNotFound` if it is gone |
| `Delete(key)` | remove, no error if absent | |
| `DeleteIfRevision(key, rev)` | remove only if the revision is still `rev` | `ErrConflict`, `ErrNotFound` |
| `List(prefix, page)` | live records under a prefix, in key order, paged | `ErrBadPage` for a token from another prefix |
| `Watch(prefix)` | a stream of changes (put, delete, expiry) under a prefix, from now | the stream ends with the adapter's error; the caller resubscribes |

Semantics every adapter shares:

- **Single key only.** An operation touches one key. There are no multi-key
  transactions and no secondary indexes. A flow that needs several keys is a
  sequence of idempotent steps with a recovery marker.
- **A TTL is mandatory** on `Put`, `Create` and `Update`, except for records the
  [layout](#key-layout) marks as permanent (`0`). A call with no lifetime that
  the layout does not allow is refused up front, as the current store refuses it.
- **A revision is opaque and strictly changes on every write.** Callers compare
  it for equality and never order it.
- **Reads filter on expiry.** `Get` and `List` return a record only if its
  expiry is in the future by the caller's clock, whether or not the engine has
  already removed it. DynamoDB removes expired items lazily and later; JetStream
  removes them at the stream's maximum age. Neither is relied on.
- **Create-if-absent sees an expired record as absent**, so a lease or a
  one-time code whose predecessor expired can be taken at once.
- **Paging is stable.** A page token continues from a key; a record written
  while paging may or may not appear, and none appears twice.
- **Watch is at-least-once and unordered across keys.** It carries the key and
  the new revision; a watcher that needs the value reads it. A watcher that falls
  behind or reconnects performs a `List` and reconciles; it never assumes it saw
  every event.

### Leases

A lease is a `Create` of `lease.<target>` with the holder's id and a short TTL,
renewed by `Update` with the revision it holds, and released by
`DeleteIfRevision`. Takeover is a `Create` after expiry. A holder that fails to
renew treats the lease as lost and stops before its next external write. Clock
skew is bounded by the TTL being many times the renewal interval.

### Export (optional)

`Get` does not say how long a record has left, and the Index cannot list its sets.
A State or Index that can says so with two optional capabilities,
`StateExporter` and `IndexExporter`: every live record or set under a prefix with
its remaining lifetime. Nothing on a request path uses them. `sluis
migrate` ([operations/migrate.md](../how-to/migrate-state.md)) reads the issuer's
state through them so a copied session keeps the lifetime it had; memory, DynamoDB and
the legacy adapter have them.

### Error mapping

An adapter maps its engine's errors to the six above. Everything else is an
`ErrUnavailable`, which the caller treats as "the store is down" and, on the
sign-in path, **refuses** the request rather than degrading, exactly as the
current store does.

## Key layout

One logical layout, two renderings. The in-memory and the legacy adapters use the
key as written. The adapters of the AWS platform use **storage layout v2**
([reference/storage-layout.md](../reference/storage-layout.md)): a key is a record
*kind* and an *id*, derived in one place (`internal/port/keys.go`). DynamoDB uses one
table with a partition key `pk` and a sort key `sk`: **`pk` is the kind
(`workspace`, `github-org`, `issuer-token`) and `sk` is the id** (`stable/opwerm`
when compound), so a prefix listing that lies in one kind (`ses.<person>.`,
`ws.dir.`) is a `Query` on one partition with `begins_with` on `sk`, in key order and
paged by `LastEvaluatedKey`, and the partition is the kind that ADR 0027's IAM
condition `dynamodb:LeadingKeys` grants a role. A prefix that names no one kind
(`ws.`, the empty prefix, a legacy `issuer:`) is a `Scan` filtered on the logical
key (`lkey`) and sorted in memory: an operator's listing and what `sluis migrate`
does. An `expires` attribute (epoch
seconds) holds the expiry, the table's TTL attribute points at it, and a revision
attribute `rev` is what conditional writes compare ([The DynamoDB
adapter](#the-dynamodb-adapter)).

No access pattern needs a secondary index: everything a caller looks up is a key
or a prefix. Sessions are listed per person under `ses.<person>.`, a session id
is resolved to its person through the pointer `sid.<sid>`, and "every session"
is a listing over all `ses.` partitions, which is an operator action and not a
hot path.

| Key | Content | Writer | TTL |
|---|---|---|---|---|
| `req.<id>` | a pending authorization request, also backing a device-code poll | issuer | 30 min |
| `code.<id>` | an authorization code | issuer | 5 min |
| `codesess.<id>` | the session a redeemed code opened, for a replayed redemption | issuer | 5 min |
| `ses.<person>.<sid>` | a per-client session: identity, client, how it began, scopes, SSO session, refresh token (hashed), authentication time | issuer | the session lifetime |
| `sid.<sid>` | pointer from a session id to `<person>`; written with the session, deleted with it | issuer | the session lifetime |
| `rt.<hash>` | live refresh token to `<person>.<sid>` | issuer | the session lifetime |
| `rtrot.<hash>` | a spent refresh token's successor, for the 30-second retry grace | issuer | 30 s |
| `sso.<id>` | the browser-wide SSO session and the clients it covers | issuer | the session lifetime |
| `tok.<jti>` | a minted token's own record, for userinfo and revocation | issuer | until the token expires |
| `keyring.<kid>` | a signing key's schedule: first seen, activation | issuer replicas | 30 days, renewed on each poll |
| `ws.dir.<id>` | a connected directory workspace: its record, **with its credential in Secrets** (`credentials/workspace/<id>/<ref>`) | console | permanent |
| `ws.slack.<workspace>` | a connected Slack workspace: its record, with its client secret and bot token in Secrets | console | permanent |
| `gh.org.<org>` | a connected GitHub organisation: its record, with its App key in Secrets | console | permanent |
| `gh.link.<account>` | a GitHub account's link, keyed by the **account id**; the token pair is in Secrets, the rest of the link, `RefreshingSince` and the `Revision` counter included, is plain | link flow, GitHub tick | permanent |
| `app.gh.link` | the link App: record, with the client secret in Secrets | console | permanent |
| `app.gh.runner.<tier>.<org>` | a runner App: record, with the key in Secrets | console | permanent |
| `app.gh.cat.<id>` | a catalogue GitHub App: record, with the key in Secrets | console | permanent |
| `app.slack.cat.<id>` | a catalogue Slack App: record, with the client secret and bot token in Secrets | console | permanent |
| `rec.slack.shared.<name>` | a Slack Connect channel's definition | console | permanent |
| `rec.slack.channel.<workspace>.<name>` | a console channel's record | console | permanent |
| `rec.console.session-key` | the key the console signs its sessions with, in Secrets (`credentials/console/session-key`); created by the first replica that starts | console | permanent |
| `lease.<target>` | the holder of a target's tick, by id | ticks | seconds, renewed |
| `gate.<target>.<name>` | a held-once ledger entry, a breaker, a fingerprint. Written today: `gate.github.<org>.confirm` and `.pass`, `gate.slack.<workspace>.confirm[.<channel>]` and `.pass` (an operator's confirmation of a removal set, 24 h; a request for a pass now, 24 h), `gate.github-claim.<account>` (the marker of a link claim, below) | ticks, console | by gate |
| `share.<host>.<channel>` | a Slack Connect share: the guests that were invited and each side's state; written by the host's tick, its write enqueues the guest's tick, and the guest's tick marks its own side accepted | host tick, guest tick | 14 days while a guest is pending, then 7 days once every guest has accepted |
| `cache.slack.user.<workspace>.<id>` | who a Slack member is (address, team, bot, guest): `users.info` once a day, not once a pass. A deactivated account is never cached | slack tick | 24 h |
| `cache.<digest>.<name>` | a shared input (a group's holders, an address's state), keyed by the policy digest. **Not written yet**: the Slack controller's shared inputs stay in memory ([why](#the-domain-stores)) | ticks | the digest's lifetime |
| `dedupe.<id>` | an idempotency marker for an external write | ticks | by use |

The records marked permanent are the only ones with no TTL (`ws.`, `gh.org.`,
`gh.link.`, `app.` and `rec.`: the layout was first drawn with a lifetime on a
link, "until the refresh expires", which is wrong for the links that hold no
tokens at all, a profile match or an import, and for a lost link, whose record is
what removes a person: a link that expired would read as unlinked). A key that no
longer appears in this table is not written by the service. Names that go into a
key (an id, a login, a channel) are written one segment each, every byte but a
letter, a digit, `-` and `_` as `~XX`, so a dot in a name cannot end its segment.

A credential is never written to State. The domain stores put it in
[Secrets](#secrets) under `credentials/<kind>/<id>` and leave a marker in the record; the
State store never sees a credential. (There was once a sealing step, an
envelope under a KMS key. It is retired: ADR 0027's sealing is superseded.)

## Blob

Whole-object storage for content too large for an item and read whole: a target's
status report (one per target, replaced on every tick) and the directory
snapshots the hub serves from.

| Operation | Meaning | Errors |
|---|---|---|
| `Read(name)` | the object and its version | `ErrNotFound` |
| `Write(name, body)` | replace the object; returns the version | `ErrUnavailable` |
| `WriteIfVersion(name, body, version)` | replace only if it is unchanged | `ErrConflict` |

Both platforms use S3: a bucket the installation names, a prefix per kind
(`reports/<target>`, `snapshots/<directory>`), server-side encryption on, and no
public access. A blob holds no credential and no personal data beyond what the
console already shows. A reader treats a missing blob as "not yet written" and a
stale one as stale, never as an error in the access decision.

## Trigger

Turns "this target has work" into a tick without a poll.

| Operation | Meaning |
|---|---|
| `Notify(target)` | ask for a tick of the target; coalesces with one already waiting |
| `Subscribe(handler)` | run `handler(target)` for every notification delivered to this process |

On Kubernetes, `Notify` is a `Put` of a `notify.<target>` key and `Subscribe` is
a watch on that prefix, with a periodic full listing as the backstop. On AWS,
`Notify` is an asynchronous `lambda:Invoke` of the tick function with the target
in the payload, and the invocation **is** the delivery: the function's handler
is the subscriber. EventBridge Scheduler invokes the same function for every
target on a period as the backstop.

A notification is a hint and may be duplicated or lost; the lease and the
backstop make both harmless. Writing a `share.` record **is** a notification of
the guest's tick.

## Export

The reverse of State: a copy of a secret the service keeps, put where a program that
cannot ask the service reads it (Alertmanager posting as a Slack bot, a runner scale
set with its GitHub App), and the disaster-recovery bundles. Nothing is read back,
and nothing depends on it
([0034](../decisions/0034-exports-go-to-openbao-directly.md)).

```go
type Export interface {
    Put(ctx, target ExportTarget, properties map[string]string, mode ExportMode) error
    Delete(ctx, target ExportTarget) error
}
```

- A target is a `Path` under the adapter's mount (`slack-apps/alerts`: segments of
  anything but `?#%\*` and space, no empty, `.` or `..` segment, no leading or
  trailing slash) and an optional `Namespace` of the store.
- **`ExportReplace`** makes the key hold exactly the properties. **`ExportPatch`**
  sets them and leaves every other property of the key, creating the key when it is
  absent. A `Put` of no properties is refused (`ErrNoProperties`): a copy is never
  emptied by a source that read nothing.
- **Idempotent.** Putting what the key already holds writes nothing, and in a store
  that versions its keys makes no new version.
- **Never on a request's path.** A caller treats a failed `Put` as "the copy is
  stale" and retries it out of band; an `Export` that is down changes nothing live.
- An error names the call, the target and the status, and never a value.

Adapters: `internal/port/memory` (`NewExport`, which a test reads back and can make
fail) and `internal/port/openbao`, a KV version 2 mount: `POST data/<path>` to
replace, `PATCH data/<path>` with a JSON merge patch to patch (and a `POST` for a key
that is not there), a `GET` first so that nothing is written when nothing differs,
and a login of its own per namespace with the `kubernetes` or the `jwt` auth method
(a token read afresh from a file, or from a `TokenSource` a Lambda sets to its web
identity token). The policy it needs is `read`, `create`, `update` and `patch` on
`<mount>/data/<prefix>/*`; `Delete` removes every version through
`<mount>/metadata/<path>` and is not used by the exporter. `porttest.RunExport` is the
conformance suite both pass; the OpenBao one runs against a fake KV mount in
`go test`.

What is copied, where and how often is `exports` in the configuration file
([../reference/configuration.md](../reference/configuration.md#exports-and-the-export-port)),
run by `internal/exports`: one worker per export, under a per-export lease on the
State, retried with backoff, and counted
([../operations/telemetry.md](../operations/telemetry.md#the-exports)).

## Inputs

Read-only. The policy, the configuration file
([0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md)) and
the secrets an operator manages. On Kubernetes they are mounted ConfigMaps and
Secrets, polled for change; on Lambda they are a file in the image, or read from
a parameter store at start. The service **never writes** an input and holds no
permission to.

## Identity

Two directions. **Inbound**, a workload proves itself to the issuer with a
ServiceAccount token or an AWS federation token
([../connect/aws-workloads.md](../how-to/connect/aws-workloads.md)); the verifier is
platform-independent, and the installation declares the clusters and accounts it
trusts ([0030](../decisions/0030-workload-identity-on-both-platforms.md)).
**Outbound**, the service takes its own identity from the platform (a projected
token or a role) and hands it to the adapters that need one; no adapter reads a
credential from anywhere else.

## Audit sink

Records the service's own actions in an audit installation. Transports: `http`
on Kubernetes, `sqs` on AWS. A record that cannot be written durably
refuses the action it describes where the action is a sign-in, as today
([sluis.md](design.md#audit)).

The sink is chosen by the `audit` concern of the resolved table: `connect` (the
legacy `audit.writer`, unchanged), `log` (the log line only) or `sqs`.

### The `sqs` adapter

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

## Conformance

One suite, written once against the port, runs against every adapter: **the
in-memory one and DynamoDB** (a local emulator is not enough:
the suite also runs against the real engine in CI for the adapter that has one
available, and the emulator-only case is named as such: DynamoDB runs on
LocalStack, an emulator, and has not yet run against AWS). It is the gate for
adding or changing an adapter, and for the migration tool, where each adapter is
a source and a destination.

The suite asserts, at least:

- **CAS races.** N concurrent `Update` calls with the same revision: exactly one
  succeeds, the rest return `ErrConflict`; N concurrent `Create` of one key:
  exactly one succeeds. `DeleteIfRevision` loses to a newer write.
- **TTL visibility.** A record is returned until its expiry and never after, on
  the caller's clock, **including after the engine's own sweep has not yet run**;
  `Create` succeeds over an expired record; `List` omits it.
- **Lease takeover.** A held lease cannot be taken; an expired one can, by
  exactly one of several racing takers; a renewal after takeover fails with
  `ErrConflict`.
- **Prefix paging.** A listing of more records than a page returns every live
  record once, in key order, across a page boundary and across a concurrent write;
  a page token from another prefix is refused.
- **Revisions.** A revision changes on every write and an `Update` with a stale
  one fails.
- **Watch.** A put, a delete and an expiry under a prefix are observed; a
  watcher that reconnects can recover by listing.
- **Limits.** A value over the size limit is refused with `ErrTooLarge`, on every
  adapter alike.
- **Blobs.** `WriteIfVersion` loses to a newer write.

An adapter that cannot pass an assertion for a stated engine reason documents the
reason in its own page and the suite names the exception; a silent skip fails the
suite.

## Implementation status

Business code uses the lease and the trigger as `rails.Leases` (`Acquire`,
`Renew`, `Release`, and `Do`, which runs a tick under a lease and cancels it if
the lease is lost) and `port.Trigger`; a report of a target is one blob written
alone (`rails.BlobReports.Put`).

The Go interfaces are in `internal/port` (`State`, `Index`, `Blob`, `Trigger`, `Secrets`,
`Identity`; the audit sink is `audit.Recorder`, unchanged), the conformance
suite is `internal/port/porttest`, and `internal/store` builds one set of ports
from the `ports.adapter` key of the configuration file and hands it to the
apps. Business packages depend on the interfaces only; a test
(`internal/port/guard_test.go`) fails if one imports `internal/kube`,
`internal/valkey` or the legacy adapter. The interfaces differ from the tables
above in three ways, all small:

- `Blob` also has `Delete` and `List(prefix)`, because a report family is
  replaced as a whole and a snapshot is deleted with its workspace, and two
  optional capabilities, `Replacer` (replace every object under a prefix in one
  write) and `ReaderAll` (read them in one request).
- `State` is accompanied by `Index`, an unordered set with an expiry refreshed
  on `Add`. It is **transitional**: the issuer's session index is a Valkey set
  today, the layout replaces it with a prefix listing, and until the migration
  has run the legacy adapter has to keep writing sets. A new feature does not use
  it.
- `Identity` is `Verify(token, audiences) (subject, error)`, the seam over the
  existing verifiers; the legacy adapter is the cluster's `TokenReview`.

### The S3 Blob

An adapter of the Blob port, in `internal/port/s3blob`. It is chosen by
`ports.blob`, which replaces the Blob of whatever `ports.adapter` brings, so
State `legacy` with Blob `s3` is a valid pair and so is State DynamoDB with
Blob S3. It takes its credentials from the platform (Pod Identity, IRSA, a
Lambda role): no key is configured. It is marked 🧪 in
[../capabilities.md](../reference/capabilities.md): it passes the conformance suite on
LocalStack, and has not yet run against AWS.

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

### The in-memory adapter

`internal/port/memory` implements every port in process memory, with the
semantics above (revisions are a counter, expiry is judged by an injectable
clock, `Watch` delivers put, delete and expiry, `Trigger` coalesces, and `Secrets` holds the
values in process memory). It is what tests and the demonstration use, and what `ports.adapter: memory` selects.

### The DynamoDB adapter

`internal/port/dynamodb` implements State, the transitional Index and Trigger over
one DynamoDB table (`ports.adapter: dynamodb`, `ports.dynamodb`:
[configuration](../reference/configuration.md)), with `aws-sdk-go-v2` and the
platform's credentials (Pod Identity, IRSA, a Lambda role: none is configured).
It is shared by every replica and process, and Blob and Identity are not its:
with it `internal/store` takes them from the legacy adapter, unless `ports.blob`
names the S3 adapter. A DynamoDB table holds no credential, so the domain
stores need a secrets adapter beside it. It is marked 🧪: it
passes the suite on LocalStack, and has not yet run against AWS.

| Item attribute | Type | Meaning |
|---|---|---|
| `pk` | S | partition key: the record kind (`workspace`, `issuer-token`), or the Index set's kind for a member |
| `sk` | S | sort key: the id (at most 1 KiB), or `<set id>/<member>` |
| `lkey` | S | the logical key the item was written for (the set, for a member) |
| `v` | B | the value (State) |
| `rev` | N | the revision: a random 64-bit number drawn on every write |
| `expires` | N | epoch seconds the item is dead from; absent when permanent. The table's TTL attribute |
| `k` | S | `i` for an Index member; absent for a State record, so no State listing returns a member |

| Operation | DynamoDB |
|---|---|
| `Get` | `GetItem` with `ConsistentRead`; an expired item is `ErrNotFound` |
| `Put` | `PutItem`, unconditional |
| `Create` | `PutItem` with `attribute_not_exists(pk) OR (attribute_exists(expires) AND expires <= :now)`; a failed condition is `ErrExists`, and of several takers of an expired record exactly one wins |
| `Update(rev)` | `PutItem` with `rev = :rev AND (attribute_not_exists(expires) OR expires > :now)` |
| `DeleteIfRevision` | `DeleteItem` with the same condition |
| `Delete` | `DeleteItem` |
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
  processes and kernel pods on DynamoDB tick across replicas, with up to one poll
  interval of delay. This is the decision for the Kubernetes deployment on AWS. The
  asynchronous `lambda:Invoke` of [0029](../decisions/0029-ticks-per-target-under-a-lease.md)
  (the table at the top of this page) is the Lambda platform's Trigger and a
  different adapter (`lambda`), where the invocation is the delivery and there is no
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

### The domain stores

`internal/portstore` implements, on State and Secrets, the interface each
domain store already had, so business code did not change; `internal/app` picks
the implementation in one place (`openStores`): **any `ports.adapter` but
`legacy` keeps the domain records on the ports, `legacy` keeps the ConfigMaps
and Secrets of `internal/kube` unchanged** (a demonstration keeps its fixed
stores either way). The controllers read the same records the same way
(`controller.RecordSource` on the Slack side, `controller.AppSource` on the
GitHub side, both implemented by `portstore`) instead of the mounted
directories, and are woken by a poll of the records' revisions, which is the
digest of the mounted files made over keys and revisions (no value is read or
opened for it). The kernel still runs `legacy`.

| Domain (interface) | Keys | In Secrets (`credentials/<kind>/<id>/<ref>`) |
|---|---|---|
| directory workspaces (`hub.Store`, `hub.CredentialStore`) | `ws.dir.<id>`: the record, naming the credential | the credential |
| GitHub organisations, the link App, confirmations, pass requests (`server.GitHubConnections`, `GitHubLinkApp`, `GitHubConfirmations`) | `gh.org.<org>`, `app.gh.link`, `gate.github.<org>.confirm`, `.pass` | the App key, the link App's client secret |
| a person's GitHub link (`server.GitHubLinks`, `controller.LinkStore`) | `gh.link.<account>`, `gate.github-claim.<account>` | the token pair |
| runner and catalogue Apps (`server.GitHubRunnerApps`, `GitHubCatalogueApps`, `SlackCatalogueApps`) | `app.gh.runner.<tier>.<org>`, `app.gh.cat.<id>`, `app.slack.cat.<id>` | the App key, or the client secret and bot token |
| Slack workspaces (`server.SlackWorkspaces`) | `ws.slack.<workspace>`, `gate.slack.<workspace>.…` | client secret and bot token |
| Slack Connect and console channel records (`server.SlackSharedRecords`, `SlackChannelRecords`) | `rec.slack.shared.<name>`, `rec.slack.channel.<workspace>.<name>` | — |
| the console's session key | `rec.console.session-key` | the key |
| `users.info` (`apply.MemberCache`) and the Slack Connect hand-off (`controller.Handoff`) | `cache.slack.user.<workspace>.<id>`, `share.<host>.<channel>` | — |
| the OAuth client (`settings.Store`) | none: see below | — |

- **Secrets, not sealing.** A credential is written to Secrets under
  `credentials/<kind>/<id>/<ref>` and the item in State names it by its ref; State never
  holds one. (The envelope-and-KMS sealing that was here is retired, together
  with the `sluis:binding` encryption context and `ports.sealer`.) The ref is
  fresh on every write, so a credential is never replaced in place: a writer
  that loses the compare-and-swap of the item has written a secret nobody
  names, which is removed, and cannot have replaced the one the winner's item
  names (a spent single-use refresh token never overwrites the new pair). The
  credential is written before the item that names it, and the one it replaces is
  removed after (best effort: a leftover is unreachable). The copy of the record
  inside the credential (which a restore read) and the Slack records' recovery
  mirror are not needed, and `ReconcileRecords` is a no-op there. A directory
  workspace's record is rewritten by every probe and carries its credential's
  name along untouched; a credential saved before its record is an item that is
  not listed. A read opens only what it needs: listing the organisations,
  workspaces or Apps never reads Secrets, and a link read with its tokens (the
  controller's check) costs one `Get` per self-link. Key characters a secret
  path does not allow (the `~XX` of a name) are written as `u-` and the
  segment's bytes in hex. Starting on an adapter set with no Secrets (the legacy
  one's, and DynamoDB's until a secrets adapter is chosen) **stops the start**,
  naming the secrets adapter, instead of failing on the first credential an
  operator connects. A record and its credential are no longer one item: the
  order above is what stands in for it.
- **Every update is a compare-and-swap** (`editRaw`): read, change, `Update(rev)`
  or `Create`, retried against what a concurrent writer left, 16 times. The
  stores that took `decide` callbacks (`Apply`) keep their contract: `decide` runs
  again on every retry, and a conflict that outlasts them is
  `ErrSharedConflict` or `ErrChannelConflict`.
- **A person's link is one item and one compare-and-swap.** `gh.link.<account>`
  holds the link, with its token pair named in Secrets. The link's own `Revision` (which a check
  uses so that a person who linked again meanwhile is never overwritten) is
  checked **under the key's revision**, so of two writers that read one link
  exactly one writes it. The refresh is the controller's two phase write, kept:
  (1) the `RefreshingSince` marker is written with `Update` at the revision read;
  (2) GitHub is asked for the new pair; (3) the pair is written, clearing the
  marker. A plain read-exchange-write would let two replicas exchange the same
  single-use refresh token, the loser's exchange being refused as a revoked
  authorization; the marker write is the claim, and only the replica whose swap
  lands may exchange. The replica that loses re-reads: when the winner has
  finished (the marker cleared, a pair that does not need renewing) it checks
  with the winner's pair, otherwise it leaves the link to the next pass. A crash
  between (1) and (3) leaves the marker, as before, and the next pass asks
  whether the old token still works. The test runs two controllers over one
  State, with a barrier that makes both read the same
  revision before either writes, and counts the exchanges at the fake GitHub:
  exactly one. With the compare removed it fails with two.
- **A claim spans keys, so it is steps with a marker.** `Claim` writes
  `gate.github-claim.<account>`, then the claimed link, then narrows each other
  account that held one of its addresses (each its own swap, re-reading the
  account first), then deletes the marker. A reader that finds the marker
  finishes the narrowing (`List` does), so a crash leaves an address proven by two
  accounts for a moment, never by none. `Adopt` and `Invalidate` are per-link
  swaps that re-check the account when they write; `Adopt`'s check across
  candidates is made against the links read at its start, so two `Adopt`s at once
  could both take one address (the kube store did it in one write); it is run by
  one actor.
- **The Slack Connect hand-off** is `share.<host>.<channel>`. After Slack accepts
  an invitation the host's tick writes the guest's side as `pending` (the write is
  the notification: it asks the guest's runner to tick through the Trigger, which
  crosses processes on a shared State; a lost notification is answered at the guest's next
  sweep), the guest's tick accepts the invitation and marks its side `accepted`,
  and the host reads it. The record lives 14 days while a guest is pending (as
  long as the invitation) and 7 days once every guest has accepted. The
  process-local hint stays when no hand-off is configured, and is used if the
  write fails. The tests run the host and the guest as two controllers over one State.
- **The `users.info` cache** answers `Observe`'s who-is-this lookup of a channel's
  other members, 24 hours, shared by every runner; the account of an address, who
  the token is, the channels and their members are read from Slack every pass,
  because a decision rests on them. A member who left is reported as a leaver up
  to a day late; nothing is removed on a cached answer, and a deactivated account
  is never cached. `slack_roster.user_cache{workspace,result=hit|miss}` counts it.
- **The shared inputs cache stays in memory.** What the Slack controller shares
  across a sweep (who holds each bound group, each directory group's members, what
  each directory serves, the decoded records) is a graph of the controller's own
  types, rebuilt from the console in one round of calls; serialising it under
  `cache.<digest>.<name>` is not cheap and buys one round of calls per runner per
  five minutes. A second replica reads the console as the first does. It is the
  first thing to move if the console's load shows it.
- **The OAuth client is an input, not a record.** `settings.Store` only reads the
  client the deployment declared (a mounted Secret, which the service never
  writes; the console's old write path and the memberships have been dead since
  they moved to policy), so it stays what it is: read from the declared Secret
  where a cluster is reachable, otherwise empty.
- **What is not atomic any more.** A console channel's check against every other
  channel's records (a channel id declared twice) is made on the listing read at
  the start of the write, not under one version of all of them as the ConfigMap
  gave: two creates at once of different names for one Slack channel could both
  pass. The names a record is kept under are what each swap protects.
- **Conformance.** `porttest` is unchanged: nothing generic was needed. The
  domain tests (`internal/portstore`, the two controllers and `internal/app`)
  run each store over **memory State and Secrets** (`portstoretest`), and open two
  "processes" onto one State where the point is a second replica.

### The legacy adapter

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
