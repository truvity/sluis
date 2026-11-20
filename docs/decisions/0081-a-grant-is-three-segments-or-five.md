# 0081 — A grant is three segments or five

**Status:** Proposed (2026-11-20). Extends [0010](0010-a-declared-vocabulary.md) (a declared vocabulary).
**Date:** 2026-11-20

## Context

Every grant was `<scope>:<thing>:<role>`, three segments, and `policy.SplitGroup` refused anything else. A project's
thing was one word, so two companies that each ran a `landing` project could not both hold it. The estate decided on
2026-11-20 to name a project by its vendor, its own name and its customer:
`<env>:<vendor>:<project>:<customer>:<kind>`, as in `prod:finttech:landing:ie:deployer` and
`all:shared:arch:internal:viewer`. Cluster groups (`<cluster>:k8s:<kind>`) stay three segments.

## Decision

- **Reading.** `SplitGroup` keeps its signature and accepts three segments or five, none empty. The scope is the first
  segment, the role the last, and the thing is everything between them, joined by `:`. Four segments, six or more, and
  any empty segment are not a grant.
- **The thing.** It is one word or exactly three. `compile.GroupName` joins whatever it is given; `ValidThing` and
  `ValidateProjects` are where a name is held to the rule.
- **Scoping.** `groupsScoping` matches a `<scope>:<thing>` pair, so the pair of `prod:truvity:dms:generic:deployer` is
  `prod` and `truvity:dms:generic`. A client lists `truvity:dms:generic` under `groups:`, and with a declared vocabulary
  the three words must be a declared thing.
- **The dual period.** A project row may carry `alias`, its old one-word thing. The roster emits every group of the
  project under both things with the same members, and the vocabulary declares both, so a client's `requires` and
  `groups:` accept either. Without an alias nothing changes. Dropping the alias drops the old groups, so no window is
  needed.
- **Company ceilings.** A pattern is three segments or five. A `*` thing in a three-segment pattern stands for any thing,
  one word or three, so a ceiling written before the move still covers the new names.

## Alternatives considered

- **Accept any count from three up.** Rejected: a mistyped four-segment name would read as a thing with one colon and
  load as a grant nobody holds. Refusing four keeps the typo visible.
- **A new reader for the long form.** Rejected: `SplitGroup` is the one place the estate takes the shape apart, and a
  second reader would agree with it until the day it did not.
- **A flag day with no alias.** Rejected: every consumer of a project's groups would have to change in one release.

## Consequences

- A relying party that reads `thing` now sees `<vendor>:<project>:<customer>` for the long form, and one that compares it
  with a single word sees no match, which is the safe direction.
- The audit catalogue names no group shape, so it is unchanged. The audit module keeps its own copy of the reader and
  moves with this one.
