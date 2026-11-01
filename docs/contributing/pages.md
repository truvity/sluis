---
title: Pages and writing style
description: How a page of the sluis site marks what is built and decided, where a page lives, how big its diagrams may be, and how it is written.
status: shipped
sidebar:
  order: 4
---

The pages on this site describe the **target state** of sluis v2: what it is meant to be, until it is built. A page is not a promise that the thing exists, so it says how far it has got. The [writing style](#writing-style) below holds for everything in the repository, commit messages included.

## Where a page lives

Docs live next to the thing they describe: the format of the decision records is in `docs/decisions/`, the format of the requirements in `docs/requirements/`, and a page about a part of sluis sits in that part's section of the site. When the thing changes, its docs change in the same pull request.

A fact is stated once, on the page that owns it, and the other pages link there ([Design principles](/principles/)). State the design as plain fact and link the decision record for the reasoning; history stays in git and the records. v1 behaviour is described only on the [v1 parity checklist](/features/).

## Status

Every page has a required `status` in its front matter, shown as a badge next to the title and checked in CI:

| Status | Badge | Meaning |
|---|---|---|
| `target` | Target state | Describes the design; not built yet. |
| `partial` | Partly built | Describes the design; part of it is built and part is not. |
| `shipped` | Shipped | Describes what has been built and released. |

The status also starts the page in `llms.txt`. Change it in the pull request that builds the thing. When a page and a [decision record](/decisions/) disagree, the record is the reasoning and the page is fixed.

## Asides

How settled a point is shows in how it is written:

- Plain prose is **decided**.
- `:::note[Proposed]` is for a point that is really undecided, and it links its open tracking issue.
- A proposed note with no issue is either written as plain text, when the point is decided, or deleted, when nothing needs it.
- A plain `:::note` or `:::caution` is a warning to the reader, not a status.

```md
:::note[Proposed]
Each node agent holds its own signing key ([#NNN](https://github.com/truvity/sluis/issues/NNN)).
:::
```

## Diagrams

Mermaid diagrams (a ```` ```mermaid ```` fence) are drawn at their natural size; a wide one scrolls in its box, and **Expand** opens it fullscreen with pan and zoom. A diagram that needs the zoom to be read is too big; keep it to a size a reader takes in at a glance:

- At most about 6 participants in a sequence diagram.
- Short labels, on nodes and on messages: a few words, not a sentence. Put the detail in the text beside the diagram.
- Split a big flow into several small diagrams, each with a heading and a line of prose.
- Prefer `flowchart LR` for a wide flowchart, with a few nodes per column, over `TB` with a long row.

## Writing style

Write for an engineer who has not followed the discussion.

### Plain and short

- Say the verdict first, then the reasons.
- Use plain words and short sentences. Prefer the active voice: "the writer seals the batch", not "the batch is sealed".
- Use a table to compare options, versions or numbers, not a paragraph.
- One idea per paragraph. Cut a word that adds nothing.

### Dated and checkable

- Date every claim that can rot: a measurement, a version, a price, the state of an upstream project. "Measured 2026-10-31" lets the next reader judge it.
- Name the version of a tool or a standard when the claim depends on it.

### Instructions

- In guidance for people and agents, prefer soft wording ("prefer", "avoid") and say why. Keep "never" and "must" for the rules that have no exception, such as secrets and the public-content rules.
- Write steps as numbered lists in the imperative.

### Files

- Use the `.yaml` extension for YAML files, not `.yml`. The exceptions are the file names a tool requires: `.github/ISSUE_TEMPLATE/config.yml` and `.yarnrc.yml`. `just check-yaml-ext` checks it.
- After an edit, reflow the paragraph you changed so it keeps the wrapping the file already uses.
- Text files use LF line endings; `.gitattributes` enforces it.
