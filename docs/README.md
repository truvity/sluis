# Documentation

This repository ships three things that are installed together or apart. Each has a documentation tree with the same
sections, organised by what the reader is doing ([Diataxis](https://diataxis.fr/)).

| | What it is | Start at |
|---|---|---|
| **sluis** | the identity and access service: who a caller is, what that gets them, and the way out | [docs/sluis](sluis/README.md) |
| **audit** | the audit trail: records written once, sealed, searchable, verifiable by an auditor | [docs/audit](audit/README.md) |
| **storage** | the state and key backends both share, chosen by the `state` and `keys` blocks | [docs/storage](storage/README.md) |

## Sections

| You are | sluis | audit | storage |
|---|---|---|---|
| learning | [getting started](getting-started/README.md) | [getting started](audit/README.md#getting-started) | [use the OpenBao backends](storage/how-to/use-the-openbao-backends.md) |
| understanding | [architecture](explanation/architecture.md), [people and agents](sluis/explanation/people-and-agents.md) | [architecture](audit/explanation/architecture.md) | [architecture](storage/architecture.md) |
| deploying | [deployment shapes](sluis/deployment/README.md) | [deployment shapes](audit/deployment/README.md) | [where the backends run](storage/deployment.md) |
| running it | [operations](sluis/operations/README.md) | [how-to: operate](audit/README.md#how-to) | [deployment](storage/deployment.md) |
| doing a task | [how-to](how-to/day-two.md) | [how-to](audit/README.md#how-to) | [how-to](storage/how-to/use-the-openbao-backends.md) |
| looking something up | [reference](reference/configuration.md) | [reference](audit/reference/configuration.md) | [the adapter block](storage/reference/adapter-block.md) |

## Why it is so

[Decisions](decisions/README.md) is one series for all three: sluis 0001 to 0042, audit's records renumbered after them
(a table maps the old numbers), storage's next. A record is never edited to reverse a decision; a new one supersedes it.

## Where a shared topic lives

A topic that crosses products lives with the owner of the contract, and the others link to it.

| Topic | Owner |
|---|---|
| the roster catalogue (what sluis records, its actions and schemas) | sluis: [audit actions](reference/audit-actions.md), [change the audit catalogue](how-to/change-the-audit-catalogue.md) |
| the record format, extension slots and the SDKs (emitter and query) | audit: [the record](audit/reference/record.md), [extension points](audit/reference/extension-points.md), [emitter library](audit/reference/emitter-library.md) |
| the adapter block (`keys`, `state`) | storage: [the adapter block](storage/explanation/adapter-block.md) |
| people and agents: client classes and sign-out scopes | sluis: [people and agents](sluis/explanation/people-and-agents.md) |

The documentation sections of sluis predate this layout and still sit at the top of `docs/` (`getting-started`,
`how-to`, `reference`, `explanation`); [docs/sluis](sluis/README.md) is their index and the home of the new pages.
Moving them under `docs/sluis/` is a mechanical change that waits for the open pull requests that edit them.
