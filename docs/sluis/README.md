# sluis

The identity and access service: who a caller is, what that gets them, and the way out. The pages follow what the
reader is doing; pick the column that matches. Pages marked *planned* describe what is decided and not yet built.

| You are | Go to | It holds |
|---|---|---|
| learning | [getting-started/](../getting-started/README.md) | one tutorial per deployment shape |
| doing a task | [how-to/](../how-to/day-two.md) | one task per page; upgrades under [how-to/upgrade/](../how-to/upgrade/v1.64.md) |
| looking something up | [reference/](../reference/configuration.md) | keys, chart values, CLI, adapters, audit actions |
| understanding | [explanation/](../explanation/design.md) | design, concepts, the why |
| deploying | [deployment/](deployment/README.md) | one page per deployment shape |
| running it | [operations/](operations/README.md) | recurring and incident tasks |
| asking why it is so | [decisions/](../decisions/README.md) | the ADRs, with their status (one series for sluis, audit and storage) |

[CHANGELOG.md](../../CHANGELOG.md) says what changed in each version; the steps to take are in the upgrade pages.

## Getting started

- [Choose a deployment shape](../getting-started/README.md)
- [AWS Lambda](../getting-started/aws-lambda.md)
- [Kubernetes with AWS storage](../getting-started/kubernetes-aws.md)
- [An existing Kubernetes install on the legacy store](../getting-started/kubernetes-legacy-store.md)

## How to

- Install and run: [install with Helm](../how-to/install-with-helm.md), [day one](../how-to/day-one.md),
  [day-two tasks](../how-to/day-two.md), [high availability](../how-to/high-availability.md),
  [telemetry](../how-to/install-telemetry.md), [back up and restore](../how-to/back-up-and-restore.md)
- Upgrade: [v1.62](../how-to/upgrade/v1.62.md), [v1.63](../how-to/upgrade/v1.63.md), [v1.64](../how-to/upgrade/v1.64.md)
- Migrate: [the State](../how-to/migrate-state.md), [the secrets layout](../how-to/migrate-secrets-layout.md), [cut over an installation](../how-to/cutover.md),
  [from the three-image chart](../how-to/migrate-from-the-access-issuer-chart.md),
  [from environment variables](../how-to/migrate-from-environment-variables.md),
  [from an IdP](../how-to/migrate-from-an-idp.md), [from google-group-sync](../how-to/migrate-from-google-group-sync.md)
- Policy: [turn enforce on](../how-to/turn-enforce-on.md), [declare a vocabulary](../how-to/declare-a-vocabulary.md),
  [test the policy](../how-to/test-the-policy.md), [bind GitHub teams](../how-to/bind-github-teams.md),
  [bind Slack channels](../how-to/bind-slack-channels-in-git.md)
- Connect something: [everything under how-to/connect/](../how-to/connect/corporate-directory.md): the
  corporate directory, GitHub, Slack, clusters, AWS, OpenBao, SSH, PostgreSQL, MCP, gateways, CI
- Contribute: [extend sluis](../how-to/extend.md), [add an adapter](../how-to/add-an-adapter.md),
  [testing](../how-to/testing.md), [CONTRIBUTING](../../CONTRIBUTING.md)

## Reference

- Configuration: [service document](../reference/configuration.md), [chart values](../reference/chart-values.md),
  [installation document](../reference/installation-document.md), [policy](../reference/policy.md),
  [grant names](../reference/taxonomy.md), [endpoints](../reference/endpoints.md)
- Platform: [adapters](../reference/adapters.md) (generated), [ports](../reference/ports.md),
  [capabilities](../reference/capabilities.md), [Lambda](../reference/lambda.md),
  [Pulumi library](../reference/pulumi-library.md), [storage layout](../reference/storage-layout.md)
- Tools: [sluisctl](../reference/sluisctl.md), [contracts](../reference/contracts.md),
  [Go module](../reference/go-module.md), [TypeScript](../reference/typescript.md),
  [audit actions](../reference/audit-actions.md), [telemetry and alerts](../reference/telemetry.md)

## Explanation

- [Why sluis exists](../explanation/why.md), [concepts](../explanation/concepts.md),
  [architecture](../explanation/architecture.md), [doctrine](../explanation/doctrine.md),
  [trust](../explanation/trust.md), [safety](../explanation/safety.md),
  [integrations](../explanation/integrations.md)
- [People and agents](explanation/people-and-agents.md): the two client classes and the three sign-out scopes
- [The design](../explanation/design.md): one process, directory model, tokens and sessions, console,
  controllers, audit, store, recovery, failure semantics
- [Ports and adapters](../explanation/ports.md), [configuration](../explanation/configuration.md),
  [policy](../explanation/policy.md), [the groups claim](../explanation/groups-in-a-token.md),
  [conformance findings](../explanation/conformance-findings.md)
