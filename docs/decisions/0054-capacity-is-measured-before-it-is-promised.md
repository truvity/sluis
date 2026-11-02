# 0054 — Capacity is measured before it is promised: a dated spike of the signing path, the state drivers and the audit writer, and tokens stay signed in place meanwhile

**Status:** Proposed
**Date:** 2026-11-02

Proposal: [issue #878](https://github.com/truvity/sluis/issues/878).

## Context

[Principle 9](/principles/#9-measure-before-deciding-and-date-the-measurement) says a design that depends on a number rests on a measurement. For the customer-identity installation on Kubernetes there is none.

- **What exists.** The boards have a recorded budget: about 7 P-384 signatures a second per card and about 21 a second for three boards (measured 2026-10-31, [Board capacity](/runtimes/bare-metal/#board-capacity)). v1's workforce load was about 25,000 tokens in seven days with a peak of 0.8 a second ([0012](/decisions/0012-cryptography-and-authority-keys/)). [#878](https://github.com/truvity/sluis/issues/878) measures the key service from Lambda.
- **What is missing for customers.** No sign-in or refresh rate, no key-service request quota and cost per signature, no access-pattern or hot-key analysis of the state table, no limit of the single audit writer, no index size. [REQ-PERF-001](/requirements/perf/#REQ-PERF-001) asks only that budgets be recorded when a path changes.
- **Why signing is the first worry.** Every token is signed in place by the store ([0012](/decisions/0012-cryptography-and-authority-keys/#keys-sign-in-place)): a sign-in signs an ID token and an access token, every refresh signs again, and every exchange into a customer boundary signs a token for it. With tokens of at most one hour ([0028](/decisions/0028-grants-come-from-a-declared-vocabulary-and-a-token-carries-fixed-claims-for-one-audience/#lifetimes)) a population of customers refreshes all day. The key service has a request quota per account and Region for asymmetric operations that is shared by every key in them; its value in force is UNVERIFIED here and is read in the spike.

The rule of [0012](/decisions/0012-cryptography-and-authority-keys/#keys-sign-in-place) is not in question: there are no derived, burst or short-lived online signing keys, because a key in memory can be copied ([principle 1](/principles/#1-a-key-is-born-in-its-store-and-never-leaves-it)). The question is whether the number of signatures that customers need fits what the store allows, and what to do if it does not.

## Decision

### The baseline

- **Tokens stay signed in place by the key store.** Nothing in this record changes [0012](/decisions/0012-cryptography-and-authority-keys/#keys-sign-in-place) before the measurement says it must.
- **No capacity figure enters a record, a page or a promise until the spike's result is recorded.** A figure from the workforce, or from a card, is not a customer figure.

### The model to fill in

The signing rate is `sign-ins × signatures per sign-in + refreshes × signatures per refresh + exchanges × signatures per exchange`. With `S` concurrent sessions, `C` clients per session and tokens of one hour, refreshes run at about `S × C / 3600` a second. For illustration only, and not a forecast, `S = 100,000` and `C = 2` give about 55 refreshes a second and so more than 100 signatures a second, two orders above the workforce peak. The real inputs (the products, their users, their clients per session) come from the products before the spike runs.

### The spike

It follows the shape of a result in [Decisions, requirements and research](/contributing/decisions/#the-shape-of-a-result). Its question is: what sign-in and refresh rate can the Kubernetes installation sustain in one AWS Region, and which part limits it first?

1. **Token signing.** P-384 `Sign` from pods of the installation in its Region: latency at p50, p95 and p99 and the rate reached with 1, 8, 64 and 256 concurrent callers, on one key and on several keys, cold and warm, with throttling errors counted. The request quota in force, read from the account's quota service on the day, and the price per request, read from the provider's price list on the day.
2. **State on DynamoDB.** The records one sign-in, one code exchange, one refresh and one exchange read and write, with consistent reads and the conditional writes and transactions of the atomic batch counted; the partition keys and whether any one is hot (the limiter's buckets of [0053](/decisions/0053-volumetric-abuse-is-stopped-at-the-edge-and-sluis-bounds-what-one-source-can-cost/#inside-sluis) and a product's `person` records are the likely ones); capacity mode against on-demand throttling; item sizes; expiry.
3. **The audit writer.** The sustained events a second that the one writer of one log seals into batches on the blob store, the backlog on the queue at that rate, the time from emit to sealed, the index's ingest rate, and the cost of unwrapping pseudonym keys per event ([0025](/decisions/0025-audit-pseudonyms-are-keyed-per-company-and-epoch/)).
4. **End to end.** Sign-in and refresh rate against p99 latency with three replicas of the core pod, a pool replica pair ([0048](/decisions/0048-customer-connections-run-in-a-pool-per-customer-boundary/)) and the audit installation, with the first bottleneck named.

The result states when and where it ran (UTC, Region, cluster size), the versions of every component, what the setup over- and under-counts (a fake upstream is faster than a real one; one Region has no cross-zone failure), the numbers in tables with units, and the raw logs' place.

### If the store's quota binds

In this order, each only if the one before it cannot meet the measured need:

1. **Ask for a higher quota.** It is a request to the provider and changes nothing in the design.
2. **Sign fewer tokens.** Do not sign an ID token that the client did not ask for, and let a resource server that can introspect take a reference token ([REQ-TOKEN-014](/requirements/token/#REQ-TOKEN-014)), which signs nothing.
3. **Spread across accounts.** A customer boundary in its own account (L4, [0047](/decisions/0047-a-customer-identity-installation-is-a-front-a-few-customer-boundaries-and-key-less-tenants/#promotion)) has its own quota.
4. **Change [0012](/decisions/0012-cryptography-and-authority-keys/#keys-sign-in-place).** A new record that weighs a short-lived online key against principle 1. It is the last road and needs its own measurement.

## Consequences

- Nobody promises a number to a product before the result exists, and the result is dated, because a quota and a price rot.
- The spike needs a cluster in a development account and a load generator that plays a real upstream's latency; it costs engineer time and a small bill.
- The record stays Proposed until the result is linked. Then it is amended into an Accepted record that states the measured figures and the road taken.
- Requirements: [REQ-PERF-003](/requirements/perf/#REQ-PERF-003), [REQ-PERF-004](/requirements/perf/#REQ-PERF-004) are added.

## Roads not taken

- **Decide a design now from the workforce numbers.** A peak of 0.8 tokens a second says nothing about a population of customers.
- **A short-lived online signing key to avoid the quota.** It breaks principle 1 and 0012 before anyone knows the quota binds.
- **Longer tokens to reduce signatures.** The maximum of an hour is a floor of the token rules, and a stolen bearer token is useful for its whole life.
- **Provision for the largest imaginable load.** It buys capacity for nobody, against principle 8.
- **Measure on a developer's laptop.** The key service, the table and the writer's queue are in the cloud; a local number measures none of them.

## Follow-ups

- The spike, its result and the amendment: [#878](https://github.com/truvity/sluis/issues/878), widened to the whole path.
