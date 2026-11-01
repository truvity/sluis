# AGENTS.md

Instructions for AI agents working in this repository. People follow the same rules, written in [docs/contributing/](docs/contributing.md); this file is their short form. If it disagrees with a page there, the page wins.

## Every task

- v2 is the orphan branch `main`; v1 is `master`. Never branch from, target or push to `master` for v2 work, and never push to `main`.
- One change, one worktree: check `git remote -v`, then `git worktree add --no-track -b <type>/<short-name> <dir> origin/main`. Push with an explicit refspec: `git push origin <branch>:<branch>`.
- Your shell may reset its working directory between calls. Use absolute paths: `git -C <abs>`, or `cd <abs> && <cmd>` in one call.
- Tools come from devbox. Run the `just` recipe, not the bare tool: `devbox run -- just check` is the gate. No testcontainers; prefer pinned release binaries.
- This repository is public: no real organisation account, hostname, ticket, secret path or person other than the maintainer. Use the example names in `docs/contributing/public-content.md`.
- Never print a secret or a token: not in output, logs, commits or pull request text.
- The maintainer decides. Present options with pros, cons and a recommendation; do not pick. Merge only as the "Merging" section of `docs/contributing/workflow.md` allows.
- If a safety check or a permission prompt blocks you, stop and report; do not work around it.

## Keys, roots and ceremonies

- Roots and ceremonies (root keys, root transitions, keyrings, trust bundles, the ceremony CLI) live in the separate project ritueel: https://github.com/truvity/ritueel and https://ritueel.id. sluis holds intermediates only.
- Keys are generated in place, on a card or in a cloud key service, and are never extractable.
- sluis has no two-person ceremonies, no air-gapped live CD and no YubiHSM in its design.
- The source of truth is `docs/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api.md` (ADR 0013, "Intermediates and the hand-off to root custody").

## Before you act, read the page

| Before you | Read and follow |
|---|---|
| create or edit a file in `docs/decisions/` | `docs/contributing/decisions.md` |
| create or edit a file in `docs/requirements/` | "Requirements" in `docs/contributing/decisions.md` |
| run a spike or write up research | "Spikes and research" in `docs/contributing/decisions.md` |
| create or edit any other page under `docs/` | `docs/contributing/pages.md` |
| create a branch, commit, push, open or merge a pull request | `docs/contributing/workflow.md` |
| run a build, a test or a check | `docs/contributing/tools.md` |
| implement a protocol, a format or an algorithm | "Standards first" in `docs/contributing/tools.md` |
| write a test that uses a hardware token | "Hardware tokens" in `docs/contributing/tools.md` |
| write a hostname, an address, an id or a name | `docs/contributing/public-content.md` |
| report a vulnerability you found or suspect | `SECURITY.md`, never an issue or a pull request |
| change anything that touches keys, PKI or signing | `docs/decisions/0013-the-hand-off-to-root-custody-and-the-signing-api.md` |
| change `GOVERNANCE.md`, `SECURITY.md` or `LICENSE` | stop: only with the maintainer's request |
| take a role in a team of agents, or report back | `docs/contributing/agents.md` |
