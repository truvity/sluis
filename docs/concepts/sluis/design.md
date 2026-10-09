# How is sluis designed?

One issuer that every cluster, cloud account, CD system and console trusts, fed by the corporate directories the company already runs. It answers two questions about a person, whether the account is live and who is in a group. It turns the answer into a token under a policy that is a file in git. It authenticates nobody: passwords, MFA and device policy stay with Google Workspace or Entra.

## One process

The directory reader, token service, console, GitHub controller and Slack controller are one process. The controllers are loops switched on by the `controllers` section of the service document, and they share its health probes. The answer about a person is a function call, so a login makes no network call except to the corporate directory.

The directory's own endpoint is not served. The controllers read the console's API, which is this process, with the policy digest on every answer.

That leaves one chart, one Deployment or Lambda function, one service document, one policy file and one health endpoint. The assembly is `internal/rosterapp`: wiring only, and the halves keep their own packages and tests.

The controllers hold GitHub App keys, so the boundary between them and the rest sits in IAM scoped to the installation, in validated configuration and in the release signature.

## Where each part is explained

| Part | Page |
|---|---|
| Every container and how they connect | [Architecture](architecture.md) |
| Two trust anchors and one vocabulary | [Trust](trust.md) |
| Workspaces, domains and what authoritative means | [Directory model](directory-model.md) |
| Snapshots, leases and shared state | [Freshness](freshness.md) |
| The policy, what is verified, six grants | [Verify and issue](verify-and-issue.md) |
| Sessions, limits and sign-out | [Sessions](sessions.md), [back-channel logout](back-channel-logout.md) |
| The web UI | [Console](console.md) |
| Joiners, movers and leavers on GitHub | [GitHub controller](github-controller.md) |
| Channel membership on Slack | [Slack reconciler](slack-reconciler.md) |
| The audit trail | [Audit](audit.md), [audit actions](../../reference/sluis/audit-actions.md) |
| State, secrets and blobs | [Store](store.md), [ports](ports.md) |
| The way in when no directory can vouch | [Recovery](recovery.md) |
| What happens when something fails | [Failure semantics](failure-semantics.md) |
| What was removed | [Not served](not-served.md) |

## Build

Run `devbox shell`, then `just check`. The chart is `charts/sluis`.

A console with no OpenID flow of its own uses gateway-native OIDC on Envoy Gateway, or upstream oauth2-proxy on other gateways ([ADR 0003](../../decisions/0003-deprecate-access-proxy.md)). See [oauth2-proxy](../../guides/sluis/connect/oauth2-proxy.md).

## Decided in

- [ADR 0003: deprecate and remove the proxy chart](../../decisions/0003-deprecate-access-proxy.md)
- [ADR 0037: one process everywhere](../../decisions/0037-one-process-everywhere.md)
