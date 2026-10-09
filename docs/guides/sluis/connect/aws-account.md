# Connect an AWS account, without Identity Center

## Purpose

Let people and CI jobs assume AWS roles with sluis tokens, with no Identity Center: the account trusts sluis's JWKS and
reads `aud`.

## Preconditions

- Rights to create an IAM OIDC identity provider and roles in the account.
- sluis's issuer URL is reachable by AWS and serves its JWKS.
- The roles' clients and groups exist in the policy (below).

## Before you start

- **Preview the trust policies before applying them** and read the `aud` condition: a role whose `aud` does not match
  the client id fails as `AccessDenied` on `AssumeRoleWithWebIdentity`, with no hint which half is wrong.
- **One trust policy per role**, no per-user statements, no group claims: the decision rides in `aud`.

## Steps

**Anchor:** the issuer.

### 1. Account side, once

An IAM OIDC identity provider for the issuer URL, and per role a trust
policy that names the role's audience:

```json
{
  "Effect": "Allow",
  "Principal": { "Federated": "arn:aws:iam::111122223333:oidc-provider/issuer.example.internal" },
  "Action": "sts:AssumeRoleWithWebIdentity",
  "Condition": { "StringEquals": { "issuer.example.internal:aud": "aws:111122223333:power" } }
}
```

IAM's OIDC provider documentation lists RS256, RS384, RS512, ES256, ES384
and ES512 as the `id_token_signing_alg_values_supported` a provider may
advertise, so the chart's default ES384 key is accepted as an RSA one is;
an installation whose other relying parties need RS256 sets
`signingKey.certificate: {algorithm: RSA, size: 2048, encoding: PKCS1}`
([reference](../../../reference/sluis/configuration.md)) and the account needs no change.

One trust policy per role. No per-user statements, no group claims: for a
custom issuer the trust policy can see `sub`, `aud`, `amr` and `email`,
and the decision rides in `aud`. Add a `sub` condition when a role is for
one workload only.

**Expect**: the account lists the provider and the role.

**Verify**: `aws iam get-open-id-connect-provider` shows the issuer URL.

**Rollback**: delete the role, then the provider.

### 2. Policy

Each role is a client of kind `exchange`; `requires` says who may assume it:

```yaml
clients:
  aws:111122223333:power:           { kind: exchange, requires: [sre] }
  aws:111122223333:platform-deployer: { kind: exchange, requires: [ci-platform] }
```

**Expect**: the client appears for a person holding `requires`.

**Verify**: render the policy and read the client.

**Rollback**: remove the client row.

### 3. Person side

`sluisctl setup` (or `aws-config` alone) writes a profile per granted
role, named `<role>@<account>`:

```ini
[profile power@111122223333]
credential_process = sluisctl aws --audience aws:111122223333:power
region = eu-central-1
```

`sluisctl aws` exchanges the cached login for that audience and calls
`AssumeRoleWithWebIdentity`; the cloud CLI sees ordinary temporary
credentials. The issuer takes that sign-in as a proof only because
sluisctl's own client declares `sign_in_exchange: true`; no other token
it signs is one.

**Expect**: `aws sts get-caller-identity --profile <role>@<account>` names the assumed role.

**Verify**: the same command.

**Rollback**: delete the profile.

### 4. Job side

The action with `audiences: aws:111122223333:platform-deployer` exchanges
the job's GitHub token at the issuer and writes the profile
`platform-deployer@111122223333` with `web_identity_token_file` pointing at
the result; the AWS CLI does the rest. Or the same `aws.ini` a person
uses works unchanged in a job granted `id-token: write`: `sluisctl aws`
exchanges the job's own token there
([github-actions.md](github-actions.md#or-the-same-files-a-laptop-uses)). The account trusts the issuer, not
GitHub: no direct GitHub provider is configured, and CI's entitlements
live in the policy.

**Expect**: a job on an allowed ref assumes the role; one on another ref is refused.

**Verify**: run it on both.

**Rollback**: remove the machine group matcher.

## Afterwards

Every other AWS service (ECR, CodeArtifact, S3, anything) works on top: `--profile <role>@<account>`, or `AWS_PROFILE`. Nothing here knows those services; see
[registries-and-artifacts.md](registries-and-artifacts.md) for the
registry and artifact recipes.
