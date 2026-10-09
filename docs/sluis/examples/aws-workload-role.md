# Example: a workload on an IAM role exchanges its AWS identity

## Goal

A Lambda function, ECS task or EC2 instance running as an IAM role proves who it is to the issuer with no stored secret and
gets a short-lived token for one client.

## What you need

- Owner rights on the AWS account, and the issuer's egress to `https://*.tokens.sts.global.api.aws` on port 443.

## The policy snippet

Once per account: `aws iam enable-outbound-web-identity-federation`, then `aws iam get-outbound-web-identity-federation-info`
prints the `IssuerUrl`. The role may ask for a token for this audience only:

```json
{
  "Effect": "Allow",
  "Action": "sts:GetWebIdentityToken",
  "Resource": "*",
  "Condition": {
    "ForAllValues:StringEquals": {"sts:IdentityTokenAudience": "https://access.example.com"},
    "StringEquals": {"sts:SigningAlgorithm": "ES384"},
    "NumericLessThanEquals": {"sts:DurationSeconds": 300}
  }
}
```

The account in the chart (there is no default account; an empty list verifies no AWS token), and the policy:

```yaml
exchange:
  aws:
    maxAge: 5m
    accounts:
      - account: "123456789012"
        name: apps
        issuer: https://example-id.tokens.sts.global.api.aws
```

```yaml
groups:
  otlp:billing:writer:
    matchers:
      - aws: { account: "123456789012", role: "billing-*" }
clients:
  otlp: { kind: exchange, requires: [otlp:billing:writer], ttl_cap: 15m }
```

## The exchange / command

```sh
TOKEN=$(aws sts get-web-identity-token --audience https://access.example.com \
          --signing-algorithm ES384 --duration-seconds 300 \
          --query WebIdentityToken --output text)
curl -u otlp: https://access.example.com/token \
  -d grant_type=urn:ietf:params:oauth:grant-type:token-exchange \
  -d subject_token="$TOKEN" \
  -d subject_token_type=urn:ietf:params:oauth:token-type:jwt \
  -d audience=otlp
```

## Verify

The response's `access_token` has `sub=aws:123456789012:role/billing-api` and `aud=otlp`. The subject is the role, never the
session. The audit trail has `roster.token.exchanged` with proof `workload`. A role in no group is refused and recorded.

## Undo

Remove the account row (a rollout) and the client. A token stays valid until its `exp` even if the role is deleted, which is
why the client carries a short `ttl_cap`.

Recipe: [AWS workloads](../../how-to/connect/aws-workloads.md).

Snippet source: `docs/how-to/connect/aws-workloads.md`; `policy/aws_test.go` holds the matcher cases.
