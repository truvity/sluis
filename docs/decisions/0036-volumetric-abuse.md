# 0036 — Volumetric abuse is stopped at the edge first, and sluis bounds what one client or source can cost with a token bucket kept as a temp kind

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #916](https://github.com/truvity/sluis/issues/916).

## Context

An installation that faces the internet is reachable by anyone, and it spends real resources on each request: a redirect to someone else's identity provider, a signature at key@, a write through store@. The requirements cover the credential endpoints (REQ-SEC-006: rate limits per account, factor and source, with a progressive delay; REQ-SEC-017: no global lock-out). What has to be said is where a limit lives, what bounds the state that unauthenticated requests create, and what a noisy client can do to its neighbours. The [threat model](/security/threat-model/) lists these as the threats T-27 to T-32.

A sign-in limiter protects availability, must be fast, and has no money in it. It is a different thing from the limit check that admits work for a customer and is audited ([0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/)).

## Decision

### At the edge

- **Volumetric protection is the edge's, and the edge comes first.** Address and network rate limits, bot protection, request size and time limits, and denial-of-service absorption belong to the gateway, the web application firewall or the content delivery network in front of the installation ([the edge on Kubernetes](/runtimes/kubernetes/)). sluis does not try to be a firewall.
- **The edge takes the volume and `entry` keeps the fine limits.** The edge applies per-address limits to `/token`, `/authorize` and the recovery routes as a flood guard, with one threshold well above normal use. It does not know clients or tenants, so the limits per client are `entry`'s. `entry` keeps all its own limits as a second layer and as the backstop. The edge rules are documented, and each installation's own infrastructure code applies them: sluis holds no credential for the edge, and no core actor calls it.
- **A production installation on the internet declares its edge,** and `sluis config check` warns when it declares none (planned).
- **The address of a client comes from a source that the installation chooses.** Behind an edge that supplies the client address in a header, that header is the source, trusted only because the entry point of each shape accepts the edge's traffic alone. Otherwise, for the local installation or a bare forwarder, it is the last `X-Forwarded-For` entry, which the forwarder itself added; what a client wrote before it is never read. An installation setting chooses the source, and the other header is ignored. The last `X-Forwarded-For` entry is built; the choice of source is planned.
- **A request with no usable address counts against its client only.** A shared bucket for such requests would let anyone lock everyone out.

### Inside sluis

- **Each of these has its own bound, by a token bucket that `entry` keeps:** `/token` per client and per source address; `/authorize` per client and per source address; and the recovery routes per source address only. There is no bucket per account on the recovery routes, so no one can lock every person out of recovery. A bucket is an item of the temp kind `RateWindow`, keyed by (boundary, kind of limit, subject of the limit) and refilled by time. A bucket of no tenant, such as a source address, has the tenant `-`. An admit is one conditional update ([0005](/decisions/0005-the-store/)). The buckets are built.
- **The address is counted before the client,** so one address cannot empty the bucket of a client for the others. A client that the installation does not declare shares one bucket, so a request cannot make a bucket by naming a client.
- **The bounds have defaults, and config lowers them.** The defaults are 300 a minute per address on `/token` and `/authorize`, 1200 a minute per client, and 30 a minute per address on the recovery routes. They are bounded defaults, tuned after load numbers. The config kind `RateBound` holds the bound of each rate, under a maximum built into the release. A tenant may lower the rates of its own clients, which are fields of `TenantClient` that the store checks against `RateBound` when it writes. Lowering is a tightening and takes effect at once; raising past the bound is refused ([0004](/decisions/0004-the-data-model/)).
- **A bucket is not backed up.** A temp kind is lost on purpose: an absent bucket reads as a full one, so a restore starts every bucket full ([0010](/decisions/0010-backup-and-recovery/)).
- **A bucket never names a person in its id.** A source address is personal data, so the id of a source-address bucket is a keyed hash of the address made by key@'s `Mac` over the boundary's `mac` data key ([0006](/decisions/0006-keys-and-sealing/)). An IPv6 address is taken as its /64.
- **The limiter fails closed.** When the store cannot answer, the request is refused with 503 and never let through unbounded.
- **An unauthenticated request creates no stored state.** The pending sign-in is a sealed cookie, valid for 10 minutes and at most 4 KiB ([0025](/decisions/0025-integrations/)). The only item a sign-in start writes is its rate bucket, and a request past a bound is refused before it writes.
- **Rates per account on the credential endpoints are planned.** They come with a progressive delay and are counted per source and per account together, so an attacker cannot lock a customer out by guessing. No limit locks an account out.
- **One upstream cannot starve another.** Each upstream has a concurrency limit, a timeout and a circuit breaker. A breaker that is open refuses new sign-ins for that upstream at once with `UNAVAILABLE`. This is planned.
- **A refusal is `RATE_LIMITED`** ([0025](/decisions/0025-integrations/)): HTTP 429 with `Retry-After`, and a generic page. The word of the limit that refused goes to the audit record only, never to the caller.
- **The limiter is sluis's own.** It has a narrow interface (allow a cost against a key) and lives in `RateWindow`. The limit check of [0046](/decisions/0046-limits-on-the-request-path-and-usage-in-audit/) is not on the sign-in path.

