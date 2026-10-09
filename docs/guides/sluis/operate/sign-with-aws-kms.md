# Sign with AWS KMS

> **Deprecated.** `signingKey.kms` logs a warning at start. New installations use the signing ring: sluis wraps generated key pairs under a symmetric key named by `keys.sign`. To move, see [signing on AWS](../../../concepts/sluis/signing-on-aws.md#the-sign-key-and-moving-between-signers). For the ring keys, see [configuration](../../../reference/sluis/configuration.md#the-service-document).

Keep the issuer's signing key in AWS KMS so the master key is never in a pod, a Secret or a backup.

## Before you start

- Create one `ECC_NIST_P384` `SIGN_VERIFY` key with an alias such as `alias/sluis-signing`. For RS256 relying parties (Kargo, EKS's OIDC provider) add one `RSA_2048`, `RSA_3072` or `RSA_4096` `SIGN_VERIFY` key under `signingKey.kms.additional`.

- Give the pod an AWS identity: Pod Identity, or `serviceAccount.awsIdentity: irsa` ([chart values](../../../reference/sluis/chart-values.md)).

- Set the sign-in state secret `issuer/state-secret` ([secrets](../../../reference/sluis/secrets.md#the-names)). A KMS key has no private bytes to derive it from.

- `signingKey.kms` excludes `signingKey.file`. In the chart write `config.signingKey.file: null` and remove `signingKey.existingSecret` and `signingKey.additional`.

- A key of another spec or usage stops the start. Without `kms:GetPublicKey` the start fails naming the permission. Without `kms:Sign` it starts and fails every token, counted in `access_issuer.kms_signatures{result="error"}`.

- Grant on the key, not the alias, or use a `kms:ResourceAliases` condition. Each token is one `kms:Sign` call, so the account's request quota bounds throughput.

## Steps

1. Attach this statement to the service's role, for each key in `signingKey.kms.keys` and `signingKey.kms.additional[].keys`:

   ```json
   {
     "Effect": "Allow",
     "Action": ["kms:Sign", "kms:GetPublicKey"],
     "Resource": ["<the ARN of each key>"]
   }
   ```

2. Configure the keys. List them oldest first: the last one signs.

   ```yaml
   config:
     signingKey:
       file: null
       kms:
         keys: [alias/sluis-signing]
         region: eu-west-1
         stateSecret: issuer/state-secret
   ```

   The algorithm is ES384. The start log names each key and its `kid`.

3. To rotate, append a new key to `keys`. It is published at once and signs after `signingKey.activationDelay`. The earlier key stays published for `signingKey.overlap`. The list is reread every `pollInterval`. Never insert a key before one already seen.

## Verify

`aws kms get-public-key --key-id <key>` as the role succeeds. `curl -s https://<issuer>/keys | jq -r '.keys[].kid'` lists the key, a sign-in succeeds and `access_issuer.kms_signatures{result="ok"}` rises. Alert on `result="error"` and `result="throttled"` ([telemetry](../../../reference/sluis/telemetry.md#alerts)).

## Roll back

Detach the statement. Restore `signingKey.file` and the chart's certificate; tokens the KMS key signed stop verifying unless it stays as a verify-only key ([cut over](../migrate/cut-over-to-kms-wrapped-signing.md)).
