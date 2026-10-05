# Signing on AWS: the key and its policy

The IAM statement, the key policy and the settings of `kms-wrapped` signing. What the trade is, how rotation works and
where the trust boundary lies: [Signing on AWS](../explanation/signing-on-aws.md). Source: `deploy/pulumi/policy.go`
(`WrappedKeyPolicyStatements`, `WrappedKeyReservedDeny`).

## The role's grant

The function's role (and the Kubernetes role, when it signs) gets, on the key and nowhere else:

```json
{
  "Sid": "SluisWrappedSigning", "Effect": "Allow",
  "Action": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
  "Resource": "<the key's ARN>",
  "Condition": {
    "StringEquals": {"kms:EncryptionContext:purpose": "sluis-signing"},
    "ForAllValues:StringEquals": {"kms:EncryptionContextKeys": ["purpose", "alg", "kid"]}
  }
}
```

## The key policy

`WrappedSigning.KeyArn` takes an existing, possibly shared, key. The library then
leaves its key policy alone, and **the estate MUST merge the reserved-context
denial below into it** (`sluispulumi.WrappedKeyPolicyStatements(roleArns)` returns
it, `WrappedKeyReservedDeny` as a map). Unset, the library creates the key (`SYMMETRIC_DEFAULT`, `ENCRYPT_DECRYPT`, rotation
enabled, protected, alias `WrappedSigning.KeyAlias`, default
`alias/sluis-signing-wrapped`) with this key policy, so a broader policy attached
to the role later does not widen what it can do with the key:

| Sid | Effect | Principal | Action | Condition |
|---|---|---|---|---|
| `EnableIAMPolicies` | Allow | the account root | `kms:*` | none (IAM policies govern the key, and key administration is the account's) |
| `SluisSigningContextReserved` | Deny | `*` | `kms:Decrypt`, `kms:Encrypt`, `kms:ReEncrypt*`, `kms:GenerateDataKey*`, `kms:CreateGrant` | `kms:EncryptionContext:purpose` is `sluis-signing`, and `aws:PrincipalArn` is **not** one of the signing roles (the function's role, and `WrappedSigning.AdditionalSigningRoleArns`, the Kubernetes role) |
| `SluisSigningRolePurposeOnly` | Deny | `*` | `kms:GenerateDataKeyPairWithoutPlaintext`, `kms:Decrypt` | `aws:PrincipalArn` is a signing role, and `kms:EncryptionContext:purpose` is not `sluis-signing` |
| `SluisSigningRoleContextKeysOnly` | Deny | `*` | the same two | a signing role, and `ForAnyValue:StringNotEquals kms:EncryptionContextKeys` `["purpose","alg","kid"]` |
| `SluisSigningRoleNothingElse` | Deny | `*` | `NotAction` the same two | a signing role |

The roles are named in a condition, not as principals, so the key can exist
before the roles.

## A shared key: the statements to merge

**MANDATORY when `KeyArn` is shared:** merge these four statements into the key's
policy, beside whatever it already has (the library puts the same four in the key
it creates; `sluispulumi.WrappedKeyPolicyStatements(roleArns)` returns them):

```json
[
  {
    "Sid": "SluisSigningContextReserved", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "Action": ["kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:CreateGrant"],
    "Condition": {
      "StringEquals": {"kms:EncryptionContext:purpose": "sluis-signing"},
      "ArnNotEquals": {"aws:PrincipalArn": ["<the function role's ARN>", "<the Kubernetes role's ARN, if it signs>"]}
    }
  },
  {
    "Sid": "SluisSigningRolePurposeOnly", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "Action": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
    "Condition": {
      "ArnEquals": {"aws:PrincipalArn": ["<the same signing role ARNs>"]},
      "StringNotEquals": {"kms:EncryptionContext:purpose": "sluis-signing"}
    }
  },
  {
    "Sid": "SluisSigningRoleContextKeysOnly", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "Action": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
    "Condition": {
      "ArnEquals": {"aws:PrincipalArn": ["<the same signing role ARNs>"]},
      "ForAnyValue:StringNotEquals": {"kms:EncryptionContextKeys": ["purpose", "alg", "kid"]}
    }
  },
  {
    "Sid": "SluisSigningRoleNothingElse", "Effect": "Deny", "Principal": {"AWS": "*"}, "Resource": "*",
    "NotAction": ["kms:GenerateDataKeyPairWithoutPlaintext", "kms:Decrypt"],
    "Condition": {"ArnEquals": {"aws:PrincipalArn": ["<the same signing role ARNs>"]}}
  }
]
```

The last three name only the signing roles, so they confine those roles to the two
calls and the one context on this key and touch no other principal. (`Encrypt`
and `GenerateDataKey*` are in the first so that no other principal can mint a
ciphertext of a key it chose under the signing context.)

## Moving a stack from remote signing

`kms` to `kmsWrapped` drops the old key ids from the JWKS at once, so it is a step of its own, at low traffic:
[Upgrade to v1.62, step 6](../how-to/upgrade/v1.62.md#6-retire-the-asymmetric-signing-keys-only-when-moving-from-kms-to-wrappedsigning).

## Configuration

**Configuration** (the http document; `preset: aws-hybrid` or
`aws-serverless` makes `kms-wrapped` the signing adapter, and `signingKey.kmsWrapped`
supplies its settings):

```yaml
preset: aws-hybrid
signingKey:
  kmsWrapped:
    keyId: alias/sluis-signing-wrapped   # or the shared key's ARN
    stateSecret: issuer/state-secret
    # algorithms: [ES384, RS256]         # the first is the default
    # rotateEvery: 24h
    # prepublish: 15m
    # retain: 65m
```

`stateSecret` is as for `signingKey.kms` (a secret NAME, which the library writes
for the function): the sign-in state is derived from it, not from a key that is replaced daily. The adapter can also be named in
`adapters.signing` (`adapter: kms-wrapped`), with the same settings under
`settings:`.

## Monitoring rotation

A rotation that keeps failing is a warning per attempt
and the key keeps signing, so alert on it: the issuer logs at ERROR, at most
hourly, once the active wrapped key is older than 1.5 times `rotateEvery`, and
the gauge `access_issuer.signing_key.active_since_timestamp`
(`access_issuer_signing_key_active_since_timestamp_seconds` in the store) gives
the age. Alert on
`time() - max by (algorithm) (access_issuer_signing_key_active_since_timestamp_seconds) > rotateEvery + prepublish + margin`
(26h for the defaults). The chart's `AccessRosterSigningKeyRotationStalled` rule
is this rule, with `alerts.rules.signingKeyRotationStalled.maxAgeSeconds`. Its
default (350 days) is for certificate keys, and the chart cannot see the signing
mode, so for wrapped signing **set it to `rotateEvery + prepublish + margin`**:
`93600` (26h) for the defaults. A Lambda deployment has no chart; paste this into
vmalert (or a PrometheusRule), with the cluster label of your store:

```yaml
- alert: SluisWrappedSigningKeyRotationStalled
  expr: time() - max by (algorithm) (access_issuer_signing_key_active_since_timestamp_seconds) > 93600
  for: 15m
  labels: {severity: warning}
  annotations:
    summary: 'The {{ $labels.algorithm }} signing key has been active for {{ $value | humanizeDuration }}: rotation is failing'
```
