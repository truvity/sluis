# Governance

This file says who decides what in sluis, how a decision is made and kept, and how releases are numbered.
It describes the project as it is today: one maintainer, a young v2 line, and a plan to tighten the rules before production use.

## Branches and versions

- `main` holds **v2**, built from scratch. It started as an orphan branch and shares no history with v1.
- `master` holds **v1**. It continues its agreed roadmap releases (v1.80 to v1.85) and takes fixes. Any other v1 feature needs a decision issue first. New feature requests target v2.
- v2 is built alongside v1 and has to reach parity with v1 as it is at cutover.
- v1 is supported until the maintainers' own production deployments run v2. There is no fixed end date. See [ADR 0001](docs/decisions/0001-v1-and-v2-lines.md).
- v1 issues carry the label `line:v1`. An issue without it is about v2. The two lines have their own boards: [sluis v2](https://github.com/orgs/truvity/projects/9) and [sluis v1](https://github.com/orgs/truvity/projects/10).
- At cutover `master` is renamed `v1` and `main` becomes the default branch. Before the rename, workflow triggers, dependency-bot settings and rulesets learn the new name.
- v2 versions are `v2.x`. Until cutover they are pre-releases named `v2.0.0-alpha.N`. The Go module path ends in `/v2`.
- **A published version is never reused, moved or deleted.** Module proxies cache every version they have seen, so reuse breaks checksums. A mistake is fixed by the next version.
- Published history is not rewritten.
- Compatibility follows semantic versioning. A breaking change ships in a major release, or in a minor release that says so in the CHANGELOG, announces it and carries an upgrade note. Stored data stays readable by the next release. Protocol contracts are checked for breaking changes.

## Roles

**Maintainer.** Oleg is the sole maintainer today. The maintainer decides scope, accepts ADRs, grants compatibility exceptions, merges and cuts releases.

**Reviewers.** Anyone may review a pull request. A review from a non-maintainer is welcome and is read, but it does not replace the maintainer's decision. Reviewers who work in a part of the code for a long time may be named as code owners when the rules tighten (see below).

**Review agent.** An AI agent that reviews pull requests adversarially. It reads the diff, looks for faults and missing tests, and reports findings on the pull request. It is read-only: it does not edit the branch, approve alone, merge or decide scope. It is a different agent from the one that wrote the change.

**Contributors.** Anyone who sends an issue or a pull request. See [CONTRIBUTING.md](CONTRIBUTING.md). A contributor may be a person or an AI agent working for a person. The person is responsible for the work.

## Scope

sluis issues credentials and brokers identity. It is not a key-value store, a transit service or a data-path proxy.
New scope enters only through an ADR that names its owner, its boundary and its non-goals.
A change to the design model (the shape of components and how they depend on each other) also needs an ADR.

## How a decision is made

1. **Proposal.** Open an issue with the *Decision proposal* form. State the question, the context, the options with pros and cons, and the options you would not take. Label `status:needs-decision` is added.
2. **Discussion.** Comments on the issue. The maintainer may also discuss it internally. Internal discussion can hold material that must not be public, such as details of a private deployment.
3. **ADR pull request.** When the direction is clear, someone opens a pull request that adds `docs/decisions/NNNN-title.md` with status `Proposed`. It follows the [template](docs/decisions/0000-template.md) and must list the roads not taken.
4. **Accepted.** The maintainer merges the pull request with status `Accepted`. Merging is the decision. A rejected proposal is merged with status `Rejected`, so the reasoning is kept.
5. **Changing a decision.** An ADR is never edited to reverse it. A new ADR supersedes the old one, and the old status line says so.

Small changes that follow existing ADRs need no proposal. A pull request is enough.

### From internal reasoning to a public ADR

Draft ADRs, research and stakeholder input are written in internal discussion first. They may name deployments, accounts, teams and people.
Only the final ADR is public. When the maintainer decides:

- the final ADR is written fresh for the public repository, not copied;
- it keeps the question, the options, the decision, the consequences and the roads not taken;
- it names no private deployment, account, hostname, ticket or person other than the maintainer, and uses neutral examples (`example.com`, `acme`);
- the internal draft is marked as decided and links to the public ADR.

Roadmap, features, architecture and decisions are public. Internal reasoning and per-deployment rollout notes are not.

## Review rules

**Now.** Every change to `main` goes through a pull request. The maintainer reviews it, and the review agent reviews it too. The maintainer merges when checks pass, and findings from the review agent are answered or fixed. Changes that touch keys, secrets, authorisation, PKI or audit also carry a short security note in the pull request: what could go wrong, and which test shows it does not.

**Agents.** An agent working for the maintainer may merge its own pull request when checks are green and review is done. It never decides scope, never merges another author's pull request, and never changes governance files without the maintainer.

**Later.** The rules tighten before sluis serves customer-identity use (CIAM profiles) in production, and before 1.0 in production. The bar is:

- a `CODEOWNERS` file;
- two approvals on a pull request;
- a named security owner for security-sensitive paths;
- a long-term support line;
- an independent penetration test;
- FAPI certification where the profile claims it.

When a rule on this list takes effect, an ADR records the date and this file is updated in the same pull request. Rulesets on `main` follow once checks exist.

## Standards

Every standard sluis claims is listed in a register with a support level, the reason, how it is verified and the evidence. A deliberate deviation from a standard needs an ADR.

## Public repository hygiene

This repository is public. Code, docs, the CHANGELOG, tests, commit messages and pull requests name no real organisation account, hostname, cluster, ticket or secret path, and no person other than the maintainer. Use neutral examples.

## Changing these rules

Open a pull request that changes this file. The maintainer decides. A change that makes the rules looser needs an ADR.
