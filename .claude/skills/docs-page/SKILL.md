---
name: docs-page
description: Use when creating or editing a page of the sluis site under docs/ (not docs/decisions/ or docs/requirements/), including its status, asides, proposed notes and diagrams.
---

Read `docs/contributing/pages.md` and follow it. For any name, address or id, also read `docs/contributing/public-content.md`.

Checklist:

- [ ] The front matter has a `title`, a `description` and a `status` as the page describes.
- [ ] Decided and proposed points are marked the way the page describes; every proposed note links its open issue.
- [ ] A rule is stated once on the page that owns it; the other pages link there. A moved page has its redirect in `site/astro.config.mjs`.
- [ ] A home page starts from the reader's pain. A change to the site's layout, Worker or headers uses the `site-change` skill instead.
- [ ] Diagrams keep to the size limits on the page.
- [ ] Links to other pages use site paths, and the linked pages exist.
- [ ] `devbox run -- just check` passes; then follow `docs/contributing/workflow.md`.
