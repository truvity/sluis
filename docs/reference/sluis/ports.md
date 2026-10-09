# Ports

The contract each port keeps and every adapter must meet. For the shape, read [ports and adapters](../../concepts/sluis/ports.md). Logical keys are in [keys](keys.md), adapter specifics in [adapter details](port-adapters.md), the adapter list in [adapters](adapters.md), build status in [capabilities](capabilities.md).

Go interfaces are in `internal/port`, the suite in `internal/port/porttest`. `internal/store` builds the set from `ports.adapter`.

## Secrets

`port.Secrets`: whole values under slash-separated paths, each with a version. A value is at most `MaxSecret` (8 KiB). The suite is `porttest.RunSecrets`. Decided in [ADR 0041](../../decisions/0041-the-secret-contract.md).

| Operation | Meaning | Errors |
|---|---|---|
| `Get(path)` | value and version | `ErrNotFound` |
| `Put(path, value)` | write unconditionally | |
| `PutIfVersion(path, value, version)` | write if the version is unchanged; empty version means only if absent | `ErrConflict`, `ErrNotFound` |
| `Delete(path)` | remove | |
| `List(prefix)` | names by whole segments, never values | |

## State

Key-value records with a lifetime and a revision, no relations. Keys look like `a.b.c`; a prefix ends at a `.`. A value is at most 256 KiB; larger content is a [blob](#blob).

| Operation | Meaning | Errors |
|---|---|---|
| `Get(key)` | value and revision | `ErrNotFound` if absent or expired |
| `Put(key, value, ttl)` | write unconditionally | `ErrTooLarge` |
| `Create(key, value, ttl)` | write if absent; expired counts as absent | `ErrExists` |
| `Update(key, value, ttl, rev)` | compare-and-swap on the revision | `ErrConflict`, `ErrNotFound` |
| `Delete(key)` | remove; no error if absent | |
| `DeleteIfRevision(key, rev)` | remove if the revision is unchanged | `ErrConflict`, `ErrNotFound` |
| `List(prefix, page)` | live records in key order, paged | `ErrBadPage` |
| `Watch(prefix)` | put, delete and expiry events from now | stream ends with the adapter error |

| Rule | Meaning |
|---|---|
| Single key | No multi-key transactions or secondary indexes |
| TTL | Required on `Put`, `Create`, `Update` unless [keys](keys.md) marks the record permanent (`0`) |
| Revision | Opaque; changes on every write; compare for equality only |
| Expiry | `Get` and `List` filter on the caller's clock, whatever the engine has swept |
| Paging | A token continues from a key; no record appears twice |
| Watch | At-least-once, unordered across keys, carries key and revision; reconcile with `List` after a reconnect |
| Other errors | Mapped to `ErrUnavailable`; sign-in refuses the request |

A lease is `Create` of `lease.<target>` with a short TTL, renewed by `Update`, released by `DeleteIfRevision`. A holder that fails to renew stops before its next external write. Code uses `rails.Leases` (`Acquire`, `Renew`, `Release`, `Do`).

Optional capabilities:

| Capability | Meaning |
|---|---|
| `StateExporter`, `IndexExporter` | Live records or sets with remaining lifetime; used by [`sluis migrate`](../../guides/sluis/migrate/migrate-state.md) |
| `RevisionPeeker` | `PeekRevision(key)` returns an eventually consistent revision, never a value |
| `Replacer`, `ReaderAll` (Blob) | Replace or read every object under a prefix in one request |

`Index` is a transitional unordered set with an expiry refreshed on `Add`. New features do not use it.

## Blob

Whole objects read whole: target status reports and directory snapshots. Both platforms use S3 with a prefix per kind (`reports/<target>`, `snapshots/<directory>`), server-side encryption and no public access. `Blob` also has `Delete` and `List(prefix)`.

| Operation | Meaning | Errors |
|---|---|---|
| `Read(name)` | object and version | `ErrNotFound` |
| `Write(name, body)` | replace | `ErrUnavailable` |
| `WriteIfVersion(name, body, version)` | replace if unchanged | `ErrConflict` |

## Trigger

| Operation | Meaning |
|---|---|
| `Notify(target)` | ask for a tick; coalesces with one waiting |
| `Subscribe(handler)` | run `handler(target)` for each notification |

| Adapter | Delivery |
|---|---|
| `dynamodb` | `Notify` puts `notify.<target>` (one-minute lifetime); `Subscribe` polls the prefix, across replicas |
| `invoke` | `Notify` is an asynchronous `lambda:Invoke` of the tick function; EventBridge Scheduler is the backstop |
| `legacy`, `memory` | In-process only |

A notification may be duplicated or lost; the lease and the backstop cover both. Writing a `share.` record notifies the guest's tick.

## Inputs, identity and audit

| Port | Contract |
|---|---|
| Inputs | Read-only policy, [configuration file](../../decisions/0032-one-configuration-file-one-binary-one-chart.md) and operator secrets. Sluis holds no permission to write them |
| Identity | `Verify(token, audiences) (subject, error)`. Inbound: ServiceAccount or AWS federation token ([connect AWS workloads](../../guides/sluis/connect/aws-workloads.md), [ADR 0030](../../decisions/0030-workload-identity-on-both-platforms.md)). Outbound: the platform's own token or role |
| Audit sink | `connect`, `sqs` or `log`, from the `audit` concern. A sign-in is refused when its record cannot be written ([design](../../concepts/sluis/design.md#audit)). Settings: [the sqs adapter](port-adapters.md#the-sqs-adapter) |

## Conformance

One suite runs against every adapter and gates adapter changes and `sluis migrate`. DynamoDB runs on LocalStack. An adapter that cannot pass an assertion documents the reason; a silent skip fails the suite.

| Assertion | Check |
|---|---|
| CAS races | Of N concurrent `Update` or `Create`, one succeeds, the rest return `ErrConflict` or `ErrExists`. `DeleteIfRevision` loses to a newer write |
| TTL visibility | A record is visible until expiry and never after; `Create` succeeds over an expired one |
| Lease takeover | A held lease cannot be taken; an expired one is taken by one racer; a late renewal fails |
| Prefix paging | Every live record once, in order, across pages and concurrent writes; a foreign token is refused |
| Revisions | Every write changes the revision; a stale `Update` fails |
| Watch | Put, delete and expiry are observed |
| Limits | Oversized values give `ErrTooLarge` |
| Blobs | `WriteIfVersion` loses to a newer write |
