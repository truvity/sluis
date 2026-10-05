# Sign with AWS KMS

## Purpose

Keep the issuer's signing key in AWS KMS (`signingKey.kms`), so the estate's master key is never in a pod, a Secret or a
backup. For key pairs that KMS generates and wraps, see `signingKey.kmsWrapped` in
[configuration](../reference/configuration.md#the-service-document).

## Preconditions

- One KMS key per estate, created as `ECC_NIST_P384` with usage `SIGN_VERIFY` and an alias such as `alias/sluis-signing`.
  For RS256 relying parties (Kargo, EKS's OIDC provider) one `RSA_2048`, `RSA_3072` or `RSA_4096` `SIGN_VERIFY` key more,
  listed under `signingKey.kms.additional`.
- The pod's AWS identity: EKS Pod Identity, or IRSA with `serviceAccount.awsIdentity: irsa`
  ([chart values](../reference/chart-values.md)).
- The sign-in state secret `issuer/state-secret` ([secrets](../reference/secrets.md#the-names)), because a KMS key has no
  private bytes to derive the state from.

## Before you start

- **`signingKey.kms` is exclusive with `signingKey.file`.** Both is refused at load. In the chart, write
  `config.signingKey.file: null` to drop the chart's default path, and remove `signingKey.existingSecret` and
  `signingKey.additional`.
- **A key of another spec or usage stops the start.**
- **Without `kms:GetPublicKey` the service refuses to start**, and the log line names the permission and the key. Without
  `kms:Sign` it starts and fails every token, counted in `access_issuer.kms_signatures{result="error"}`.
- **An alias is not a key policy resource.** Grant on the key the alias points at, or use a `kms:ResourceAliases` condition
  if the grant must follow the alias.
- **Throughput has a ceiling.** Every token is one `kms:Sign` call, so the account's KMS request quota for asymmetric
  `Sign` (a regional, adjustable quota shared by everything in the account that signs) bounds token throughput.
- **Preview before every apply, and read the preview.**

## Steps

### 1. Grant the role

**Run** attach this to the role the service runs as, on each key in `signingKey.kms.keys` and in every
`signingKey.kms.additional[].keys`:

```json
{
  "Effect": "Allow",
  "Action": ["kms:Sign", "kms:GetPublicKey"],
  "Resource": ["<the ARN of each key>"]
}
```

**Expect** the role to list both actions on each key.

**Verify** `aws kms get-public-key --key-id <key>` as that role succeeds.

**Rollback**: detach the statement.

### 2. Configure the keys

**Run**

```yaml
config:
  signingKey:
    file: null
    kms:
      keys: [alias/sluis-signing]        # oldest first; the last one signs
      region: eu-west-1
      stateSecret: issuer/state-secret
```

**Expect** at start the log names each key and its `kid`, the RFC 7638 thumbprint of the public key (the same as a file
holding that key would have). The algorithm is ES384; each token is a `kms:Sign` of the SHA-384 of the signing input.

**Verify** `curl -s https://<issuer>/keys | jq -r '.keys[].kid'` lists the key; a sign-in succeeds and
`access_issuer.kms_signatures{result="ok"}` rises. Every signature is also in CloudTrail.

**Rollback**: restore `signingKey.file` and the chart's certificate. Tokens the KMS key signed stop verifying unless it is
kept as a verify-only key ([cut over](cut-over-to-kms-wrapped-signing.md) shows the mechanics).

### 3. Rotate by appending a key

**Run** append a new key to `keys`. It is published at once and signs only after `signingKey.activationDelay`; the earlier
key stays published for `signingKey.overlap`; the list is re-read every `pollInterval`, which also notices an alias moved
to another key. Never insert a key before one already seen.

**Expect** both keys in the JWKS, then the new one signing.

**Verify** the `kid`s in `/keys`; `kms_signatures` by `kid` moves to the new key.

**Rollback**: none, because a published key stays published for the overlap. List order is age: only the last key is newly
adopted, and a key that has retired stays retired while it is listed (remove it when convenient).

## Afterwards

- Watch `access_issuer.kms_signatures{result="throttled"}`: it says the quota is being hit. Request a raise before it is.
- Raise an alert on `result="error"` ([telemetry](../reference/telemetry.md#alerts)).
