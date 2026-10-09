# Use ECR and CodeArtifact

ECR and CodeArtifact trust the AWS credential that sluis prepares: a profile `<role>@<account>` ([AWS account](aws-account.md)). Everything here is AWS tooling reading those profiles.

## Before you start

- Grant roles used only for registry or artifact access.
- Use one `--profile` per account or domain.

```yaml
groups:
  ci-app:   { matchers: [{ github: { repository: acme/app, ref: refs/heads/master } }] }
  engineer: { members: [engineers@example.com] }
clients:
  aws:111122223333:ecr-push:         { kind: exchange, requires: [ci-app] }
  aws:444455556666:artifacts-reader: { kind: exchange, requires: [engineer] }
```

## ECR in a job

One step per account, with the profile in the environment:

```yaml
- uses: aws-actions/amazon-ecr-login@v2
  env: { AWS_PROFILE: ecr-push@111122223333, AWS_REGION: eu-central-1 }
  with: { registries: "111122223333" }
- uses: aws-actions/amazon-ecr-login@v2
  env: { AWS_PROFILE: artifacts-reader@444455556666, AWS_REGION: eu-west-1 }
  with: { registries: "444455556666" }
```

## ECR on a laptop

Map Amazon's credential helper per registry host in `~/.docker/config.json`:

```json
{ "credHelpers": {
    "111122223333.dkr.ecr.eu-central-1.amazonaws.com": "ecr-login",
    "444455556666.dkr.ecr.eu-west-1.amazonaws.com": "ecr-login" } }
```

The helper reads `AWS_PROFILE`. When two registries need two roles, set the profile per shell or use the helper's per-registry mapping. `sluisctl setup` prints this block for your granted roles.

## CodeArtifact

Log in once per domain and tool, always with `--profile`. Tokens live twelve hours. In a job, run the login after the sluis step. On a laptop, `sluisctl setup` prints the lines for your domains.

| Tool | Command |
|---|---|
| npm, pnpm, yarn | `aws codeartifact login --tool npm --domain d --domain-owner 444455556666 --repository npm-store --profile artifacts-reader@444455556666` |
| pip | `aws codeartifact login --tool pip …` |
| twine | `aws codeartifact login --tool twine …` |
| NuGet / dotnet | `aws codeartifact login --tool nuget …` / `--tool dotnet …` |
| Swift | `aws codeartifact login --tool swift …` |
| Ruby (gem, bundler) | `aws codeartifact login --tool ruby …` |
| Cargo | `aws codeartifact login --tool cargo …` |
| Go | `export GOPROXY=https://aws:$(aws codeartifact get-authorization-token --domain d --domain-owner 444455556666 --query authorizationToken --output text --profile artifacts-reader@444455556666)@d-444455556666.d.codeartifact.eu-west-1.amazonaws.com/go/go-store/` |
| Maven | the same token as `<password>` for the repository's `<server>` in `settings.xml`, endpoint from `aws codeartifact get-repository-endpoint --format maven` |
| Gradle | the same token in `gradle.properties` for the repository credentials |
| generic | `aws codeartifact get-authorization-token` and the endpoint from `get-repository-endpoint --format generic` |

For any other AWS service, pass `--profile <role>@<account>`.
