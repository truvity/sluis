---
name: new-decision
description: Use when creating, rewriting or changing the status of an architecture decision record (ADR) in docs/decisions/, or turning a decision proposal issue into a record.
---

Read `docs/contributing/decisions.md` and follow it, with `docs/contributing/design.md` for the order of thinking. It points to `docs/decisions/README.md` for the format and to the writing style in `docs/contributing/pages.md` for the prose; read both.

Checklist, each step as the pages describe it:

- [ ] The proposal issue exists and is linked.
- [ ] The record is a copy of `docs/decisions/0000-template.md` with the next free number.
- [ ] Every section is filled, including the roads not taken (one line each, with the reason it lost) and the follow-ups. Git keeps what merged, never what was dropped.
- [ ] The rule is stated once: in this record, or in a reference page with test vectors that the record links. A second record links to its heading and does not restate it.
- [ ] The status is right for what is decided; open parts are listed with their issue.
- [ ] The index in `docs/decisions/README.md` has the row.
- [ ] Life cycle: before the first v2 release a record may be rewritten, merged and renumbered, with no history lines; then `docs/decisions/renumbered.json` is updated in the same pull request, so each old address redirects to the record that took it over. After the first release records are append-only: supersede, do not edit the decision.
- [ ] Every external fact is dated and has a source; an unverified claim is left out or marked "to be confirmed" (`docs/contributing/design.md`, "Measure and date"). An `Accepted` record has no open part.
- [ ] Requirements and target-state pages that follow are added or named.
- [ ] The pull request holds no more records than the page allows.
- [ ] `devbox run -- just check-decisions`, then `devbox run -- just check`, pass; then follow `docs/contributing/workflow.md`.
