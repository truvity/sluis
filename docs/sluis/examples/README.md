# Worked examples

Each example is one integration from goal to undo, on one template: **Goal, What you need, The policy snippet, The exchange
or command, Verify, Undo**. The recipe it condenses is linked at the end of each page; the integrations index is
[Connect something](../../how-to/connect/README.md).

Hostnames and accounts are examples (`example.com`, `111122223333`). Every snippet is accepted by
`sluisctl policy render` (policy) or the chart's values schema (values), and each page names the fixture or test it comes
from.

| Integration | Example |
|---|---|
| GitHub Actions runners | [A GitHub App for each runner tier](runner-apps.md) |
| GitHub Apps | [A catalogue App, its key and a job's token](github-app-in-the-catalogue.md) |
| Cloudflare (queued) | [Short-lived R2 credentials](cloudflare-r2-credentials.md), [a Cloudflare API token for a CI job](cloudflare-api-token.md) |
| OIDC clients | [A console with its own sign-in](oidc-console-app.md), [Envoy Gateway OIDC](oidc-envoy-gateway.md), [oauth2-proxy](oidc-oauth2-proxy.md), [an MCP server](oidc-mcp-server.md), [a generated client secret](oidc-generated-client-secret.md) |
| AWS | [A person's profile](aws-person-profile.md), [a CI job's role](aws-ci-job-role.md), [a workload's role](aws-workload-role.md) |

The two Cloudflare examples are written against the Cloudflare STS pull requests, which ship in the next release candidate,
and name their field and flag names from those branches. Webhook Apps for Argo CD and Kargo are not here; they arrive with
their feature.
