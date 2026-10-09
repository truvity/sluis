# Connect AWS workloads

A Lambda function, ECS task or EC2 instance running as an IAM role proves itself with AWS outbound identity federation and exchanges the proof for a short-lived token ([service to service](service-to-service.md)). For people assuming roles, see [AWS account](aws-account.md).

## Before you start

- Outbound federation is enabled, or STS answers `OutboundWebIdentityFederationDisabled`.

- The issuer reaches `https://*.tokens.sts.global.api.aws` on port 443. The chart ships no egress rules.

- A VPC function with no route out needs an STS interface endpoint.

- You cannot revoke AWS tokens or the minted token. Give the client a short `ttl_cap`.

## Steps

### 1. Enable federation in each account

```sh
aws iam enable-outbound-web-identity-federation
aws iam get-outbound-web-identity-federation-info   # prints IssuerUrl
```

`IssuerUrl` goes in step 3.

### 2. Let the role ask for a token for one audience

```json
{
  "Effect": "Allow",
  "Action": "sts:GetWebIdentityToken",
  "Resource": "*",
  "Condition": {
    "ForAllValues:StringEquals": {
      "sts:IdentityTokenAudience": "https://access.example.com"
    },
    "StringEquals": { "sts:SigningAlgorithm": "ES384" },
    "NumericLessThanEquals": { "sts:DurationSeconds": 300 }
  }
}
```

`sts:IdentityTokenAudience` is multi-valued: a plain `StringEquals` is an implicit deny. The audience defaults to the issuer URL (`exchange.aws.audience`).

### 3. Add the account to the issuer

```yaml
exchange:
  aws:
    audience: ""          # default: the issuer URL
    maxAge: 5m
    accounts:
      - account: "111122223333"
        name: apps                      # for logs
        issuer: https://example-id.tokens.sts.global.api.aws
        orgId: o-example1234            # optional: require this organization
        algs: [ES384, RS256]            # optional: default both
        # jwksUri: ""                   # optional: default <issuer>/.well-known/jwks.json
```

An empty list verifies no AWS token; a bad row stops the issuer at start. Adding a row is a rollout ([configuration](../../../reference/sluis/configuration.md), `AWS_FEDERATION_FILE`).

### 4. Write the matcher

```yaml
groups:
  otlp:billing:writer:
    matchers:
      - aws: { account: "111122223333", role: "billing-*" }
clients:
  otlp: { kind: exchange, requires: [otlp:billing:writer], ttl_cap: 15m }
```

`account` is exact and required. See the [AWS matcher](../../../reference/sluis/policy-groups.md#the-aws-matcher).

### 5. Exchange

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

The `access_token` has `sub=aws:111122223333:role/billing-api`, `aud=otlp` and the role's groups. It lives no longer than the shortest group lifetime and the client's `ttl_cap`. It has no refresh token.

## Verify

Each exchange is a `roster.token.exchanged` record with `proof` `workload`. A role in no group is refused.

## What the verifier checks

A token passes when all of these hold. Once the issuer is recognised, any failure is final.

- `iss` is a configured account's issuer, and `aud` contains the configured audience.

- The signature verifies against that key set with an algorithm in `algs`. An unknown `kid` re-fetches the set once per ten seconds at most.

- `iat` is no older than `maxAge`, and `exp` and `nbf` are valid.

- The `aws_account` claim, the account in the role ARN and the row's account match. `orgId` matches when the row sets it.

- `sub` is a role ARN `arn:aws:iam::<account>:role/<path><name>`.

The subject `aws:<account>:role/<path><name>` is stable across sessions, functions and instances. The verifier refuses the `assumed-role` form, users, the account root, federated users, other partitions and names outside `[A-Za-z0-9_+=,.@-]`. See the AWS [token claims](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_outbound_token_claims.html).
