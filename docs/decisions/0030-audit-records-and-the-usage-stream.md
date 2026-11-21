# 0030 — Audit records are OCSF events and usage is a separate stream: both moved to truvity/audit, and sluis keeps its action table and its `unmapped.sluis` fields

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #909](https://github.com/truvity/sluis/issues/909).

## Context

A log entry holds a security record and, for an action that uses something, usage events. The record is an event of a published schema (OCSF) in a canonical serialisation, validated against a catalogue that the emitting product publishes. The usage events are a stream of their own, with their own guarantee. None of this depends on an identity product, so the format moved to [truvity/audit](https://github.com/truvity/audit) ([0028](/decisions/0028-audit-pieces/) says why). This record stays under its number and says where each part went and what sluis keeps.

## Decision

### What moved to audit

| Subject of the old record | Now in audit |
|---|---|
| The record format: OCSF classes, the pinned version, canonical JSON and its bounds, the lean stored record, the health `check` exemption, a trace id and no trace content | [0004 the record format](https://github.com/truvity/audit/blob/master/docs/decisions/0004-the-record-format.md) |
| One product object, `unmapped.<product>`, with a closed schema and no OCSF extension | 0004 |
| The action table, the generated schemas, the catalogue and its policy version, validation in three places | 0004, and [0015 configuration kinds](https://github.com/truvity/audit/blob/master/docs/decisions/0015-configuration-kinds.md) for `AuditCatalogue` |
| The attributes an OCSF class requires, and `unknown` for a record that established no person | 0004 |
| The class mapping rules | audit's [action table and catalogue](https://github.com/truvity/audit/blob/master/docs/reference/action-table-and-catalogue.md) |
| The usage class: never dropped, an idempotency key, one call and one message for both parts, the usage log, no personal data | [0006 the usage class](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md) |
| The export of OCSF events | [0012 search and the reader API](https://github.com/truvity/audit/blob/master/docs/decisions/0012-search-and-the-reader-api.md) |

### What atoom owns

The registry that declares audit's documents is atoom's `state` block ([atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)); the extensible registry is planned work in atoom's [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md).

### What sluis keeps

- **sluis's action table is sluis's input to audit's emitter.** It is the file `docs/architecture/audit-actions.yaml`: for each sluis action its class and activity, required fields, the treatment of each field, the meters it may carry, its subject, target and request-id paths, and which fields are searchable. Audit's generator makes the schemas, the example records and the emitters' code from it. The writer refuses a record whose action is not in the catalogue.
- **sluis maps its words to audit's.** The boundary is the scope in `metadata.log_name`, an actor of sluis is a component whose `actor.user.uid` is `<actor>@<boundary>`, and the product is `sluis`. A person or an external actor is a pseudonym ([0029](/decisions/0029-the-audit-log/)).
- **sluis's own facts go in `unmapped.sluis`,** and nowhere else in `unmapped`.
- **The two ids `unregistered` and `unknown-audience` belong to no client.** They stand in a record for a request that names no registered client or an audience that is none, so store@ refuses a client document that declares either name ([0021](/decisions/0021-clients/)).
- **A request that entry's HTTP library rejects itself is audited too,** with the edge row and a closed reason. This is planned; today only a request that reaches the handler is audited ([0003](/decisions/0003-units-transports-and-fusion/)).
- **sluis aggregates usage per customer, organisation and product,** the claims of its token ([0020](/decisions/0020-grant-vocabulary-and-token-claims/), [0032](/decisions/0032-customer-identity-installations/)). How sluis calls the usage class is [0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/).

### The sluis fields: `unmapped.sluis`

The fields that no OCSF attribute holds and that sluis needs are declared in the action table as each action is built. They include the audience and scopes of a token, the sender binding, the authentication context, the key used, a configuration document taken, an attestation, an enrolment proof, a certificate, the actor chain of an exchange, and an approver. The generated schema is the exact list, and within a major version of sluis a field is only added. The emitter's own `lost` field is audit's ([0005](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md)); in sluis it is `unmapped.sluis.lost`. There are no rows for an allowance: sluis keeps no balance.

## Consequences

- One home for the format. A change to OCSF handling, canonical rules or the catalogue is a record in audit.
- A new sluis action is a row in sluis's table, and sluis publishes its catalogue before its emitters run.
- The generator, the emitters and the test vectors are still in this repository until they move to audit.

## Roads not taken

- **Keep the record format in sluis.** Every product that emits would take its format from an identity product.
- **A sluis OCSF extension for the fields without a core home.** A registry entry and a compile step in every strict consumer, dropped by Security Lake, for typed fields no consumer reads yet.

## Follow-ups

- Open: the neutral name of the pseudonym domain label (sluis binds `sluis-pseudonym-v1` today) and of the `lost` field for products other than sluis are audit's questions; sluis's pseudonyms change with the label.
- Open: which sluis actions carry meters, and the first meters, are written with the first caller of the usage stream ([#841](https://github.com/truvity/sluis/issues/841)).
- The emitter, the catalogue and the usage stream: [#841](https://github.com/truvity/sluis/issues/841).
