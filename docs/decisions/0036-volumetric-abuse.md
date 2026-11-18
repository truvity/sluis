# 0036 — Volumetric abuse is stopped at the edge, and sluis bounds what one connection, client or source can cost with a token bucket kept as a temp kind

**Status:** Accepted; amended by 0044
**Date:** 2026-11-18

Proposal: [issue #916](https://github.com/truvity/sluis/issues/916).

## Context

A customer-facing installation is reachable by anyone, and it spends real resources on each request: a redirect to someone else's identity provider, a signature at key@, a write through store@. The requirements cover the credential endpoints ([REQ-SEC-006](/requirements/sec/#REQ-SEC-006): rate limits per account, factor and source, with a progressive delay; [REQ-SEC-017](/requirements/sec/#REQ-SEC-017): no global lock-out) and the roadmap promises "rate limits per connection". What has to be said is where a limit lives, what bounds the state that unauthenticated requests create, and what a noisy tenant can do to its neighbours. The [threat model](/security/threat-model/#availability-and-abuse) lists these as the threats T-27 to T-32.

The quota actor admits work with a strict count and an audited balance ([0039](/decisions/0039-quota-allowance/#the-allowance)). A sign-in limiter is a different thing: it protects availability, must be fast, and has no money in it.

## Decision

### At the edge

- **Volumetric protection is the edge's.** Address and network rate limits, bot management, request size and time limits, and denial-of-service absorption belong to the gateway, the web application firewall or the content delivery network in front of the installation ([Edge](/runtimes/kubernetes/#edge)). sluis does not try to be a firewall.
- **An internet-facing production installation declares its edge,** and `sluis config check` warns when it declares none. sluis reads the client address only from a trusted proxy.

### Inside sluis

- **Each of these has its own bound, by a token bucket that `entry` keeps:** the sign-in starts per connection; the callbacks per connection; the token endpoint per client; and the sign-in starts per source address and per account on the credential-bearing endpoints ([REQ-SEC-006](/requirements/sec/#REQ-SEC-006)). A bucket is an item of the temp kind `RateWindow`, keyed by (boundary, kind of limit, subject of the limit) and refilled by time; a bucket of no tenant, such as a source address, has the tenant `-`. The refresh calls per session are bounded by `session` on the session chain, and the discovery and key-set refetches per connection by a minimum refetch interval on `UpstreamMetadata` and `VerifierKeyCache`. An admit is one conditional update ([0005](/decisions/0005-the-store/#the-store-api)).
- **A bucket is not backed up.** A temp kind is lost on purpose: an absent bucket reads as a full one, so a restore starts every bucket full ([0010](/decisions/0010-backup-and-recovery/#what-the-archive-holds)).
- **A bucket never names a person in its id.** A source address is personal data, so the id of a source-address bucket is a keyed hash of the address made by key@'s `Mac` over the boundary's `mac` data key ([0004](/decisions/0004-the-data-model/#the-address), [0006](/decisions/0006-keys-and-sealing/#sign-and-mac)).
- **Rates are bounded in config.** The config kind `RateBound` holds the bound of each rate, under a maximum built into the release. A tenant may lower the rates of its own clients, which are fields of `TenantClient` that the store checks against `RateBound` when it writes. Lowering is a tightening and takes effect at once; raising past the bound is refused ([0004](/decisions/0004-the-data-model/#layers-and-the-direction-rule)).
- **An unauthenticated request creates no stored state.** The pending sign-in is a sealed cookie, valid for 10 minutes and at most 4 KiB ([0025](/decisions/0025-integrations/#the-upstream-contract)); the only item a sign-in start writes is its rate bucket. A request past a bound is refused before it writes.
- **One connection cannot starve another.** Each upstream has a concurrency limit, a timeout and a circuit breaker ([0033](/decisions/0033-customer-connections/#the-upstream-hosts-are-known)); a breaker that is open refuses new sign-ins for that connection at once with `UNAVAILABLE`.
- **A refusal is `RATE_LIMITED`** ([0025](/decisions/0025-integrations/#what-an-integration-may-return)): HTTP 429 with `Retry-After`, and the generic page of [REQ-UX-001](/requirements/ux/#REQ-UX-001). The reason is in the log only.
- **No limit locks an account out.** Limits are per source and per account together, with a progressive delay ([REQ-SEC-017](/requirements/sec/#REQ-SEC-017)), so an attacker cannot lock a customer out by guessing.
- **The limiter is sluis's own.** It has a narrow interface (allow a cost against a key) and lives in `RateWindow`. Quota is not on the sign-in path. If the quota actor later offers the same interface at the sign-in path's latency and availability, it may back the interface; it does so only after the measurement of [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/), and until then nothing changes.

### The threat model

The [threat model](/security/threat-model/#availability-and-abuse) lists the threats T-27 to T-32: a sign-in flood, exhaustion of pending state, a noisy or slow tenant provider, sluis used to amplify requests to a provider, a dependency outage, and exhaustion of the key service's request quota. Each has a control from this record or from [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/), a residual and a test.

### Amendment of 2026-11-18: the limits are built in the core

2026-11-18 (maintainer decision H7; [0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#amendment-of-2026-11-18-the-order-after-the-core-the-shared-blocks-hardening-the-console-and-kv)). The limiter of this record, the `RateWindow` bucket that `entry` keeps, is part of the core's security hardening: rate and abuse limits in `entry` on `/token`, `/authorize` and the recovery paths are built in the core phase, and the record is core, so its `Layer` line is gone. The edge stays the first line of defence ([Edge](/runtimes/kubernetes/#edge), decision E1 of 2026-11-18). Where the text says "connection" ([0033](/decisions/0033-customer-connections/), superseded), it reads the installation's upstream sign-in; where it names quota ([0039](/decisions/0039-quota-allowance/), superseded), nothing is built, and the limiter stays sluis's own.

### Amendment of 2026-11-18: the edge first, and the address of a client (RL1 to RL5)

2026-11-18 (maintainer decisions RL1 A, RL2, RL3 A, RL4 and RL5 A; [#1896](https://github.com/truvity/sluis/issues/1896), a follow-up of the limiter built in [#1857](https://github.com/truvity/sluis/issues/1857) and [#1892](https://github.com/truvity/sluis/pull/1892)). The limiter of the amendment above stands as the second layer.

- **RL5 A: the edge first, `entry` as the backstop.** In every shape behind the Cloudflare edge, Cloudflare does the per-address limits and the bot protection, and supplies the address of the client. `entry` keeps all its limits as a second layer, and does the limits per client, which only sluis can do, because it knows the declared clients.
- **RL4: the edge takes the volume and `entry` keeps the fine limits.** `entry` keeps its bounded defaults as the backstop (300 a minute per address on `/token` and `/authorize`, 1200 a minute per client, 30 a minute per address on the recovery routes, and 503 when the limiter cannot check). The defaults are tuned after load numbers. The edge rules are documented, and each installation's own infrastructure code applies them (E2 A of [0044](/decisions/0044-sluis-v2-is-a-core-building-blocks-and-add-ons/#amendment-of-2026-11-18-the-order-after-the-core-the-shared-blocks-hardening-the-console-and-kv)).
- **The target plan is Cloudflare Free** (maintainer, 2026-11-18; as Cloudflare documents the plan, to be re-checked when the rules are written). On Free the edge is one rate limiting rule per zone, counted per address only, with a period of 10 seconds and the action Block: a flood guard whose expression covers `/token`, `/authorize` and the recovery routes, with one threshold per 10 seconds set well above normal use. It stops floods before they reach the installation and is not the fine limit. The rest of the edge on Free is a few custom WAF rules that refuse the methods and paths the issuer does not serve, Bot Fight Mode off on the issuer host (on Free it cannot be skipped per path and would challenge machine clients on `/token`), and an optional Turnstile on recovery, which is an installation choice. A paid plan may tighten the edge with more rules, longer periods and a managed challenge; those are optional. The fine limits stay in `entry`, so RL5 A holds on Free: the edge first for volume, `entry` for precision.
- **RL1 A: the recovery routes are limited per address only.** There is no bucket on the registration client, so no one can lock every person out of recovery.
- **RL2: the address comes from a source that the installation chooses.** Behind Cloudflare it is `CF-Connecting-IP`, trusted only because the entry point of each shape accepts Cloudflare traffic alone (API Gateway with mutual TLS and the default endpoint off, the cluster's tunnel, and `cloudflared` on the boards). Otherwise, for the local installation or a bare forwarder, it is the last `X-Forwarded-For` entry. An installation setting chooses the source, and the other header is ignored.
- **RL3 A: a request with no usable address counts against its client only.**

## Consequences

- A flood is absorbed outside sluis; what gets through is bounded per connection, client and source, so one tenant's burst or provider outage costs that tenant.
- The limiter writes on every admitted sign-in start, and a bucket with a short refill is one conditional write per allowed request. On boards each write is a Raft commit, and a replicated commit takes 20 to 40 ms (measured 2026-11-02, [Board capacity](/runtimes/bare-metal/#board-capacity)). The write rate of the limiter is counted in [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/).
- A hot bucket, such as the bucket of a busy client, is one item that every admit updates. [0037](/decisions/0037-capacity-is-measured-before-it-is-promised/) measures whether a hot item limits the installation.
- Operators must size the edge. A sluis behind no edge is protected against cost, not against saturation of its link.
- Requirements: [REQ-SEC-020](/requirements/sec/#REQ-SEC-020), [REQ-SEC-021](/requirements/sec/#REQ-SEC-021), [REQ-SEC-024](/requirements/sec/#REQ-SEC-024), [REQ-SEC-025](/requirements/sec/#REQ-SEC-025), [REQ-SEC-026](/requirements/sec/#REQ-SEC-026) state these rules, and [REQ-SESS-047](/requirements/sess/#REQ-SESS-047) to [REQ-SESS-050](/requirements/sess/#REQ-SESS-050) the sign-in cookie.

## Roads not taken

- **Everything at the edge.** The edge does not know connections, clients or tenants, so one tenant's loop would look like traffic.
- **Everything in sluis.** It spends sluis's own capacity to refuse what a firewall refuses for free, and it puts sluis on a link it cannot defend.
- **Waiting for the quota actor.** The milestone's own done-when asks for limits per connection, and quota is a later milestone.
- **The quota actor on the sign-in path.** It would make a strict, audited counter an availability dependency of every sign-in.
- **A global lock-out after failures.** It turns guessing into a way to lock customers out.
- **A fixed global rate limit.** One busy product would starve the others.
- **A bucket keyed by the plain source address.** An id never contains personal data, and the temp kind would hold addresses in the clear.
- **A durable bucket.** A bucket may be lost at no cost, so a durable kind would add backup and restore work for nothing.

## Follow-ups

- The limiter, the bounds and the breakers in the generic OIDC upstream, built in the core phase: [#916](https://github.com/truvity/sluis/issues/916).
- Rate limits on the credential endpoints: [#796](https://github.com/truvity/sluis/issues/796).
- The limits of `entry` on `/token`, `/authorize` and the recovery routes, per client and per source address, built in the core ([Limits at the edge](/reference/contracts/#limits-at-the-edge)): [#1857](https://github.com/truvity/sluis/issues/1857).
