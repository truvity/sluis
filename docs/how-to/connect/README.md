# Connect something

One row per integration: what it connects, the direction, the how-to and, where one exists, the
[worked example](../../sluis/examples/README.md). Direction is read from sluis: *in* means the thing signs in to or calls
sluis; *out* means sluis calls it or manages it; *both* means each.

## People and consoles

| Integration | Connects | Direction | How-to | Example |
|---|---|---|---|---|
| Console with its own sign-in | a web UI you write | in | [console-app](console-app.md) | [example](../../sluis/examples/oidc-console-app.md) |
| Native or gateway OIDC | the choice between the two | in | [choosing](choosing-native-or-gateway-oidc.md) | |
| Envoy Gateway OIDC | a console behind Envoy Gateway | in | [envoy-gateway-oidc](envoy-gateway-oidc.md) | [example](../../sluis/examples/oidc-envoy-gateway.md) |
| oauth2-proxy | a console behind any other gateway | in | [oauth2-proxy](oauth2-proxy.md) | [example](../../sluis/examples/oidc-oauth2-proxy.md) |
| Business surface | a test surface for employees | in | [business-surface](business-surface.md) | |
| MCP server | a resource server and self-described clients | in | [mcp](mcp.md) | [example](../../sluis/examples/oidc-mcp-server.md) |
| Argo CD | its own OIDC flow | in | [argocd](argocd.md) | |
| Kargo | its own OIDC flow and CLI | in | [kargo](kargo.md) | |
| Generated client secret | a relying party's secret | out | [generate](../let-the-issuer-generate-a-clients-secret.md) | [example](../../sluis/examples/oidc-generated-client-secret.md) |

## Services and workloads

| Integration | Connects | Direction | How-to | Example |
|---|---|---|---|---|
| Service to service | a workload calling a service | in | [service-to-service](service-to-service.md) | |
| Kubernetes cluster | people and CI to an API server | in | [kubernetes-cluster](kubernetes-cluster.md) | |
| GitHub Actions | a job exchanging its identity token | in | [github-actions](github-actions.md) | [example](../../sluis/examples/aws-ci-job-role.md) |
| AWS account | people and CI assume roles | in | [aws-account](aws-account.md) | [person](../../sluis/examples/aws-person-profile.md), [CI](../../sluis/examples/aws-ci-job-role.md) |
| AWS workloads | a role proves itself | in | [aws-workloads](aws-workloads.md) | [example](../../sluis/examples/aws-workload-role.md) |
| Registries and artifacts | ECR and CodeArtifact on the profiles | in | [registries](registries-and-artifacts.md) | |
| Infrastructure as code | a Pulumi or Terraform program as an App | in | [iac](infrastructure-as-code.md) | |

## Secrets, hosts and data

| Integration | Connects | Direction | How-to | Example |
|---|---|---|---|---|
| OpenBao | certificates for SSH and databases | both | [openbao](openbao.md), [issuer side](openbao-issuer-side.md) | |
| SSH | people, machines and hosts | in | [ssh](ssh.md) | |
| PostgreSQL | short-lived client certificates | in | [postgresql](postgresql.md) | |
| R2 credential broker | prefix-scoped object credentials | in | [r2-storage](r2-storage.md) | |
| Cloudflare tokens and R2 | short-lived tokens and R2 credentials | both | [cloudflare-tokens](../cloudflare-tokens.md) | [R2](../../sluis/examples/cloudflare-r2-credentials.md), [token](../../sluis/examples/cloudflare-api-token.md) |

## Directories, GitHub and Slack

| Integration | Connects | Direction | How-to | Example |
|---|---|---|---|---|
| Corporate directory | the people source | out | [corporate-directory](corporate-directory.md) | |
| Google Workspace | users, groups and domains | out | [google-workspace](google-workspace.md) | |
| GitHub organisation | teams and runner Apps | out | [github-organisation](github-organisation.md) | [runner Apps](../../sluis/examples/runner-apps.md) |
| GitHub account links | a person's account | both | [github-account-links](github-account-links.md) | |
| GitHub App catalogue | declared Apps and their grants | both | [catalogue](github-apps-catalogue.md), [tokens](github-app-tokens.md), [keys](github-app-keys.md) | [example](../../sluis/examples/github-app-in-the-catalogue.md) |
| Slack workspace | channels from the policy | out | [slack-workspace](slack-workspace.md) | |
| Slack Apps catalogue | declared Slack Apps | out | [slack-apps-catalogue](slack-apps-catalogue.md) | |
| Slack Connect channels | channels between workspaces | out | [slack-connect-channels](slack-connect-channels.md) | |
| Slack console channels | channels kept on the console | out | [slack-console-channels](slack-console-channels.md) | |
