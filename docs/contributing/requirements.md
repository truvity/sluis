---
title: Requirements
description: When to add a sluis v2 requirement, how its id is chosen and kept, and where the catalogue's fields and checks are defined.
status: shipped
sidebar:
  order: 4
---

The requirements catalogue in `docs/requirements/*.yaml` is the source of truth for what sluis v2 must do. The site's [requirements pages](/requirements/) are generated from it. Its fields, its vocabularies and the steps to add a record are in [docs/requirements/README.md](https://github.com/truvity/sluis/blob/main/docs/requirements/README.md); follow that file for every edit.

## When a requirement is needed

A decision that creates an obligation adds a requirement or cites an existing one, and the record names the ids.

## Ids

An id is `REQ-<AREA>-NNN`, for example `REQ-AUTHN-002`. It is never reused and never renumbered, not even for a withdrawn record, so a link or a test name keeps working. A record that no longer applies is withdrawn, not deleted.

## Fields

Every record carries the fields the README defines, among them `source` (where the requirement comes from), `level`, `status` and `runtimes`. Use only the values the README and `index.yaml` declare; a new value is declared there first.

## Linking the issues that deliver it

Add `issues:` with the issue numbers when an issue in truvity/sluis delivers the requirement, for example `issues: [915]`. Issue numbers go only there; `features` takes only `parity` or `design`. A requirement may have no issue yet. The field is a link, not a milestone: the project status page groups requirements by the Product milestone of their linked issues, so set the milestone on the issue, not on the record. `just check-requirements` warns, for now, about every planned or partial requirement without `issues`, and prints the count per area.

## Checks

`just check-requirements` checks the catalogue: the schema, one keyword per statement, unique ids, a verification for every MUST, known standards and open decisions, and content that must not be public. `just site-lint` runs it too. Run it, then `just check`, before you push.
