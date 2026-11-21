---
title: Audit
description: "What sluis emits to audit and where it goes: the emit port, the action table, the scope mapping, and the rules sluis keeps. The audit itself is truvity/audit."
status: partial
sidebar:
  order: 6
---

Audit is a product of its own, [truvity/audit](https://github.com/truvity/audit), and a neighbour of sluis, not a sluis family. It owns the log, the records, the pieces, the search and the verification. **This page says what sluis hands to audit and which rules sluis keeps; everything about the log is in audit's documentation** ([why it moved](/decisions/0028-audit-pieces/)).

## What audit is for sluis

Every flow in sluis is audited, refusals included, and sluis has no audit family of actors ([0002](/decisions/0002-families-of-actors/)). Audit's emitter library is a part of each sluis process, and the seal host of sluis's key service ([0006](/decisions/0006-keys-and-sealing/)) signs and seals for audit. Where audit runs beside sluis, the two share one scope: sluis is one project of it and a product that keeps its audit trail beside sluis is another ([0008](/decisions/0008-boundaries-and-tenants/)). Audit also runs without sluis ([audit 0001](https://github.com/truvity/audit/blob/master/docs/decisions/0001-purpose-scope-and-neighbours.md)).

## What every sluis actor emits

- **Every actor has an emit port** to audit's emitter, in Go and Rust ([0002](/decisions/0002-families-of-actors/#ports-of-a-work-actor)). One call builds one message, and the transport carries it to audit's writer.
- **A security record never blocks an operation that does not need it.** A fact that cannot be delivered is lost, and the loss is counted ([audit 0005](https://github.com/truvity/audit/blob/master/docs/decisions/0005-the-security-class.md)). The actions that issue something use the required call and fail when the record is not durable ([0029](/decisions/0029-the-audit-log/)).
- **A usage record exists only for an action that carries a meter.** Its emit blocks and is never dropped ([audit 0006](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md), [0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)).
- **The action table is sluis's.** [`docs/architecture/audit-actions.yaml`](https://github.com/truvity/sluis/blob/main/docs/architecture/audit-actions.yaml) lists each action, its OCSF class and activity, its outcomes and the members of `unmapped.sluis` it carries. Audit's generator makes the schemas and the emitters' tables from it ([audit's action table](https://github.com/truvity/audit/blob/master/docs/reference/action-table-and-catalogue.md), [0030](/decisions/0030-audit-records-and-the-usage-stream/)).
- **The scope mapping is sluis's.** A boundary is an audit scope, an actor is a component named `<actor>@<boundary>`, a tenant is audit's tenant, and the product object is `unmapped.sluis` ([audit 0003](https://github.com/truvity/audit/blob/master/docs/decisions/0003-the-neutral-model.md)). A person is a pseudonym in a record, made from the identifier sluis chooses.

## Where the records go

Audit's transport carries each message to the project's writer, which seals it into a signed, witnessed log, one audit per scope ([audit 0002](https://github.com/truvity/audit/blob/master/docs/decisions/0002-pieces-and-holders.md), [audit 0008](https://github.com/truvity/audit/blob/master/docs/decisions/0008-the-log.md)). Readers use audit's reader and its `audit verify`, which works from the bucket alone and checks the history of checkpoints too ([log layout](https://github.com/truvity/audit/blob/master/docs/reference/log-layout.md), [audit 0012](https://github.com/truvity/audit/blob/master/docs/decisions/0012-search-and-the-reader-api.md), [audit 0014](https://github.com/truvity/audit/blob/master/docs/decisions/0014-the-front-the-console-page-and-audit-verify.md)). sluis's console embeds audit's console page and owns nothing of it ([0027](/decisions/0027-the-console/#the-audit-page)).

## The rules sluis keeps at the boundary

- **A read of the audit is an audited action.** A search is the action `reader.search` in the action table. The rows for get, export and tail come with those calls.
- **The grant comes from sluis's policy.** Today the audit reader asks `policy` for a grant, and key@ opens a tenant's records only under a grant that `policy` sealed ([0007](/decisions/0007-policy-and-sealed-grants/)). Planned: audit decides with Cedar as a library and its own schema, and sluis's grants are one input ([audit 0013](https://github.com/truvity/audit/blob/master/docs/decisions/0013-access.md)).
- **The limit check is sluis's.** It compares a usage counter with a limit on the request path, and a refusal by a limit is not metered use ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/), [0040](/decisions/0040-meters-and-usage-evidence/)).
- **Break-glass recovery does not wait for audit.** It spools its record locally and delivers it later ([Break-glass recovery](/security/break-glass/)).
- **The requirements that stay in sluis** are at [REQ-AUDIT](/requirements/audit/).

## Tenants, encryption and erasure

Evidence is sealed under each tenant's `evidence` key ([audit 0011](https://github.com/truvity/audit/blob/master/docs/decisions/0011-evidence-encryption-and-pseudonyms.md)). The tenants' keys are the boundary's. A tenant's exit deletes its `data`, `pseudonym`, `chain` and `credential` wraps at once, and its `evidence` wrap when the evidence retention ends ([0008](/decisions/0008-boundaries-and-tenants/#tenant-lifecycle), [0006](/decisions/0006-keys-and-sealing/#erasing-a-tenant)).

## The audit front

An audit has a front of its own, owned by audit, on a host that is a sibling of sluis's host and never a child of it ([audit 0014](https://github.com/truvity/audit/blob/master/docs/decisions/0014-the-front-the-console-page-and-audit-verify.md)). Every passkey verifier of sluis lists it ([0017](/decisions/0017-identity-and-sign-in/)). Planned: the build is [#985](https://github.com/truvity/sluis/issues/985).

## Built and planned

| Part | State |
|---|---|
| Emitters in Go and Rust, the security class, the action table | Built here. They move to audit in steps. |
| The writer, the first indexer and reader, `sluis audit verify` | Built here, as audit pieces. The registry still lists them as actors of this repository. |
| The usage part of a message, the `Meter` kind, the limit check | Planned ([#841](https://github.com/truvity/sluis/issues/841)). |
| The witness, the audit front and console page, audit's own Cedar policy | Planned, in audit. |

Audit's own pages say what is built there: its [README](https://github.com/truvity/audit/blob/master/README.md) and [reference](https://github.com/truvity/audit/blob/master/docs/reference/README.md). The command is `sluis audit verify` until it is renamed to `audit verify`.
