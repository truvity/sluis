# 0050 — The token of an infrastructure exchange carries the context realm's pairwise subject, and any subject of sluis's own is derived per boundary

**Status:** Accepted
**Date:** 2026-11-21

Proposal: [issue #961](https://github.com/truvity/sluis/issues/961).

## Context

[0048](/decisions/0048-product-apis-take-the-identity-providers-tokens/) lets `exchange` take a token of the group's identity provider only as the subject of an exchange for a call down to infrastructure. It left open which `sub` the token of that exchange carries: the person's sluis subject, through the link of [0019](/decisions/0019-person-and-upstream-link/), or the pairwise identifier that the context realm gave the person.

The group's identity rules make a person's identifier pairwise for each boundary, so that two boundaries cannot match the same person by it. A sluis-wide subject for the person would be an identifier that crosses boundaries.

## Decision

- **The token of an infrastructure exchange carries, as `sub`, the pairwise identifier of the context realm** that issued the subject token. sluis does not replace it with a subject of its own that is the same in several boundaries.
- **When sluis needs a stable subject of its own for that person, it derives one per boundary,** so it stays pairwise: the same person has unrelated subjects in two boundaries.
- **The link of 0019 does not join the person's identity across boundaries** for an exchanged token. Within one boundary it may still tie the context realm's identifier to records sluis keeps there.

## Consequences

- A token from one boundary's exchange cannot be matched to the same person's token from another boundary by its subject. An infrastructure service that needs to know who a person is across boundaries has to ask the group's identity provider, which owns that mapping.
- Audit records of an exchange name the subject as the token carries it. Audit's pseudonyms ([truvity/audit](https://github.com/truvity/audit)) then apply as for any subject.
- The open point of 0048 on `sub` is closed. The others stay open there.

## Roads not taken

- **The person's sluis subject, the same in every boundary.** It would rebuild the cross-boundary identifier that the group's identity rules exist to prevent.
- **A subject sluis derives once and reuses in every boundary.** The same problem, one step removed.

## Follow-ups

- The exchange actor: [#961](https://github.com/truvity/sluis/issues/961).
- Open: how a subject of sluis's own is derived per boundary. [0006](/decisions/0006-keys-and-sealing/) retired the `pairwise` key, and a person's subject is made of no key, so the derivation needs a decision of its own if one is ever needed.
