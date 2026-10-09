# Concepts

## Record

A record is one thing that happened, as seen by one source. It is defined in [record.proto](../../../audit/proto/audit/v1/record.proto). The spine is `id`, `occurred_at`, `recorded_at`, `source`, `action`, `operation`, `outcome`, `tenant_id`, `actor`, `targets` and `context`. Around it sit `subject`, `capture`, `previous_attributes`, `data`, `meter`, `attributes` and `unmapped`.

A profile decides which fields a copy carries. Framework profiles name what a core field must, may and may never keep. A field no framework profile names is dropped. Each extension property carries a class (shared, audit, metering, history or evidence) and a PII level, and a profile keeps the classes its framework profiles keep. `audit profile explain <name>` prints what a profile keeps.

The record carries identifiers, never identity attributes. Whoever may see names and e-mail addresses resolves them at read time.

## Action and operation

`action` is fine-grained and namespaced by source, for example `wallet.credential.issued`. `operation` is one of seven values: create, access, modify, remove, authentication, transfer, restore. Both appear on every record.

## Catalogue

A catalogue lists a source's actions and what each carries: operation, framework categories, profiles, capture level, delivery mode, target types, the schema of `data`, a sentence template per locale and an optional meter. It also lists the source's actor kinds, target types, context areas and meters.

You author it next to the emitting code and validate it in that code's CI. It is registered at deploy and copied into the archive on first use. The format is [catalogue.schema.json](../../../audit/sdk/schemas/catalogue.schema.json).

## Extension slots

A slot is a fixed place where a source attaches its own data. A discriminator selects a registered JSON Schema.

| slot | keyed by |
|---|---|
| `data` | action |
| `targets[].attributes` | target type |
| `actor.attributes` | actor kind |
| `context.areas.<area>` | area |
| `meter.dimensions` | meter |

Extension schemas are closed objects. Every property carries its class and PII level. See [extension points](../../reference/audit/extension-points.md).

## Actor kinds and identity tiers

An actor kind has a category: internal (staff), external (end users and customers' people) or machine (services, API keys, the system). Framework profiles set the identity treatment per category: clear, pseudonym, scoped or omit.

Tenant identifiers stay in clear everywhere. Internal actors stay in clear for the security retention.

With no key provider, the default, you declare that external identifiers are already opaque. A framework profile's `pseudonym` then behaves as `clear`. With a key provider, external actors and subjects become a keyed pseudonym per tenant and purpose, and erasure is key destruction. See [key custody](key-custody.md).

## Profiles and framework profiles

A framework profile is what one framework requires: fields, categories, identity treatment, retention, integrity and review, with citations. A profile is a deployment's composition of framework profiles plus a destination. The writer produces one copy per profile. See [framework profiles](../../../audit/profiles/README.md).

## Prefixes

Each copy lands under `records/<profile>/<tenant>/` and then the hour of ingest, one object per ingest batch, with Object Lock retention set per object. A reader finds a record by the hour it was ingested, not the date it occurred. The [bucket contract](../../reference/audit/bucket-contract.md) specifies the layout.

The writer copies the descriptions on first use. The catalogue lands at `catalogue/<app>/<version>`, written once. The extension schemas, the record's schema and its proto land under a schema prefix.

## Projections

A projection is anything that is not a prefix copy: the facet index and counts, metering rollups and statements, SIEM exports and analytics tables. Each is idempotent and rebuildable from the prefixes.

## Meta-events

These are records too, in the [common catalogue](../../../audit/sdk/catalogue/common.yaml): reads and exports of the trail, catalogue and profile changes, key destruction, legal holds, verifications, writer lifecycle and the daily clock check.

## Decided in

- [0043 Record schema](../../decisions/0043-record-schema-proto-with-json-schema-slots.md).
- [0044 Profiles composed from framework profiles](../../decisions/0044-profiles-composed-from-framework-profiles.md).
- [0045 S3 Object Lock as the record](../../decisions/0045-s3-object-lock-as-the-record.md).
- [0047 Identity tiers and pseudonymisation](../../decisions/0047-identity-tiers-and-pseudonymisation.md).
- [0055 No pseudonymisation keys by default](../../decisions/0055-no-pseudonymisation-keys-by-default.md).
- [0060 v1 bucket layout](../../decisions/0060-v1-bucket-layout.md).
