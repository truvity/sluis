# 0023 — Quota's API is idempotent by a request id and `Idempotency-Key`, and its data follows the origin model

**Status:** Accepted
**Date:** 2026-10-31

Decided by the maintainer. Proposal: [issue #909](https://github.com/truvity/sluis/issues/909). Build: [issue #915](https://github.com/truvity/sluis/issues/915).

## Context

Quota is one of the four audit pieces ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/)): the `quota` engine and its state, kept with compare-and-set. 0004 decided its mode: quota meters by default and enforces per policy rule, for chosen subjects or audiences, and enforcement is switched on in production only after a load test of the mint path. Quota can run beside sluis and share its driver instances, or on its own.

What 0004 did not decide is what an application calls and what quota keeps. Applications that sell or limit their own usage ask for four operations: admit before work, confirm the usage after it, cancel what did not happen, and push top-ups that a billing system has sold. Each of them is retried by networks and clients, so each must count once. The requirements say so (REQ-QUOTA-003, REQ-QUOTA-005), and they ask for a balance query (REQ-QUOTA-007), hourly aggregates whose sums match the facts (REQ-QUOTA-012), an effect on a balance that is separate from the meter and may be negative, zero or positive (REQ-QUOTA-004, REQ-QUOTA-010), and a declared choice of what happens when a balance runs out or quota is unreachable (REQ-QUOTA-006, REQ-QUOTA-011).

`X-Request-ID` is a common header for this, but no RFC defines it. The IETF `Idempotency-Key` header is a working-group draft, which the standards register lists as a candidate (NC-15). Connect calls travel over HTTP, over Unix sockets, as Lambda invocations and in memory, and a header does not survive all of them.

