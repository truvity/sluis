# Example: a CI job assumes an AWS role

## Goal

A GitHub Actions job on `main` assumes a deploy role. The account trusts the issuer, not GitHub, so the job's entitlement
lives in the policy.

## What you need

- The account side of [the person example](aws-person-profile.md) for the audience `aws:111122223333:platform-deployer`.
- `github.owners` set to your organisation on the issuer: the allow-list is what makes a GitHub token yours.

## The policy snippet

```yaml
groups:
  ci-deploy:
    matchers:
      - github:
          repository: example-org/platform
          ref: refs/heads/main
          event_name: push
          job_workflow_ref: example-org/platform/.github/workflows/deploy.yml@refs/heads/main
lifetimes: { ci-deploy: 1h }
clients:
  aws:111122223333:platform-deployer: { kind: exchange, requires: [ci-deploy] }
```

## The exchange / command

```yaml
permissions: { id-token: write, contents: read }
steps:
  - id: access
    uses: truvity/sluis@<commit-sha>   # vX.Y.Z
    with:
      issuer: https://access.example.com
      audiences: aws:111122223333:platform-deployer
      default-profile: platform-deployer@111122223333
      region: eu-central-1
  - run: aws sts get-caller-identity
```

The action writes the profile with `web_identity_token_file`; the AWS CLI does the rest. The same `aws.ini` a person uses
also works in a job with `id-token: write`.

## Verify

A run on `main` assumes the role; a run on another ref, or a pull request's copy of the workflow, is refused. Each exchange
is a `roster.token.exchanged` record.

## Undo

Remove the group's matcher (the job then holds nothing) and the client row.

Recipe: [Connect GitHub Actions](../github-actions.md) and
[Connect an AWS account](../aws-account.md#4-set-up-the-job).

Snippet source: `docs/guides/sluis/connect/github-actions.md`; accepted by `sluisctl policy render`.
