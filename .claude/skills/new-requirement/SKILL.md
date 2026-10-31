---
name: new-requirement
description: Use when adding, splitting, withdrawing or editing a requirement (REQ-<AREA>-NNN) in docs/requirements/*.yaml, or when a decision creates an obligation that needs one.
---

Read `docs/contributing/requirements.md` and follow it. It points to `docs/requirements/README.md` for the fields, the ids and the steps; read that file too.

Checklist, each step as the pages describe it:

- [ ] The area and the id are chosen as the README says; no id is reused or renumbered.
- [ ] Every field uses a value the README or `index.yaml` declares.
- [ ] The decision record that creates the obligation cites the id.
- [ ] `devbox run -- just check-requirements`, then `devbox run -- just check`, pass.
- [ ] Then follow `docs/contributing/workflow.md`.
