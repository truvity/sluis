# Ports: the contract of each port

What each port promises and every adapter must keep: operations, errors, semantics and the conformance suite. The
shape and the reasons are in [ports and adapters](../explanation/ports.md); the logical keys are in [keys](keys.md);
what each adapter does beyond the contract is in [adapter details](port-adapters.md); which adapters exist is
[adapters](adapters.md); where each piece is built and how far is [capabilities](capabilities.md).

## Secrets

`port.Secrets` (`internal/port/secrets.go`) is whole values under slash-separated
paths, each with a version: `Get(path) (value, version)`, `Put`, `PutIfVersion`
(`ErrConflict` when the version moved, `ErrNotFound` when gone, an empty version
means "only if absent"), `Delete` and `List(prefix)` (names, never values, by whole
segments). A value is at most `MaxSecret` (8 KiB, an SSM advanced parameter's
limit). Exports live under `export/` (`port.ExportPrefix`). The suite is
`porttest.RunSecrets`; the `memory`, `ssm` and `openbao` adapters pass it, and an adapter for a
real store runs it against the engine.

The secrets concern is wired by `internal/store`: an adapter that configuration
chose (a preset, the platform answers or `adapters.secrets`) is built from its
settings and is `Set.Secrets`. With nothing chosen (the `ports` keys alone, which
is every deployment before the presets) there is no Secrets port, as before.

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
  [layout](keys.md) marks as permanent (`0`). A call with no lifetime that
  the layout does not allow is refused up front, as the current store refuses it.
- **A revision is opaque and strictly changes on every write.** Callers compare
  it for equality and never order it.
- **Reads filter on expiry.** `Get` and `List` return a record only if its
  expiry is in the future by the caller's clock, whether or not the engine has
  already removed it. DynamoDB removes expired items lazily and later, and
  the legacy adapter's Valkey and ConfigMap have no lifetime of their own. None is relied on.
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
migrate` ([migrating state](../how-to/migrate-state.md)) reads the issuer's
state through them so a copied session keeps the lifetime it had; memory, DynamoDB and
the legacy adapter have them.

### Error mapping

An adapter maps its engine's errors to the six above. Everything else is an
`ErrUnavailable`, which the caller treats as "the store is down" and, on the
sign-in path, **refuses** the request rather than degrading, exactly as the
current store does.

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

With the `dynamodb` adapter, `Notify` is a `Put` of a `notify.<target>` key (a one-minute lifetime) and `Subscribe` is
a polling watch on that prefix, so a notification crosses processes and replicas. With `invoke` (the Lambda
platform), `Notify` is an asynchronous `lambda:Invoke` of the tick function with the target in the payload, and the
invocation **is** the delivery: the function's handler is the subscriber. EventBridge Scheduler invokes the same
function for every target on a period as the backstop. `legacy` and `memory` are in-process: a notification reaches
only this process.

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
([configuration](exports.md)),
run by `internal/exports`: one worker per export, under a per-export lease on the
State, retried with backoff, and counted
([telemetry](../operations/telemetry.md#the-exports)).

## Inputs

Read-only. The policy, the configuration file
([0032](../decisions/0032-one-configuration-file-one-binary-one-chart.md)) and
the secrets an operator manages. On Kubernetes they are mounted ConfigMaps and
Secrets, polled for change; on Lambda they are the configuration layer's files, or read from
a parameter store at start. The service **never writes** an input and holds no
permission to.

## Identity

Two directions. **Inbound**, a workload proves itself to the issuer with a
ServiceAccount token or an AWS federation token
([connecting AWS workloads](../how-to/connect/aws-workloads.md)); the verifier is
platform-independent, and the installation declares the clusters and accounts it
trusts ([0030](../decisions/0030-workload-identity-on-both-platforms.md)).
**Outbound**, the service takes its own identity from the platform (a projected
token or a role) and hands it to the adapters that need one; no adapter reads a
credential from anywhere else.

## Audit sink

Records the service's own actions in an audit installation. Transports: `connect`
(the audit installation's receiver) and `sqs` (a queue its writer Lambda consumes). A record that cannot be written durably
refuses the action it describes where the action is a sign-in, as today
([design](../explanation/design.md#audit)).

The `sqs` adapter's settings, IAM and durability are in [adapter details](port-adapters.md#the-sqs-adapter). The sink is chosen by the `audit` concern of the resolved table: `connect` (the
`audit.writer` of a service document), `log` (the log line only) or `sqs`.

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

## The Go interfaces

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

