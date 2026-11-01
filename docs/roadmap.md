---
title: Roadmap
description: Where sluis v2 is going. The four product milestones in priority order, what each must do to be done, and the order the installations are built in.
status: shipped
---

The roadmap lives in the `Product milestone` field of the public [sluis v2 board](https://github.com/orgs/truvity/projects/9), and the [project status](/status/) page shows how far each milestone has got. This page says what each milestone is for and when it is done; the reasoning for the order is in [ADR 0018](/decisions/0018-runtimes-installations-and-delivery-order/#delivery-order).

v1 keeps its own roadmap on the [sluis v1 board](https://github.com/orgs/truvity/projects/10), on `master`. New feature requests target v2 ([ADR 0001](/decisions/0001-v1-and-v2-lines/)).

## Where v2 stands

As of 2026-11-01, v2 is in the design phase and has no code. What exists are the [decision records](/decisions/), the [requirements](/requirements/) and the design pages. Root custody lives in [ritueel](https://ritueel.id), a separate project also in design; the first version of sluis needs ritueel's software-root mode ([#1071](https://github.com/truvity/sluis/issues/1071)).

## Product milestones

v2 is delivered in four product milestones, in this priority order.

1. **First working version.** The local installation, plus the foundations every installation stands on. The local installation is Docker Compose on a workstation: one container per actor, built from the published images, with the minimal drivers (embedded SQLite state, a SQLite index, a files blob, a local lock, a spool or direct transport), software keys by default, sign-in through the generic OpenID Connect upstream tested against a real identity provider ([#1106](https://github.com/truvity/sluis/issues/1106)), and the audit log with Search ([The local installation](/decisions/0018-runtimes-installations-and-delivery-order/#the-local-installation)). The foundations are the build and release train, the contracts and the permission manifest, the driver pattern, configuration and key custody. The milestone also holds the distribution site, the published images with checksums, SBOMs and keyless provenance ([ADR 0019](/decisions/0019-release-train-packaging-and-distribution/#signing-and-checksums), [#873](https://github.com/truvity/sluis/issues/873)), workload identity ([#870](https://github.com/truvity/sluis/issues/870)), federation ([#963](https://github.com/truvity/sluis/issues/963)), the verifier drivers ([#965](https://github.com/truvity/sluis/issues/965)) and the conformance pipeline ([#931](https://github.com/truvity/sluis/issues/931)).
   - **Done when** the example install file for local starts under Docker Compose from the published images, and the integration tests run against it in CI.
2. **v1 parity.** The boards, lambda and kubernetes installations, and everything v1 does. Parity means that the workloads that use v1 today can be wired to the new sluis, possibly in a different way or with a different configuration, not that behaviour is identical.
   - **Done when** every row of the [v1 parity checklist](/features/) is `Done` or `Changed` (with its decision), and the boards, lambda and kubernetes installations are built and tested ([installations](/runtimes/installations/)).
3. **CIAM upstream.** A customer-identity product signs in a product's customers through sluis. sluis stays a broker and hosts no customer accounts: the CIAM product is one more OpenID Connect upstream ([OIDC upstream](/integrations/oidc-upstream/)).
   - **Done when** the generic OIDC upstream ([#916](https://github.com/truvity/sluis/issues/916)) does claim mapping per upstream, `acr` and `amr` passthrough, step-up (RFC 9470), end-user subjects, back-channel logout and rate limits per connection.
4. **Quota.** `quota` as a front for installations: applications ask it to admit work before it runs, confirm or cancel the usage after, and push top-ups. It runs beside an installation or on its own ([ADR 0015](/decisions/0015-audit-pieces-transport-search-and-front/), [ADR 0017](/decisions/0017-audit-records-the-usage-stream-and-quota/#quota)).
   - **Done when** quota meters by default and enforces per policy rule for chosen subjects or audiences, a request counts once however often it is retried, and under strict enforcement the admitted total never exceeds the limit under concurrent requests (REQ-QUOTA-001 and REQ-QUOTA-002; work item [#915](https://github.com/truvity/sluis/issues/915)).

## Delivery order

The tested [installations](/runtimes/installations/) are built in this order:

1. **local first,** with the foundations (milestone 1).
2. **Then boards, lambda and kubernetes, built and tested in parallel.** They start when the first working version is done, and none waits for another.
   - **boards.** Three single-board computers behind a Cloudflare tunnel, a YubiKey in each node, state and index on rqlite, and audit logs on R2 with the other nodes as peer witnesses ([Bare metal](/runtimes/bare-metal/)).
   - **lambda.** AWS Lambda.
   - **kubernetes.** The one tested Kubernetes installation ([Kubernetes](/decisions/0018-runtimes-installations-and-delivery-order/#kubernetes)).

EC2 is a possible bare-metal installation, not a delivery target, and not tested. Other installations are possible and untested. The order is reviewed when each installation ships, and ADR 0018 records any change.

## Later

Work planned after the parity milestone is marked Later on the board. It includes CIBA, in poll mode with a passkey on the approving device, once a real consumer exists ([#980](https://github.com/truvity/sluis/issues/980)); the FAPI 2.0 security profile ([#912](https://github.com/truvity/sluis/issues/912)); ACME for host certificates ([#978](https://github.com/truvity/sluis/issues/978)); device login ([#855](https://github.com/truvity/sluis/issues/855)); a FIPS build variant when a customer needs one ([#868](https://github.com/truvity/sluis/issues/868)); and other clouds and stores. Garage as the boards' blob store is backlog, because it ignores conditional writes ([#982](https://github.com/truvity/sluis/issues/982)).

## Where the work is tracked

The v1 features that v2 must match are issues labelled [`parity`](https://github.com/truvity/sluis/issues?q=is%3Aissue+label%3Aparity), listed on the [v1 parity checklist](/features/). Spike results are in the decision records they settled, such as [ADR 0014](/decisions/0014-the-language-follows-the-executable/#the-spikes-are-gates), and on the issue of each spike.
