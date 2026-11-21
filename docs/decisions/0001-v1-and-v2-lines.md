# 0001 — v1 and v2 are two lines, releases of one major version work with the one before, and v2 replaces v1 by a parallel switch

**Status:** Accepted
**Date:** 2026-11-18

Proposal: [issue #771](https://github.com/truvity/sluis/issues/771).

## Context

v1 lives on `master` and is in production use. v2 is built from scratch on `main` and shares no history with v1. v2 has to cover what v1 does before v1 can be switched off.

Three questions about change have one answer.

- **Which line gets what.** A rule of "fixes only" for v1 would stop the roadmap already agreed for v1, and everyone who needs those releases would wait for v2.
- **Upgrades.** Lambda updates one function at a time, Kubernetes replaces pods one by one, and the boards update one node at a time. A window with two versions in it always exists unless the product stops. v1 stopped it with a maintenance freeze; an installation that serves customers has no window to stop in.
- **Cutover.** v1's own cutover froze the service, copied state and switched one issuer. v2 cannot copy state: it has a new key hierarchy, an opaque `sub` ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)) and an issuer host for each boundary. v1's `sub` is a person's e-mail address, so everything keyed on it changes.

The product principles join products by a contract with vectors, keep v1's keys where they are, build one product for several installations, and ask for a consumer before a feature ([principles](/principles/)).

## Decision

### The two lines

- **v1 keeps its agreed roadmap.** `master` continues the roadmap releases agreed on 2026-10-26 and takes fixes.
- **v2 is built on `main`** and covers what v1 does, as the cutover below says.
- **New feature requests target v2.** v1 builds only its agreed roadmap and fixes. Any other v1 feature needs a decision issue first.
- **Issues say which line they are about.** v1 issues carry the label `line:v1`. An issue without it is about v2. The bug form has a required *Version* dropdown: "v1.x (master)", "v2 (main)" or "Not sure".
- **v1 is supported until it is switched off.** Support means every kind of change v1 gets today: fixes and its roadmap. The end is the switch below, not a fixed date.
- **These rules live on `main`.** `master` is not changed for them.

### N and N-1 inside a major version

- **Within one major version, release N works with release N-1** for every contract and every stored item that sluis ships or pins: the messages between actors, the services of atoom's blocks and ports, the items of every kind, and the signed config documents. A client library built with N-1 keeps working against a server of N.
- **A stored item changes by expand and contract.** Release N reads old and new and writes old; release N+1 writes new; a later release stops reading old. A field is never removed in the release that stops writing it (REQ-SUPPLY-026).
- **Deployments roll.** Readers are updated before writers. No release inside a major needs a stop. Lambda functions, pods and board nodes update one at a time, and the mixed window is a tested state, not an accident. Boundaries that load the same installation-wide document differ by one version of it while a change lands, and every reader tolerates that ([0008](/decisions/0008-boundaries-and-tenants/)).
- **Rollback is the previous release.** N-1 reads what N wrote, so going back needs no migration.
- **Contract vectors in CI enforce it** (REQ-SUPPLY-025, REQ-SUPPLY-027). The vectors of the previous release run against the current actors, and the current vectors run against the previous release's artifacts, for messages, items and documents. Protocol contracts are also checked for breaking changes ([GOVERNANCE.md](https://github.com/truvity/sluis/blob/main/GOVERNANCE.md)).
- **A major version may break N-1.** It ships with an upgrade note and, where stored data changes, a tool that migrates it, and it may need a stop.
- **The rule holds from v2.0.0.** Between alphas anything may change ([0013](/decisions/0013-release-train-packaging-and-distribution/)). The configuration schema keeps its own rule: a published `$id` never changes ([0004](/decisions/0004-the-data-model/)).

### Cutover from v1

- **v2 runs as a new issuer beside v1 and never takes over v1's host.** v1's keys never move, the v2 chain comes from the root custodian, and each boundary has its own issuer host ([0008](/decisions/0008-boundaries-and-tenants/)).
- **No relying party moves until v2 covers every required v1 parity row.** The [roadmap](/roadmap/) says which rows are required; the [v1 parity checklist](/features/) lists them.
- **Then the relying parties move in waves,** one at a time. A relying party is a cluster API server, a cloud identity provider, a GitOps tool, a console or a CI system. Each is rewired and switched alone, and a move is undone by pointing that relying party back.
- **v1 is switched off after the last wave,** at the interval the roadmap states. v1 keeps serving the other relying parties until then, and fixes continue on v1 until then.
- **An inventory comes first.** Before the first relying party moves, a list names every relying party of the v1 issuer and every consumer that keys on v1's `sub`, `aud` or `groups` format (REQ-OPS-030). A consumer keyed on the e-mail `sub` is re-keyed to v2's opaque `sub` before it moves, through directory sync ([#805](https://github.com/truvity/sluis/issues/805)).
- **A subject changes its spelling, and each relying party is re-keyed once.** v2 issues no v1 `sub` ([0020](/decisions/0020-grant-vocabulary-and-token-claims/)), so the inventory lists which of these each consumer reads:

  | Subject | v1 `sub` | v2 `sub` |
  |---|---|---|
  | A person | the e-mail address | the person's own opaque subject, the same for every client and never pairwise; a relying party that needs a readable name reads the `email` claim of the ID token |
  | A CI job | `github:<org>/<repo>` | `spiffe://<trust-domain>/ci/github/<org>/<repo>` |
  | A Kubernetes ServiceAccount | `<cluster>:k8s:<namespace>:<name>`, or `k8s:<namespace>:<name>` with no cluster | `spiffe://<trust-domain>/k8s/<cluster>/ns/<namespace>/sa/<name>`, always with the cluster |
  | An AWS role | `aws:<account>:role/<path><name>` | no proof kind yet; a planned one would spell it `spiffe://<trust-domain>/aws/<account>/role/<path>/<name>` ([0022](/decisions/0022-workload-identity-and-host-enrolment/)) |

  A trust policy that is rendered from the configuration ([REQ-DEPLOY-014](/requirements/deploy/#REQ-DEPLOY-014)) is regenerated at the switch with the new spelling. One that someone wrote by hand, such as an AWS trust policy that pins an old v1 string, goes on the inventory. If the inventory finds a consumer of the e-mail `sub` that cannot be re-keyed, the exception is an extra claim declared for that one client, never another spelling of `sub`.
- **No state is carried.** What happens to each thing follows its storage class ([0004](/decisions/0004-the-data-model/)): a tool converts v1's policy, clients and vocabulary into v2's config documents; what v2 can learn again from its upstreams and its workloads is learned again; durable items such as certificate records are made again for each workload; vendor-issued secrets are connected again as secret-source values or tenant connections; and sessions are not carried, so everyone signs in again.

### v1's history

- **v1's audit archives and billing history stay where they are, read-only,** under their locks. They are not imported: v1's hash chains do not survive an import, and v2's log does not carry v1's format ([audit's log](https://github.com/truvity/audit/blob/master/docs/decisions/0008-the-log.md)).
- **A final v1 verifier is pinned for the retention period.** The last v1 release's verifier and reader are kept as a tagged source and as built binaries with checksums and attestations, so an auditor can verify a v1 archive after v1 is switched off. The retention period is that of the longest v1 compliance profile.
- **v1's billing history is exported once** to a file with its digest recorded, and kept for the billing retention. sluis builds no billing log of its own; a product builds its billing from [audit's usage log](https://github.com/truvity/audit/blob/master/docs/decisions/0006-the-usage-class.md).

### Deliverables

- **Neutral rendered artifacts are the contract.** The registries render IAM policies, the function and schedule map, units, chart values and bootstrap file fragments as plain files that any tool applies (REQ-DEPLOY-014). They are versioned with the release and covered by golden tests ([0002](/decisions/0002-families-of-actors/#registries-generate-the-enforcement)).
- **A thin Pulumi library lives in its own Go module and release** and only consumes those artifacts, for the maintainers' own estate, which is its consumer (REQ-DEPLOY-015). The product does not depend on it, and sluis ships no other infrastructure-as-code library before a consumer asks.

## Consequences

- v1 users get the agreed roadmap without waiting for v2.
- The maintainer carries two lines for a while. A fix that applies to both is made twice, once per branch.
- A release causes no downtime inside a major version, on every runtime, at the price of a rule that CI has to enforce: vectors for each contract, item and document, kept from release to release.
- Changing a stored item takes at least two releases. A change that cannot be split that way waits for a major version.
- Each relying party moves in its own window and can be put back. Two live issuers exist for the length of the cutover.
- The inventory can find that a consumer is keyed on the e-mail `sub`. That consumer is rewired first, and its owner has to act.
- Old v1 code lives on past the switch, and for a while an auditor has two places to look. The cost is a pinned build and a tagged source for years.
- The maintainer's estate keeps using a Pulumi library without the product being tied to one tool; the cost is one more module to release and test.
- `GOVERNANCE.md`, `CONTRIBUTING.md`, `SECURITY.md`, the roadmap page and the issue forms say the same thing as this record. Until the switch, a reader who starts on `master` does not see these rules at once.

## Roads not taken

- **Freeze v1 now.** The agreed v1 roadmap would stop and its users would wait an open-ended time for v2, while v1 is the line in use.
- **Accept only the v1 changes the maintainers' own deployments need.** Every change becomes a case-by-case judgement that outside users cannot predict; a published roadmap with a rule for exceptions is easier to hold to.
- **A fixed end-of-life date for v1.** The date would be a guess that either cuts v1 off before v2 is ready or moves and stops meaning anything.
- **Lockstep upgrades with a maintenance window, as v1.** Sign-in would be down at every release, which is hard on the boards and Lambda and against high availability.
- **A format version on each item with lazy migration only.** It cannot express a change of contract between two actors.
- **A big-bang cutover per installation, as v1 did.** The freeze copied state, which v2 cannot; one failing relying party would stall all; a switch is hard to undo once people have signed in on v2.
- **v2 takes over v1's issuer address.** It needs v1's signing keys, which never leave their store, and it breaks one issuer for each boundary.
- **Carry v1 state across.** The key hierarchy, the subjects and the issuers all differ, and a copy would import what the new design does not trust.
- **Import v1's history into the v2 log.** The hash chains break, and it needs an importer, a provenance record and a new evidence rule.
- **Export v1's archives to cold storage and drop the verifier.** Nobody could verify them later, which is weak for a regulated profile.
- **A Pulumi component library as the deliverable, per runtime.** The product would be tied to one tool.
- **Rendered artifacts only, with no library.** The maintainers' estate would write the glue again for a consumer that exists.

## Follow-ups

- Compatibility per artifact, written before v2.0.0: [#1715](https://github.com/truvity/sluis/issues/1715).
- The cutover, its inventory and its tooling, the pinned v1 verifier and the one-time billing export: [#1017](https://github.com/truvity/sluis/issues/1017).
- The Pulumi library: [#845](https://github.com/truvity/sluis/issues/845); rendering of the bootstrap file and service configuration: [#846](https://github.com/truvity/sluis/issues/846).
