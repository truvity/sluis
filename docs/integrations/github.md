---
title: GitHub
description: How sluis uses GitHub Apps, organisation teams and Actions identity tokens, to mint repository tokens, keep teams in step with the directory and verify CI jobs.
status: target
sidebar:
  order: 1
---

GitHub is the integration with the most roles. It is one process that holds the credentials of the GitHub Apps and plays four roles: it verifies Actions identity tokens, supplies team facts, mints installation tokens and reconciles team membership.

## What it's for

- **A CI job publishes a package or cuts a release** without a stored secret. The job presents its Actions identity token, sluis checks it against GitHub's published keys, and the job receives an installation token for one repository with the one permission it needs. The token lives for minutes.
- **A pipeline opens pull requests across several repositories** (dependency updates, generated changes) with a token that covers exactly those repositories and nothing else.
- **A new engineer joins the right teams** because the company directory says they are in a group. When they leave the group, they leave the team, and an organisation owner is reported, never removed.
- **An infrastructure-as-code program** (a Pulumi or Terraform run) gets a token for the organisation it manages and no longer needs a personal access token.
- **Self-hosted runners register** with an App that can do nothing but manage runners.
- **A job proves where it runs** so that other systems, such as a cloud account, can trust it. The proof is the Actions identity token; GitHub holds no sluis secret.

## Integration roles

| Role | What it does for GitHub |
|---|---|
| `WorkloadVerifier` | Verifies an Actions identity token against GitHub's key set and returns attributes: `github.repository`, `ref`, `job_workflow_ref`, `environment`, `visibility`, owner and repository ids. It needs no secret, so it can run apart from the App keys. |
| `FactsSource` | Reports organisation members, team members and maintainers. |
| `CredentialMinter` | Mints an installation token for a catalogued App, for a mint grant that the `policy` role signed. |
| `Provisioner` | Makes the organisation's teams equal the desired state core publishes: invites, adds, promotes and removes. |

A person's GitHub account is linked to their work address through a separate **link App**, so that a team can be filled from a directory group.

## What sluis asks the vendor for

Each App asks GitHub for the least that its job needs. One App does one job; sluis does not combine them.

| App | Purpose | Permissions |
|---|---|---|
| Organisation App, one per organisation | Reads and changes membership | Organisation `members`: read for a facts-only install, write when it provisions. `organization_administration`: read, to see the organisation's settings. No webhook. Private. |
| Catalogue App, any number | Mints installation tokens for jobs | Exactly the permissions you declare for it, such as `contents` and `pull_requests`. An App that approves reviews carries no administration permission; an App that administers stays out of the approving path. `metadata: read` is always there. |
| Link App, one | Lets a person link their account | The person's own email addresses (read), by OAuth, as the person. Installed nowhere. Public, because a private App is authorised only by members of its owner. |
| Runner App, one per organisation per tier | Registers self-hosted runners | `organization_self_hosted_runners`: write. |

The workload verifier asks GitHub for nothing. It reads the public key set that GitHub publishes for Actions tokens.

Egress is limited to `api.github.com` (and GitHub's token issuer for the key set). The App private keys are operator secrets: they are referenced in configuration as `cred:` values and never inlined.

## What policy controls

The policy is the only place that decides.

- **Who may mint what.** A grant names a group, the repositories (names or globs) and the permissions, and the token never exceeds one grant. Grants are not added together. A request for a repository or a permission outside one grant is refused.
- **Which jobs count as which group.** Matchers on the verified attributes: repository, ref, `job_workflow_ref`, event, environment, visibility. Pin a grant to one reviewed workflow file, not every job in the repository. The trusted-owner list is the trust boundary: an empty list verifies nothing.
- **Which teams hold which groups.** A binding maps directory groups to teams as members and maintainers.
- **Caps and rate limits** are enforced by `policy` and `quota` before the minter is called. The integration enforces only GitHub's own limits.
- **Safety, in the reconciler's configuration:** dry run until the organisation is enabled, a seat check, and a breaker that holds a pass which would remove over half of the members.

## What is audited

- Every token request, with the outcome (success, denied, failure), the actor (the verified subject), the App, the grant used, the repositories and permissions granted, the installation and the expiry. **The token itself is never recorded.**
- Every membership change: invited, added, role set, removed, and each change that was held for an operator.
- The lifecycle of an App: created, installed, disconnected, and each link made, narrowed or lost.
- A removal the directory does not vouch for is held, and an organisation owner the directory does not know is reported.

## Setup

An example, with the flow in [the setup patterns](/integrations/connecting/#github-app-manifest-flow).

1. Declare the organisation and the Apps you want, with their permissions.
2. Run `sluis integrations connect github --org example-org --app publisher`. sluis prints a link; an organisation owner opens it and confirms the manifest with one click.
3. The owner picks the repositories on GitHub's install page.
4. sluis stores the App's private key as a credential and records the App id and installation as learned data.
5. Write the grants in the policy, and run a dry-run pass for the teams before you enable the organisation.

```yaml
# /etc/sluis/install.yaml (example)
integrations:
  github:
    example-org:
      roles: [verifier, facts, minter, provisioner]
      verifier:
        owners: [example-org]            # the trust boundary for Actions tokens
      apps:
        - id: publisher                  # a catalogue App
          permissions: { contents: write, pull_requests: write }
        - id: org                        # the organisation App
          permissions: { members: write, organization_administration: read }
      # the private keys are learned at connect time and held as credentials:
      #   cred:sluis-int-github.example-org.publisher.app-key
```

A job then asks for a token:

```yaml
permissions:
  id-token: write          # the job's identity token
steps:
  - run: sluis token github --app publisher --repository app --permission contents=write
```

## v1 → v2 notes

- v1 does all of this today. The App flow is a console action; teams are bound in the policy's `github` table; the token request is the `truvity/sluis` GitHub Action or `sluisctl github-token`. See the v1 guides for [Actions](/v1/guides/sluis/connect/github-actions/), [App tokens](/v1/guides/sluis/connect/github-app-tokens/), [the App catalogue](/v1/guides/sluis/connect/github-apps-catalogue/) and [organisations](/v1/guides/sluis/connect/github-organisation/).
- In v1 the grants live in the App catalogue; in v2 they are policy, and the minter receives a signed mint grant. Caps and rates move from the minter into `policy` and `quota`.
- The shell-only Action stays as the way to request a token from a workflow. The command line becomes the single `sluis` binary, so `sluisctl github-token` becomes a subcommand of `sluis`.
- Parity issues: [#783](https://github.com/truvity/sluis/issues/783) CI identity tokens, [#808](https://github.com/truvity/sluis/issues/808) team controller, [#809](https://github.com/truvity/sluis/issues/809) account links, [#810](https://github.com/truvity/sluis/issues/810) App catalogue and runner Apps, [#811](https://github.com/truvity/sluis/issues/811) installation tokens, [#826](https://github.com/truvity/sluis/issues/826) the exchange Action.

:::note[Proposed]
The names `sluis integrations connect` and `sluis token github` are examples of the command shape; the command tree is not yet decided.
:::
