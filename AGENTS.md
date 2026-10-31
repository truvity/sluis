# AGENTS.md

Instructions for AI agents working in this repository. People follow the same rules, written in [docs/contributing/](docs/contributing.md); this file is their short form. If it disagrees with a page there, the page wins.

## Every task

- v2 is the orphan branch `main`; v1 is `master`. Never branch from, target or push to `master` for v2 work, and never push to `main`.
- One change, one worktree: check `git remote -v`, then `git worktree add --no-track -b <type>/<short-name> <dir> origin/main`. Push with an explicit refspec: `git push origin <branch>:<branch>`.
- Your shell may reset its working directory between calls. Use absolute paths: `git -C <abs>`, or `cd <abs> && <cmd>` in one call.
- Tools come from devbox. Run the `just` recipe (`devbox run -- just site-check`), not the bare tool.
- No testcontainers. Prefer pinned release binaries over containers.
- This repository is public. Use example names (`sluis.example.com`, `acme`, `123456789012`); never a real hostname, account id, ticket key, person or company.
- Never print a secret or a token: not in output, logs, commits or pull request text.
- The maintainer decides. Present options with pros, cons and a recommendation; do not pick, and do not merge unless the maintainer asked you to.
- If a safety check or a permission prompt blocks you, stop and report; do not work around it.

## Before you act, read the page

| Before you | Read and follow |
|---|---|
| create or edit a file in `docs/decisions/` | `docs/contributing/decisions.md` |
| create or edit a file in `docs/requirements/` | `docs/contributing/requirements.md` |
| run a spike or write up research | `docs/contributing/research.md` |
| create or edit any other page under `docs/` | `docs/contributing/pages.md` and `docs/contributing/writing.md` |
| create a branch, commit, push or open a pull request | `docs/contributing/workflow.md` |
| run a build, a test or a check | `docs/contributing/tools.md` |
| write a hostname, an address, an id or a name | `docs/contributing/public-content.md` |
| take a role in a team of agents, or report back | `docs/contributing/agents.md` |
