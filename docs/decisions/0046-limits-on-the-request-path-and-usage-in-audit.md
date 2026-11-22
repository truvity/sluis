# 0046 — Limits are checked on the request path, balances live in the product, and usage records go to audit

**Status:** Accepted; partly superseded by 0053
**Date:** 2026-11-21

Proposal: [issue #841](https://github.com/truvity/sluis/issues/841).

## Context

A product that sells what sluis does for it, such as sign-ins, mints or signatures, needs two things from sluis: evidence of what was used, and a check that stops work at a limit. It does not need sluis to know a price.

The evidence is a usage record. Billing needs exactly-once counting: a lost record under-bills, and a duplicate over-bills. That class of record, its guarantee and its idempotency key belong to audit ([audit 0006](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md)). This record says how sluis calls it.

A limit needs a fast check on the request path, not only a log read afterwards. And money, plans and balances are the product's: a price change must touch no sluis configuration and need no deploy.

## Decision

### Limits and balances

- **sluis checks a limit on the request path.** A limit check compares a counter of usage for a customer, an organisation and a product in the current period with a limit. It answers allow or refuse, next to the policy decision ([0007](/decisions/0007-policy-and-sealed-grants/)). At the limit sluis refuses the request before any work is done.
- **What is limited is use,** counted per customer, organisation and product: the three claims of the sluis token ([0020](/decisions/0020-grant-vocabulary-and-token-claims/), [0032](/decisions/0032-customer-identity-installations/)). A counter holds identifiers and no personal data.
- **The product writes the limit,** and sluis holds the counter. A limit is set by the product for its customer, so a new plan needs no sluis deploy.
- **sluis holds no plan, price, money or balance.** The product keeps its plans and balances and builds its billing log from the usage log, outside sluis and outside audit.
- **A refusal by a limit is not metered use** ([0040](/decisions/0040-meters-and-usage-evidence/)).

Planned: the limit check, which has no actor yet. The roadmap places it ([roadmap](/roadmap/)).

### Usage goes to audit

- **A sluis operation that produces usage emits one message through audit's emitter,** with the security record and the usage part built from one call ([0030](/decisions/0030-audit-records-and-the-usage-stream/)). The action table names the meters the action may carry.
- **It uses the required call.** The operation waits until the message is durable in the transport and fails when it is not: a full spool or a writer that refuses is an error of the operation, and the operation did not happen. This is the emitter's `EmitRequired`, which is built; the usage part of the message is planned.
- **Every usage event carries an idempotency key** of its source and its id, so a retry after an uncertain answer neither loses nor doubles a record. The guarantee differs by class and the pipeline does not split: a security fact keeps its counted loss ([0029](/decisions/0029-the-audit-log/)).
- **The usage events carry no personal data.** The counters of the limit check are made from them.

### The counters: the starting point

The counter of a limit is a hot item that every check changes. The shape of its write rests on a dated measurement of one admit taking one unit from one hot item, made on 2026-11-02 with many concurrent writers, on one workstation with a real `fsync`; after each of more than 100 runs the store was read back, and no limit was exceeded and no unit was lost:

| Driver and setup | Conditional update, per second | Read, then compare-and-set, per second |
|---|---|---|
| SQLite file, one committer that batches | 3,800 to 15,400 | about 220 to 230 |
| rqlite 10.6.6, one node | about 11,800 at 32 writers, 28,000 at 128 | 61 to 165 |
| rqlite, three nodes on one host | 780 at 32, 4,000 at 128 | 27 to 88 |
| DynamoDB on demand, one region | about 1,000 to 1,080 | 101 to 281 |

- **The starting point is one conditional update of one item per check,** with the delta applied by the driver. A transaction that also writes an idempotency record measured 91 to 149 a second on DynamoDB.
- **The design peak is about 100 checks a second on one counter,** with a p99 of 50 ms or less within one region. These are targets that a test per driver checks, and not promises.
- **A throttled or unreachable check is "unavailable", never "exhausted".** The check does not guess.
- **On the three-board installation a counter update costs Raft commits,** and the installation's total measured near 275 commits a second on 2026-11-03 ([0037](/decisions/0037-capacity-is-measured-before-it-is-promised/)), so the boards do not promise the peak.

## Consequences

- A full spool stops the operations that produce usage. This is the price of exactly-once counting, and the operator sees an error and not a silent gap.
- A price change, a new plan and a new customer need no sluis deploy. The product writes the limit.
- The limit check adds one counter update to the request path of every limited operation.
- sluis has no billing calendar, grace period or close job, and no balance to restore.
- Requirement REQ-QUOTA-097 states the check; it is planned.

## Roads not taken

- **Keep the balance in sluis.** The product would push an allowance, and sluis would count it down and refuse at zero. Money-adjacent state would sit in an identity product, and billing correctness would still depend on the product's own log.
- **No limit check, usage only.** A limit would be enforced after the fact by the product, and a burst would pass.
- **A separate usage pipeline.** A second path could emit twice or without a security record.
- **A transaction for every update of a hot counter.** It measured 91 to 149 a second on DynamoDB against about 1,000 for a conditional update.
- **Read, then compare-and-set on a version.** It measured 27 to 88 a second on three-node rqlite, below the peak, against 780 or more for a conditional update.

## Follow-ups

- Open: the shape of the counters, their period, and the configuration of a limit are written with the first caller of the check ([#841](https://github.com/truvity/sluis/issues/841)). The measurement above was made for an allowance, so the counters are measured again.
- Open: which actor serves the limit check is decided with its first caller ([#841](https://github.com/truvity/sluis/issues/841)).
- Open: what the request path does when the check cannot answer. A calling rule that declares fail open or fail closed, with no default, would let review choose each risk; whether the limit check does the same is open ([#841](https://github.com/truvity/sluis/issues/841)).
- The emitter's separation of the two classes and the usage stream: [#841](https://github.com/truvity/sluis/issues/841).
