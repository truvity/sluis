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

Add `issues:` with the issue numbers when an issue in truvity/sluis delivers the requirement, for example `issues: [915]`. Issue numbers go only there; `features` takes only `parity` or `design`. A requirement may have no issue yet. The field is a link, not a milestone: the project status page ([ADR 0063](/decisions/0063-the-site-is-static-on-cloudflare-workers-with-a-live-status-api/)) groups requirements by the Product milestone of their linked issues, so set the milestone on the issue, not on the record. `just check-requirements` warns, for now, about every planned or partial requirement without `issues`, and prints the count per area.

## Marking a requirement done

`done` means the behaviour exists and its tests pass. Nobody sets it for a plan or an intention, and a new record is never `done`.

- Who: the author of the pull request that adds the passing test sets it, by hand, in that same pull request. The reviewer confirms that the named test proves the clause. There is no automatic promotion.
- How: change `status` to `done` and add `tests:` with the repository paths of the files that hold the passing tests, for example `tests: [core/authn/authorize_test.go]`. A test case names its id (`REQ-AUTHN-002/plain_method`, see the [README](https://github.com/truvity/sluis/blob/main/docs/requirements/README.md)), so the file carries the id of the record. Drop `features` and `decisions` that no longer apply; `issues` stays, because the status page places the record by the milestone of its issues.
- What CI checks (`just check-requirements`): a `done` record has `tests`; every path is relative to the repository root, stays inside it and names an existing file; and the file contains the record's id. A path that does not exist, a file that does not name the id, or a `done` record without `tests` fails the run. The check proves that the evidence is named and present. A reviewer judges whether the test proves the clause, and the test's own CI run proves that it passes.
- Undoing: when the tests are removed or the behaviour is dropped, the pull request that does it moves the record back (`planned` or `partial`) or withdraws it. A `done` record whose test file is deleted fails the check.

The [status page](/status/) counts `done` records in its Done column, per milestone and per area.

## Checks

`just check-requirements` checks the catalogue: the schema, one keyword per statement, unique ids, a verification for every MUST, the tests of every `done` record, known standards and open decisions, and content that must not be public. `just site-lint` runs it too. Run it, then `just check`, before you push.
