# Why does audit exist?

Organisations keep several audit logs that disagree. One product writes an activity table. Another writes sampled log lines. A billing system counts requests in its own store. An identity provider keeps events for thirty days. Each answer to an auditor, a disputing customer or a regulator has a different shape, retention and idea of the actor.

Audit gives one answer.

## What it provides

| property | meaning |
|---|---|
| Complete | An accepted record is never lost. Privileged and billable actions wait until the record is durable. |
| Immutable and provable | Copies live under S3 Object Lock in compliance mode where a framework demands it. Each object names the SHA-256 of its bytes and each record its own hash. `verify` checks them from the archive alone. Seals add proof that nothing was removed. |
| Purpose-bound | One event is kept once per purpose, with that purpose's fields, identity treatment and retention. |
| Owned by the application | One installation belongs to one application, in its namespace, with no shared audit service to wait on. |
| Extensible | Applications add data through JSON Schema slots and register an action catalogue. |
| Searchable | Faceted search, typed predicates, cursor pagination, a tail cursor, exports and a viewer. |
| Metered | The same records feed usage billing with idempotent projections and immutable monthly statements. |

Purpose-bound copies differ by profile. Security keeps client addresses for a year. Billing keeps quantities for seven years and no people. Evidence keeps lifecycle facts for seven years after expiry. A tenant's history keeps sentences and diffs as long as the product promises.

## What it is not

- Not an application log pipeline. Logs may be sampled, rotated and dropped. Audit records are facts with a legal life.
- Not a SIEM. It exports to one in OCSF, ECS or OpenTelemetry form.
- Not a wallet's transaction log. It never carries presentation contents, attribute values or identifiers that link transactions.
- Not a ledger database. Object storage is the record, and every database is a rebuildable projection.

## Who reads it

A security analyst during an incident, a tenant administrator, a finance controller, a customer disputing an invoice, an internal auditor, a conformity assessment body under eIDAS and a tax inspector seven years later. Each reads a different profile of the same records.

## Decided in

- [0045 S3 Object Lock as the record](../../decisions/0045-s3-object-lock-as-the-record.md)
- [0044 Profiles composed from framework profiles](../../decisions/0044-profiles-composed-from-framework-profiles.md)
- [0053 One installation per service or product](../../decisions/0053-one-installation-per-service-or-product.md)
- [0061 Seals](../../decisions/0061-seals.md)
