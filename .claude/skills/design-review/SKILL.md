---
name: design-review
description: Use when asked to analyse a milestone or a large topic before writing records or issues, to check how complete its design is, or to decide which questions to bring to the maintainer.
---

Read `docs/contributing/design.md` and follow it: principles first, ask only real conflicts, generalise before detail. This skill is the procedure; the result is a report, and no record or issue is written until the maintainer has read it.

Inputs, read before step 1:

- `docs/principles.md`, the design principles.
- The accepted decision records in `docs/decisions/` (the index is `docs/decisions/README.md`).
- `docs/requirements/*.yaml`, the requirements catalogue (`docs/requirements/README.md` explains the fields).
- `docs/roadmap.md`, the milestones and what each is done when.
- `docs/features.md`, the v1 parity checklist.
- The export of the project board: the items of the sluis v2 board with their `Product milestone`, from the maintainer or from `gh project item-list` (a read, over the maintainer's token). Prefer a file the maintainer gives you.

Steps:

1. **List the high-level questions** the milestone or topic raises: topology, trust and custody, data ownership, failure, operations, compatibility, and what the topic adds. Mark each question SETTLED, PARTLY or OPEN, and name the principle or the record behind each mark. A question that other milestones share is listed once, with them named.
2. **Map the issues and the records under the questions.** For each question: the records that answer it, the requirements that bind it (REQ ids), the issues that deliver it. Note an issue that sits under no question, a question with no issue, and a record or requirement whose issue is missing or in the wrong milestone.
3. **Propose the records.** For each PARTLY or OPEN question the principles can settle, draft the answer and cite the principle; do not ask about it. List the records to write, split or merge, with titles (`docs/contributing/decisions.md` says how a record is written).
4. **List the owner questions**, only the real conflicts: two principles pulling apart, a principle that does not reach, a product, legal or finance fact nobody here can know. Each has options, the pros and cons of each, and a recommendation.
5. **Give a completeness estimate with its weights.** Weight the high-level questions (say which weight and why: a question that blocks the first working version weighs more than one that waits for a later milestone), score each SETTLED as 1, PARTLY as 0.5, OPEN as 0, and show the sum as a percentage with the table behind it. State the date and the commit it was measured on.

Report in the order of "What an agent reports" in `docs/contributing/agents.md`: the verdict and the completeness figure first. Label each claim READ, MEASURED or UNVERIFIED, and keep private names out of it.
