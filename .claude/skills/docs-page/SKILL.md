---
name: docs-page
description: Use when creating or editing a page of the sluis site under docs/ (not docs/decisions/ or docs/requirements/), including its status, asides, open-decision notes and diagrams.
---

Read `docs/contributing/pages.md` and `docs/contributing/writing.md` and follow them. For any name, address or id, also read `docs/contributing/public-content.md`.

Checklist:

- [ ] The front matter has a `title`, a `description` and a `status` as the page describes.
- [ ] Agreed, proposed and open points are marked the way the page describes.
- [ ] Diagrams keep to the size limits on the page.
- [ ] Links to other pages use site paths, and the linked pages exist.
- [ ] `devbox run -- just site-check` passes; then follow `docs/contributing/workflow.md`.