[0016](/decisions/0016-data-has-an-origin-and-a-sensitivity/#quota-by-origin) already placed quota's data: limits, enforcement rules and the exhaustion and unavailability behaviour are declared, and an operator may lower a limit on the console, tighten-only; balances and top-ups are issued, durable, backed up and rolled forward; meters and hourly aggregates are derived from the log.

The threats this record bears on are T-13 and T-25 of the [threat model](/security/threat-model/): a sender that floods its own queue is bounded by quotas and limits, and an operator's change of a limit or a balance is audited under the operator's name.

## Decision

### The API

- **Quota's API is a Connect service with unary calls:** `Admit`, `Confirm` (called emit in earlier notes), `Cancel`, `TopUp`, `Adjust` and `Balance` (the names are illustrative; the contract fixes them). `Balance` has no side effects, so it can be called with GET.
- **The TypeScript deliverable is a Connect client library built on connect-es.** Other languages use their Connect clients.
- **Every call that changes something carries a `request_id` field, and the field is canonical.** It is in the schema, so it survives every transport, including Unix sockets, Lambda invocations and in-memory calls.
- **`entry` also accepts the `Idempotency-Key` header** of the IETF draft and copies it into the field. A request that carries both with different values is refused.
- **A request id is scoped to the calling client and the method, and quota remembers it for 24 hours** as ephemeral state. After a restore, the remembered ids of the last 24 hours are rebuilt from the log, because every record carries its request id.
- **Conflicts follow the draft.** A request id reused with a different payload is refused, and a duplicate that arrives while the first is still running is refused as a conflict (HTTP 409, Connect `aborted`). A refusal for an exhausted balance is `resource_exhausted` (HTTP 429) with `Retry-After`. (The Connect codes are inferred from the research of 2026-10-30.)

### Reservations

- **`Admit` reserves an amount with a time to live,** declared per rule, 5 minutes by default.
- **`Confirm` settles the actual amount,** at most the reserved one unless the rule allows overage.
- **`Cancel` releases a reservation.**
- **A reservation that is neither confirmed nor cancelled is released when it expires,** and counted in the meters as expired. An honest crash costs the customer nothing, and abuse shows in the expired count.

### Top-ups, corrections and refunds

- **`TopUp` takes positive amounts only,** from declared clients whose policy allows `quota.topup` on that balance, identified by their workload identity. A top-up cannot drain a balance.
- **A correction is an `Adjust` call,** with a reason and an amount of either sign. It is allowed to a declared client, or to an operator with step-up ([0048](/decisions/0048-passkeys-step-up-and-device-login/)).
- **A refund is an `Adjust` that names the confirmed event it refunds.** It is bounded by that event's amount, and refunding the same event twice is refused.
- **Top-ups, corrections and refunds are audited,** each with enough data to roll the balance forward (REQ-AUDIT-019, REQ-QUOTA-005).

### Amounts, balances and effects

- **Amounts are signed 64-bit integers in a unit declared per meter and per balance,** for example `requests` or `cents`. There are no decimals anywhere in quota.
- **A balance belongs to a declared account in a boundary and a company.** Declared rules map subjects, clients or a whole company to an account. A balance's key is (boundary, company, account, meter), so record keys carry no personal data.
- **Only quota's own records carry an effect on a balance:** `Admit`, `Confirm`, `Cancel`, `TopUp` and `Adjust` records carry a signed effect on a named balance. Application records carry meters only. An application that wants to change a balance calls quota; it does not just emit (REQ-QUOTA-004, REQ-QUOTA-010).

### Limits

- **There are two kinds of limit:**
  - **a rate,** a token bucket per second or per minute, kept as ephemeral state;
  - **a cap per calendar period in UTC,** a day or a month, from balances or meters.
- **Limits are declared,** and an operator may lower one on the console, tighten-only (REQ-QUOTA-013). The edge may also rate-limit by address; that is the edge's, not quota's.

### The data follows the origin model

| Data | Origin | Writer | Backup and restore |
|---|---|---|---|
| Limits, enforcement rules, accounts, exhaustion and unavailability behaviour | declared | nobody at run time | in version control |
| A limit lowered below the declared one | operator-set, tighten-only | `quota`, on a request routed by `console` | backed up; a restore never loosens it |
| Balances, top-ups and adjustments | issued, durable | `quota` | backed up, then rolled forward from the admit, confirm, cancel, top-up and adjust records |
| Reservations, rate buckets and remembered request ids | issued, ephemeral | `quota` | not backed up; request ids of the last 24 hours rebuilt from the log |
| Meters and hourly aggregates | derived | `quota` | not backed up; rebuilt from the log after a restore |

- **Only `quota` writes balances,** one writer per record kind ([0017](/decisions/0017-state-is-one-port-with-compare-and-set-and-clusters-replicate-with-rqlite/)). Every value is encrypted in the port library ([0058](/decisions/0058-boundaries-are-enforced-in-three-layers-per-port-and-every-state-value-is-encrypted/)).
- **Meters are derived from the log by `quota`,** which follows the log as the indexer does. (inferred from 0016, which names `quota` as the deriving actor)
- **An hourly aggregate lists the ids of its events,** so the sum of the aggregates equals the count of the facts (REQ-QUOTA-012).

### Enforcement

- **Enforcement is strict only in 2.0.** Strict enforcement never admits more than the limit under concurrent requests (REQ-QUOTA-002). It uses compare-and-set on the state port now, and the conditional adjust of ADR 0057 once that is decided.
- **Soft enforcement comes later,** when a load test shows the need. When it comes, it exceeds a limit by no more than one grant per instance (REQ-QUOTA-009).
- **An enforcing rule declares whether an exhausted balance blocks or allows overage, and whether an unreachable quota fails open or closed** (REQ-QUOTA-006, REQ-QUOTA-011). There is no default: `sluis config check` refuses an enforcing rule that does not declare both.
- **The actor that mints asks `quota` before it mints,** where a rule enforces. (inferred: `policy` has no state and no network, so it cannot ask)
- **Enforcement is switched on in production only after a load test of the mint path with enforcement on** ([0004](/decisions/0004-audit-is-four-pieces-joined-by-a-transport-port/), REQ-QUOTA-008).

## Consequences

- A retried call never counts twice, on any transport, within 24 hours. HTTP-native clients can use the standard header, and sluis's own callers set the field. A retry after 24 hours counts again.
- Following a draft means following its changes. The field stays canonical whatever the draft does, so a change to the header touches only `entry`'s mapping.
- Balances are money-like records with one writer, a backup and a roll-forward. A restore loses no top-up or adjustment that reached the log. Integers make the arithmetic exact on every store, but a unit finer than the integer must be chosen up front.
- A client that never confirms gets its work without paying, within the reservation's time to live; the expired count makes that visible.
- Top-ups cannot drain a balance, and corrections and refunds are rare, explained and visible. That is two calls to build and test, and a refund needs its original event within the retention.
- An application changes a balance only through quota, so roll-forward reads one kind of record and balances change only under quota's compare-and-set.
- The account is one more declared concept, and it fits per-customer billing and per-client caps alike.
- Every enforcing rule carries two more declared lines, and every risk choice is reviewed.
- Meters come from the log, so they cost nothing on the hot path but lag it by the seal interval and the deriving pass. Rates add state writes on the hot path.
- Strict enforcement puts a compare-and-set on the mint path. On rqlite that is a consensus round trip per admitted request, which is why the load test gates it.
- The TypeScript library is one more artifact on the release train ([0029](/decisions/0029-one-repository-one-version-one-release-train-and-packaging-follows-the-runtime/)).

## Roads not taken

### `X-Request-ID` as the idempotency key

- Pros: widely used, and an earlier proposal named it.
- Cons: no RFC defines it, and it is commonly used for tracing, not for idempotency.
- Why not: the standard candidate is the IETF `Idempotency-Key` draft.

### Only the `Idempotency-Key` header, or only a request id field

- Pros: the header is one mechanism, the standard one; the field alone needs no mapping in `entry`.
- Cons: a header does not survive a Lambda invocation or an in-memory call, and it is not in the schema. HTTP-native clients and the standards register expect the header.
- Why not: both, with the field canonical.

### Remember request ids for 7 days, or as long as their effect exists

- Pros: 7 days covers long offline retries; remembering as long as the effect never double-counts.
- Cons: seven times the state on the hot path; unbounded state for top-ups.
- Why not: 24 hours, rebuilt from the log after a restore.

### Consume an expired reservation, or no reservations

- Pros: consuming is safe for the seller; no reservations is one state change per admitted call.
- Cons: a crash would charge the customer, and refunds would become routine. Without reservations usage is charged before it happens, and every failure needs a refund.
- Why not: reserve with a time to live and release on expiry.

### Decimal strings

- Pros: natural for prices.
- Cons: decimal arithmetic in every language and every store, and compare-and-set on strings.
- Why not: signed 64-bit integers in a declared unit.

### One `Apply` call with a signed amount, or top-ups from the console only

- Pros: one call; no machine credential that can add credit.
- Cons: a billing system with top-up rights could also drain balances; billing integrations could not automate top-ups.
- Why not: positive top-ups from declared clients, and corrections as a separate, explained call.

### Cancel after Confirm, or no refunds

- Pros: one call for both cases; nothing to build.
- Cons: Cancel would mean two things, and a late cancel would change settled meters. Without refunds, refunds and credits are indistinguishable in the records.
- Why not: a refund is an `Adjust` bounded by the event it names.

### Effects from any record, or computed from a price table

- Pros: applications could change balances by emitting; a price table keeps records as they are.
- Cons: balances would change from the log, behind the seal interval and outside quota's compare-and-set. Top-ups and refunds do not fit a price table.
- Why not: only quota's own records carry effects.

### Balances per subject, or per client

- Pros: simple for per-user limits; simple for API caps.
- Cons: organisation-wide budgets would need a sum over subjects; no per-customer balances.
- Why not: a balance belongs to a declared account.

### Defaults for exhaustion and outage

- Pros: block-and-fail-closed is safe for the seller; allow-and-fail-open puts availability first.
- Cons: failing closed stops minting for every enforcing rule in a quota outage; failing open silently stops enforcing.
- Why not: no default; each enforcing rule declares both.

### Soft enforcement in 2.0, or soft only

- Pros: hot limits could use soft enforcement from the start; soft is cheapest on the mint path.
- Cons: two accuracy guarantees to build and test before parity; soft only has no mode that never exceeds, which money-like balances need.
- Why not: strict only first, soft when a load test shows the need.

### Sliding windows only, or caps only

- Pros: one mechanism; nothing on the hot path for rates.
- Cons: monthly budgets would not match invoices; no per-subject rate limits, which the edge cannot see.
- Why not: rates and calendar caps.

### Meters counted on the hot path

- Pros: meters are current at once.
- Cons: a state write on every metered call, and meters that are not rebuilt from the facts drift from them.
- Why not: meters are derived from the log (0016); only balances that enforce are on the hot path.

### An OpenAPI or REST face for quota

- Pros: callable with any HTTP client.
- Cons: a second surface to keep in step.
- Why not: the public API is Connect only for now; the REST research is parked.
