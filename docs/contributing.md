---
title: Contributing
description: How to take part in sluis. The rules live in the repository; this page points to them and will grow into a short guide.
status: shipped
---

The rules for taking part are kept in the repository, so they apply wherever the code is read:

- [CONTRIBUTING.md](https://github.com/truvity/sluis/blob/main/CONTRIBUTING.md): how to propose and make a change.
- [GOVERNANCE.md](https://github.com/truvity/sluis/blob/main/GOVERNANCE.md): who decides what.
- [SECURITY.md](https://github.com/truvity/sluis/blob/main/SECURITY.md): how to report a vulnerability privately.
- [Decisions](/decisions/): the architecture decision records.

Questions and ideas are welcome as [issues](https://github.com/truvity/sluis/issues).

## Writing target-state pages

The pages on this site describe the **target state** of sluis v2: what it is meant to be, until it is built. A page is not a promise that the thing exists, so it says how far it has got.

### Status

Every page has a required `status` in its front matter, shown as a badge next to the title and checked in CI:

| Status | Badge | Meaning |
|---|---|---|
| `target` | Target state | Describes the design; not built yet. |
| `partial` | Partly built | Describes the design; part of it is built and part is not. |
| `shipped` | Shipped | Describes what has been built and released. |

The status also starts the page in `llms.txt`. Change it in the same pull request that builds the thing, and a page marked `shipped` describes what shipped. When a page and a [decision record](/decisions/) disagree, the record is the reasoning and the page is fixed.

### Asides

How settled a point is shows in how it is written:

- Plain prose is **agreed**.
- `:::note[Proposed]` is the design as proposed, awaiting the maintainer.
- `:::caution[Open decision OD-n: short title]` is undecided and lists the options. The id must be a row of the "Open" table on [Open decisions](/open-decisions/); `just site-lint` refuses an id that is not there, and refuses an id that has been decided. A decided point is written as plain prose.

```md
:::note[Proposed]
Each node agent holds its own signing key.
:::

:::caution[Open decision OD-12: Index host on Lambda]
Options: Aurora Serverless v2, or DynamoDB only.
:::
```
