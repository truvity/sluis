# 0028 — Audit is its own product, truvity/audit: its pieces, transport, search and front moved there

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #903](https://github.com/truvity/sluis/issues/903).

## Context

Every flow in sluis is audited, and so is every flow of a product that keeps its audit trail beside sluis. Several parties emit and several parties read, and who emits and who reads must be kept apart without a separate audit for every pair. The answer is four pieces (writer, witness, indexer, reader) fed by an emitter library over a transport, one search index per scope, and an audit installation with its own front.

That design does not need an identity product. A session service, a certificate tool and any product that sells by use need the same audit. So audit is a product of its own, [truvity/audit](https://github.com/truvity/audit), built on atoom's ports and blocks ([atoom 0013](https://github.com/truvity/atoom/blob/master/docs/decisions/0013-what-moves-from-sluis.md) records what moved). The decisions of this record moved with it. This record stays under its number, so that links to it keep working, and says where each part went and what sluis keeps.

## Decision

### What moved to audit

| Subject of the old record | Now in audit |
|---|---|
| One audit per scope, a project per emitting unit, the scope's auditor reads every project | [0002 pieces and holders](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md), [0003 the neutral model](https://github.com/truvity/audit/blob/master/docs/decisions/0003-the-neutral-model.md) |
| The pieces (emitter, writer, witness, indexer, reader), no notary, the index is derived data, bounded blob requests | 0002 |
| The transport: direct and queue modes, a stream per project, the default per shape, the spool fallback, emitters known by platform identity | 0002 |
| The documents the pieces read (`AuditLog`, `AuditWitnesses`, `AuditCatalogue`) | [0015 configuration kinds](https://github.com/truvity/audit/blob/master/docs/decisions/0015-configuration-kinds.md) |
| Search: one index per scope, the indexer, what the index stores, purge at a tenant's exit, rebuild, the index drivers, row-level security, provisioning modes, the reader API | [0012 search and the reader API](https://github.com/truvity/audit/blob/master/docs/decisions/0012-search-and-the-reader-api.md) |
| Access: two independent checks, the bindings, audit without sluis | [0013 access](https://github.com/truvity/audit/blob/master/docs/decisions/0013-access.md) |
| The audit front, its console, `audit verify` | [0014 the front, the console page and `audit verify`](https://github.com/truvity/audit/blob/master/docs/decisions/0014-the-front-the-console-page-and-audit-verify.md) |
| The Lambda shape of the pieces | [0016 distribution and the Lambda shape](https://github.com/truvity/audit/blob/master/docs/decisions/0016-distribution-and-the-lambda-shape.md) |

### What atoom owns

The transport, index and blob ports, their conformance kits and their drivers are atoom's, with their own pages ([transport](https://github.com/truvity/atoom/blob/master/transport/docs/transport-port.md), [index](https://github.com/truvity/atoom/blob/master/index/docs/index-port.md), [blob](https://github.com/truvity/atoom/blob/master/blob/docs/blob-port.md)). The spool transport, the `sqlite` index and the file system blob drivers are built there. The NATS JetStream transport and the `rqlite` and PostgreSQL index drivers are atoom drivers and are planned; atoom's [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md) orders them, and the same plan makes the three ports generic.

### What sluis keeps

- **sluis emits records through audit's emitter** (Go and Rust). Every work actor has it on its audit emit port ([0002](/decisions/0002-families-of-actors/)).
- **sluis maps its boundary to an audit scope** and keeps its tenants as tenants. Its actors are components, and its actor name stays `<actor>@<boundary>`. sluis is one project of each scope it runs in, and a product that keeps its audit trail beside sluis is a project of the same scope ([0008](/decisions/0008-boundaries-and-tenants/)).
- **sluis declares which of its actions are audited,** in its action table. The `unmapped.sluis` fields of that table stay sluis's input; audit's name for the product object is `unmapped.<product>` ([0030](/decisions/0030-audit-records-and-the-usage-stream/)).
- **sluis's key service hosts audit's keys where audit runs beside sluis** ([0006](/decisions/0006-keys-and-sealing/)), and `policy` seals the grant that key@ checks before it opens a tenant's records ([0007](/decisions/0007-policy-and-sealed-grants/)). Today the grant names the tenant and not the log, so the log check is Cedar's alone; audit records the open question in its record 0013.
- **sluis's `ca` issues the client certificates** of the database roles of a PostgreSQL index ([0023](/decisions/0023-certificate-authorities/)), and `exchange`, the one writer of `Tenant`, creates a tenant on a product's push ([0008](/decisions/0008-boundaries-and-tenants/)).
- **sluis's console embeds audit's console page** ([0027](/decisions/0027-the-console/)). An audit installation has its own front, owned by audit. The front's host is a sibling of sluis's host, and every passkey verifier of sluis lists it ([0017](/decisions/0017-identity-and-sign-in/)).
- **The audit writer on Lambda fits audit's Lambda record,** behind the Web Adapter like every function ([0011](/decisions/0011-runtimes-and-installations/)).
- **The code still sits in sluis.** The emitters, the writer, the indexer and the reader are built here, and they move to audit in steps. Audit's records say what is built and what is planned.

## Consequences

- One record of each fact. A change to the pieces, the transport, search or the front is a change in audit, and a record in audit.
- sluis depends on audit's emitter and on audit's console page, each by version.
- sluis has no audit family of actors. The actors `writer`, `indexer` and `reader` in this repository are audit's pieces, built here until the code moves.

## Roads not taken

- **Keep audit inside sluis.** Every product that wants an audit trail would depend on an identity product.
- **Audit calls sluis's policy actor.** Audit without sluis would need one, and audit would depend on sluis's grant format. Audit uses the shared Cedar library with its own schema.
- **A second console bundle in sluis for the audit pages.** The page is one bundle that other consoles embed.

## Follow-ups

- Open: the NATS JetStream mapping (one operator per environment, an account per scope, a subject per project, the user named after its emitter, a scoped signing key per role) and the rule that the keys NATS requires are a named exception of the key service are in no record of audit or atoom. They are decided with the transport driver.
- Open: the measurement of rqlite full-text search (rqlite 10.6.6, one node and three nodes, 2026-10-30) and of the spool sockets (2026-11-04, one audit group opens both sockets, so the other emitters are refused by the guard and not at the socket) are in no record of audit or atoom. They belong to the driver research of atoom and to the separate receive group that audit plans.
- The build of the search piece and its parity with v1: [#839](https://github.com/truvity/sluis/issues/839). The build of the audit front: [#985](https://github.com/truvity/sluis/issues/985).
