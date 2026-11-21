# 0040 — Meters and usage evidence moved to truvity/audit: sluis declares its meters through audit's `Meter` kind

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #841](https://github.com/truvity/sluis/issues/841).

## Context

A usage event names a meter. The readers of the usage log need to agree on what a meter is, which outcomes of an action become usage evidence, what happens to a meter over releases, and which time a usage fact counts at. These rules serve every product that emits usage, not only sluis, so they belong to the audit product, [truvity/audit](https://github.com/truvity/audit) ([0028](/decisions/0028-audit-pieces/) says why). This record stays under its number and says where the rules went and what sluis keeps.

## Decision

### What moved to audit

All of the old rules are in audit's [record 0007, meters](https://github.com/truvity/audit/blob/master/docs/decisions/0007-meters.md):

- a meter is a reviewed, signed document with a name, a unit and a kind (`count` or `gauge`), and a gauge declares how its samples combine;
- a meter declares the outcomes it records, and the default is `success`;
- a meter's name, unit and kind never change, and a renamed meter is a new one, with the old one retired;
- a check against the previous release fails when a meter was renamed, retyped or dropped without being retired;
- usage is never taken from telemetry;
- audit closes no period, and a fact counts at its time of use within bounds of its recorded time, otherwise at its recorded time.

The usage class that the meters serve is [audit 0006](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md).

### What atoom owns

The `Meter` kind is declared to atoom's `state` kind registry, which any product extends ([audit 0015](https://github.com/truvity/audit/blob/master/docs/decisions/0015-configuration-kinds.md), [atoom 0008](https://github.com/truvity/atoom/blob/master/docs/decisions/0008-blocks.md)). The registry is planned work in atoom's [plan](https://github.com/truvity/atoom/blob/master/docs/plan.md).

### What sluis keeps

- **sluis declares its meters through audit's `Meter` kind.** The actions of its action table name the meters they may carry ([0030](/decisions/0030-audit-records-and-the-usage-stream/)), and its emitter refuses an undeclared meter.
- **A refusal by policy or by a limit check is never metered use** ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)).
- **sluis builds no billing calendar, grace period or close job.** The product builds its billing log from the usage log.

Planned: the `Meter` kind, the meter fields of the action table and the comparison check. The `Meter` kind is a kind of sluis's store today.

## Consequences

- One home for the meter rules. A change is a record in audit.
- sluis's table and its `Meter` documents grow with retired meters, as every product's do.

## Roads not taken

- **Keep the meter rules in sluis.** Every product that bills by use would read an identity product's records to learn what a meter is.
- **Count usage from telemetry.** Telemetry is sampled and may be dropped, and the usage stream is neither.

## Follow-ups

- Open: which sluis actions carry a meter, with which units, is written with the first caller of the usage stream ([#841](https://github.com/truvity/sluis/issues/841)).
- The action table fields for meters, the `Meter` kind and the comparison check, in the emitter: [#841](https://github.com/truvity/sluis/issues/841).
