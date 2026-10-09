# sluis

The identity and access service: who a caller is, what that gets them, and the way out. The pages follow what the
reader is doing; pick the column that matches. Pages marked *planned* describe what is decided and not yet built.

| You are | Go to | It holds |
|---|---|---|
| learning | [getting-started/](../../get-started/sluis/README.md) | one tutorial per deployment shape |
| doing a task | [how-to/](../../guides/sluis/operate/day-two.md) | one task per page; upgrades under [how-to/upgrade/](../../guides/sluis/upgrade/v1.64.md) |
| looking something up | [reference/](../../reference/sluis/configuration.md) | keys, chart values, CLI, adapters, audit actions |
| understanding | [explanation/](design.md) | design, concepts, the why |
| deploying | [deployment/](../../get-started/sluis/deployment/README.md) | one page per deployment shape |
| running it | [operations/](../../guides/sluis/operate/README.md) | recurring and incident tasks |
| asking why it is so | [decisions/](../../decisions/README.md) | the ADRs, with their status (one series for sluis, audit and storage) |

[CHANGELOG.md](../../../CHANGELOG.md) says what changed in each version; the steps to take are in the upgrade pages.

## Getting started

- [Choose a deployment shape](../../get-started/sluis/README.md)
- [AWS Lambda](../../get-started/sluis/aws-lambda.md)
- [Kubernetes with AWS storage](../../get-started/sluis/kubernetes-aws.md)
- [An existing Kubernetes install on the legacy store](../../get-started/sluis/kubernetes-legacy-store.md)

## How to

- Install and run: [install with Helm](../../guides/sluis/operate/install-with-helm.md), [day one](../../guides/sluis/operate/day-one.md),
  [day-two tasks](../../guides/sluis/operate/day-two.md), [high availability](../../guides/sluis/operate/high-availability.md),
  [telemetry](../../guides/sluis/operate/install-telemetry.md), [back up and restore](../../guides/sluis/operate/back-up-and-restore.md)
- Upgrade: [v1.62](../../guides/sluis/upgrade/v1.62.md), [v1.63](../../guides/sluis/upgrade/v1.63.md), [v1.64](../../guides/sluis/upgrade/v1.64.md)
- Migrate: [the State](../../guides/sluis/migrate/migrate-state.md), [the secrets layout](../../guides/sluis/migrate/migrate-secrets-layout.md), [cut over an installation](../../guides/sluis/migrate/cutover.md),
  [from the three-image chart](../../guides/sluis/migrate/migrate-from-the-access-issuer-chart.md),
  [from environment variables](../../guides/sluis/migrate/migrate-from-environment-variables.md),
  [from an IdP](../../guides/sluis/migrate/migrate-from-an-idp.md), [from google-group-sync](../../guides/sluis/migrate/migrate-from-google-group-sync.md)
- Policy: [turn enforce on](../../guides/sluis/turn-enforce-on.md), [declare a vocabulary](../../guides/sluis/declare-a-vocabulary.md),
  [test the policy](../../guides/sluis/test-the-policy.md), [bind GitHub teams](../../guides/sluis/bind-github-teams.md),
  [bind Slack channels](../../guides/sluis/bind-slack-channels-in-git.md)
- Connect something: [everything under how-to/connect/](../../guides/sluis/connect/google-workspace.md): the
  corporate directory, GitHub, Slack, clusters, AWS, OpenBao, SSH, PostgreSQL, MCP, gateways, CI
- Contribute: [extend sluis](../../guides/sluis/extend.md), [add an adapter](../../guides/sluis/add-an-adapter.md),
  [testing](../../guides/sluis/testing.md), [CONTRIBUTING](../../../CONTRIBUTING.md)

## Reference

- Configuration: [service document](../../reference/sluis/configuration.md), [chart values](../../reference/sluis/chart-values.md),
  [installation document](../../reference/sluis/installation-document.md), [policy](../../reference/sluis/policy.md),
  [grant names](../../reference/sluis/taxonomy.md), [endpoints](../../reference/sluis/endpoints.md)
- Platform: [adapters](../../reference/sluis/adapters.md) (generated), [ports](../../reference/sluis/ports.md),
  [capabilities](../../reference/sluis/capabilities.md), [Lambda](../../reference/sluis/lambda.md),
  [Pulumi library](../../reference/sluis/pulumi-library.md), [storage layout](../../reference/sluis/storage-layout.md)
- Tools: [sluisctl](../../reference/sluis/sluisctl.md), [contracts](../../reference/sluis/contracts.md),
  [Go module](../../sdk/go/sluis.md), [TypeScript](../../sdk/typescript/sluis.md),
  [audit actions](../../reference/sluis/audit-actions.md), [telemetry and alerts](../../reference/sluis/telemetry.md)

## Explanation

- [Why sluis exists](why.md), [concepts](concepts.md),
  [architecture](architecture.md), [doctrine](doctrine.md),
  [trust](trust.md), [safety](safety.md),
  [integrations](integrations.md)
- [People and agents](people-and-agents.md): the two client classes and the three sign-out scopes
- [The design](design.md): one process, directory model, tokens and sessions, console,
  controllers, audit, store, recovery, failure semantics
- [Ports and adapters](ports.md), [configuration](configuration.md),
  [policy](policy.md), [the groups claim](groups-in-a-token.md),
  [conformance findings](conformance-findings.md)
