# Contributing

Thank you for helping. This file is the entry point for work on sluis v2 (`main`).
The working rules are written once, in [docs/contributing/](docs/contributing.md), and published on [sluis.id/contributing](https://sluis.id/contributing/). People and AI agents follow the same pages.
Who decides what is in [GOVERNANCE.md](GOVERNANCE.md). Security reports go through [SECURITY.md](SECURITY.md), not through issues.

v1 lives on `master`. It builds its own roadmap (v1.80 to v1.85) and takes fixes. New feature requests target v2. Any other v1 feature needs a decision issue first.
For a v1 change, branch from `master` and open the pull request against `master`.
The two lines are in [ADR 0001](docs/decisions/0001-v1-and-v2-lines.md).

An issue about v1 carries the label `line:v1`. An issue without it is about v2.
Work is tracked on two public boards: [sluis v2](https://github.com/orgs/truvity/projects/9) and [sluis v1](https://github.com/orgs/truvity/projects/10).

## Before you start

- Look for an existing issue or pull request first.
- For a bug, open an issue with the *Bug* form.
- For a feature, open an issue with the *Feature request* form. It is planned for v2. Wait for the maintainer's answer before you build anything large.
- For a question about direction, scope or design, open an issue with the *Decision proposal* form. The steps from there to a decision record are in [Decisions, requirements and research](docs/contributing/decisions.md).

## Proposing a feature

Describe the problem before the solution. Say who has the problem and what they do today.
The maintainer replies with one of: accepted, needs a decision, out of scope. New scope needs an ADR first (see [GOVERNANCE.md](GOVERNANCE.md)).
Accepted features are tracked on the public [sluis v2](https://github.com/orgs/truvity/projects/9) board and in the [roadmap](docs/roadmap.md).

## How we work

Before you change anything, read the page for what you are about to do. The table of pages is in [docs/contributing.md](docs/contributing.md#the-pages).

## Licence

sluis is MIT licensed. By sending a contribution you agree that it is licensed under the [LICENSE](LICENSE).
