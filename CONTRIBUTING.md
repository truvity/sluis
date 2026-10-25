# Contributing

Thank you for helping. This file describes how to work on sluis v2 (`main`).
Who decides what is in [GOVERNANCE.md](GOVERNANCE.md). Security reports go through [SECURITY.md](SECURITY.md), not through issues.

v1 lives on `master` and gets fixes only. For a v1 fix, branch from `master` and open the pull request against `master`.

## Before you start

- Look for an existing issue or pull request first.
- For a bug, open an issue with the *Bug* form.
- For a feature, open an issue with the *Feature request* form. Wait for the maintainer's answer before you build anything large.
- For a question about direction, scope or design, open an issue with the *Decision proposal* form. See below.

## Proposing a feature

Describe the problem before the solution. Say who has the problem and what they do today.
The maintainer replies with one of: accepted, needs a decision, out of scope. New scope needs an ADR first (see [GOVERNANCE.md](GOVERNANCE.md)).
Accepted features are tracked on the public [sluis roadmap](https://github.com/orgs/truvity/projects) project and in `docs/roadmap.md` once that file exists.

## Proposing a decision

1. Open an issue with the *Decision proposal* form.
2. When the direction is clear, open a pull request that adds an ADR from [the template](docs/decisions/0000-template.md). See [docs/decisions/README.md](docs/decisions/README.md).
3. The maintainer accepts the decision by merging the ADR.

## Workflow

- Branch from `main`. Name the branch `type/short-name`, for example `feat/token-exchange` or `docs/adr-readme`.
- Keep a pull request to one change. Do not stack pull requests on each other.
- Open a draft pull request early if you want feedback. Mark it ready when checks are green.
- Merge is by rebase. The maintainer merges.
- Do not rewrite history on a branch after someone has reviewed it. Add commits instead, and let the rebase merge tidy them.

### Commits

Use conventional commits: `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, `ci:`, `build:`. Add `!` after the type for a breaking change, for example `feat!:`.
Write the subject in the imperative, in one short line. Say why in the body when it is not obvious.
The history feeds release notes.

### CHANGELOG

`CHANGELOG.md` is written by hand, for the person who uses a release. A pull request that changes what a user notices adds a bullet under `## Unreleased`. The maintainer renames that heading to the version at release time.
Start a breaking bullet with **Breaking:** and say what to do first. Pure refactors, tests and dependency bumps need no bullet.

### Tests and docs

A change comes with its tests and its documentation in the same pull request.
Changes to protocols and stored formats also keep the compatibility rules in [GOVERNANCE.md](GOVERNANCE.md).
Build and check commands arrive with the code. This file will name them then.

### Public repository

Nothing in the repository, a commit message or a pull request may name a real organisation account, hostname, cluster, ticket, secret path or person (other than the maintainer). Use `example.com`, `acme` and `globex`.

## Review

- Every pull request is reviewed by the maintainer and by the review agent.
- Expect questions about tests, edge cases and failure modes. Answer them in the pull request, or fix the code.
- Reply to every comment, even if only to say "done". Resolve a thread only when the reviewer agrees.
- Changes to keys, secrets, authorisation, PKI or audit get a security note in the pull request.
- Review comments are about the change, not the person.

## AI-agent contributions

AI agents may write issues, code, tests and docs for sluis. Their work is reviewed like any other.

- A person owns the contribution. That person has read it, understands it and answers for it.
- Mention the assistance in the pull request. Keep a `Co-Authored-By` trailer on commits an agent wrote.
- An agent follows this file and `AGENTS.md` when it exists. It does not decide scope and does not touch governance files unless asked.
- Output that is plausible but unverified is not a contribution. Run it, test it, and say what you ran.
- Low-effort bulk pull requests may be closed without review.

## Licence

sluis is MIT licensed. By sending a contribution you agree that it is licensed under the [LICENSE](LICENSE).
