# Contributing

Thank you for helping. This file is the entry point for work on sluis v2 (`main`).
The working rules are written once, in [docs/contributing/](docs/contributing.md), and published on [sluis.id/contributing](https://sluis.id/contributing/). People and AI agents follow the same pages.
Who decides what is in [GOVERNANCE.md](GOVERNANCE.md). Security reports go through [SECURITY.md](SECURITY.md), not through issues.

v1 lives on `master`. It builds its agreed roadmap (v1.80 to v1.85) and takes fixes. New feature requests target v2. Any other v1 feature needs a decision issue first.
For a v1 change, branch from `master` and open the pull request against `master`.
The two lines are in [ADR 0001](docs/decisions/0001-v1-and-v2-lines.md).

An issue about v1 carries the label `line:v1`. An issue without it is about v2.
Work is tracked on two public boards: [sluis v2](https://github.com/orgs/truvity/projects/9) and [sluis v1](https://github.com/orgs/truvity/projects/10).

## Before you start

- Look for an existing issue or pull request first.
- For a bug, open an issue with the *Bug* form.
- For a feature, open an issue with the *Feature request* form. It is planned for v2. Wait for the maintainer's answer before you build anything large.
- For a question about direction, scope or design, open an issue with the *Decision proposal* form. See below.

## Proposing a feature

Describe the problem before the solution. Say who has the problem and what they do today.
The maintainer replies with one of: accepted, needs a decision, out of scope. New scope needs an ADR first (see [GOVERNANCE.md](GOVERNANCE.md)).
Accepted features are tracked on the public [sluis v2](https://github.com/orgs/truvity/projects/9) board and in the [roadmap](docs/roadmap.md).

## Proposing a decision

1. Open an issue with the *Decision proposal* form.
2. When the direction is clear, open a pull request that adds an ADR from [the template](docs/decisions/0000-template.md). The steps are in [Decision records](docs/contributing/decisions.md), the format in [docs/decisions/README.md](docs/decisions/README.md).
3. The maintainer accepts the decision by merging the ADR.

## How we work

Read the page for what you are about to do:

| Before you | Read |
|---|---|
| create a branch, commit, push, open a pull request or answer a review | [Branches and pull requests](docs/contributing/workflow.md) |
| run a build, a test or a check | [Tools, checks and verification](docs/contributing/tools.md) |
| write or rewrite a decision record | [Decision records](docs/contributing/decisions.md) |
| add or change a requirement | [Requirements](docs/contributing/requirements.md) |
| run a spike or write up research | [Spikes and research](docs/contributing/research.md) |
| create or edit a page of the site | [Target-state pages](docs/contributing/pages.md) and [Writing style](docs/contributing/writing.md) |
| write a name, an address, an id or anything secret | [Public content and secrets](docs/contributing/public-content.md) |
| contribute with an AI agent | [AI agents](docs/contributing/agents.md) and [AGENTS.md](AGENTS.md) |

## Licence

sluis is MIT licensed. By sending a contribution you agree that it is licensed under the [LICENSE](LICENSE).
