# sluis

sluis is an OpenID Connect issuer for workforce access. People sign in through their directory. CI jobs and workloads exchange a platform token for AWS, GitHub, Kubernetes and Cloudflare credentials. Pick your row.

| You are | Go to | It holds |
|---|---|---|
| Learning | [Get started](../../get-started/sluis/README.md) | One tutorial per deployment shape. |
| Doing a task | [Guides](../../guides/sluis/operate/day-two.md) | One task per page, and upgrades. |
| Looking something up | [Reference](../../reference/sluis/configuration.md) | Keys, chart values, CLI, adapters, audit actions. |
| Running it | [Operate](../../guides/sluis/operate/README.md) | Recurring and incident tasks. |
| Asking why | [Decisions](../../decisions/README.md) | The ADRs and their status. |

[CHANGELOG.md](../../../CHANGELOG.md) lists what changed in each version. The upgrade guides list the steps.

## Explanation

- [Why sluis exists](why.md), [design](design.md)
- [Architecture](architecture.md), [terms](concepts.md), [trust](trust.md)
- [Sessions](sessions.md), [people and agents](people-and-agents.md), [console](console.md)
- [Policy](policy.md), [groups claim](groups-in-a-token.md), [configuration](configuration.md)
- [Ports](ports.md), [integrations](integrations.md), [AWS Lambda](lambda.md)
- [Recovery](recovery.md), [audit](audit.md), [failure semantics](failure-semantics.md)
- [Security model](security-model.md), [safety](safety.md), [conformance findings](conformance-findings.md)
