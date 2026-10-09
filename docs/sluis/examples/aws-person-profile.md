# Example: a person assumes an AWS role with `sluisctl aws-config`

## Goal

A person signs in once and uses ordinary AWS CLI profiles, one per role they are granted, with no Identity Center.

## What you need

- An IAM OIDC identity provider for the issuer URL in the account, and a role whose trust policy checks the audience.
- `sluisctl` and a person in the group the client `requires`.

## The policy snippet

Trust policy of the role (account `111122223333`):

```json
{
  "Effect": "Allow",
  "Principal": {"Federated": "arn:aws:iam::111122223333:oidc-provider/issuer.example.com"},
  "Action": "sts:AssumeRoleWithWebIdentity",
  "Condition": {"StringEquals": {"issuer.example.com:aud": "aws:111122223333:power"}}
}
```

Policy: each role is an `exchange` client.

```yaml
clients:
  aws:111122223333:power: { kind: exchange, requires: [sre] }
```

## The exchange / command

```sh
sluisctl login
sluisctl aws-config          # a profile per granted role, named <role>@<account>
```

which writes

```ini
[profile power@111122223333]
credential_process = sluisctl aws --audience aws:111122223333:power
region = eu-central-1
```

## Verify

`aws sts get-caller-identity --profile power@111122223333` names the assumed role. A wrong `aud` condition fails as
`AccessDenied` on `AssumeRoleWithWebIdentity` with no hint which half is wrong.

## Undo

Delete the profile, remove the client row, then delete the role and the provider.

Recipe: [Connect an AWS account](../../how-to/connect/aws-account.md).

Snippet source: `docs/how-to/connect/aws-account.md`; the client row is accepted by `sluisctl policy render`.
