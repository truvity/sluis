# How is sluis designed?

One issuer that every cluster, cloud account, CD system and console trusts, fed by the corporate directories the company already runs. It answers two questions about a person, whether the account is live and who is in a group. It turns the answer into a token under a policy that is a file in git. It authenticates nobody: passwords, MFA and device policy stay with Google Workspace or Entra.

## One binary per module

The issuer, the signer and the console are one process, `sluis-issuer`. The GitHub, Slack, Google, Cloudflare and backup modules are processes of their own, one binary each, that the issuer calls ([ADR 0071](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md)). A module whose `remote` is unset in the issuer's document runs inside the issuer instead and shares its health probes. A login makes no network call except to the corporate directory and the Google module.

The directory's own endpoint is not served. The controllers read the console's API with their own identity, with the policy digest on every answer.

That leaves one chart, one policy file, and per process one service document and one health endpoint. The assembly is `internal/rosterapp`: wiring only, and the halves keep their own packages and tests.

A module alone holds its credentials: a GitHub App key is in the GitHub module's namespace and nowhere else. The boundary sits in IAM per role, in validated configuration and in the release signature.

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
- [ADR 0071: least-privilege modules, one process per credential holder](../../decisions/0071-least-privilege-modules-one-credential-holder-per-function.md)
