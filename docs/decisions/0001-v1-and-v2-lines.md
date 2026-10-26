# 0001 — v1 and v2 are two lines that coexist until v2 replaces v1

**Status:** Accepted
**Date:** 2026-10-26

Decided by the maintainer. Proposal: [issue #771](https://github.com/truvity/sluis/issues/771).

## Context

v1 lives on `master` and is in production use. v2 is built from scratch on `main` and shares no history with v1. v2 has to reach parity with v1 as it is at cutover.

Until now the rule was that v1 gets fixes only. That would stop the roadmap that was already agreed for v1, and everyone who needs those releases would have to wait for v2.
We need clear rules for where a feature goes, where a bug is reported, how long v1 is supported and how the work is tracked.

## Decision

- **v1 keeps its roadmap.** `master` is no longer "fixes only". It continues its agreed roadmap releases, v1.80 to v1.85, and takes fixes:
  - v1.80: FAPI 2.0 and identity seams.
  - v1.81: component contracts, plugin protocol and conformance.
  - v1.82: passkeys and generic OIDC upstream.
  - v1.83: environment agents and canary deploys.
  - v1.84: Kubernetes-only deployment shape.
  - v1.85: rotation engine phase 1 and compatibility cleanup.
- **v2 is built alongside.** It is built on `main` and has to reach parity with v1 as it is at cutover.
- **New feature requests target v2.** v1 builds only its agreed roadmap and fixes. Any other v1 feature needs a decision issue first.
- **Issues say which line they are about.** v1 issues carry the label `line:v1`. An issue without it is about v2. The bug form has a required *Version* dropdown: "v1.x (master)", "v2 (main)" or "Not sure".
- **Two project boards.** [sluis v2](https://github.com/orgs/truvity/projects/9) tracks v2. [sluis v1](https://github.com/orgs/truvity/projects/10) tracks the v1 roadmap and fixes.
- **v1 is supported until the maintainers' own production deployments run v2.** Support means every kind of change v1 gets today: fixes and its roadmap. There is no fixed end date and no extra tail after that.
- **These rules live on `main` only.** GitHub shows the community files of the default branch, which is `master` until cutover. `master` is not changed for this decision.

## Consequences

- v1 users get the agreed roadmap without waiting for v2.
- The maintainer carries two lines for a while. A fix that applies to both is made twice, once per branch.
- v2 aims at a moving target, because parity is measured against v1 at cutover, not against v1 today.
- The end of v1 support depends on a fact, not a date. The maintainer announces it when it holds, and a new record amends the support statement.
- `GOVERNANCE.md`, `CONTRIBUTING.md`, `SECURITY.md`, the roadmap page and the issue forms say the same thing as this record.
- Until cutover, GitHub shows the community files of `master`. A reader who starts there will not see these rules at once.

## Roads not taken

### Freeze v1 now

- Pros: all effort goes to v2. One line to carry.
- Cons: the agreed v1 roadmap stops. Anyone who needs those releases would wait for v2.
- Why not: the wait is open-ended, and v1 is the line that is in use.

### Accept only v1 changes that the maintainers' own deployments need

- Pros: a short list of v1 work, tied to real use.
- Cons: every change becomes a case-by-case judgement. Outside users cannot tell what v1 will accept.
- Why not: a published roadmap and a clear rule for exceptions are easier to explain and to hold to.

### Fix an end-of-life date for v1

- Pros: a clear date to plan around.
- Cons: the date would be a guess. It either cuts v1 off before v2 is ready, or it is moved and stops meaning anything.
- Why not: support that ends when the maintainers' own deployments run v2 is tied to what has happened, not to a guess.
