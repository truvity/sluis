---
name: open-pr
description: Use when starting a change on a new branch or worktree, committing, pushing, opening a pull request against main, or answering review feedback on one.
---

Read `docs/contributing/workflow.md` and follow it, with `docs/contributing/tools.md` for the checks and `docs/contributing/agents.md` for attribution.

Checklist:

- [ ] `git remote -v` checked; the worktree is off `origin/main` with `--no-track`, on a `type/short-name` branch.
- [ ] One topic in the pull request, with its tests and docs.
- [ ] The `just` recipes CI runs pass locally; say which ones you ran.
- [ ] Commits are conventional and keep the tool's attribution lines.
- [ ] Rebased on the current `main`; pushed with an explicit refspec, never to `main` or `master`.
- [ ] The pull request follows `.github/pull_request_template.md`.
- [ ] Before each further push, every reviewer and review-bot comment is read and answered.
- [ ] CI is green; the merge is the maintainer's.
