# Connect GitHub Actions

A workflow holds no secret. It requests its identity token, exchanges it at the issuer for the clients its groups admit, and uses the result. Use the action `truvity/sluis` or `sluisctl`: both exchange the same token.

## Before you start

- `github.owners` is the trust boundary. Any repository gets a valid GitHub token, and an empty list verifies nothing.
- The audience a workflow requests is the issuer's own URL. It is not configurable.
- Deploy the issuer before you write `visibility` in a policy; an older issuer refuses it.

## 1. Trust the owner

```yaml
github:
  owners: [acme]
```

## 2. Map jobs to groups

```yaml
groups:
  ci-deploy:   { matchers: [{ github: { repository: acme/platform, ref: refs/heads/main } }] }
  ci-any-main: { matchers: [{ github: { owner: acme, ref: refs/heads/* } }] }
  ci-private:  { matchers: [{ github: { owner: acme, visibility: private } }] }
lifetimes: { ci-deploy: 1h, ci-any-main: 1h, ci-private: 1h }
clients:
  aws:111122223333:deployer: { kind: exchange, requires: [ci-deploy] }
  k8s:staging:               { kind: public,   requires: [ci-deploy, engineer] }
  k8s:staging-readonly:      { kind: public,   requires: [ci-any-main] }
```

`visibility` admits every private repository of the organisation, not its public ones or forks. Its values are `public`, `private` and `internal`.

To pin a job to one workflow file, add `workflow_ref`, `job_workflow_ref`, `sha`, `event_name` or `ref_type`:

```yaml
{ repository: acme/platform, ref: refs/heads/main, event_name: push,
  job_workflow_ref: acme/platform/.github/workflows/deploy.yml@refs/heads/main }
```

## 3. Use the action

```yaml
permissions:
  id-token: write          # lets the action ask GitHub for this job's identity token
  contents: read
steps:
  - id: access
    # Pin a release by commit; there is no floating `v1` tag.
    uses: truvity/sluis@<commit-sha>   # vX.Y.Z
    with:
      issuer: https://access.example
      audiences: k8s:staging, aws:111122223333:deployer
      kubeconfig: true
      default-profile: deployer@111122223333
      region: eu-example-1
  - run: kubectl --context staging -n demo rollout status deploy/app
  - run: aws s3 ls
```

The action makes one RFC 8693 exchange per audience at `<issuer>/token` and masks every token. For `aws:<account>:<role>` it writes the profile `<role>@<account>` and exports `AWS_CONFIG_FILE`.

| Input | Default | Meaning |
|---|---|---|
| `issuer` | required | The issuer URL |
| `audiences` | `""` | `k8s:<cluster>` or `aws:<account>:<role>`, comma or newline separated. May be empty with `github-app` |
| `kubeconfig` | `"false"` | `"true"` writes one user and context per `k8s:` audience and exports `KUBECONFIG`. The cluster entry comes from the platform's kubeconfig |
| `default-profile` | `""` | Exported as `AWS_PROFILE`; must be a profile this run wrote |
| `region` | `""` | Written into every AWS profile |
| `github-app` | `""` | A [catalogue App](github-app-tokens.md#2-mint-a-token) id to mint an installation token of |
| `repositories` | `""` | Names without the owner. Empty needs a grant of every repository |
| `permissions` | `""` | `name:level` or `name=level`. Empty asks for the grant's permissions |

| Output | Holds |
|---|---|
| `profiles` | AWS profile names written, space separated |
| `kubeconfig` | The kubeconfig path |
| `github-token` | The masked installation token |

A missing `id-token: write`, a refused audience or an audience of neither shape fails the step with the issuer's sentence.

## 4. Use a shared workflow

A shared workflow takes the token source as an input, so callers store no private key:

```yaml
# the caller
permissions:
  contents: read
  id-token: write            # a called workflow can narrow this, never widen it
jobs:
  tag:
    uses: example-org/shared-workflows/.github/workflows/auto-release.yaml@<commit-sha>   # vX.Y.Z
    with:
      token-source: access-roster
      access-roster-issuer: https://access.example
      github-app: ci-automation          # the catalogue id
```

```yaml
# inside the shared workflow's job
- id: access
  uses: truvity/sluis@<commit-sha>   # vX.Y.Z
  with:
    issuer: ${{ inputs.access-roster-issuer }}
    github-app: ${{ inputs.github-app }}
    repositories: ${{ github.event.repository.name }}
    permissions: contents:write
- run: gh release create "v${VERSION}" --target "${GITHUB_SHA}" --generate-notes
  env:
    GH_TOKEN: ${{ steps.access.outputs.github-token }}
```

Put the two token sources in two jobs. A job's `permissions` are static, and `id-token: write` fails every caller that did not grant it. Pin the grant to the workflow as in [Pinning a grant](github-app-tokens.md#3-pin-a-grant-to-one-workflow).

Jobs on self-hosted runners exchange their own token the same way. The runners register with a [runner App](github-organisation.md#5-add-runner-apps).

## 5. Reuse a laptop's files (optional)

With `sluisctl` in the toolchain, the files a person uses work unchanged in a job with `id-token: write`:

```yaml
exec:
  command: sluisctl
  args: [kube-token, --audience, k8s:staging, --issuer, https://issuer.example.internal]
```

```ini
[profile test]
credential_process = sluisctl aws --audience aws:111122223333:test --issuer https://issuer.example.internal
```

In a job `sluisctl` reads `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` and exchanges the job's token. List the people's groups and the job's group in the client's `requires`. Select a named profile, never a committed `[default]`. Other consumers read `sluisctl token --audience <client>`.

## Verify

The step prints no error and `aws sts get-caller-identity --profile deployer@111122223333` answers. A CI token lives as long as the issuer's CI client setting; re-run the action before a longer step.

For ECR and CodeArtifact on the written profiles, see [Registries and artifacts](registries-and-artifacts.md). Install `sluisctl` as in [Installing sluisctl](../../../concepts/sluis/sluisctl.md#installing-it).
