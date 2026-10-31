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

## Checks

`just site-lint` checks the catalogue: the schema, one keyword per statement, unique ids, a verification for every MUST, known standards and open decisions, and content that must not be public. Run it, then `just site-check`, before you push.
