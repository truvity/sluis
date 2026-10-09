# Connect an AWS account without Identity Center

Let people and CI jobs assume AWS roles with sluis tokens. The account trusts the sluis JWKS and reads `aud`.

## Before you start

- You can create an IAM OIDC identity provider and roles, and AWS can reach the issuer URL and its JWKS.
- Preview the trust policies and read the `aud` condition. A mismatched `aud` fails as `AccessDenied` on `AssumeRoleWithWebIdentity`.
- Write one trust policy per role. The decision rides in `aud`, so no per-user statements and no group claims.

## Steps

### 1. Create the provider and trust policy

Create an IAM OIDC identity provider for the issuer URL. Add one trust policy per role that names the role's audience:

```json
{
  "Effect": "Allow",
  "Principal": { "Federated": "arn:aws:iam::111122223333:oidc-provider/issuer.example.internal" },
  "Action": "sts:AssumeRoleWithWebIdentity",
  "Condition": { "StringEquals": { "issuer.example.internal:aud": "aws:111122223333:power" } }
}
```

Add a `sub` condition when a role is for one workload only. IAM accepts ES384, so the default key needs no change. If other relying parties need RS256, set `signingKey.certificate: {algorithm: RSA, size: 2048, encoding: PKCS1}` ([reference](../../../reference/sluis/configuration.md)).

Run `aws iam get-open-id-connect-provider` to see the issuer URL. Roll back by deleting the role, then the provider.

### 2. Declare the roles

Each role is a client of kind `exchange`. `requires` says who may assume it:

```yaml
clients:
  aws:111122223333:power:           { kind: exchange, requires: [sre] }
  aws:111122223333:platform-deployer: { kind: exchange, requires: [ci-platform] }
```

Render the policy and read the client. Roll back by removing the row.

### 3. Set up the person profile

`sluisctl setup` (or `aws-config` alone) writes a profile `<role>@<account>` per granted role:

```ini
[profile power@111122223333]
credential_process = sluisctl aws --audience aws:111122223333:power
region = eu-central-1
```

`sluisctl aws` exchanges the cached login for that audience and calls `AssumeRoleWithWebIdentity`. Check with `aws sts get-caller-identity --profile power@111122223333`. Roll back by deleting the profile.

### 4. Set up the job

The action with `audiences: aws:111122223333:platform-deployer` exchanges the job's GitHub token and writes the profile `platform-deployer@111122223333` with `web_identity_token_file`. A job with `id-token: write` can instead use the person's `aws.ini` ([GitHub Actions](github-actions.md#5-reuse-a-laptops-files-optional)).

The account trusts the issuer, not GitHub. Run the job on an allowed ref and on another ref; only the first assumes the role. Roll back by removing the machine group matcher.

## Afterwards

Every other AWS service works on the same profile: `--profile <role>@<account>` or `AWS_PROFILE`. For ECR and CodeArtifact see [registries and artifacts](registries-and-artifacts.md).
