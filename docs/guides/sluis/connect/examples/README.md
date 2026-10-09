# Worked examples

Each example takes one integration from goal to undo: Goal, What you need, The policy snippet, The exchange or command, Verify, Undo. Each page links its recipe. The index is [Connect something](../README.md).

Hostnames and accounts are examples. `sluisctl policy render` accepts every policy snippet and the chart's values schema accepts every values snippet. Each page names its fixture or test.

| Integration | Example |
|---|---|
| GitHub Actions runners | [A GitHub App for each runner tier](runner-apps.md) |
| GitHub Apps | [A catalogue App, its key and a job's token](github-app-in-the-catalogue.md) |
| Cloudflare | [Short-lived R2 credentials](cloudflare-r2-credentials.md), [a Cloudflare API token for a CI job](cloudflare-api-token.md) |
| OIDC clients | [A console with its own sign-in](oidc-console-app.md), [Envoy Gateway OIDC](oidc-envoy-gateway.md), [oauth2-proxy](oidc-oauth2-proxy.md), [an MCP server](oidc-mcp-server.md), [a generated client secret](oidc-generated-client-secret.md) |
| AWS | [A person's profile](aws-person-profile.md), [a CI job's role](aws-ci-job-role.md), [a workload's role](aws-workload-role.md) |

The two Cloudflare examples use field and flag names from the Cloudflare STS pull requests, which ship in the next release candidate. Webhook Apps for Argo CD and Kargo have no example yet.