### The threat model

The [threat model](/security/threat-model/) lists the threats T-27 to T-32: a sign-in flood, exhaustion of pending state, a noisy or slow upstream, sluis used to amplify requests to an upstream, a dependency outage, and exhaustion of the key service's request quota. Each has a control from this record or from [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/), a residual and a test.

## Consequences

- A flood is absorbed outside sluis. What gets through is bounded per client and source, so one client's burst costs that client.
- The limiter writes on every admitted request, and a bucket with a short refill is one conditional write per allowed request. On boards each write is a Raft commit, and a replicated commit takes 20 to 40 ms (measured on 2026-11-02, see [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/)). The write rate of the limiter is counted there.
- A hot bucket, such as the bucket of a busy client, is one item that every admit updates. [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/) measures whether a hot item limits the installation.
- Operators must size the edge. A sluis behind no edge is protected against cost, not against saturation of its link.
- Requirements: REQ-SEC-020, REQ-SEC-021, REQ-SEC-024, REQ-SEC-025 and REQ-SEC-026 state these rules, and REQ-SESS-047 to REQ-SESS-050 the sign-in cookie. The limits are part of the core's security hardening; see the [roadmap](/roadmap/).

## Roads not taken

- **Everything at the edge.** The edge does not know clients or tenants, so one client's loop would look like traffic.
- **Everything in sluis.** It spends sluis's own capacity to refuse what a firewall refuses for free, and it puts sluis on a link it cannot defend.
- **The limit check of 0046 on the sign-in path.** It would make a strict, audited counter an availability dependency of every sign-in.
- **A global lock-out after failures.** It turns guessing into a way to lock customers out.
- **A bucket on the recovery routes per client.** One attacker could empty it and lock every person out of recovery.
- **A fixed global rate limit.** One busy product would starve the others.
- **A bucket keyed by the plain source address.** An id never contains personal data, and the temp kind would hold addresses in the clear.
- **A durable bucket.** A bucket may be lost at no cost, so a durable kind would add backup and restore work for nothing.

## Follow-ups

- The limiter, the bounds and the breakers in the generic OpenID upstream: [#916](https://github.com/truvity/sluis/issues/916).
- Rate limits on the credential endpoints: [#796](https://github.com/truvity/sluis/issues/796).
- The limits of `entry` on `/token`, `/authorize` and the recovery routes, per client and per source address, built: [#1857](https://github.com/truvity/sluis/issues/1857).
- The edge first and the choice of the address source: [#1896](https://github.com/truvity/sluis/issues/1896).
