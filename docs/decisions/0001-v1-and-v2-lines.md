# 0001 — v1 and v2 are two lines that coexist until v2 replaces v1

**Status:** Accepted
**Date:** 2026-11-01

Proposal: [issue #771](https://github.com/truvity/sluis/issues/771).

## Context

v1 lives on `master` and is in production use. v2 is built from scratch on `main` and shares no history with v1. v2 has to reach parity with v1 as it is at cutover.

A rule of "fixes only" for v1 would stop the roadmap already agreed for v1, and everyone who needs those releases would wait for v2. We need clear rules for where a feature goes, where a bug is reported, how long v1 is supported and how the work is tracked.

## Decision

- **v1 keeps its roadmap.** `master` continues the roadmap releases agreed on 2026-10-26, which the [sluis v1](https://github.com/orgs/truvity/projects/10) board tracks, and takes fixes.
- **v2 is built alongside.** It is built on `main` and has to reach parity with v1 as it is at cutover.
- **New feature requests target v2.** v1 builds only its agreed roadmap and fixes. Any other v1 feature needs a decision issue first.
- **Issues say which line they are about.** v1 issues carry the label `line:v1`. An issue without it is about v2. The bug form has a required *Version* dropdown: "v1.x (master)", "v2 (main)" or "Not sure".
- **Two project boards.** [sluis v2](https://github.com/orgs/truvity/projects/9) tracks v2. [sluis v1](https://github.com/orgs/truvity/projects/10) tracks the v1 roadmap and fixes.
- **v1 is supported until the maintainers' own production deployments run v2.** Support means every kind of change v1 gets today: fixes and its roadmap. There is no fixed end date and no extra tail after that.
- **These rules live on `main` only.** GitHub shows the community files of the default branch, which is `master` until cutover. `master` is not changed for this decision.

## Consequences

- v1 users get the agreed roadmap without waiting for v2.
- The maintainer carries two lines for a while. A fix that applies to both is made twice, once per branch.
- v2 aims at a moving target, because parity is measured against v1 at cutover.
- The end of v1 support depends on a fact, not a date. The maintainer announces it when it holds, and a new record amends the support statement.
- `GOVERNANCE.md`, `CONTRIBUTING.md`, `SECURITY.md`, the roadmap page and the issue forms say the same thing as this record. Until cutover, a reader who starts on `master` does not see these rules at once.

## Roads not taken

- **Freeze v1 now.** The agreed v1 roadmap would stop and its users would wait an open-ended time for v2, while v1 is the line in use.
- **Accept only the v1 changes the maintainers' own deployments need.** Every change becomes a case-by-case judgement that outside users cannot predict; a published roadmap with a rule for exceptions is easier to hold to.
- **A fixed end-of-life date for v1.** The date would be a guess that either cuts v1 off before v2 is ready or moves and stops meaning anything.

## Follow-ups

None.
