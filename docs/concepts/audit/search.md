# How does search work?

Search has two halves: the index, written from the archive, and the searchers that read it. The searchers are Postgres over the index, a scan over the archive for a deployment with no database, and a memory one for tests. One conformance suite, `index/indextest`, holds all three.

## The index is a projection

Nothing a search returns is evidence. Every index row derives from the locked archive objects.

The writer does not index. An unreachable, lagging or absent index cannot touch the archive. The indexer (`audit-observe`) follows the bucket from its cursor when the database returns.

The index can change shape without migrating the trail. You can throw the database away: follow the bucket from an empty cursor, or run `audit reindex`.

## Indexing

```go
type Indexer interface {
    Index(ctx, profile string, rows []Row) error
    Purge(ctx, profile string, before time.Time, what Scope) error
}
```

`Index` is idempotent by `(profile, id)`. Counting has no call of its own: only the insert transaction knows whether a row was new, so `Row.Facets()` derives the deltas and the insert applies them. Facets fall in the hour a record was recorded, not the hour it occurred.

`Purge` takes a scope. `Identifying` clears the actor, subject, client address and correlation identifiers and keeps the event and the actor's kind. `Everything` removes the rows.

The core columns come from the record. An extension property is indexed only when its action schema marks it `filterable`, and counted only when marked `facet`. A reindex resolves the marks against the source and version the record carries, never the newest catalogue.

## Postgres layout

| table | holds |
|---|---|
| `events_core` | what happened |
| `events_context` | who it happened to, purgeable on its own schedule |
| `events_data` | the filterable extension properties |
| `facet_counts` | what the [Audit page](audit-page.md) navigation reads |
| `seen` | the writer's dedupe record |

No shipped framework profile states a purge schedule, so the purge job's `identifyingAfter` has no default.

The three event tables are partitioned monthly on `recorded_at`. The unique key is `(profile, id, recorded_at)`. The write path creates partitions, so a missed job loses nothing. Indexes are tenant-leading composites, BRIN on time, GIN on `target_ids`.

Row-level security backs the grant. The policies read two transaction-local settings: `audit.tenant_ids`, a JSON array of permitted tenants, and `audit.all_tenants`, which only an operator's grant sets. A connection that sets neither sees nothing.

`postgres.NewReader` pins every read inside a read-only transaction, and `audit-query` uses it. The policies bind only a role that does not own the tables. Connect the query service as a separate role with `select` only.

`audit migrate` applies the schema. A writer whose database is at another version refuses to start. Because the `seen` table is shared, the writer needs a database to run more than one replica; see [split writer](split-writer.md).

## Query model

A query names fields and operators from a closed set. The Authorizer's grant is AND-ed in as one more term, so no path reaches a row outside it.

Postgres supports predicates on core fields, targets and filterable extension properties. It supports no regular expression and no substring search. A prefix is a range.

Paging is keyset. Every ordering ends with the identifier, so ties are stable.

### Tail

`next` on the last page stays valid and advances on `recorded_at` plus the writer sequence. A poller never misses a late record. An empty page keeps the boundary it was asked from.

The archive scan orders by `occurred_at` only and refuses `recorded_at`. `searcher: s3scan` therefore searches the trail and cannot follow it.

## The archive scan

The scan's `Capabilities` state that it counts no facets and orders only by occurred time within an ingest day. It refuses other requests with the reason.

Its cursor is an object and a line. A cursor belongs to the searcher that issued it, and one from elsewhere is refused.

The scan walks ingest days, per the [bucket contract](../../reference/audit/bucket-contract.md). A query on `occurred_at` reads ingest days from its start to its end plus `Lateness` (default 24 hours). A record that arrived later is not found. The index has no such limit.

A budget in objects and seconds stops a scan and returns a cursor. A query without a time range walks back to a horizon, not to the beginning. You set both.

## Reading is recorded

Every answer records `audit.search`, `audit.facets` or `audit.get`, naming the caller, the target and the allowing rule. A refused read is recorded too.

A record outside the grant is reported as absent, not forbidden. The service carries an `OnUnrecorded` hook. Alert on it: an unrecorded read means the service is failing.

## Reindex

```sh
audit reindex --profile <p> --from <day> --to <day> \
    --database <url> --bucket <b> --catalogue <file>...
```

The range is of ingest days, read tenant by tenant in key order. Rerunning an indexed range is safe.

The catalogues are required. Without them the rebuild omits data columns, and a later run cannot repair that, because indexing counts a record once.

## Decided in

- [0045 S3 Object Lock as the record](../../decisions/0045-s3-object-lock-as-the-record.md).
- [0048 Search contract](../../decisions/0048-search-contract.md).
- [0062 Observe follows the bucket](../../decisions/0062-observe-follows-the-bucket.md).
- [0066 Indexer and query are separate processes](../../decisions/0066-indexer-and-query-are-separate-processes.md).
