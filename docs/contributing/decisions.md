---
title: Decision records
description: How a sluis v2 decision goes from a proposal issue to an accepted record, who decides, and what every record must keep.
status: shipped
sidebar:
  order: 3
---

A decision record (ADR) says why sluis is shaped the way it is, what else was weighed, and what follows. The format, the numbering and the status values are in [docs/decisions/README.md](https://github.com/truvity/sluis/blob/main/docs/decisions/README.md); the process is in [GOVERNANCE.md](https://github.com/truvity/sluis/blob/main/GOVERNANCE.md). This page is the order of work.

## The maintainer decides

A contributor or an agent presents the options, each with its pros and cons, and a recommendation. It does not pick. The maintainer decides: merging a record with status `Accepted` is the decision.

## From proposal to record

1. **Proposal.** Open an issue with the *Decision proposal* form: the question, the context, the options with pros and cons, and the options you would not take. The form labels it `type:decision`; give it a `Product milestone` on the project board, which places the planned record on the project status page ([ADR 0020](/decisions/0020-the-site-is-static-on-cloudflare-workers/)).
2. **Record.** When the direction is clear, copy [the template](https://github.com/truvity/sluis/blob/main/docs/decisions/0000-template.md) to `docs/decisions/NNNN-short-title-in-kebab-case.md`, with the next free number. Link the proposal issue in the context.
3. **Index.** Add the record to the index table in `docs/decisions/README.md`, with its status. Every record has a row, and every row has a record.
4. **Requirements.** A decision that creates an obligation adds a requirement or cites an existing one ([Requirements](/contributing/requirements/)).
5. **Pages.** The target-state pages that the decision changes follow in the same pull request, or in the next one ([Target-state pages](/contributing/pages/)).
6. **Pull request.** At most five records per pull request. A larger batch is split. The one exception was the 2026-11-01 merge and renumbering of the whole set.

## Accepted means fully decided

A record is `Accepted` only when every part of it is decided. While a part is still open, the record stays `Proposed`, the open part is tracked in an issue, and the record lists it under Follow-ups with a link to that issue. An accepted record never says "not decided here" or names a future record number: a planned decision has an issue, and the record links the issue.

## Roads not taken

Every record keeps the options it did not take, one line each: the option in bold, then why it lost. Git keeps the text that was merged, not the reasoning that was dropped, so this part is required. Keep the roads that bound a live rule; a road that bounds nothing can go.

## Rewriting a record

Before the first v2 release the maintainer may have records rewritten, merged and renumbered in place; after it, a changed mind gets a new record. A rewritten record carries no history lines; git keeps the history. The rules are in the "Design phase" section of `docs/decisions/README.md`. On 2026-11-01 the records were merged into twenty and renumbered; the old numbers and where they went are in its last table.

v1 records stay on `master`. Refer to one as "v1 ADR NNNN" with a link to `master`; never copy or renumber it.

## What CI checks

CI checks the file name, the required sections, the status value, the one-line roads, the proposal line and the index rows, and that every "ADR NNNN" and every link to a record across the docs and the site names a record and a heading that exist; run `just check-decisions`, then `just check`, before you push. Passing it is necessary, not sufficient: the maintainer reviews the reasoning.
